package logger

import (
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/globals"
	"root-firmware/pkg/testutil"
)

func setupTestLogger(t *testing.T) func() {
	t.Helper()

	w = nil
	config.ResetForTesting()

	origOutput := log.Writer()
	origFlags := log.Flags()

	cleanupGlobals := testutil.SetupTempGlobals(t)

	if err := config.Init(); err != nil {
		t.Fatalf("config.Init() error = %v", err)
	}

	return func() {
		cleanupGlobals()
		log.SetOutput(origOutput)
		log.SetFlags(origFlags)
		config.ResetForTesting()
		w = nil
	}
}

func TestGetLogs_EmptyAfterInit(t *testing.T) {
	cleanup := setupTestLogger(t)
	defer cleanup()

	Init()

	logs := GetLogs()
	if len(logs) != 0 {
		t.Errorf("GetLogs() = %d logs, want 0", len(logs))
	}
}

func TestWrite_AddsLogEntry(t *testing.T) {
	cleanup := setupTestLogger(t)
	defer cleanup()

	Init()

	log.Print("test message")

	logs := GetLogs()
	if len(logs) != 1 {
		t.Fatalf("GetLogs() = %d logs, want 1", len(logs))
	}
	if !strings.Contains(logs[0].Msg, "test message") {
		t.Errorf("log message = %q, want to contain 'test message'", logs[0].Msg)
	}
}

func TestWrite_SetsTimestamp(t *testing.T) {
	cleanup := setupTestLogger(t)
	defer cleanup()

	Init()

	before := time.Now().UnixMilli()
	log.Print("timestamped message")
	after := time.Now().UnixMilli()

	logs := GetLogs()
	if len(logs) != 1 {
		t.Fatal("expected 1 log entry")
	}

	ts := logs[0].Timestamp
	if ts < before || ts > after {
		t.Errorf("timestamp %v not in range [%v, %v]", ts, before, after)
	}
}

func TestWrite_TimestampsAreIncreasing(t *testing.T) {
	cleanup := setupTestLogger(t)
	defer cleanup()

	Init()

	log.Print("first")
	time.Sleep(1 * time.Millisecond)
	log.Print("second")

	logs := GetLogs()
	if len(logs) != 2 {
		t.Fatal("expected 2 log entries")
	}

	if logs[1].Timestamp <= logs[0].Timestamp {
		t.Errorf("timestamps should be increasing: first=%v, second=%v", logs[0].Timestamp, logs[1].Timestamp)
	}
}

func TestWrite_TruncatesLongMessages(t *testing.T) {
	cleanup := setupTestLogger(t)
	defer cleanup()

	Init()

	// Create a message longer than maxLogMsgSize (2500)
	longMsg := strings.Repeat("x", 3000)
	log.Print(longMsg)

	logs := GetLogs()
	if len(logs) != 1 {
		t.Fatal("expected 1 log entry")
	}

	if len(logs[0].Msg) > maxLogMsgSize+50 { // Allow for "... (truncated)" suffix
		t.Errorf("message length = %d, should be truncated", len(logs[0].Msg))
	}
	if !strings.Contains(logs[0].Msg, "(truncated)") {
		t.Error("truncated message should contain '(truncated)'")
	}
}

func TestWrite_EnforcesMaxLogs(t *testing.T) {
	cleanup := setupTestLogger(t)
	defer cleanup()

	Init()

	// Write more than maxLogs entries
	for i := range maxLogs + 50 {
		log.Printf("log entry %d", i)
	}

	logs := GetLogs()
	if len(logs) != maxLogs {
		t.Errorf("GetLogs() = %d logs, want %d (maxLogs)", len(logs), maxLogs)
	}

	// First log should be entry 50, not entry 0 (oldest dropped)
	if !strings.Contains(logs[0].Msg, "log entry 50") {
		t.Errorf("oldest log = %q, want 'log entry 50'", logs[0].Msg)
	}
}

func TestLogs_PersistToDisk(t *testing.T) {
	cleanup := setupTestLogger(t)
	defer cleanup()

	Init()
	log.Print("persisted message")

	// Reset and reload
	w = nil
	Init()

	logs := GetLogs()
	if len(logs) != 1 {
		t.Fatalf("GetLogs() after reload = %d logs, want 1", len(logs))
	}
	if !strings.Contains(logs[0].Msg, "persisted message") {
		t.Error("log was not persisted")
	}
}

func TestLoad_HandlesCorruptedFile(t *testing.T) {
	cleanup := setupTestLogger(t)
	defer cleanup()

	// Create corrupted logs file
	os.WriteFile(globals.LogsPath, []byte("not valid cbor{{{"), 0644)

	Init()

	// Should start with empty logs, not crash
	logs := GetLogs()
	if logs == nil {
		t.Error("GetLogs() should return empty slice, not nil")
	}
}

func TestLoad_HandlesMissingFile(t *testing.T) {
	cleanup := setupTestLogger(t)
	defer cleanup()

	// Don't create the file - Init should handle missing file gracefully
	Init()

	logs := GetLogs()
	if len(logs) != 0 {
		t.Errorf("GetLogs() with missing file = %d logs, want 0", len(logs))
	}
}

func TestGetLogs_ReturnsCopy(t *testing.T) {
	cleanup := setupTestLogger(t)
	defer cleanup()

	Init()
	log.Print("original")

	logs := GetLogs()
	logs[0].Msg = "modified"

	// Original should be unchanged
	logsAgain := GetLogs()
	if strings.Contains(logsAgain[0].Msg, "modified") {
		t.Error("GetLogs() should return a copy, not the original slice")
	}
}
