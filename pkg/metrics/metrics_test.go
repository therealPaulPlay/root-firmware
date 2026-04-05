package metrics

import (
	"os"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"root-firmware/pkg/config"
	"root-firmware/pkg/globals"
	"root-firmware/pkg/testutil"
)

func setupTestMetrics(t *testing.T) func() {
	t.Helper()

	instance = nil
	config.ResetForTesting()

	cleanupGlobals := testutil.SetupTempGlobals(t)

	if err := config.Init(); err != nil {
		t.Fatalf("config.Init() error = %v", err)
	}

	return func() {
		cleanupGlobals()
		config.ResetForTesting()
		instance = nil
	}
}

func TestGetPoints_BeforeInit(t *testing.T) {
	cleanup := setupTestMetrics(t)
	defer cleanup()

	points := GetPoints()
	if points != nil {
		t.Errorf("GetPoints() before Init() = %v, want nil", points)
	}
}

func TestLoad_HandlesErrors(t *testing.T) {
	tests := []struct {
		name    string
		setup   func()
	}{
		{"missing file", func() {}},
		{"corrupted file", func() {
			os.WriteFile(globals.MetricsPath, []byte("not valid cbor{{{"), 0644)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := setupTestMetrics(t)
			defer cleanup()

			tt.setup()

			points := load()
			if len(points) != 0 {
				t.Errorf("load() = %d points, want 0", len(points))
			}
		})
	}
}

func TestLoad_LoadsExistingData(t *testing.T) {
	cleanup := setupTestMetrics(t)
	defer cleanup()

	existingPoints := []DataPoint{
		{Timestamp: 1000, CPU: 10.5, Memory: 50.0, Temperature: 45.0, Disk: 30.0},
		{Timestamp: 2000, CPU: 15.0, Memory: 55.0, Temperature: 46.0, Disk: 31.0},
	}
	data, _ := cbor.Marshal(existingPoints)
	os.WriteFile(globals.MetricsPath, data, 0644)

	points := load()
	if len(points) != 2 {
		t.Fatalf("load() = %d points, want 2", len(points))
	}
	if points[0].CPU != 10.5 {
		t.Errorf("points[0].CPU = %v, want 10.5", points[0].CPU)
	}
}

func TestSave_PersistsData(t *testing.T) {
	cleanup := setupTestMetrics(t)
	defer cleanup()

	points := []DataPoint{
		{Timestamp: 1234, CPU: 25.0, Memory: 65.0, Temperature: 48.0, Disk: 35.0},
	}

	save(points)

	data, err := os.ReadFile(globals.MetricsPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var loaded []DataPoint
	cbor.Unmarshal(data, &loaded)
	if len(loaded) != 1 {
		t.Fatalf("loaded %d points, want 1", len(loaded))
	}
	if loaded[0].CPU != 25.0 {
		t.Errorf("loaded[0].CPU = %v, want 25.0", loaded[0].CPU)
	}
}
