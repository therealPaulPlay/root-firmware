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
	"sync"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/globals"
)

const (
	firmwareEndpoint   = "/firmware/observer"
	updateCheckTimeout = 10 * time.Second  // Timeout for checking update availability
	downloadTimeout    = 30 * time.Minute  // Timeout for downloading firmware
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
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
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

func (u *Updater) CheckForUpdates() error {
	relayDomain, ok := config.Get().GetKey("relayDomain")
	if !ok {
		log.Println("Skipping update check: relay domain not configured")
		return nil
	}

	client := &http.Client{Timeout: updateCheckTimeout}
	resp, err := client.Get("https://" + relayDomain.(string) + firmwareEndpoint)
	if err != nil {
		u.setError(fmt.Sprintf("failed to check for updates: %v", err))
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("server returned status %d", resp.StatusCode)
		u.setError(err.Error())
		return err
	}

	var info FirmwareInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		u.setError(fmt.Sprintf("failed to parse response: %v", err))
		return err
	}

	// Validate SHA256 format (64 hex chars)
	if len(info.SHA256) != 64 {
		err := fmt.Errorf("invalid SHA256 length: %d", len(info.SHA256))
		u.setError(err.Error())
		return err
	}
	if _, err := hex.DecodeString(info.SHA256); err != nil {
		u.setError(fmt.Sprintf("invalid SHA256 format: %v", err))
		return err
	}

	u.mu.Lock()
	defer u.mu.Unlock()

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

	return nil
}

func (u *Updater) StartUpdate() error {
	u.mu.Lock()
	if u.status != StatusUpdateAvailable {
		u.mu.Unlock()
		return fmt.Errorf("no update available")
	}
	downloadURL := u.downloadURL
	expectedSHA256 := u.downloadSHA256
	log.Printf("Starting firmware update to version %s", u.availableVersion)
	u.status = StatusDownloading
	u.mu.Unlock()

	// Download and verify firmware
	if err := u.downloadFile(downloadURL, globals.UpdateImagePath, expectedSHA256); err != nil {
		u.setError(fmt.Sprintf("download failed: %v", err))
		os.Remove(globals.UpdateImagePath)
		return err
	}

	// Flash to inactive partition
	u.mu.Lock()
	u.status = StatusInstalling
	u.mu.Unlock()

	if err := u.flashFirmware(); err != nil {
		u.setError(fmt.Sprintf("installation failed: %v", err))
		os.Remove(globals.UpdateImagePath)
		return err
	}

	// Clean up and schedule reboot
	os.Remove(globals.UpdateImagePath)
	log.Println("Update successful, rebooting in 2 seconds...")

	go func() {
		time.Sleep(2 * time.Second)
		if err := exec.Command("sudo", "reboot").Run(); err != nil {
			log.Printf("Failed to reboot: %v", err)
		}
	}()

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

	// Download to temp file first
	tmp, err := os.CreateTemp("", "firmware-*.img")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	// Download and hash simultaneously
	hash := sha256.New()
	writer := io.MultiWriter(tmp, hash)

	bytesWritten, err := io.Copy(writer, resp.Body)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write firmware: %w", err)
	}
	tmp.Close()

	// Verify checksum
	actualSHA256 := hex.EncodeToString(hash.Sum(nil))
	if actualSHA256 != expectedSHA256 {
		return fmt.Errorf("checksum mismatch: got %s, want %s", actualSHA256, expectedSHA256)
	}

	// Move verified file to destination
	if err := os.Rename(tmpPath, destination); err != nil {
		return fmt.Errorf("failed to move firmware: %w", err)
	}

	log.Printf("Downloaded and verified %d bytes (SHA256: %s)", bytesWritten, actualSHA256)
	return nil
}

func (u *Updater) flashFirmware() error {
	activePartition, err := getActivePartition()
	if err != nil {
		return fmt.Errorf("failed to detect active partition: %w", err)
	}

	inactivePartition := getInactivePartition(activePartition)
	log.Printf("Flashing firmware to %s (active: %s)", inactivePartition, activePartition)

	// Flash firmware to inactive partition
	cmd := exec.Command("sudo", "dd", "if="+globals.UpdateImagePath, "of="+inactivePartition, "bs=4M", "conv=fsync")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("flash failed: %w (output: %s)", err, string(output))
	}

	// Switch boot partition
	if err := switchBootPartition(inactivePartition); err != nil {
		return fmt.Errorf("boot switch failed: %w", err)
	}

	// Set boot counter for automatic rollback if new firmware fails
	if err := setBootCounter(); err != nil {
		log.Printf("Warning: failed to set boot counter: %v", err)
	}

	log.Println("Firmware flashed and boot partition switched successfully")
	return nil
}

func (u *Updater) setError(msg string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status = StatusError
	u.errorMsg = msg
}
