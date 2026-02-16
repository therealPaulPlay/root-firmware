// Package testutil provides shared test utilities for the firmware test suite
package testutil

import (
	"path/filepath"
	"testing"

	"root-firmware/pkg/globals"
)

// SetupTempGlobals redirects global paths to a temp directory and returns a cleanup function
func SetupTempGlobals(t *testing.T) func() {
	t.Helper()

	origFirmwareDataDir := globals.FirmwareDataDir
	origConfigPath := globals.ConfigPath
	origRecordingsPath := globals.RecordingsPath
	origEventLogPath := globals.EventLogPath
	origLogsPath := globals.LogsPath
	origMetricsPath := globals.MetricsPath

	tempDir := t.TempDir()
	globals.FirmwareDataDir = filepath.Join(tempDir, ".firmware-data")
	globals.ConfigPath = filepath.Join(globals.FirmwareDataDir, "config.json")
	globals.RecordingsPath = filepath.Join(tempDir, "recordings")
	globals.EventLogPath = filepath.Join(globals.RecordingsPath, "events.json")
	globals.LogsPath = filepath.Join(globals.FirmwareDataDir, "logs.json")
	globals.MetricsPath = filepath.Join(globals.FirmwareDataDir, "metrics.json")

	return func() {
		globals.FirmwareDataDir = origFirmwareDataDir
		globals.ConfigPath = origConfigPath
		globals.RecordingsPath = origRecordingsPath
		globals.EventLogPath = origEventLogPath
		globals.LogsPath = origLogsPath
		globals.MetricsPath = origMetricsPath
	}
}
