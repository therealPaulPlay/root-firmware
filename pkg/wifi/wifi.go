package wifi

import (
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"root-firmware/pkg/config"
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
		instance.applyStoredWiFiConfig()
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

// applyStoredWiFiConfig applies WiFi configuration from config.json on boot
func (w *WiFi) applyStoredWiFiConfig() {
	ssidVal, hasSSID := config.Get().GetKey("wifiSSID")
	passwordVal, hasPassword := config.Get().GetKey("wifiPassword")

	if !hasSSID || !hasPassword {
		return // No WiFi configured
	}

	ssid, ok1 := ssidVal.(string)
	password, ok2 := passwordVal.(string)
	if !ok1 || !ok2 {
		log.Println("WiFi: Invalid WiFi config format")
		return
	}

	// Apply country code if set (skip otherwise)
	if countryCodeVal, ok := config.Get().GetKey("wifiCountryCode"); ok {
		if code, ok := countryCodeVal.(string); ok && len(code) == 2 {
			if err := w.setCountryCode(code); err != nil {
				log.Printf("WiFi: Failed to set country code: %v", err)
			}
		}
	}

	// Configure stored WiFi (wpa_supplicant will auto-reconnect)
	go func() {
		if _, err := w.configureNetwork(ssid, password); err != nil {
			log.Printf("WiFi: Failed to configure network: %v", err)
		}
	}()
}

// setCountryCode sets the WiFi regulatory domain country code
// countryCode should be ISO 3166-1 alpha-2 format (e.g., "US", "GB", "DE")
func (w *WiFi) setCountryCode(countryCode string) error {
	if len(countryCode) != 2 {
		return fmt.Errorf("invalid country code format (must be 2 letters)")
	}
	countryCode = strings.ToUpper(countryCode)

	if err := exec.Command("sudo", "iw", "reg", "set", countryCode).Run(); err != nil {
		return fmt.Errorf("failed to set country code: %w", err)
	}

	log.Printf("WiFi: Country code set to %s", countryCode)
	return nil
}

// Connect connects to a WiFi network and verifies internet access
// password should be empty string for unsecured networks
// countryCode is optional ISO 3166-1 alpha-2 code
func (w *WiFi) Connect(ssid, password, countryCode string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Validate SSID length (IEEE 802.11 spec: 0-32 bytes)
	if len(ssid) == 0 || len(ssid) > 32 {
		return fmt.Errorf("invalid SSID length (must be 1-32 bytes)")
	}

	// Set country code if provided
	if countryCode != "" {
		if err := w.setCountryCode(countryCode); err != nil {
			log.Printf("WiFi: Warning - failed to set country code: %v", err)
		}
	}

	// Configure network and get the network ID
	networkID, err := w.configureNetwork(ssid, password)
	if err != nil {
		return err
	}

	// Wait for connection and verify internet access
	if err := w.waitForInternet(ssid, 15*time.Second); err != nil {
		w.removeNetwork(networkID)

		// Revert to saved network if different from the one that just failed
		if savedSSID, ok := config.Get().GetKey("wifiSSID"); ok && savedSSID.(string) != ssid {
			if savedPassword, ok := config.Get().GetKey("wifiPassword"); ok {
				log.Printf("WiFi: Reverting to saved network: %s", savedSSID)
				if _, revertErr := w.configureNetwork(savedSSID.(string), savedPassword.(string)); revertErr != nil {
					log.Printf("WiFi: Failed to revert to saved network: %v", revertErr)
				}
			}
		}

		return err
	}

	// Only save credentials after successful internet verification
	if err := config.Get().SetKey("wifiSSID", ssid); err != nil {
		return fmt.Errorf("failed to save WiFi SSID: %w", err)
	}
	if err := config.Get().SetKey("wifiPassword", password); err != nil {
		return fmt.Errorf("failed to save WiFi password: %w", err)
	}
	if countryCode != "" {
		if err := config.Get().SetKey("wifiCountryCode", countryCode); err != nil {
			log.Printf("WiFi: Warning - failed to save country code: %v", err)
		}
	}

	return nil
}

// configureNetwork configures a WiFi network using wpa_cli and returns the network ID
func (w *WiFi) configureNetwork(ssid, password string) (string, error) {
	// Add network via wpa_cli
	output, err := exec.Command("wpa_cli", "-i", "wlan0", "add_network").Output()
	if err != nil {
		return "", fmt.Errorf("failed to add network: %w", err)
	}
	networkID := strings.TrimSpace(string(output))

	// Set SSID
	if err := exec.Command("wpa_cli", "-i", "wlan0", "set_network", networkID, "ssid", fmt.Sprintf(`"%s"`, ssid)).Run(); err != nil {
		return "", fmt.Errorf("failed to set SSID: %w", err)
	}

	// Set password or key_mgmt=NONE for open networks
	if password == "" {
		if err := exec.Command("wpa_cli", "-i", "wlan0", "set_network", networkID, "key_mgmt", "NONE").Run(); err != nil {
			return "", fmt.Errorf("failed to set key_mgmt: %w", err)
		}
	} else {
		if err := exec.Command("wpa_cli", "-i", "wlan0", "set_network", networkID, "psk", fmt.Sprintf(`"%s"`, password)).Run(); err != nil {
			return "", fmt.Errorf("failed to set password: %w", err)
		}
	}

	// Enable the network
	if err := exec.Command("wpa_cli", "-i", "wlan0", "enable_network", networkID).Run(); err != nil {
		return "", fmt.Errorf("failed to enable network: %w", err)
	}

	// Select this network
	if err := exec.Command("wpa_cli", "-i", "wlan0", "select_network", networkID).Run(); err != nil {
		return "", fmt.Errorf("failed to select network: %w", err)
	}

	return networkID, nil
}

// removeNetwork removes a specific network by ID
// Don't call reconnect here, the caller will explicitly reconfigure the saved network if available
func (w *WiFi) removeNetwork(networkID string) {
	if err := exec.Command("wpa_cli", "-i", "wlan0", "remove_network", networkID).Run(); err != nil {
		log.Printf("WiFi: Failed to remove network %s: %v", networkID, err)
	}
}

// waitForInternet waits for internet connectivity up to the specified timeout
func (w *WiFi) waitForInternet(ssid string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(1 * time.Second)

		// Check if connected
		output, err := exec.Command("iwgetid", "-r").Output()
		if err != nil || len(strings.TrimSpace(string(output))) == 0 {
			continue // Not connected yet
		}

		// Ping Google DNS to verify internet
		if exec.Command("ping", "-c", "1", "-W", "2", "8.8.8.8").Run() == nil {
			log.Printf("WiFi: Connected to %s", ssid)
			return nil
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
		log.Println("WiFi: 5GHz support is unknown (detection failed)")
		return
	}

	freqRe := regexp.MustCompile(`:\s*([\d.]+)\s*GHz`)
	for _, match := range freqRe.FindAllStringSubmatch(string(output), -1) {
		var freq float64
		fmt.Sscanf(match[1], "%f", &freq)
		if freq > 5.0 {
			w.supports5GHz = true
			log.Println("WiFi: 5GHz is supported")
			return
		}
	}

	w.supports5GHz = false
	log.Println("WiFi: 5GHz is not supported")
}

func (w *WiFi) parseNetworks(output string) []Network {
	var networks []Network
	seen := make(map[string]int) // SSID -> index in networks slice

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

		// Deduplicate networks with identical SSID (e.g. multiple channels): keep entry with strongest signal
		if idx, exists := seen[network.SSID]; exists {
			if network.Signal > networks[idx].Signal {
				networks[idx] = network
			}
		} else {
			seen[network.SSID] = len(networks)
			networks = append(networks, network)
		}
	}

	// Sort networks by signal strength (strongest first)
	sort.Slice(networks, func(i, j int) bool {
		return networks[i].Signal > networks[j].Signal
	})

	return networks
}
