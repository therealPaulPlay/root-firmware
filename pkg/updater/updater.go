package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/globals"
)

const (
	firmwareEndpoint   = "/firmware/observer/update"
	updateCheckTimeout = 10 * time.Second
	downloadTimeout    = 30 * time.Minute
	raucBundlePath     = "/data/.update.raucb"
)

type UpdateStatus string

const (
	StatusUpToDate        UpdateStatus = "up-to-date"
	StatusUpdateAvailable UpdateStatus = "update-available"
	StatusDownloading     UpdateStatus = "downloading"
	StatusInstalling      UpdateStatus = "installing"
	StatusError           UpdateStatus = "error"
)

type FirmwareInfo struct {
	Version    string `json:"version"`
	URL        string `json:"url"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Compatible string `json:"compatible"`
}

type Updater struct {
	mu               sync.RWMutex
	status           UpdateStatus
	availableVersion string
	downloadURL      string
	downloadSHA256   string
	errorMsg         string
}

var instance *Updater
var once sync.Once

func Init() {
	once.Do(func() { instance = &Updater{status: StatusUpToDate} })
}

func Get() *Updater {
	if instance == nil {
		panic("updater not initialized - call Init() first")
	}
	return instance
}

func (u *Updater) GetStatus() (UpdateStatus, string, string) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.status, u.availableVersion, u.errorMsg
}

// CheckForUpdates queries the relay server for available firmware updates.
func (u *Updater) CheckForUpdates() {
	relayDomain, ok := config.Get().GetKey("relayDomain")
	if !ok {
		log.Println("Updater: Skipping update check: relay domain not configured")
		return
	}

	client := &http.Client{Timeout: updateCheckTimeout}
	resp, err := client.Get("https://" + relayDomain.(string) + firmwareEndpoint)
	if err != nil {
		log.Printf("Updater: Failed to check for updates: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Updater: Server returned status %d", resp.StatusCode)
		return
	}

	var info FirmwareInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		log.Printf("Updater: Failed to parse response: %v", err)
		return
	}

	// Validate SHA256 format (64 hex chars)
	if len(info.SHA256) != 64 {
		u.setError(fmt.Sprintf("invalid SHA256 length: %d", len(info.SHA256)))
		return
	}
	if _, err := hex.DecodeString(info.SHA256); err != nil {
		u.setError(fmt.Sprintf("invalid SHA256 format: %v", err))
		return
	}

	// Validate compatible string matches our system
	if info.Compatible != "" && info.Compatible != globals.RAUCCompatible {
		log.Printf("Updater: Incompatible update (got %s, want %s)", info.Compatible, globals.RAUCCompatible)
		return
	}

	// Available version must ONLY be set if an update is available
	u.mu.Lock()
	if info.Version != globals.FirmwareVersion {
		u.status = StatusUpdateAvailable
		u.availableVersion = info.Version
		u.downloadURL = info.URL
		u.downloadSHA256 = info.SHA256
		u.errorMsg = ""
	} else {
		u.status = StatusUpToDate
		u.errorMsg = ""
	}
	u.mu.Unlock()

	// Signal successful update check for boot confirmation tracking
	markUpdateCheckSuccessful()
}

// StartUpdate downloads and installs the available update via RAUC.
func (u *Updater) StartUpdate() error {
	u.mu.Lock()
	if u.status != StatusUpdateAvailable {
		u.mu.Unlock()
		return fmt.Errorf("no update available")
	}
	downloadURL := u.downloadURL
	expectedSHA256 := u.downloadSHA256
	log.Printf("Updater: Starting RAUC update to version %s", u.availableVersion)
	u.status = StatusDownloading
	u.mu.Unlock()

	// Remove any stale files from previous interrupted updates
	os.Remove(raucBundlePath)
	if matches, err := filepath.Glob("/data/.rauc-bundle-*.raucb"); err == nil {
		for _, f := range matches {
			os.Remove(f)
		}
	}

	// Download RAUC bundle
	if err := u.downloadFile(downloadURL, raucBundlePath, expectedSHA256); err != nil {
		u.setError(fmt.Sprintf("download failed: %v", err))
		os.Remove(raucBundlePath)
		return err
	}

	// Install using RAUC
	u.mu.Lock()
	u.status = StatusInstalling
	u.mu.Unlock()

	if err := u.installWithRAUC(); err != nil {
		u.setError(fmt.Sprintf("RAUC installation failed: %v", err))
		os.Remove(raucBundlePath)
		return err
	}

	// Clean up and schedule reboot
	os.Remove(raucBundlePath)
	log.Println("Updater: RAUC update successful, rebooting in 2 seconds...")

	go func() {
		time.Sleep(2 * time.Second)
		if err := exec.Command("reboot").Run(); err != nil {
			log.Printf("Updater: Failed to reboot: %v", err)
		}
	}()

	return nil
}

// installWithRAUC installs the downloaded bundle using the RAUC update framework.
func (u *Updater) installWithRAUC() error {
	log.Println("Updater: Installing update via RAUC...")

	cmd := exec.Command("rauc", "install", raucBundlePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := strings.TrimSpace(string(output))
		if strings.Contains(outputStr, "was not provided by any .service files") {
			return fmt.Errorf("RAUC service not running (is rauc.service enabled?): %s", outputStr)
		}
		return fmt.Errorf("rauc install failed: %w (output: %s)", err, outputStr)
	}

	log.Printf("Updater: RAUC install completed: %s", strings.TrimSpace(string(output)))
	return nil
}

func (u *Updater) downloadFile(url, destination, expectedSHA256 string) error {
	client := &http.Client{Timeout: downloadTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// Download to temp file on /data
	// Any of the /tmp directories can't be used since we utilize tmpfs and they exceed the memory size
	tmp, err := os.CreateTemp("/data", ".rauc-bundle-*.raucb")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	// Download and hash simultaneously with progress logging
	hash := sha256.New()
	writer := io.MultiWriter(tmp, hash)
	progressReader := &progressReader{r: resp.Body, total: resp.ContentLength}

	bytesWritten, err := io.Copy(writer, progressReader)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write bundle: %w", err)
	}
	tmp.Close()

	// Verify checksum
	actualSHA256 := hex.EncodeToString(hash.Sum(nil))
	if actualSHA256 != expectedSHA256 {
		return fmt.Errorf("checksum mismatch: got %s, want %s", actualSHA256, expectedSHA256)
	}

	// Move verified file to destination
	if err := os.Rename(tmpPath, destination); err != nil {
		return fmt.Errorf("failed to move bundle: %w", err)
	}

	log.Printf("Updater: Downloaded and verified %d bytes (SHA256: %s)", bytesWritten, actualSHA256)
	return nil
}

func (u *Updater) setError(msg string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status = StatusError
	u.errorMsg = msg
	log.Printf("Updater: Error %s", msg)
}

type progressReader struct {
	r          io.Reader
	total      int64
	read       int64
	lastLogged int
}

// Read reads the response and logs the download progress on updates
func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read += int64(n)
	if p.total > 0 {
		if percent := int(p.read * 10 / p.total); percent > p.lastLogged {
			p.lastLogged = percent
			log.Printf("Updater: Download progress: %d%%", percent*10)
		}
	}
	return n, err
}
