package wifi

import (
	"fmt"
	"log"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"root-firmware/pkg/config"
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

	exec.Command("nmcli", "device", "wifi", "rescan").Run()
	time.Sleep(3 * time.Second)

	output, err := exec.Command("nmcli", "-t", "-f", "SSID,SIGNAL,SECURITY,FREQ", "device", "wifi", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("scan failed: %w", err)
	}

	return w.parseNetworks(string(output)), nil
}

func (w *WiFi) applyStoredWiFiConfig() {
	ssidVal, hasSSID := config.Get().GetKey("wifiSSID")
	passwordVal, hasPassword := config.Get().GetKey("wifiPassword")

	if !hasSSID || !hasPassword {
		return
	}

	ssid, ok1 := ssidVal.(string)
	password, ok2 := passwordVal.(string)
	if !ok1 || !ok2 {
		log.Println("WiFi: Invalid config format")
		return
	}

	if countryCodeVal, ok := config.Get().GetKey("wifiCountryCode"); ok {
		if code, ok := countryCodeVal.(string); ok && len(code) == 2 {
			exec.Command("iw", "reg", "set", strings.ToUpper(code)).Run()
		}
	}

	go func() {
		w.mu.Lock()
		defer w.mu.Unlock()

		// Activate existing connection, or create new one if none exists (e.g. after update)
		if exec.Command("nmcli", "connection", "show", ssid).Run() == nil {
			if exec.Command("nmcli", "connection", "up", ssid).Run() == nil {
				log.Printf("WiFi: Connected to %s", ssid)
			} else {
				log.Printf("WiFi: Failed to connect to %s (network unavailable)", ssid)
			}
		} else {
			log.Printf("WiFi: Creating connection from configuration")
			if err := w.connectNetwork(ssid, password); err != nil {
				log.Printf("WiFi: Failed to connect to stored network: %v", err)
			} else {
				log.Printf("WiFi: Connected to %s", ssid)
			}
		}
	}()
}

// Connect connects to a network and verifies internet access (User-initiated)
func (w *WiFi) Connect(ssid, password, countryCode string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(ssid) == 0 || len(ssid) > 32 {
		return fmt.Errorf("invalid SSID length (must be 1-32 bytes)")
	}

	// Remember previous state for rollback
	previousSSID := w.getActiveSSID()
	previousRegDomain := w.getActiveRegDomain()

	if countryCode != "" {
		exec.Command("iw", "reg", "set", strings.ToUpper(countryCode)).Run()
	}

	// Delete any existing connection profile to avoid stale security settings
	exec.Command("nmcli", "connection", "delete", ssid).Run()

	if err := w.connectNetwork(ssid, password); err != nil {
		if countryCode != "" {
			exec.Command("iw", "reg", "set", previousRegDomain).Run()
		}
		return err
	}

	if err := w.waitForInternet(15 * time.Second); err != nil {
		// Connection failed - delete this connection and try to restore previous
		exec.Command("nmcli", "connection", "delete", ssid).Run()

		if countryCode != "" {
			exec.Command("iw", "reg", "set", previousRegDomain).Run()
		}
		if previousSSID != "" && previousSSID != ssid {
			log.Printf("WiFi: Reverting to %s", previousSSID)
			exec.Command("nmcli", "connection", "up", previousSSID).Run()
		}
		return err
	}

	// Save credentials after successful connection
	config.Get().SetKey("wifiSSID", ssid)
	config.Get().SetKey("wifiPassword", password)
	if countryCode != "" {
		config.Get().SetKey("wifiCountryCode", countryCode)
	}

	log.Printf("WiFi: Connected to %s", ssid)
	return nil
}

func (w *WiFi) connectNetwork(ssid, password string) error {
	var cmd *exec.Cmd
	if password == "" {
		cmd = exec.Command("nmcli", "--wait", "15", "device", "wifi", "connect", ssid)
	} else {
		cmd = exec.Command("nmcli", "--wait", "15", "device", "wifi", "connect", ssid, "password", password)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("connection failed: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func (w *WiFi) waitForInternet(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if exec.Command("ping", "-c", "1", "-W", "2", "8.8.8.8").Run() == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("no internet connection")
}

func (w *WiFi) IsConnected() bool {
	return w.getActiveSSID() != ""
}

func (w *WiFi) GetCurrentNetwork() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.getActiveSSID()
}

func (w *WiFi) getActiveSSID() string {
	output, err := exec.Command("nmcli", "-t", "-f", "NAME,DEVICE", "connection", "show", "--active").Output()
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(output), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && parts[1] == "wlan0" {
			return parts[0]
		}
	}
	return ""
}

func (w *WiFi) getActiveRegDomain() string {
	output, _ := exec.Command("iw", "reg", "get").Output()
	for line := range strings.SplitSeq(string(output), "\n") {
		if code, found := strings.CutPrefix(line, "country "); found && len(code) >= 2 {
			// "country DE: DFS-ETSI" -> "DE"
			return code[0:2]
		}
	}
	return "00" // World regulatory domain as fallback
}

func (w *WiFi) parseNetworks(output string) []Network {
	var networks []Network
	seen := make(map[string]int)

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Format: SSID:SIGNAL:SECURITY:FREQ (SSID may contain colons)
		parts := strings.Split(line, ":")
		if len(parts) < 4 {
			continue
		}

		security := parts[len(parts)-2]
		signalStr := parts[len(parts)-3]
		ssid := strings.Join(parts[:len(parts)-3], ":")

		if ssid == "" || ssid == "--" {
			continue
		}

		network := Network{SSID: ssid}

		if signal, err := strconv.Atoi(signalStr); err == nil {
			network.Signal = signal
		}

		network.Secured = security != "" && security != "--"

		if idx, exists := seen[network.SSID]; exists {
			if network.Signal > networks[idx].Signal {
				networks[idx] = network
			}
		} else {
			seen[network.SSID] = len(networks)
			networks = append(networks, network)
		}
	}

	// Sort networks by signal strength, strongest first
	sort.Slice(networks, func(i, j int) bool {
		return networks[i].Signal > networks[j].Signal
	})

	return networks
}
