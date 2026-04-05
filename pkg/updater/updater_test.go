package updater

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/testutil"
)

// resetSingletons resets updater singleton and boot confirm state for test isolation
func resetSingletons() {
	instance = nil
	once = sync.Once{}
	config.ResetForTesting()

	healthMu.Lock()
	initComplete = false
	relayConnected = false
	updateCheckSuccess = false
	slotMarkedGood = false
	healthMu.Unlock()
}

func setupTestUpdater(t *testing.T) func() {
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

func TestGetStatus(t *testing.T) {
	resetSingletons()
	defer resetSingletons()

	Init()
	u := Get()

	// Set some values directly
	u.mu.Lock()
	u.status = StatusUpdateAvailable
	u.availableVersion = "2.0.0"
	u.errorMsg = ""
	u.mu.Unlock()

	status, version, errMsg, _ := u.GetStatus()
	if status != StatusUpdateAvailable {
		t.Errorf("status = %s, want %s", status, StatusUpdateAvailable)
	}
	if version != "2.0.0" {
		t.Errorf("availableVersion = %s, want 2.0.0", version)
	}
	if errMsg != "" {
		t.Errorf("errorMsg = %s, want empty", errMsg)
	}
}

func TestSetError(t *testing.T) {
	resetSingletons()
	defer resetSingletons()

	Init()
	u := Get()

	u.setError("something went wrong")

	status, _, errMsg, _ := u.GetStatus()
	if status != StatusError {
		t.Errorf("status = %s, want %s", status, StatusError)
	}
	if errMsg != "something went wrong" {
		t.Errorf("errorMsg = %s, want 'something went wrong'", errMsg)
	}
}

func TestStartUpdate_NoUpdateAvailable(t *testing.T) {
	resetSingletons()
	defer resetSingletons()

	Init()

	if Get().StartUpdate() {
		t.Error("StartUpdate() should return false when no update available")
	}
}

func TestStartUpdate_AlreadyInProgress(t *testing.T) {
	resetSingletons()
	defer resetSingletons()

	Init()
	u := Get()

	u.mu.Lock()
	u.status = StatusDownloading
	u.mu.Unlock()

	if u.StartUpdate() {
		t.Error("StartUpdate() should return false when already in progress")
	}
}

func TestProgressReader(t *testing.T) {
	data := make([]byte, 1000)
	for i := range data {
		data[i] = byte(i % 256)
	}

	reader := &progressReader{
		r:     bytes.NewReader(data),
		total: int64(len(data)),
	}

	// Read in chunks
	buf := make([]byte, 100)
	totalRead := 0
	for {
		n, err := reader.Read(buf)
		totalRead += n
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
	}

	if totalRead != len(data) {
		t.Errorf("totalRead = %d, want %d", totalRead, len(data))
	}
	if reader.read != int64(len(data)) {
		t.Errorf("reader.read = %d, want %d", reader.read, len(data))
	}
}

func TestProgressReader_UnknownTotal(t *testing.T) {
	data := []byte("test data")
	reader := &progressReader{
		r:     bytes.NewReader(data),
		total: -1, // Unknown content length
	}

	buf := make([]byte, 100)
	n, _ := reader.Read(buf)

	if n != len(data) {
		t.Errorf("Read() = %d, want %d", n, len(data))
	}
	// Should not panic with unknown total
	if reader.lastLogged != 0 {
		t.Errorf("lastLogged = %d, want 0 (no progress logged for unknown total)", reader.lastLogged)
	}
}

func TestIsNewerVersion(t *testing.T) {
	tests := []struct {
		candidate string
		current   string
		want      bool
	}{
		// Basic version bumps
		{"1.0.1", "1.0.0", true},
		{"1.1.0", "1.0.0", true},
		{"2.0.0", "1.9.9", true},

		// Same version is not newer
		{"1.0.0", "1.0.0", false},

		// Older versions are not newer
		{"1.0.0", "1.0.1", false},
		{"0.0.11", "0.0.12", false},

		// Pre-release is lower than its release
		{"0.0.12", "0.0.12-dev.2", true},
		{"0.0.12", "0.0.12-beta.1", true},
		{"0.0.12-dev.2", "0.0.12", false},
		{"0.0.12-beta.1", "0.0.12", false},

		// Same pre-release is not newer
		{"0.0.12-dev.2", "0.0.12-dev.2", false},
		{"0.0.12-beta.1", "0.0.12-beta.1", false},

		// Higher pre-release of same base
		{"0.0.12-dev.3", "0.0.12-dev.2", true},
		{"0.0.12-beta.2", "0.0.12-beta.1", true},
		{"0.0.12-dev.2", "0.0.12-dev.3", false},

		// Beta is higher than dev (alphabetical per semver)
		{"0.0.12-beta.1", "0.0.12-dev.1", false},
		{"0.0.12-dev.1", "0.0.12-beta.1", true},

		// Release is newer than older pre-release base
		{"0.0.13", "0.0.12-dev.2", true},
		{"0.0.13", "0.0.12-beta.1", true},

		// Older release is not newer than higher pre-release base
		{"0.0.11", "0.0.12-dev.2", false},
		{"0.0.11", "0.0.12-beta.1", false},

		// Unparseable falls back to inequality
		{"dev", "0.0.12", true},
		{"dev", "dev", false},
	}

	for _, tt := range tests {
		t.Run(tt.candidate+"_vs_"+tt.current, func(t *testing.T) {
			got := isNewerVersion(tt.candidate, tt.current)
			if got != tt.want {
				t.Errorf("isNewerVersion(%q, %q) = %v, want %v", tt.candidate, tt.current, got, tt.want)
			}
		})
	}
}

func TestScheduleAutoUpdate(t *testing.T) {
	cleanup := setupTestUpdater(t)
	defer cleanup()

	u := Get()

	u.mu.Lock()
	u.status = StatusUpdateAvailable
	u.availableVersion = "2.0.0"
	u.scheduleAutoUpdate()
	u.mu.Unlock()

	_, _, _, scheduled := u.GetStatus()
	if scheduled.IsZero() {
		t.Fatal("scheduledFor should not be zero after scheduling")
	}

	// Scheduled time must be between 5:00 and 8:00 and at least 3h away
	if h := scheduled.Hour(); h < 5 || h >= 8 {
		t.Errorf("scheduled hour %d is outside 5-8 window", h)
	}
	if time.Until(scheduled) < 3*time.Hour {
		t.Error("scheduled time should be at least 3h away")
	}

	// Verify persisted config
	val, ok := config.Get().GetKey("scheduledUpdateAt")
	if !ok {
		t.Fatal("scheduledUpdateAt should be persisted in config")
	}
	if ms, ok := val.(uint64); !ok || int64(ms) != scheduled.UnixMilli() {
		t.Errorf("persisted scheduledUpdateAt = %v, want %d", val, scheduled.UnixMilli())
	}

	// Clean up timer
	u.mu.Lock()
	u.scheduleTimer.Stop()
	u.mu.Unlock()
}

func TestCancelScheduledUpdate(t *testing.T) {
	cleanup := setupTestUpdater(t)
	defer cleanup()

	u := Get()

	u.mu.Lock()
	u.status = StatusUpdateAvailable
	u.availableVersion = "2.0.0"
	u.scheduleAutoUpdate()
	u.mu.Unlock()

	if _, _, _, s := u.GetStatus(); s.IsZero() {
		t.Fatal("scheduledFor should not be zero after scheduling")
	}

	u.RemoveScheduledUpdateWithLock()

	if _, _, _, s := u.GetStatus(); !s.IsZero() {
		t.Error("scheduledFor should be zero after cancellation")
	}
	if val, ok := config.Get().GetKey("scheduledUpdateAt"); ok {
		t.Errorf("persisted schedule should be cleared, got %v", val)
	}
}

func TestBootConfirmFlow(t *testing.T) {
	resetSingletons()
	defer resetSingletons()

	// Only init complete - should not mark slot good
	MarkInitComplete()
	healthMu.Lock()
	if slotMarkedGood {
		t.Error("slotMarkedGood should be false with only initComplete")
	}
	healthMu.Unlock()

	// Add relay connected - still not enough
	MarkRelayConnected()
	healthMu.Lock()
	if slotMarkedGood {
		t.Error("slotMarkedGood should be false without updateCheckSuccess")
	}
	healthMu.Unlock()

	// All conditions met - should attempt to mark slot good (will fail without RAUC but flag gets set)
	markUpdateCheckSuccessful()
	healthMu.Lock()
	if !slotMarkedGood {
		t.Error("slotMarkedGood should be true after all conditions met")
	}
	if !initComplete || !relayConnected || !updateCheckSuccess {
		t.Error("all flags should be true")
	}
	healthMu.Unlock()
}
