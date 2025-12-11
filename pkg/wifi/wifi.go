package wifi

import (
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Network struct {
	SSID    string `json:"ssid"`
	Signal  int    `json:"signal"` // 0-100
	Secured bool   `json:"secured"`
}

type WiFi struct {
	mu sync.Mutex
}

var instance *WiFi
var once sync.Once

func Init() {
	once.Do(func() {
		instance = &WiFi{}
	})
}

func Get() *WiFi {
	if instance == nil {
		panic("wifi not initialized - call Init() first")
	}
	return instance
}

// Scan scans for available WiFi networks
func (w *WiFi) Scan() ([]Network, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	exec.Command("sudo", "iwlist", "wlan0", "scan").Run() // Trigger scan

	// Read scan results
	output, err := exec.Command("sudo", "iwlist", "wlan0", "scan").Output()
	if err != nil {
		return nil, fmt.Errorf("scan failed: %w", err)
	}

	return parseNetworks(string(output)), nil
}

// Connect connects to a WiFi network and verifies internet access
// password should be empty string for unsecured networks
func (w *WiFi) Connect(ssid, password string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Escape SSID for safe use in shell
	escapedSSID := strings.ReplaceAll(ssid, `\`, `\\`)
	escapedSSID = strings.ReplaceAll(escapedSSID, `"`, `\"`)

	var config []byte
	var err error

	if password == "" {
		// Unsecured network
		config = []byte(fmt.Sprintf(`network={
	ssid="%s"
	key_mgmt=NONE
}`, escapedSSID))
	} else {
		// Secured network - use wpa_passphrase with escaped SSID
		// Pass password via stdin to avoid command injection
		cmd := exec.Command("wpa_passphrase", escapedSSID)
		cmd.Stdin = strings.NewReader(password)
		config, err = cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to generate config: %w", err)
		}
	}

	// Append to wpa_supplicant.conf using stdin to avoid injection
	appendCmd := exec.Command("sudo", "tee", "-a", "/etc/wpa_supplicant/wpa_supplicant.conf")
	appendCmd.Stdin = bytes.NewReader(config)
	appendCmd.Stdout = nil // Discard output
	if err := appendCmd.Run(); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Reconfigure wpa_supplicant
	if err := exec.Command("wpa_cli", "-i", "wlan0", "reconfigure").Run(); err != nil {
		return fmt.Errorf("failed to reconfigure: %w", err)
	}

	// Wait up to 15 seconds for connection with internet access
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)

		// Check if connected and has internet
		output, err := exec.Command("iwgetid", "-r").Output()
		if err != nil || len(strings.TrimSpace(string(output))) == 0 {
			continue // Not connected yet
		}

		// Ping Google DNS to verify internet
		if exec.Command("ping", "-c", "1", "-W", "2", "8.8.8.8").Run() == nil {
			return nil // Success!
		}
	}

	return fmt.Errorf("failed to establish internet connection")
}

// IsConnected checks if connected to any network
func (w *WiFi) IsConnected() bool {
	output, err := exec.Command("iwgetid", "-r").Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(output))) > 0
}

// GetCurrentNetwork returns the currently connected network SSID
func (w *WiFi) GetCurrentNetwork() string {
	output, err := exec.Command("iwgetid", "-r").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func parseNetworks(output string) []Network {
	var networks []Network

	ssidRe := regexp.MustCompile(`ESSID:"([^"]+)"`)
	qualityRe := regexp.MustCompile(`Quality=(\d+)/(\d+)`)
	encryptionRe := regexp.MustCompile(`Encryption key:(on|off)`)

	for _, cell := range strings.Split(output, "Cell ")[1:] {
		ssidMatch := ssidRe.FindStringSubmatch(cell)
		if len(ssidMatch) < 2 {
			continue
		}

		network := Network{SSID: ssidMatch[1]}

		// Parse signal quality
		if qualityMatch := qualityRe.FindStringSubmatch(cell); len(qualityMatch) > 2 {
			var quality, max int
			fmt.Sscanf(qualityMatch[1], "%d", &quality)
			fmt.Sscanf(qualityMatch[2], "%d", &max)
			if max > 0 {
				network.Signal = (quality * 100) / max
			}
		}

		// Parse encryption
		if encMatch := encryptionRe.FindStringSubmatch(cell); len(encMatch) > 1 {
			network.Secured = encMatch[1] == "on"
		}

		networks = append(networks, network)
	}

	return networks
}
