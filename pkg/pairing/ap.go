package pairing

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

type AP struct {
	mu      sync.Mutex
	running bool
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
// SSID will be "Root-Camera" and IP will be 192.168.4.1
func (w *AP) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.running {
		return nil
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

	// Stop hostapd and dnsmasq
	exec.Command("sudo", "killall", "hostapd").Run()
	exec.Command("sudo", "killall", "dnsmasq").Run()

	// Bring down interface
	exec.Command("sudo", "ip", "link", "set", "wlan0", "down").Run()

	w.running = false
	return nil
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

	// Start dnsmasq
	return exec.Command("sudo", "dnsmasq", "-C", "/tmp/dnsmasq.conf").Run()
}

func (w *AP) startHostAPD() error {
	// Create hostapd config
	config := `interface=wlan0
driver=nl80211
ssid=Root-Camera
hw_mode=g
channel=7
wmm_enabled=0
macaddr_acl=0
auth_algs=1
ignore_broadcast_ssid=0
`
	// Write config to /tmp/hostapd.conf
	cmd := exec.Command("sudo", "tee", "/tmp/hostapd.conf")
	cmd.Stdin = strings.NewReader(config)
	if err := cmd.Run(); err != nil {
		return err
	}

	// Start hostapd in background
	cmd = exec.Command("sudo", "hostapd", "/tmp/hostapd.conf")
	return cmd.Start() // Start in background
}

// IsRunning returns whether AP mode is active
func (w *AP) IsRunning() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}
