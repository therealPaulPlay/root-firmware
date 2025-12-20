package updater

import (
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
}

type Updater struct {
	mu               sync.RWMutex
	status           UpdateStatus
	availableVersion string
	downloadURL      string
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

	u.mu.Lock()
	defer u.mu.Unlock()

	if info.Version != globals.FirmwareVersion {
		u.status = StatusUpdateAvailable
		u.availableVersion = info.Version
		u.downloadURL = info.URL
		u.errorMsg = ""
	} else {
		// Only clear error if we successfully checked and are up-to-date
		if u.status != StatusUpdateAvailable {
			u.status = StatusUpToDate
			u.errorMsg = ""
		}
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
	u.status = StatusDownloading
	u.mu.Unlock()

	log.Printf("Starting firmware update to version %s", u.availableVersion)

	// Download firmware
	if err := u.downloadFile(downloadURL, globals.UpdateImagePath); err != nil {
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

func (u *Updater) downloadFile(url, destination string) error {
	client := &http.Client{Timeout: downloadTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	out, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer out.Close()

	bytesWritten, err := io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write firmware: %w", err)
	}

	log.Printf("Downloaded %d bytes to %s", bytesWritten, destination)
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
