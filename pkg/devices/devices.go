package devices

import (
	"encoding/json"
	"sync"
	"time"

	"root-firmware/pkg/config"
)

type Device struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	PublicKey        []byte    `json:"publicKey"`
	CameraPrivateKey []byte    `json:"cameraPrivateKey"` // Camera's private key for this device
	ConnectedAt      time.Time `json:"connectedAt"`
	LastKeyRotation  time.Time `json:"lastKeyRotation"` // Last time keys were rotated
}

type Devices struct {
	mu         sync.Mutex
	kickTimers map[string]*time.Timer
}

var instance *Devices
var once sync.Once

const kickDelay = 5 * time.Minute

func Init() {
	once.Do(func() {
		instance = &Devices{
			kickTimers: make(map[string]*time.Timer),
		}
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
func (d *Devices) Add(id, name string, publicKey, cameraPrivateKey []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	devices := d.getDevices()

	// Remove if already exists
	filtered := []Device{}
	for _, dev := range devices {
		if dev.ID != id {
			filtered = append(filtered, dev)
		}
	}

	// Add new device
	now := time.Now()
	filtered = append(filtered, Device{
		ID:               id,
		Name:             name,
		PublicKey:        publicKey,
		CameraPrivateKey: cameraPrivateKey,
		ConnectedAt:      now,
		LastKeyRotation:  now,
	})

	return config.Get().SetKey("connectedDevices", filtered)
}

// Remove immediately removes a device
func (d *Devices) Remove(deviceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Cancel kick timer if exists
	if timer, exists := d.kickTimers[deviceID]; exists {
		timer.Stop()
		delete(d.kickTimers, deviceID)
	}

	devices := d.getDevices()
	filtered := []Device{}
	for _, dev := range devices {
		if dev.ID != deviceID {
			filtered = append(filtered, dev)
		}
	}

	return config.Get().SetKey("connectedDevices", filtered)
}

// ScheduleKick schedules device removal in 5 minutes
func (d *Devices) ScheduleKick(deviceID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Cancel existing timer if any
	if timer, exists := d.kickTimers[deviceID]; exists {
		timer.Stop()
	}

	// Schedule removal
	d.kickTimers[deviceID] = time.AfterFunc(kickDelay, func() {
		d.mu.Lock()
		defer d.mu.Unlock()

		devices := d.getDevices()
		filtered := []Device{}
		for _, dev := range devices {
			if dev.ID != deviceID {
				filtered = append(filtered, dev)
			}
		}

		config.Get().SetKey("connectedDevices", filtered)
		delete(d.kickTimers, deviceID)
	})

	return nil
}

// RotateKeys updates the encryption keys for a device
func (d *Devices) RotateKeys(deviceID string, newPublicKey, newCameraPrivateKey []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	devices := d.getDevices()
	updated := []Device{}
	found := false

	for _, dev := range devices {
		if dev.ID == deviceID {
			dev.PublicKey = newPublicKey
			dev.CameraPrivateKey = newCameraPrivateKey
			dev.LastKeyRotation = time.Now()
			found = true
		}
		updated = append(updated, dev)
	}

	if !found {
		return nil // Device not found, no error
	}

	return config.Get().SetKey("connectedDevices", updated)
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
