package notifications

import (
	"fmt"
	"sync"
	"testing"

	rootproto "github.com/therealPaulPlay/root-e2ee-protocol/go-server"

	"root-firmware/pkg/config"
	"root-firmware/pkg/devices"
	"root-firmware/pkg/testutil"
)

func resetSingletons() {
	instance = nil
	once = sync.Once{}
	devices.ResetForTesting()
	config.ResetForTesting()
}

func setupTest(t *testing.T) func() {
	t.Helper()
	resetSingletons()

	cleanupGlobals := testutil.SetupTempGlobals(t)

	if err := config.Init(); err != nil {
		t.Fatalf("config.Init() error = %v", err)
	}

	devices.Init()
	Init()

	return func() {
		cleanupGlobals()
		resetSingletons()
	}
}

// addPairedDevice is a helper that adds a device to the devices package
func addPairedDevice(t *testing.T, id, name string) {
	t.Helper()
	if err := devices.Get().Add(id, name, []byte{0x01, 0x02}, rootproto.KeyTypeP256); err != nil {
		t.Fatalf("failed to add paired device: %v", err)
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

func TestGetEntries_EmptyInitially(t *testing.T) {
	cleanup := setupTest(t)
	defer cleanup()

	entries := Get().getEntries()
	if len(entries) != 0 {
		t.Errorf("getEntries() = %d entries, want 0", len(entries))
	}
}

func TestEnable_Success(t *testing.T) {
	cleanup := setupTest(t)
	defer cleanup()

	addPairedDevice(t, "device-1", "Test Device")

	err := Get().Enable("device-1", "fcm-token-abc")
	if err != nil {
		t.Fatalf("Enable() error = %v", err)
	}

	entries := Get().getEntries()
	if len(entries) != 1 {
		t.Fatalf("getEntries() = %d entries, want 1", len(entries))
	}
	if entries[0].DeviceID != "device-1" {
		t.Errorf("DeviceID = %s, want device-1", entries[0].DeviceID)
	}
	if entries[0].FCMToken != "fcm-token-abc" {
		t.Errorf("FCMToken = %s, want fcm-token-abc", entries[0].FCMToken)
	}
}

func TestEnable_UnpairedDevice(t *testing.T) {
	cleanup := setupTest(t)
	defer cleanup()

	err := Get().Enable("unpaired-device", "fcm-token-abc")
	if err == nil {
		t.Error("Enable() should error for unpaired device")
	}
}

func TestEnable_ReplacesExisting(t *testing.T) {
	cleanup := setupTest(t)
	defer cleanup()

	addPairedDevice(t, "device-1", "Test Device")

	n := Get()
	n.Enable("device-1", "old-token")
	err := n.Enable("device-1", "new-token")
	if err != nil {
		t.Fatalf("Enable() error = %v", err)
	}

	entries := n.getEntries()
	if len(entries) != 1 {
		t.Fatalf("getEntries() = %d entries, want 1 (should replace, not duplicate)", len(entries))
	}
	if entries[0].FCMToken != "new-token" {
		t.Errorf("FCMToken = %s, want new-token", entries[0].FCMToken)
	}
}

func TestEnable_MaxDevicesLimit(t *testing.T) {
	cleanup := setupTest(t)
	defer cleanup()

	n := Get()

	// Add and enable MaxNotificationDevices devices
	for i := 0; i < MaxNotificationDevices; i++ {
		id := fmt.Sprintf("device-%d", i)
		addPairedDevice(t, id, "Device")
		if err := n.Enable(id, fmt.Sprintf("token-%d", i)); err != nil {
			t.Fatalf("Enable() error on device %d = %v", i, err)
		}
	}

	// The next one should fail
	addPairedDevice(t, "device-extra", "Extra Device")
	err := n.Enable("device-extra", "token-extra")
	if err == nil {
		t.Error("Enable() should error when max devices reached")
	}
}

func TestDisable_Success(t *testing.T) {
	cleanup := setupTest(t)
	defer cleanup()

	addPairedDevice(t, "device-1", "Test Device")

	n := Get()
	n.Enable("device-1", "fcm-token-abc")

	err := n.Disable("device-1")
	if err != nil {
		t.Fatalf("Disable() error = %v", err)
	}

	if n.IsEnabled("device-1") {
		t.Error("IsEnabled() = true after Disable(), want false")
	}
}

func TestDisable_NonexistentDevice(t *testing.T) {
	cleanup := setupTest(t)
	defer cleanup()

	// Disabling a device that was never enabled should not error
	err := Get().Disable("nonexistent")
	if err != nil {
		t.Errorf("Disable() error = %v, want nil", err)
	}
}

func TestIsEnabled_True(t *testing.T) {
	cleanup := setupTest(t)
	defer cleanup()

	addPairedDevice(t, "device-1", "Test Device")

	n := Get()
	n.Enable("device-1", "fcm-token-abc")

	if !n.IsEnabled("device-1") {
		t.Error("IsEnabled() = false, want true")
	}
}

func TestIsEnabled_False(t *testing.T) {
	cleanup := setupTest(t)
	defer cleanup()

	if Get().IsEnabled("device-1") {
		t.Error("IsEnabled() = true for non-registered device, want false")
	}
}

func TestEnable_MultipleDevices(t *testing.T) {
	cleanup := setupTest(t)
	defer cleanup()

	addPairedDevice(t, "device-1", "Device One")
	addPairedDevice(t, "device-2", "Device Two")

	n := Get()
	n.Enable("device-1", "token-1")
	n.Enable("device-2", "token-2")

	entries := n.getEntries()
	if len(entries) != 2 {
		t.Errorf("getEntries() = %d entries, want 2", len(entries))
	}

	if !n.IsEnabled("device-1") || !n.IsEnabled("device-2") {
		t.Error("both devices should be enabled")
	}
}

func TestCooldownMinutes(t *testing.T) {
	cleanup := setupTest(t)
	defer cleanup()

	n := Get()

	// Defaults to 0
	if v := n.GetCooldownMinutes(); v != 0 {
		t.Errorf("GetCooldownMinutes() = %d, want 0", v)
	}

	// Valid values within bounds
	for _, v := range []int{0, 1, 15, 30} {
		if err := n.SetCooldownMinutes(v); err != nil {
			t.Fatalf("SetCooldownMinutes(%d) error = %v", v, err)
		}
		if got := n.GetCooldownMinutes(); got != v {
			t.Errorf("GetCooldownMinutes() = %d, want %d", got, v)
		}
	}

	// Out of bounds rejected, previous value preserved
	n.SetCooldownMinutes(10)
	if err := n.SetCooldownMinutes(-1); err == nil {
		t.Error("SetCooldownMinutes(-1) should error")
	}
	if err := n.SetCooldownMinutes(31); err == nil {
		t.Error("SetCooldownMinutes(31) should error")
	}
	if got := n.GetCooldownMinutes(); got != 10 {
		t.Errorf("GetCooldownMinutes() = %d after invalid set, want 10", got)
	}
}

func TestDisable_OnlyRemovesTarget(t *testing.T) {
	cleanup := setupTest(t)
	defer cleanup()

	addPairedDevice(t, "device-1", "Device One")
	addPairedDevice(t, "device-2", "Device Two")

	n := Get()
	n.Enable("device-1", "token-1")
	n.Enable("device-2", "token-2")

	n.Disable("device-1")

	if n.IsEnabled("device-1") {
		t.Error("device-1 should be disabled")
	}
	if !n.IsEnabled("device-2") {
		t.Error("device-2 should still be enabled")
	}
}
