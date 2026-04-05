package devices

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/fxamacker/cbor/v2"

	"root-firmware/pkg/config"
	"root-firmware/pkg/globals"
)

type Device struct {
	ID           string  `cbor:"id"`
	Name         string  `cbor:"name"`
	PublicKey    []byte  `cbor:"publicKey"`
	ProductAlias string  `cbor:"productAlias"`
	PairedAt     int64   `cbor:"pairedAt"`
}

type Devices struct {
	mu       sync.Mutex
	onRemove func(deviceID string)
}

var instance *Devices
var once sync.Once

func Init() {
	once.Do(func() {
		instance = &Devices{}
	})
}

// ResetForTesting resets the devices singleton for test isolation
// Should only be used in unit tests
func ResetForTesting() {
	instance = nil
	once = sync.Once{}
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
		PairedAt:     time.Now().UnixMilli(),
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

// OnRemove registers a callback that is invoked after a device is removed
func (d *Devices) OnRemove(fn func(deviceID string)) {
	d.onRemove = fn
}

// Remove immediately removes a device and invokes the onRemove callback
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

	if err := config.Get().SetKey("connectedDevices", filtered); err != nil {
		return err
	}

	if d.onRemove != nil {
		d.onRemove(deviceID)
	}
	return nil
}

func (d *Devices) getDevices() []Device {
	val, ok := config.Get().GetKey("connectedDevices")
	if !ok {
		return []Device{}
	}

	data, err := cbor.Marshal(val)
	if err != nil {
		log.Printf("Devices: Failed to marshal devices: %v", err)
		return []Device{}
	}
	var devices []Device
	if err := cbor.Unmarshal(data, &devices); err != nil {
		log.Printf("Devices: Failed to unmarshal devices: %v", err)
		return []Device{}
	}
	return devices
}
