package devices

import (
	"sync"
	"testing"

	rootproto "github.com/therealPaulPlay/root-e2ee-protocol/go-server"

	"root-firmware/pkg/config"
	"root-firmware/pkg/testutil"
)

func resetSingletons() {
	instance = nil
	once = sync.Once{}
	config.ResetForTesting()
}

func setupTestDevices(t *testing.T) func() {
	t.Helper()
	resetSingletons()

	cleanupGlobals := testutil.SetupTempGlobals(t)

	if err := config.Init(); err != nil {
		t.Fatalf("config.Init() error = %v", err)
	}

	Init()

	return func() {
		cleanupGlobals()
		resetSingletons()
	}
}

func TestGet_PanicsWithoutInit(t *testing.T) {
	resetSingletons()
	defer resetSingletons()

	defer func() {
		if r := recover(); r == nil {
			t.Error("Get() should panic when not initialized")
		}
	}()

	Get()
}

func TestGetAll_EmptyInitially(t *testing.T) {
	cleanup := setupTestDevices(t)
	defer cleanup()

	devices := Get().GetAll()
	if len(devices) != 0 {
		t.Errorf("GetAll() = %d devices, want 0", len(devices))
	}
}

func TestAdd_NewDevice(t *testing.T) {
	cleanup := setupTestDevices(t)
	defer cleanup()

	d := Get()
	pubKey := []byte{0x01, 0x02, 0x03, 0x04}

	err := d.Add("device-123", "Test Device", pubKey, rootproto.KeyTypeP256)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	devices := d.GetAll()
	if len(devices) != 1 {
		t.Fatalf("GetAll() = %d devices, want 1", len(devices))
	}

	dev := devices[0]
	if dev.ID != "device-123" {
		t.Errorf("device.ID = %s, want device-123", dev.ID)
	}
	if dev.Name != "Test Device" {
		t.Errorf("device.Name = %s, want Test Device", dev.Name)
	}
	if string(dev.PublicKey) != string(pubKey) {
		t.Errorf("device.PublicKey = %v, want %v", dev.PublicKey, pubKey)
	}
	if dev.ProductAlias != "My ROOT Observer" {
		t.Errorf("device.ProductAlias = %s, want My ROOT Observer", dev.ProductAlias)
	}
	if dev.PairedAt <= 0 {
		t.Error("device.PairedAt should be set")
	}
}

func TestAdd_ReplacesExistingDevice(t *testing.T) {
	cleanup := setupTestDevices(t)
	defer cleanup()

	d := Get()

	// Add initial device
	d.Add("device-123", "Original Name", []byte{0x01}, rootproto.KeyTypeP256)

	// Add same device ID with new data
	newKey := []byte{0x02, 0x03}
	err := d.Add("device-123", "New Name", newKey, rootproto.KeyTypeP256)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	devices := d.GetAll()
	if len(devices) != 1 {
		t.Fatalf("GetAll() = %d devices, want 1 (should replace, not duplicate)", len(devices))
	}

	dev := devices[0]
	if dev.Name != "New Name" {
		t.Errorf("device.Name = %s, want New Name", dev.Name)
	}
	if string(dev.PublicKey) != string(newKey) {
		t.Errorf("device.PublicKey not updated")
	}
}

func TestAdd_MultipleDevices(t *testing.T) {
	cleanup := setupTestDevices(t)
	defer cleanup()

	d := Get()

	d.Add("device-1", "Device One", []byte{0x01}, rootproto.KeyTypeP256)
	d.Add("device-2", "Device Two", []byte{0x02}, rootproto.KeyTypeP256)
	d.Add("device-3", "Device Three", []byte{0x03}, rootproto.KeyTypeP256)

	devices := d.GetAll()
	if len(devices) != 3 {
		t.Errorf("GetAll() = %d devices, want 3", len(devices))
	}
}

func TestGetByID_Found(t *testing.T) {
	cleanup := setupTestDevices(t)
	defer cleanup()

	d := Get()
	d.Add("device-123", "Test Device", []byte{0x01, 0x02}, rootproto.KeyTypeP256)

	dev, found := d.GetByID("device-123")
	if !found {
		t.Fatal("GetByID() found = false, want true")
	}
	if dev.ID != "device-123" {
		t.Errorf("device.ID = %s, want device-123", dev.ID)
	}
	if dev.Name != "Test Device" {
		t.Errorf("device.Name = %s, want Test Device", dev.Name)
	}
}

