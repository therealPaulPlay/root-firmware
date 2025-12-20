package wifi

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"root-firmware/pkg/globals"
)

type Network struct {
	SSID        string `json:"ssid"`
	Signal      int    `json:"signal"` // 0-100
	Secured     bool   `json:"secured"`
	Unsupported bool   `json:"unsupported"` // 5GHz networks (The hardware doesn't support it)
}

type WiFi struct {
	mu           sync.Mutex
	supports5GHz bool
}

var instance *WiFi
var once sync.Once

func Init() {
	once.Do(func() {
		instance = &WiFi{}
		instance.detectCapabilities()
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

	return w.parseNetworks(string(output)), nil
}

// Connect connects to a WiFi network and verifies internet access
// password should be empty string for unsecured networks
func (w *WiFi) Connect(ssid, password string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Validate SSID length (IEEE 802.11 spec: 0-32 bytes)
	if len(ssid) == 0 || len(ssid) > 32 {
		return fmt.Errorf("invalid SSID length (must be 1-32 bytes)")
	}

	var config []byte
	var err error

	if password == "" {
		// Unsecured network - write config directly without shell escaping
		// SSID is written as hex string to avoid any injection issues
		config = []byte(fmt.Sprintf(`network={
	ssid="%s"
	key_mgmt=NONE
}
`, sanitizeSSID(ssid)))
	} else {
		// Secured network - use wpa_passphrase which properly escapes everything
		// Both SSID and password are passed safely (no shell interpretation)
		cmd := exec.Command("wpa_passphrase", ssid)
		cmd.Stdin = strings.NewReader(password)
		config, err = cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to generate config: %w", err)
		}
	}

	// Append to wpa_supplicant.conf
	f, err := os.OpenFile(globals.WpaSupplicantPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open wpa_supplicant.conf: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(config); err != nil {
		return fmt.Errorf("failed to write wpa config: %w", err)
	}

	// Reconfigure wpa_supplicant
	if err := exec.Command("wpa_cli", "-i", "wlan0", "reconfigure").Run(); err != nil {
		return fmt.Errorf("failed to reconfigure: %w", err)
	}

	// Wait up to 15 seconds for connection with internet access
	for range 15 {
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

// detectCapabilities detects if the WiFi hardware supports 5GHz
func (w *WiFi) detectCapabilities() {
	output, err := exec.Command("iwlist", "wlan0", "freq").Output()
	if err != nil {
		w.supports5GHz = false
		log.Println("WiFi initialized - 5GHz support: unknown (detection failed)")
		return
	}

	freqRe := regexp.MustCompile(`:\s*([\d.]+)\s*GHz`)
	for _, match := range freqRe.FindAllStringSubmatch(string(output), -1) {
		var freq float64
		fmt.Sscanf(match[1], "%f", &freq)
		if freq > 5.0 {
			w.supports5GHz = true
			log.Println("WiFi initialized - 5GHz support: yes")
			return
		}
	}

	w.supports5GHz = false
	log.Println("WiFi initialized - 5GHz support: no")
}

// sanitizeSSID escapes special characters in SSID for safe use in wpa_supplicant.conf
// According to wpa_supplicant.conf spec, SSIDs with special chars should use backslash escaping
func sanitizeSSID(ssid string) string {
	// Replace backslash first to avoid double-escaping
	result := strings.ReplaceAll(ssid, `\`, `\\`)
	// Escape double quotes
	result = strings.ReplaceAll(result, `"`, `\"`)
	// Escape newlines (would break config file format)
	result = strings.ReplaceAll(result, "\n", `\n`)
	result = strings.ReplaceAll(result, "\r", `\r`)
	// Escape tab
	result = strings.ReplaceAll(result, "\t", `\t`)
	return result
}

func (w *WiFi) parseNetworks(output string) []Network {
	var networks []Network

	ssidRe := regexp.MustCompile(`ESSID:"([^"]+)"`)
	qualityRe := regexp.MustCompile(`Quality=(\d+)/(\d+)`)
	encryptionRe := regexp.MustCompile(`Encryption key:(on|off)`)
	frequencyRe := regexp.MustCompile(`Frequency:([\d.]+) GHz`)

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

		// Parse frequency and mark as unsupported if hardware doesn't support it
		if freqMatch := frequencyRe.FindStringSubmatch(cell); len(freqMatch) > 1 {
			var freq float64
			fmt.Sscanf(freqMatch[1], "%f", &freq)
			// Mark 5GHz networks as unsupported if hardware doesn't support 5GHz
			network.Unsupported = freq > 3.0 && !w.supports5GHz
		}

		networks = append(networks, network)
	}

	return networks
}
