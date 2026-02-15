package devices

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/globals"
)

type Device struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`         // Device name
	PublicKey    []byte  `json:"publicKey"`    // Device's public key
	ProductAlias string  `json:"productAlias"` // User-defined name for the ROOT product (e.g. "Living Room Camera")
	PairedAt     float64 `json:"pairedAt"`     // Unix milliseconds (float64 for compatibility)
}

type Devices struct {
	mu sync.Mutex
}

var instance *Devices
var once sync.Once

func Init() {
	once.Do(func() {
		instance = &Devices{}
	})
}

func Get() *Devices {
	if instance == nil {
		panic("devices not initialized - call Init() first")
	}
	return instance
}

// GetAll returns all connected devices
func (d *Devices) GetAll() []Device {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.getDevices()
}

// Get returns a specific device by ID
func (d *Devices) GetByID(id string) (*Device, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	devices := d.getDevices()
	for _, dev := range devices {
		if dev.ID == id {
			return &dev, true
		}
	}
	return nil, false
}

// Add adds a new device or updates existing one
func (d *Devices) Add(id, name string, publicKey []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	devices := d.getDevices()

	// Remove if already exists (even if this device already exists, we want to freshly pair it with the new key etc.)
	filtered := []Device{}
	for _, dev := range devices {
		if dev.ID != id {
			filtered = append(filtered, dev)
		}
	}

	// Add new device
	filtered = append(filtered, Device{
		ID:           id,
		Name:         name,
		PublicKey:    publicKey,
		ProductAlias: "My ROOT " + strings.ToUpper(globals.ProductModel[:1]) + globals.ProductModel[1:], // Prefix needs to match client-side default prefix
		PairedAt:     float64(time.Now().UnixMilli()),
	})

	return config.Get().SetKey("connectedDevices", filtered)
}

// RenewKey updates the device's public key
func (d *Devices) RenewKey(id string, publicKey []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	devices := d.getDevices()

	// Find and update the device's public key
	for i, dev := range devices {
		if dev.ID == id {
			devices[i].PublicKey = publicKey
			return config.Get().SetKey("connectedDevices", devices)
		}
	}

	return fmt.Errorf("device not found: %s", id)
}

// SetProductAlias updates the user-defined product alias for a device
// This alias can be included in notifications and other places where it's useful to differentiate between
// different paired ROOT products
func (d *Devices) SetProductAlias(id string, alias string) error {
	if len(alias) < 3 || len(alias) > 30 {
		return fmt.Errorf("alias must be between 3 and 30 characters")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	devices := d.getDevices()

	for i, dev := range devices {
		if dev.ID == id {
			devices[i].ProductAlias = alias
			return config.Get().SetKey("connectedDevices", devices)
		}
	}

	return fmt.Errorf("device not found: %s", id)
}

// Remove immediately removes a device
func (d *Devices) Remove(deviceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	devices := d.getDevices()
	filtered := []Device{}
	for _, dev := range devices {
		if dev.ID != deviceID {
			filtered = append(filtered, dev)
		}
	}

	return config.Get().SetKey("connectedDevices", filtered)
}

func (d *Devices) getDevices() []Device {
	val, ok := config.Get().GetKey("connectedDevices")
	if !ok {
		return []Device{}
	}

	data, _ := json.Marshal(val)
	var devices []Device
	json.Unmarshal(data, &devices)
	return devices
}
