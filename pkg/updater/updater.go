package updater

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/globals"
)

const firmwareEndpoint = "/firmware/observer"

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
	u.mu.Lock()
	u.status = StatusUpToDate
	u.errorMsg = ""
	u.mu.Unlock()

	relayDomain, ok := config.Get().GetKey("relayDomain")
	if !ok {
		log.Println("Skipping update check: relay domain not configured")
		return nil
	}

	resp, err := http.Get("https://" + relayDomain.(string) + firmwareEndpoint)
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

	if info.Version != globals.FirmwareVersion {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.status = StatusUpdateAvailable
		u.availableVersion = info.Version
		u.downloadURL = info.URL
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

	if err := u.downloadFile(downloadURL, globals.UpdateImagePath); err != nil {
		u.setError(fmt.Sprintf("download failed: %v", err))
		return err
	}

	u.mu.Lock()
	u.status = StatusInstalling
	u.mu.Unlock()

	if err := u.flashFirmware(); err != nil {
		u.setError(fmt.Sprintf("installation failed: %v", err))
		os.Remove(globals.UpdateImagePath)
		return err
	}

	os.Remove(globals.UpdateImagePath)

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
	activePartition, err := u.getActivePartition()
	if err != nil {
		return fmt.Errorf("failed to detect active partition: %w", err)
	}

	inactivePartition := u.getInactivePartition(activePartition)
	log.Printf("Flashing firmware to %s (active: %s)", inactivePartition, activePartition)

	// Flash firmware to inactive partition
	cmd := exec.Command("sudo", "dd", "if="+globals.UpdateImagePath, "of="+inactivePartition, "bs=4M", "conv=fsync")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("flash failed: %w (output: %s)", err, string(output))
	}

	// Switch boot partition
	if err := u.switchBootPartition(inactivePartition); err != nil {
		return fmt.Errorf("boot switch failed: %w", err)
	}

	log.Println("Firmware flashed and boot partition switched successfully")
	return nil
}

func (u *Updater) getActivePartition() (string, error) {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return "", fmt.Errorf("failed to read /proc/cmdline: %w", err)
	}

	matches := regexp.MustCompile(`root=(/dev/\S+)`).FindStringSubmatch(string(data))
	if len(matches) < 2 {
		return "", fmt.Errorf("could not detect root partition in cmdline")
	}

	partition := matches[1]

	// Validate it's one of our configured partitions
	if partition != globals.PartitionA && partition != globals.PartitionB {
		return "", fmt.Errorf("active partition %s is not one of configured partitions (%s, %s)",
			partition, globals.PartitionA, globals.PartitionB)
	}

	return partition, nil
}

func (u *Updater) getInactivePartition(active string) string {
	// Toggle between configured partitions
	if active == globals.PartitionA {
		return globals.PartitionB
	}
	return globals.PartitionA
}

func (u *Updater) switchBootPartition(newRoot string) error {
	data, err := os.ReadFile(globals.BootCmdlinePath)
	if err != nil {
		return fmt.Errorf("failed to read boot config: %w", err)
	}

	newCmdline := regexp.MustCompile(`root=/dev/\S+`).ReplaceAllString(string(data), "root="+newRoot)

	if err := os.WriteFile(globals.BootCmdlinePath, []byte(newCmdline), 0644); err != nil {
		return fmt.Errorf("failed to write boot config: %w", err)
	}

	log.Printf("Boot partition switched to %s", newRoot)
	return nil
}

func (u *Updater) setError(msg string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status = StatusError
	u.errorMsg = msg
}
