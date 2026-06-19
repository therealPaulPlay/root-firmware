package config

import (
	"os"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"root-firmware/pkg/globals"
	"root-firmware/pkg/testutil"
)

func setupTestConfig(t *testing.T) func() {
	t.Helper()
	ResetForTesting()

	cleanupGlobals := testutil.SetupTempGlobals(t)

	return func() {
		cleanupGlobals()
		ResetForTesting()
	}
}

func TestGenerateRandomSuffix(t *testing.T) {
	randomString6 := generateRandomSuffix(6)
	if len(randomString6) != 6 {
		t.Fatal("random string should be 6 chars long")
	}
	if randomString6 == "XXXXXX" {
		t.Fatal("random should not fail")
	}
	for i, char := range randomString6 {
		if char < 'A' || char > 'Z' {
			t.Errorf("char %d = %c, want A-Z", i, char)
		}
	}
	if secondRandomString6 := generateRandomSuffix(6); secondRandomString6 == randomString6 {
		t.Fatal("random strings should not be equal")
	}
}

func TestInit_CreatesConfigFile(t *testing.T) {
	cleanup := setupTestConfig(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// Config file should exist
	if _, err := os.Stat(globals.ConfigPath); os.IsNotExist(err) {
		t.Error("Init() did not create config file")
	}

	// Should have required keys
	cfg := Get()
	if id, ok := cfg.GetKey("id"); !ok || id == "" {
		t.Error("Init() did not set device id")
	}
	if name, ok := cfg.GetKey("bluetoothName"); !ok || name == "" {
		t.Error("Init() did not set bluetoothName")
	}
	if _, ok := cfg.GetKey("productPrivateKeyP256"); !ok {
		t.Error("Init() did not set productPrivateKeyP256")
	}
	if _, ok := cfg.GetKey("productPublicKeyP256"); !ok {
		t.Error("Init() did not set productPublicKeyP256")
	}
	if _, ok := cfg.GetKey("fileEncryptionKeyAES256"); !ok {
		t.Error("Init() did not set fileEncryptionKeyAES256")
	}
}

func TestInit_LoadsExistingConfig(t *testing.T) {
	cleanup := setupTestConfig(t)
	defer cleanup()

	// Create a config file manually
	if err := os.MkdirAll(globals.FirmwareDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	configData, _ := cbor.Marshal(map[string]any{"id": "test-id-123", "customKey": "customValue"})
	if err := os.WriteFile(globals.ConfigPath, configData, 0644); err != nil {
		t.Fatal(err)
	}

	if err := Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	cfg := Get()
	if id, _ := cfg.GetKey("id"); id != "test-id-123" {
		t.Errorf("GetKey(id) = %v, want test-id-123", id)
	}
	if val, _ := cfg.GetKey("customKey"); val != "customValue" {
		t.Errorf("GetKey(customKey) = %v, want customValue", val)
	}
}

func TestInit_HandlesCorruptedConfig(t *testing.T) {
	cleanup := setupTestConfig(t)
	defer cleanup()

	// Create a corrupted config file
	if err := os.MkdirAll(globals.FirmwareDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globals.ConfigPath, []byte("not valid cbor{{{"), 0644); err != nil {
		t.Fatal(err)
	}

	// Should not error - it recreates the config
	if err := Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// Corrupted backup should exist
	if _, err := os.Stat(globals.ConfigPath + ".corrupted"); os.IsNotExist(err) {
		t.Error("Init() did not backup corrupted config")
	}

	// New config should be valid
	cfg := Get()
	if _, ok := cfg.GetKey("id"); !ok {
		t.Error("Init() did not create new config after corruption")
	}
}

func TestGet_PanicsWithoutInit(t *testing.T) {
	cleanup := setupTestConfig(t)
	defer cleanup()

	defer func() {
		if r := recover(); r == nil {
			t.Error("Get() should panic when not initialized")
		}
	}()

	Get()
}

func TestSetKey_AndGetKey(t *testing.T) {
	cleanup := setupTestConfig(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	cfg := Get()

	// Set string value
	if err := cfg.SetKey("testString", "hello"); err != nil {
		t.Fatalf("SetKey() error = %v", err)
	}
	if val, ok := cfg.GetKey("testString"); !ok || val != "hello" {
		t.Errorf("GetKey(testString) = %v, %v; want hello, true", val, ok)
	}

	// Set numeric value (becomes uint64 after CBOR normalization)
	if err := cfg.SetKey("testNumber", 42); err != nil {
		t.Fatalf("SetKey() error = %v", err)
	}
	if val, ok := cfg.GetKey("testNumber"); !ok || val != uint64(42) {
		t.Errorf("GetKey(testNumber) = %v (%T), want 42 (uint64)", val, val)
	}

	// Set boolean value
	if err := cfg.SetKey("testBool", true); err != nil {
		t.Fatalf("SetKey() error = %v", err)
	}
	if val, ok := cfg.GetKey("testBool"); !ok || val != true {
		t.Errorf("GetKey(testBool) = %v, want true", val)
	}

	// Set slice value (becomes []any after CBOR normalization)
	if err := cfg.SetKey("testSlice", []string{"a", "b", "c"}); err != nil {
		t.Fatalf("SetKey() error = %v", err)
	}
	if val, ok := cfg.GetKey("testSlice"); !ok {
		t.Error("GetKey(testSlice) not found")
	} else if arr, ok := val.([]any); !ok || len(arr) != 3 {
		t.Errorf("GetKey(testSlice) = %v (%T), want []any with 3 elements", val, val)
	}
}

func TestSetKey_DeletesWithNil(t *testing.T) {
	cleanup := setupTestConfig(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	cfg := Get()

	// Set then delete
	cfg.SetKey("toDelete", "value")
	if _, ok := cfg.GetKey("toDelete"); !ok {
		t.Fatal("SetKey() did not set value")
	}

	if err := cfg.SetKey("toDelete", nil); err != nil {
		t.Fatalf("SetKey(nil) error = %v", err)
	}
	if _, ok := cfg.GetKey("toDelete"); ok {
		t.Error("SetKey(nil) did not delete key")
	}
}

func TestSetKey_PersistsToDisk(t *testing.T) {
	cleanup := setupTestConfig(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	cfg := Get()
	cfg.SetKey("persisted", "value123")

	// Reset and reload
	ResetForTesting()
	if err := Init(); err != nil {
		t.Fatal(err)
	}

	cfg = Get()
	if val, ok := cfg.GetKey("persisted"); !ok || val != "value123" {
		t.Errorf("Value not persisted: got %v, %v", val, ok)
	}
}

func TestGetKey_NonexistentKey(t *testing.T) {
	cleanup := setupTestConfig(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	cfg := Get()
	val, ok := cfg.GetKey("nonexistent")
	if ok {
		t.Errorf("GetKey(nonexistent) ok = true, want false")
	}
	if val != nil {
		t.Errorf("GetKey(nonexistent) = %v, want nil", val)
	}
}

func TestGetFileEncryptionKeyAES256(t *testing.T) {
	cleanup := setupTestConfig(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	cfg := Get()
	key, err := cfg.GetFileEncryptionKeyAES256()
	if err != nil {
		t.Fatalf("GetFileEncryptionKeyAES256() error = %v", err)
	}
	// AES-256 key should be 32 bytes
	if len(key) != 32 {
		t.Errorf("GetFileEncryptionKeyAES256() key length = %d, want 32", len(key))
	}
}

func TestGetFileEncryptionKeyAES256_Errors(t *testing.T) {
	tests := []struct {
		name       string
		configData map[string]any
	}{
		{"invalid type", map[string]any{"id": "test-id", "fileEncryptionKeyAES256": 12345}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := setupTestConfig(t)
			defer cleanup()

			if err := os.MkdirAll(globals.FirmwareDataDir, 0755); err != nil {
				t.Fatal(err)
			}
			data, _ := cbor.Marshal(tt.configData)
			if err := os.WriteFile(globals.ConfigPath, data, 0644); err != nil {
				t.Fatal(err)
			}

			if err := Init(); err != nil {
				t.Fatal(err)
			}

			_, err := Get().GetFileEncryptionKeyAES256()
			if err == nil {
				t.Error("GetFileEncryptionKeyAES256() should error")
			}
		})
	}
}

func TestInit_ProvisionsMissingKeys(t *testing.T) {
	cleanup := setupTestConfig(t)
	defer cleanup()

	if err := os.MkdirAll(globals.FirmwareDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Seed an existing config that has an id but is missing the keys added later
	data, _ := cbor.Marshal(map[string]any{"id": "existing-id"})
	if err := os.WriteFile(globals.ConfigPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	// The pre-existing value is preserved
	if id, ok := Get().GetKey("id"); !ok || id != "existing-id" {
		t.Errorf("existing id should be preserved, got %v (ok=%v)", id, ok)
	}
	// Missing keys are provisioned
	if _, ok := Get().GetKey("bluetoothName"); !ok {
		t.Error("bluetoothName should be provisioned")
	}
	if _, err := Get().GetFileEncryptionKeyAES256(); err != nil {
		t.Errorf("fileEncryptionKeyAES256 should be provisioned: %v", err)
	}
}

func TestCborNormalize(t *testing.T) {
	tests := []struct {
		name  string
		input any
		check func(t *testing.T, result any)
	}{
		{
			name:  "string",
			input: "hello",
			check: func(t *testing.T, result any) {
				if result != "hello" {
					t.Errorf("got %v, want hello", result)
				}
			},
		},
		{
			name:  "int becomes uint64",
			input: 42,
			check: func(t *testing.T, result any) {
				if result != uint64(42) {
					t.Errorf("got %v (%T), want 42 (uint64)", result, result)
				}
			},
		},
		{
			name:  "[]string becomes []any",
			input: []string{"a", "b"},
			check: func(t *testing.T, result any) {
				arr, ok := result.([]any)
				if !ok {
					t.Errorf("got %T, want []any", result)
					return
				}
				if len(arr) != 2 || arr[0] != "a" || arr[1] != "b" {
					t.Errorf("got %v, want [a b]", arr)
				}
			},
		},
		{
			name:  "map[string]string becomes map[any]any",
			input: map[string]string{"key": "value"},
			check: func(t *testing.T, result any) {
				m, ok := result.(map[any]any)
				if !ok {
					t.Errorf("got %T, want map[any]any", result)
					return
				}
				if m["key"] != "value" {
					t.Errorf("got %v, want map[key:value]", m)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := cborNormalize(tt.input)
			if err != nil {
				t.Fatalf("cborNormalize() error = %v", err)
			}
			tt.check(t, result)
		})
	}
}
