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

// TODO: Solid detection which partition is used
// TODO: Clean fallback to other partition if software fails to start

const (
	firmwareEndpoint  = "/firmware/observer"
	updateImagePath   = "/tmp/firmware-update.img"
	inactivePartition = "/dev/mmcblk0p3" // TODO: Auto-detect inactive partition
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
	once.Do(func() {
		instance = &Updater{status: StatusUpToDate}
	})
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
	u.mu.Lock()
	u.status = StatusUpToDate
	u.errorMsg = ""
	u.mu.Unlock()

	relayDomain, ok := config.Get().GetKey("relayDomain")
	if !ok {
		log.Println("Skipping update check: relay domain not configured")
		return nil
	}

	url := "https://" + relayDomain.(string) + firmwareEndpoint

	resp, err := http.Get(url)
	if err != nil {
		u.setError(fmt.Sprintf("failed to check for updates: %v", err))
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		u.setError(fmt.Sprintf("server returned status %d", resp.StatusCode))
		return fmt.Errorf("server returned status %d", resp.StatusCode)
	}

	var info FirmwareInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		u.setError(fmt.Sprintf("failed to parse response: %v", err))
		return err
	}

	if info.Version != globals.FirmwareVersion {
		u.mu.Lock()
		u.status = StatusUpdateAvailable
		u.availableVersion = info.Version
		u.downloadURL = info.URL
		u.mu.Unlock()
	}

	return nil
}

func (u *Updater) StartUpdate() error {
	u.mu.Lock()
	if u.status != StatusUpdateAvailable {
		u.mu.Unlock()
		return fmt.Errorf("no update available")
	}
	u.status = StatusDownloading
	downloadURL := u.downloadURL
	u.mu.Unlock()

	if err := u.downloadFile(downloadURL, updateImagePath); err != nil {
		u.setError(fmt.Sprintf("download failed: %v", err))
		return err
	}

	u.mu.Lock()
	u.status = StatusInstalling
	u.mu.Unlock()

	if err := u.flashFirmware(); err != nil {
		u.setError(fmt.Sprintf("installation failed: %v", err))
		os.Remove(updateImagePath)
		return err
	}

	os.Remove(updateImagePath)

	go func() {
		time.Sleep(2 * time.Second)
		exec.Command("sudo", "reboot").Run()
	}()

	return nil
}

func (u *Updater) downloadFile(url, destination string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func (u *Updater) flashFirmware() error {
	cmd := exec.Command("sudo", "dd", "if="+updateImagePath, "of="+inactivePartition, "bs=4M", "status=progress")
	return cmd.Run()
}

func (u *Updater) setError(msg string) {
	u.mu.Lock()
	u.status = StatusError
	u.errorMsg = msg
	u.mu.Unlock()
}
