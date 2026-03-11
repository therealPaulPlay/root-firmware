package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
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
	scheduledFor     time.Time
	scheduleTimer    *time.Timer
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

func (u *Updater) GetStatus() (UpdateStatus, string, string, time.Time) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.status, u.availableVersion, u.errorMsg, u.scheduledFor
}

// CheckForUpdates queries the relay server for available firmware updates
func (u *Updater) CheckForUpdates() {
	u.mu.RLock()
	status := u.status
	u.mu.RUnlock()

	// Skip if update in progress
	if status == StatusDownloading || status == StatusInstalling {
		return
	}

	relayDomain, ok := config.Get().GetKey("relayDomain")
	if !ok {
		log.Println("Updater: Skipping update check: relay domain not configured")
		return
	}
	relayDomainStr, ok := relayDomain.(string)
	if !ok {
		log.Println("Updater: Skipping update check: relay domain has invalid type")
		return
	}

	client := &http.Client{Timeout: updateCheckTimeout}
	resp, err := client.Get("https://" + relayDomainStr + firmwareEndpoint)
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

	u.mu.Lock()

	// Re-check under write lock: status may have changed during the HTTP request
	// (e.g. a scheduled auto-update started while we were checking)
	if u.status == StatusDownloading || u.status == StatusInstalling {
		u.mu.Unlock()
		return
	}

	if info.Version != globals.FirmwareVersion {
		alreadyScheduled := u.availableVersion == info.Version && !u.scheduledFor.IsZero()
		u.status = StatusUpdateAvailable
		u.availableVersion = info.Version
		u.downloadURL = info.URL
		u.downloadSHA256 = info.SHA256
		u.errorMsg = ""

		// Schedule auto-update if not already scheduled for this version and not a dev build
		if !alreadyScheduled && globals.FirmwareVersion != "dev" {
			u.scheduleAutoUpdate()
		}
	} else {
		u.removeScheduledUpdate()
		u.status = StatusUpToDate
		u.errorMsg = ""
	}
	u.mu.Unlock()

	// Signal successful update check for boot confirmation tracking
	markUpdateCheckSuccessful()
}

// StartUpdate begins downloading and installing the available update via RAUC asynchronously
// Returns false if no update is available or an update is already in progress
func (u *Updater) StartUpdate() bool {
	u.mu.Lock()
	if u.status != StatusUpdateAvailable {
		u.mu.Unlock()
		return false
	}
	downloadURL := u.downloadURL
	expectedSHA256 := u.downloadSHA256
	log.Printf("Updater: Starting RAUC update to version %s", u.availableVersion)
	u.status = StatusDownloading

	// Stop the schedule timer but keep the persisted config value
	// so a power loss during update can recover it; cleared on success before reboot
	if u.scheduleTimer != nil {
		u.scheduleTimer.Stop()
		u.scheduleTimer = nil
	}
	u.scheduledFor = time.Time{}
	u.mu.Unlock()

	go u.performUpdate(downloadURL, expectedSHA256)
	return true
}

func (u *Updater) performUpdate(downloadURL, expectedSHA256 string) {
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
		return
	}

	// Install using RAUC
	u.mu.Lock()
	u.status = StatusInstalling
	u.mu.Unlock()

	if err := u.installWithRAUC(); err != nil {
		u.setError(fmt.Sprintf("RAUC installation failed: %v", err))
		os.Remove(raucBundlePath)
		return
	}

	// Clean up bundle, persisted schedule, and reboot
	os.Remove(raucBundlePath)
	if err := config.Get().SetKey("scheduledUpdateAt", nil); err != nil {
		log.Printf("Updater: Failed to clear scheduled update config: %v", err)
	}
	log.Println("Updater: RAUC update successful, rebooting in 2 seconds...")

	time.Sleep(2 * time.Second)
	if err := exec.Command("reboot").Run(); err != nil {
		log.Printf("Updater: Failed to reboot: %v", err)
	}
}

// installWithRAUC installs the downloaded bundle using the RAUC update framework
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

	log.Println("Updater: RAUC install completed successfully")
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

// scheduleAutoUpdate picks a random time between 5:00-8:00 AM local time, or recovers a persisted schedule
// mu must be held by the caller
func (u *Updater) scheduleAutoUpdate() {
	now := time.Now()
	scheduled := time.Time{}

	// Try to recover a persisted schedule (e.g. after power outage)
	if val, ok := config.Get().GetKey("scheduledUpdateAt"); ok {
		if ms, ok := val.(float64); ok {
			scheduled = time.UnixMilli(int64(ms))
		}
	}

	// Generate a new schedule if none persisted or still valid
	if scheduled.IsZero() || time.Until(scheduled) < 3*time.Hour {
		daysAhead := 1 // Push to next day (if e.g. missed persisted schedule)

		// If no persisted schedule, choose random day within next 3-6 days
		if scheduled.IsZero() {
			daysAhead = 3 + rand.Intn(4) // 0 - 3
		}
		offsetMinutes := rand.Intn(180) // random minute within 5:00-8:00 (180min window)
		scheduled = time.Date(now.Year(), now.Month(), now.Day()+daysAhead, 5, offsetMinutes, 0, 0, now.Location())
	}

	if u.scheduleTimer != nil {
		u.scheduleTimer.Stop()
	}

	u.scheduledFor = scheduled
	if err := config.Get().SetKey("scheduledUpdateAt", scheduled.UnixMilli()); err != nil {
		log.Printf("Updater: Failed to persist scheduled update: %v", err)
	}

	u.scheduleTimer = time.AfterFunc(time.Until(scheduled), func() {
		log.Println("Updater: Starting scheduled auto-update")
		u.StartUpdate()
	})
}

// RemoveScheduledUpdateWithLock cancels a pending scheduled auto-update
func (u *Updater) RemoveScheduledUpdateWithLock() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.removeScheduledUpdate()
}

// removeScheduledUpdate is the internal version; mu must be held by the caller
func (u *Updater) removeScheduledUpdate() {
	if u.scheduleTimer != nil {
		u.scheduleTimer.Stop()
		u.scheduleTimer = nil
	}
	u.scheduledFor = time.Time{}
	if err := config.Get().SetKey("scheduledUpdateAt", nil); err != nil {
		log.Printf("Updater: Failed to clear scheduled update config: %v", err)
	}
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
