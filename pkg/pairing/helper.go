package pairing

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/devices"
	"root-firmware/pkg/encryption"
	"root-firmware/pkg/wifi"
)

const codeExpiry = 5 * time.Minute

type PairingCode struct {
	Code      string
	ExpiresAt time.Time
}

type Pairing struct {
	mu          sync.Mutex
	code        *PairingCode
	onCodeSpeak func(string)
}

var helperInstance *Pairing
var helperOnce sync.Once

func InitHelper(onCodeSpeak func(string)) {
	helperOnce.Do(func() {
		helperInstance = &Pairing{
			onCodeSpeak: onCodeSpeak,
		}
		helperInstance.generateCode()
	})
}

func GetHelper() *Pairing {
	if helperInstance == nil {
		panic("pairing helper not initialized - call InitHelper() first")
	}
	return helperInstance
}

func (b *Pairing) generateCode() {
	code := fmt.Sprintf("%06d", randomInt(0, 999999))
	b.code = &PairingCode{
		Code:      code,
		ExpiresAt: time.Now().Add(codeExpiry),
	}

	if b.onCodeSpeak != nil {
		go b.onCodeSpeak(code)
	}
}

// RefreshCode generates and returns a new pairing code
func (b *Pairing) RefreshCode() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.generateCode()
	return b.code.Code
}

// PairDevice pairs a device using the pairing code
func (b *Pairing) PairDevice(deviceID, deviceName, code string, devicePublicKey []byte) (map[string]interface{}, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Verify code
	if b.code == nil || b.code.Code != code || time.Now().After(b.code.ExpiresAt) {
		return nil, fmt.Errorf("invalid or expired code")
	}

	// Generate keypair for key exchange
	keypair, err := encryption.GenerateKeypair()
	if err != nil {
		return nil, fmt.Errorf("failed to generate keys: %w", err)
	}

	// Add device
	if err := devices.Get().Add(deviceID, deviceName, devicePublicKey); err != nil {
		return nil, fmt.Errorf("failed to add device: %w", err)
	}

	// Store keypair for this device (for key rotation)
	// TODO: Store keypair.PrivateKey associated with deviceID

	// Generate new code for next pairing
	b.generateCode()

	// Get relay URL (nil if not configured)
	var relayURL interface{} = nil
	if val, ok := config.Get().GetKey("relayUrl"); ok {
		relayURL = val
	}

	// Scan for WiFi networks
	networks, _ := wifi.Get().Scan()

	return map[string]interface{}{
		"cameraPublicKey":   keypair.PublicKey,
		"wifiConnected":     wifi.Get().IsConnected(),
		"relayUrl":          relayURL,
		"availableNetworks": networks,
	}, nil
}

func randomInt(min, max int) int {
	b := make([]byte, 4)
	rand.Read(b)
	n := int(b[0]) | int(b[1])<<8 | int(b[2])<<16 | int(b[3])<<24
	if n < 0 {
		n = -n
	}
	return min + (n % (max - min + 1))
}
