package bluetooth

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/encryption"
)

const (
	codeExpiry = 5 * time.Minute
	kickDelay  = 5 * time.Minute
)

type Device struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	PublicKey   []byte    `json:"publicKey"`
	ConnectedAt time.Time `json:"connectedAt"`
}

type PairingCode struct {
	Code      string
	ExpiresAt time.Time
}

type Bluetooth struct {
	mu          sync.Mutex
	code        *PairingCode
	kickTimers  map[string]*time.Timer
	onCodeSpeak func(string)
}

var instance *Bluetooth
var once sync.Once

func Init(onCodeSpeak func(string)) {
	once.Do(func() {
		instance = &Bluetooth{
			kickTimers:  make(map[string]*time.Timer),
			onCodeSpeak: onCodeSpeak,
		}
		instance.generateCode()
	})
}

func Get() *Bluetooth {
	if instance == nil {
		panic("bluetooth not initialized - call Init() first")
	}
	return instance
}

func (b *Bluetooth) generateCode() {
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
func (b *Bluetooth) RefreshCode() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.generateCode()
	return b.code.Code
}

// PairDevice pairs a device using the pairing code
func (b *Bluetooth) PairDevice(deviceID, deviceName, code string, devicePublicKey []byte) (map[string]interface{}, error) {
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

	// Get existing devices
	devices := b.getDevices()
	isFirstDevice := len(devices) == 0

	// Remove if already exists, then add
	filtered := []Device{}
	for _, d := range devices {
		if d.ID != deviceID {
			filtered = append(filtered, d)
		}
	}

	filtered = append(filtered, Device{
		ID:          deviceID,
		Name:        deviceName,
		PublicKey:   devicePublicKey,
		ConnectedAt: time.Now(),
	})

	config.Get().SetKey("connectedDevices", filtered)

	// Store keypair for this device (for key rotation)
	// TODO: Store keypair.PrivateKey associated with deviceID

	// Generate new code for next pairing
	b.generateCode()

	return map[string]any{
		"cameraPublicKey": keypair.PublicKey,
		"wifiConnected":   true, // TODO: implement wifi check
		"hasInternet":     true, // TODO: implement internet check
		"isFirstDevice":   isFirstDevice,
	}, nil
}

// SetRelayServer sets relay URL (only first device can call this)
func (b *Bluetooth) SetRelayServer(deviceID, relayURL string) error {
	devices := b.GetDevices()
	if len(devices) != 1 || devices[0].ID != deviceID {
		return fmt.Errorf("only first device can set relay server")
	}
	return config.Get().SetKey("relayUrl", relayURL)
}

// GetDevices returns all connected devices
func (b *Bluetooth) GetDevices() []Device {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.getDevices()
}

// ScheduleKick schedules device removal in 5 minutes
func (b *Bluetooth) ScheduleKick(deviceID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Cancel existing timer if any
	if timer, exists := b.kickTimers[deviceID]; exists {
		timer.Stop()
	}

	// Schedule removal
	b.kickTimers[deviceID] = time.AfterFunc(kickDelay, func() {
		b.mu.Lock()
		defer b.mu.Unlock()

		devices := b.getDevices()
		filtered := []Device{}
		for _, d := range devices {
			if d.ID != deviceID {
				filtered = append(filtered, d)
			}
		}

		config.Get().SetKey("connectedDevices", filtered)
		delete(b.kickTimers, deviceID)
	})

	return nil
}

// RemoveDevice immediately removes a device (for self-removal)
func (b *Bluetooth) RemoveDevice(deviceID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Cancel kick timer if exists
	if timer, exists := b.kickTimers[deviceID]; exists {
		timer.Stop()
		delete(b.kickTimers, deviceID)
	}

	devices := b.getDevices()
	filtered := []Device{}
	for _, d := range devices {
		if d.ID != deviceID {
			filtered = append(filtered, d)
		}
	}

	return config.Get().SetKey("connectedDevices", filtered)
}

func (b *Bluetooth) getDevices() []Device {
	val, ok := config.Get().GetKey("connectedDevices")
	if !ok {
		return []Device{}
	}

	data, _ := json.Marshal(val)
	var devices []Device
	json.Unmarshal(data, &devices)
	return devices
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
