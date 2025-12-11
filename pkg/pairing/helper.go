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
	})
}

func GetHelper() *Pairing {
	if helperInstance == nil {
		panic("pairing helper not initialized - call InitHelper() first")
	}
	return helperInstance
}

// GetCode returns the current pairing code, generating a new one if needed
func (b *Pairing) GetCode() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Generate new code if expired or not set
	if b.code == nil || time.Now().After(b.code.ExpiresAt) {
		code := fmt.Sprintf("%06d", randomInt(0, 999999))
		b.code = &PairingCode{
			Code:      code,
			ExpiresAt: time.Now().Add(codeExpiry),
		}
		if b.onCodeSpeak != nil {
			go b.onCodeSpeak(code)
		}
	}

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

	// Add device with camera's private key for decrypting device messages
	if err := devices.Get().Add(deviceID, deviceName, devicePublicKey, keypair.PrivateKey); err != nil {
		return nil, fmt.Errorf("failed to add device: %w", err)
	}

	// Invalidate code after successful pairing
	b.code = nil

	// Get relay URL
	relayURL, _ := config.Get().GetKey("relayUrl")

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
