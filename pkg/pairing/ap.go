package pairing

import (
	"crypto/rand"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"root-firmware/pkg/config"
)

type AP struct {
	mu            sync.Mutex
	running       bool
	ssid          string
	hostapdCmd    *exec.Cmd
	dnsmasqCmd    *exec.Cmd
}

var apInstance *AP
var apOnce sync.Once

func InitAP() {
	apOnce.Do(func() {
		apInstance = &AP{}
	})
}

func GetAP() *AP {
	if apInstance == nil {
		panic("AP not initialized - call InitAP() first")
	}
	return apInstance
}

// Start creates a WiFi access point for pairing
func (w *AP) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.running {
		return nil
	}

	// Generate or retrieve SSID
	if w.ssid == "" {
		// Check if SSID is stored in config
		if storedSSID, ok := config.Get().GetKey("apSSID"); ok {
			w.ssid = storedSSID.(string)
		} else {
			// Generate new SSID with 4 random letters
			w.ssid = generateSSID()
			config.Get().SetKey("apSSID", w.ssid)
		}
	}

	// Stop any existing wpa_supplicant (client mode)
	exec.Command("sudo", "killall", "wpa_supplicant").Run()

	// Configure wlan0 with static IP
	if err := exec.Command("sudo", "ip", "addr", "flush", "dev", "wlan0").Run(); err != nil {
		return fmt.Errorf("failed to flush wlan0: %w", err)
	}

	if err := exec.Command("sudo", "ip", "addr", "add", "192.168.4.1/24", "dev", "wlan0").Run(); err != nil {
		return fmt.Errorf("failed to set IP: %w", err)
	}

	if err := exec.Command("sudo", "ip", "link", "set", "wlan0", "up").Run(); err != nil {
		return fmt.Errorf("failed to bring up wlan0: %w", err)
	}

	// Start dnsmasq for DHCP
	if err := w.startDNSMasq(); err != nil {
		return fmt.Errorf("failed to start dnsmasq: %w", err)
	}

	// Start hostapd for AP
	if err := w.startHostAPD(); err != nil {
		return fmt.Errorf("failed to start hostapd: %w", err)
	}

	w.running = true
	return nil
}

// Stop stops the WiFi access point
func (w *AP) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.running {
		return nil
	}

	var lastErr error

	// Stop hostapd process
	if w.hostapdCmd != nil && w.hostapdCmd.Process != nil {
		if err := w.hostapdCmd.Process.Kill(); err != nil {
			lastErr = fmt.Errorf("failed to kill hostapd: %w", err)
		}
		w.hostapdCmd = nil
	}

	// Stop dnsmasq process
	if w.dnsmasqCmd != nil && w.dnsmasqCmd.Process != nil {
		if err := w.dnsmasqCmd.Process.Kill(); err != nil {
			lastErr = fmt.Errorf("failed to kill dnsmasq: %w", err)
		}
		w.dnsmasqCmd = nil
	}

	// Bring down interface
	if err := exec.Command("sudo", "ip", "link", "set", "wlan0", "down").Run(); err != nil {
		lastErr = fmt.Errorf("failed to bring down wlan0: %w", err)
	}

	w.running = false
	return lastErr
}

func (w *AP) startDNSMasq() error {
	// Create dnsmasq config for DHCP (192.168.4.2 - 192.168.4.20)
	config := `interface=wlan0
dhcp-range=192.168.4.2,192.168.4.20,255.255.255.0,24h
domain=wlan
address=/root.camera/192.168.4.1
`
	// Write config to /tmp/dnsmasq.conf
	cmd := exec.Command("sudo", "tee", "/tmp/dnsmasq.conf")
	cmd.Stdin = strings.NewReader(config)
	if err := cmd.Run(); err != nil {
		return err
	}

	// Start dnsmasq in background
	w.dnsmasqCmd = exec.Command("sudo", "dnsmasq", "-C", "/tmp/dnsmasq.conf", "--no-daemon")
	return w.dnsmasqCmd.Start()
}

func (w *AP) startHostAPD() error {
	// Create hostapd config with dynamic SSID
	config := fmt.Sprintf(`interface=wlan0
driver=nl80211
ssid=%s
hw_mode=g
channel=7
wmm_enabled=0
macaddr_acl=0
auth_algs=1
ignore_broadcast_ssid=0
`, w.ssid)
	// Write config to /tmp/hostapd.conf
	cmd := exec.Command("sudo", "tee", "/tmp/hostapd.conf")
	cmd.Stdin = strings.NewReader(config)
	if err := cmd.Run(); err != nil {
		return err
	}

	// Start hostapd in background
	w.hostapdCmd = exec.Command("sudo", "hostapd", "/tmp/hostapd.conf")
	return w.hostapdCmd.Start()
}

// IsRunning returns whether AP mode is active
func (w *AP) IsRunning() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}

// generateSSID creates a random SSID in format "ROOT-Observer-XXXX"
func generateSSID() string {
	letters := make([]byte, 4)
	rand.Read(letters)

	// Convert to uppercase letters A-Z
	for i := range letters {
		letters[i] = 'A' + (letters[i] % 26)
	}

	return fmt.Sprintf("ROOT-Observer-%s", string(letters))
}
