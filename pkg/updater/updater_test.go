package updater

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
)

// resetSingletons resets updater singleton and boot confirm state for test isolation
func resetSingletons() {
	instance = nil
	once = sync.Once{}

	healthMu.Lock()
	initComplete = false
	relayConnected = false
	updateCheckSuccess = false
	slotMarkedGood = false
	healthMu.Unlock()
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

	status, version, errMsg := u.GetStatus()
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

	status, _, errMsg := u.GetStatus()
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
	u := Get()

	err := u.StartUpdate()
	if err == nil {
		t.Error("StartUpdate() should error when no update available")
	}
	if !strings.Contains(err.Error(), "no update available") {
		t.Errorf("error = %v, want 'no update available'", err)
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

	err := u.StartUpdate()
	if err == nil {
		t.Error("StartUpdate() should error when already in progress")
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Errorf("error = %v, want 'already in progress'", err)
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