func TestGetByID_NotFound(t *testing.T) {
	cleanup := setupTestDevices(t)
	defer cleanup()

	d := Get()
	d.Add("device-123", "Test Device", []byte{0x01}, rootproto.KeyTypeP256)

	dev, found := d.GetByID("nonexistent")
	if found {
		t.Error("GetByID() found = true, want false")
	}
	if dev != nil {
		t.Errorf("GetByID() = %v, want nil", dev)
	}
}

func TestRenewKey_Success(t *testing.T) {
	cleanup := setupTestDevices(t)
	defer cleanup()

	d := Get()
	d.Add("device-123", "Test Device", []byte{0x01, 0x02}, rootproto.KeyTypeP256)

	newKey := []byte{0x03, 0x04, 0x05}
	err := d.RenewKey("device-123", newKey, rootproto.KeyTypeP256)
	if err != nil {
		t.Fatalf("RenewKey() error = %v", err)
	}

	dev, _ := d.GetByID("device-123")
	if string(dev.PublicKey) != string(newKey) {
		t.Errorf("PublicKey = %v, want %v", dev.PublicKey, newKey)
	}
}

func TestRenewKey_DeviceNotFound(t *testing.T) {
	cleanup := setupTestDevices(t)
	defer cleanup()

	d := Get()

	err := d.RenewKey("nonexistent", []byte{0x01}, rootproto.KeyTypeP256)
	if err == nil {
		t.Error("RenewKey() should error for nonexistent device")
	}
}

func TestSetProductAlias_Success(t *testing.T) {
	cleanup := setupTestDevices(t)
	defer cleanup()

	d := Get()
	d.Add("device-123", "Test Device", []byte{0x01}, rootproto.KeyTypeP256)

	err := d.SetProductAlias("device-123", "Living Room Camera")
	if err != nil {
		t.Fatalf("SetProductAlias() error = %v", err)
	}

	dev, _ := d.GetByID("device-123")
	if dev.ProductAlias != "Living Room Camera" {
		t.Errorf("ProductAlias = %s, want Living Room Camera", dev.ProductAlias)
	}
}

func TestSetProductAlias_Errors(t *testing.T) {
	tests := []struct {
		name        string
		deviceID    string
		alias       string
		setupDevice bool
	}{
		{"too short", "device-123", "AB", true},
		{"too long", "device-123", "This alias is way too long and exceeds thirty characters", true},
		{"device not found", "nonexistent", "Valid Alias", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := setupTestDevices(t)
			defer cleanup()

			d := Get()
			if tt.setupDevice {
				d.Add("device-123", "Test Device", []byte{0x01}, rootproto.KeyTypeP256)
			}

			err := d.SetProductAlias(tt.deviceID, tt.alias)
			if err == nil {
				t.Errorf("SetProductAlias(%q, %q) should error", tt.deviceID, tt.alias)
			}
		})
	}
}

func TestRemove_Success(t *testing.T) {
	cleanup := setupTestDevices(t)
	defer cleanup()

	d := Get()
	d.Add("device-1", "Device One", []byte{0x01}, rootproto.KeyTypeP256)
	d.Add("device-2", "Device Two", []byte{0x02}, rootproto.KeyTypeP256)

	err := d.Remove("device-1")
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	devices := d.GetAll()
	if len(devices) != 1 {
		t.Errorf("GetAll() = %d devices, want 1", len(devices))
	}
	if devices[0].ID != "device-2" {
		t.Error("Wrong device was removed")
	}
}

func TestRemove_NonexistentDevice(t *testing.T) {
	cleanup := setupTestDevices(t)
	defer cleanup()

	d := Get()
	d.Add("device-1", "Device One", []byte{0x01}, rootproto.KeyTypeP256)

	// Removing nonexistent device should not error (idempotent)
	err := d.Remove("nonexistent")
	if err != nil {
		t.Errorf("Remove() error = %v, want nil", err)
	}

	// Original device should still exist
	devices := d.GetAll()
	if len(devices) != 1 {
		t.Errorf("GetAll() = %d devices, want 1", len(devices))
	}
}
