package notifications

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		waitUntilIdle(t)
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

// fakeRelay points notifications at a TLS test server responding with the given status, returning received bodies
func fakeRelay(t *testing.T, status *atomic.Int32) <-chan string {
	t.Helper()
	requests := make(chan string, 10)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- r.URL.Path + " " + string(body)
		w.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(server.Close)
	original := httpClient
	t.Cleanup(func() { httpClient = original })
	httpClient = server.Client()
	config.Get().SetKey("relayDomain", strings.TrimPrefix(server.URL, "https://"))
	return requests
}

func nextRequest(t *testing.T, requests <-chan string) string {
	t.Helper()
	select {
	case req := <-requests:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for relay request")
		return ""
	}
}

// waitUntilIdle waits for in-flight sends to finish
func waitUntilIdle(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		n := Get()
		n.mu.Lock()
		busy := false
		for _, p := range n.pending {
			busy = busy || p.sending
		}
		n.mu.Unlock()
		if !busy {
			return
		}
	}
	t.Fatal("sends still in flight")
}

func TestSendPending_DeliversOnlyMostRecent(t *testing.T) {
	cleanup := setupTest(t)
	defer cleanup()

	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	requests := fakeRelay(t, &status)
	addPairedDevice(t, "device-1", "Device")
	Get().Enable("device-1", "token-1")

	// Both fail while offline
	Get().SendEventToAll("person", "event-old", nil)
	nextRequest(t, requests)
	Get().SendEventToAll("vehicle", "event-new", nil)
	nextRequest(t, requests)
	waitUntilIdle(t)

	// Back online: only the most recent is delivered, once
	status.Store(http.StatusOK)
	Get().SendPending()
	if req := nextRequest(t, requests); !strings.Contains(req, "event-new") {
		t.Errorf("retried request = %s, want event-new", req)
	}
	waitUntilIdle(t)
	Get().SendPending()
	select {
	case req := <-requests:
		t.Errorf("unexpected request after delivery: %s", req)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestDisable_DropsPendingNotification(t *testing.T) {
	cleanup := setupTest(t)
	defer cleanup()

	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	requests := fakeRelay(t, &status)
	addPairedDevice(t, "device-1", "Device")
	Get().Enable("device-1", "token-1")

	Get().SendEventToAll("person", "event-1", nil)
	nextRequest(t, requests)
	waitUntilIdle(t)

	Get().Disable("device-1")
	Get().Enable("device-1", "token-1") // Re-enabling must not resurrect the old notification
	Get().SendPending()
	select {
	case req := <-requests:
		t.Errorf("unexpected request after re-enabling: %s", req)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSendPending_NoDuplicateWhileInFlight(t *testing.T) {
	cleanup := setupTest(t)
	defer cleanup()

	var status atomic.Int32
	status.Store(http.StatusOK)
	requests := fakeRelay(t, &status)
	release := make(chan struct{})
	inner := httpClient.Transport
	httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-release // Hold the request in flight
		return inner.RoundTrip(r)
	})
	addPairedDevice(t, "device-1", "Device")
	Get().Enable("device-1", "token-1")

	Get().SendEventToAll("person", "event-1", nil)
	Get().SendPending() // Simulated reconnect during the send
	close(release)

	nextRequest(t, requests)
	select {
	case req := <-requests:
		t.Errorf("duplicate request: %s", req)
	case <-time.After(200 * time.Millisecond):
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSendEventToAll_DeliversToAllDevices(t *testing.T) {
	cleanup := setupTest(t)
	defer cleanup()

	var status atomic.Int32
	status.Store(http.StatusOK)
	requests := fakeRelay(t, &status)
	addPairedDevice(t, "device-1", "Device")
	addPairedDevice(t, "device-2", "Device")
	Get().Enable("device-1", "token-1")
	Get().Enable("device-2", "token-2")

	Get().SendEventToAll("person", "event-1", nil)
	got := nextRequest(t, requests) + nextRequest(t, requests)
	if !strings.Contains(got, "token-1") || !strings.Contains(got, "token-2") {
		t.Errorf("expected a send to both devices, got: %s", got)
	}
}
