package pairing

import (
	"sync"
	"testing"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/devices"
	"root-firmware/pkg/globals"
	"root-firmware/pkg/testutil"
)

func resetSingletons() {
	helperInstance = nil
	helperOnce = sync.Once{}
	config.ResetForTesting()
}

func setupTestPairing(t *testing.T) func() {
	t.Helper()
	resetSingletons()

	cleanupGlobals := testutil.SetupTempGlobals(t)

	if err := config.Init(); err != nil {
		t.Fatalf("config.Init() error = %v", err)
	}
	devices.Init()
	InitHelper()

	return func() {
		cleanupGlobals()
		resetSingletons()
	}
}

// --- Viewfinder chunking tests ---

func TestGetViewfinderChunks_EmptyData(t *testing.T) {
	chunks, err := GetViewfinderChunks([]byte{})
	if err != nil {
		t.Fatalf("GetViewfinderChunks() error = %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty data, got %d", len(chunks))
	}
}

func TestGetViewfinderChunks_SmallData(t *testing.T) {
	// 8 pixels of grayscale data
	data := make([]byte, 8)
	for i := range data {
		data[i] = byte(i * 32) // Various gray levels
	}

	chunks, err := GetViewfinderChunks(data)
	if err != nil {
		t.Fatalf("GetViewfinderChunks() error = %v", err)
	}

	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk for small data, got %d", len(chunks))
	}

	// Verify chunk structure
	chunk := chunks[0]
	if _, ok := chunk["data"]; !ok {
		t.Error("chunk missing 'data' field")
	}
	if idx, ok := chunk["index"]; !ok || idx != 0 {
		t.Error("chunk missing or wrong 'index' field")
	}
}

func TestGetViewfinderChunks_FullFrame(t *testing.T) {
	data := make([]byte, globals.ViewfinderWidth*globals.ViewfinderHeight)
	for i := range data {
		data[i] = byte(i % 256)
	}

	chunks, err := GetViewfinderChunks(data)
	if err != nil {
		t.Fatalf("GetViewfinderChunks() error = %v", err)
	}

	if len(chunks) == 0 {
		t.Fatal("expected multiple chunks for full frame")
	}

	// Verify indices are sequential
	for i, chunk := range chunks {
		if idx, ok := chunk["index"].(int); !ok || idx != i {
			t.Errorf("chunk %d has wrong index: %v", i, chunk["index"])
		}
	}
}

func TestGetViewfinderChunks_2BitEncoding(t *testing.T) {
	// Test that 8-bit values are properly reduced to 2-bit (4 shades)
	// 0x00 -> 0, 0x40 -> 1, 0x80 -> 2, 0xC0 -> 3
	data := []byte{0x00, 0xFF, 0x80, 0x40} // 4 pixels = 1 byte

	chunks, err := GetViewfinderChunks(data)
	if err != nil {
		t.Fatalf("GetViewfinderChunks() error = %v", err)
	}

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}

	// 4 pixels * 2 bits = 8 bits = 1 byte
	chunkData := chunks[0]["data"].([]byte)
	if len(chunkData) != 1 {
		t.Errorf("expected 1 byte for 4 pixels, got %d", len(chunkData))
	}
}

// --- Pairing code tests ---

func TestGenerateCode_Format(t *testing.T) {
	cleanup := setupTestPairing(t)
	defer cleanup()

	code := GetHelper().GenerateCode()

	if len(code) != 8 {
		t.Errorf("code length = %d, want 8", len(code))
	}

	// Should be all digits
	for _, c := range code {
		if c < '0' || c > '9' {
			t.Errorf("code contains non-digit: %c", c)
		}
	}
}

func TestGenerateCode_Unique(t *testing.T) {
	cleanup := setupTestPairing(t)
	defer cleanup()

	codes := make(map[string]bool)
	for range 10 {
		code := GetHelper().GenerateCode()
		if codes[code] {
			t.Errorf("duplicate code generated: %s", code)
		}
		codes[code] = true
	}
}

func TestGenerateCode_SetsExpiry(t *testing.T) {
	cleanup := setupTestPairing(t)
	defer cleanup()

	helper := GetHelper()
	before := time.Now()
	helper.GenerateCode()
	after := time.Now()

	helper.mu.Lock()
	expiry := helper.code.ExpiresAt
	helper.mu.Unlock()

	expectedMin := before.Add(codeExpiry)
	expectedMax := after.Add(codeExpiry)

	if expiry.Before(expectedMin) || expiry.After(expectedMax) {
		t.Errorf("expiry %v not in expected range [%v, %v]", expiry, expectedMin, expectedMax)
	}
}

func TestPairDevice_NoCodeGenerated(t *testing.T) {
	cleanup := setupTestPairing(t)
	defer cleanup()

	helper := GetHelper()

	err := helper.PairDevice("device-123", "Test Device", []byte{0x01, 0x02})
	if err == nil {
		t.Error("PairDevice() should error when no code generated")
	}
}

func TestPairDevice_CodeNotVerified(t *testing.T) {
	cleanup := setupTestPairing(t)
	defer cleanup()

	helper := GetHelper()
	helper.GenerateCode()

	err := helper.PairDevice("device-123", "Test Device", []byte{0x01, 0x02})
	if err == nil {
		t.Error("PairDevice() should error when code not verified")
	}
}
