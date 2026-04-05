package events

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"root-firmware/pkg/config"
	"root-firmware/pkg/globals"
	"root-firmware/pkg/testutil"
)

// resetSingletons resets storage and config singletons for test isolation
func resetSingletons() {
	instance = nil
	once = sync.Once{}
	config.ResetForTesting()
}

func setupTestStorage(t *testing.T) func() {
	t.Helper()
	resetSingletons()

	cleanupGlobals := testutil.SetupTempGlobals(t)

	if err := config.Init(); err != nil {
		t.Fatalf("config.Init() error = %v", err)
	}

	return func() {
		cleanupGlobals()
		resetSingletons()
	}
}

func TestInit_CreatesDirectoriesAndEventLog(t *testing.T) {
	cleanup := setupTestStorage(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// Recordings directory should exist
	if _, err := os.Stat(globals.RecordingsPath); os.IsNotExist(err) {
		t.Error("Init() did not create recordings directory")
	}

	// Event log should exist
	if _, err := os.Stat(globals.EventLogPath); os.IsNotExist(err) {
		t.Error("Init() did not create event log")
	}

	// Event log should be valid CBOR with empty events array
	data, _ := os.ReadFile(globals.EventLogPath)
	var log EventLog
	if err := cbor.Unmarshal(data, &log); err != nil {
		t.Errorf("Event log is not valid CBOR: %v", err)
	}
	if len(log.Events) != 0 {
		t.Errorf("Event log should be empty, got %d events", len(log.Events))
	}
}

func TestInit_RecoverCorruptedEventLog(t *testing.T) {
	cleanup := setupTestStorage(t)
	defer cleanup()

	// Create recordings dir and corrupted event log
	os.MkdirAll(globals.RecordingsPath, 0755)
	os.WriteFile(globals.EventLogPath, []byte("not valid cbor{{{"), 0644)

	if err := Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// Corrupted backup should exist
	if _, err := os.Stat(globals.EventLogPath + ".corrupted"); os.IsNotExist(err) {
		t.Error("Init() did not backup corrupted event log")
	}

	// New event log should be valid
	data, _ := os.ReadFile(globals.EventLogPath)
	var log EventLog
	if err := cbor.Unmarshal(data, &log); err != nil {
		t.Errorf("Recovered event log is not valid CBOR: %v", err)
	}
}

func TestInit_CleansUpOrphanedTempFiles(t *testing.T) {
	cleanup := setupTestStorage(t)
	defer cleanup()

	// Create recordings dir with orphaned temp files
	os.MkdirAll(globals.RecordingsPath, 0755)
	os.WriteFile(filepath.Join(globals.RecordingsPath, "temp-abc123.mp4"), []byte("orphan"), 0644)
	os.WriteFile(filepath.Join(globals.RecordingsPath, "temp-def456.mp4"), []byte("orphan"), 0644)
	os.WriteFile(filepath.Join(globals.RecordingsPath, "real-recording.mp4"), []byte("keep"), 0644)

	if err := Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// Temp files should be removed
	if _, err := os.Stat(filepath.Join(globals.RecordingsPath, "temp-abc123.mp4")); !os.IsNotExist(err) {
		t.Error("Init() did not remove orphaned temp file")
	}

	// Real file should remain
	if _, err := os.Stat(filepath.Join(globals.RecordingsPath, "real-recording.mp4")); os.IsNotExist(err) {
		t.Error("Init() removed non-temp file")
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

func TestGetEventLogPaginated_EmptyInitially(t *testing.T) {
	cleanup := setupTestStorage(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	events, nextCursor, total, err := Get().GetEventLogPaginated(200, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("GetEventLogPaginated() error = %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0", len(events))
	}
	if nextCursor != 0 {
		t.Errorf("nextCursor = %d, want 0", nextCursor)
	}
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
}

func TestGetEventLogPaginated_ReturnsNewestFirst(t *testing.T) {
	cleanup := setupTestStorage(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	// Manually write events (oldest first as stored on disk)
	log := EventLog{
		Events: []Event{
			{ID: "oldest", Timestamp: 1000},
			{ID: "middle", Timestamp: 2000},
			{ID: "newest", Timestamp: 3000},
		},
	}
	data, _ := cbor.Marshal(log)
	os.WriteFile(globals.EventLogPath, data, 0644)

	events, _, _, err := Get().GetEventLogPaginated(200, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("GetEventLogPaginated() error = %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	if events[0].ID != "newest" {
		t.Errorf("events[0].ID = %s, want newest", events[0].ID)
	}
	if events[2].ID != "oldest" {
		t.Errorf("events[2].ID = %s, want oldest", events[2].ID)
	}
}

func TestGetEventLogPaginated_Pagination(t *testing.T) {
	cleanup := setupTestStorage(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	log := EventLog{
		Events: []Event{
			{ID: "1", Timestamp: 1000},
			{ID: "2", Timestamp: 2000},
			{ID: "3", Timestamp: 3000},
			{ID: "4", Timestamp: 4000},
			{ID: "5", Timestamp: 5000},
		},
	}
	data, _ := cbor.Marshal(log)
	os.WriteFile(globals.EventLogPath, data, 0644)

	// First page of 2
	events, nextCursor, total, _ := Get().GetEventLogPaginated(2, 0, 0, 0, nil)
	if len(events) != 2 {
		t.Fatalf("page 1: got %d events, want 2", len(events))
	}
	if events[0].ID != "5" {
		t.Errorf("page 1 first = %s, want 5", events[0].ID)
	}
	if nextCursor != 2 {
		t.Errorf("nextCursor = %d, want 2", nextCursor)
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}

	// Second page
	events, nextCursor, _, _ = Get().GetEventLogPaginated(2, nextCursor, 0, 0, nil)
	if len(events) != 2 {
		t.Fatalf("page 2: got %d events, want 2", len(events))
	}
	if events[0].ID != "3" {
		t.Errorf("page 2 first = %s, want 3", events[0].ID)
	}
	if nextCursor != 4 {
		t.Errorf("nextCursor = %d, want 4", nextCursor)
	}

	// Last page
	events, nextCursor, _, _ = Get().GetEventLogPaginated(2, nextCursor, 0, 0, nil)
	if len(events) != 1 {
		t.Fatalf("page 3: got %d events, want 1", len(events))
	}
	if nextCursor != 0 {
		t.Errorf("nextCursor = %d, want 0 (no more)", nextCursor)
	}
}

func TestGetEventLogPaginated_Filtering(t *testing.T) {
	cleanup := setupTestStorage(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	log := EventLog{
		Events: []Event{
			{ID: "1", Timestamp: 1000, EventType: "person"},
			{ID: "2", Timestamp: 2000, EventType: "motion"},
			{ID: "3", Timestamp: 3000, EventType: "person"},
			{ID: "4", Timestamp: 4000, EventType: "pet"},
			{ID: "5", Timestamp: 5000, EventType: "motion"},
		},
	}
	data, _ := cbor.Marshal(log)
	os.WriteFile(globals.EventLogPath, data, 0644)

	// Filter by type
	events, _, total, _ := Get().GetEventLogPaginated(200, 0, 0, 0, []string{"person"})
	if total != 2 {
		t.Errorf("type filter: total = %d, want 2", total)
	}
	if len(events) != 2 {
		t.Fatalf("type filter: got %d events, want 2", len(events))
	}

	// Filter by time range
	events, _, total, _ = Get().GetEventLogPaginated(200, 0, 2000, 4000, nil)
	if total != 3 {
		t.Errorf("time filter: total = %d, want 3", total)
	}

	// Combined filter
	events, _, total, _ = Get().GetEventLogPaginated(200, 0, 2000, 4000, []string{"person"})
	if total != 1 {
		t.Errorf("combined filter: total = %d, want 1", total)
	}
	if events[0].ID != "3" {
		t.Errorf("combined filter: got ID %s, want 3", events[0].ID)
	}
}

func TestGetRecordingPath_Found(t *testing.T) {
	cleanup := setupTestStorage(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	// Create a recording file
	recordingID := "test-recording-123"
	recordingFile := filepath.Join(globals.RecordingsPath, recordingID+".mp4")
	os.WriteFile(recordingFile, []byte("video data"), 0644)

	path, err := Get().GetRecordingPath(recordingID)
	if err != nil {
		t.Fatalf("GetRecordingPath() error = %v", err)
	}
	if path != recordingFile {
		t.Errorf("GetRecordingPath() = %s, want %s", path, recordingFile)
	}
}

func TestGetRecordingPath_NotFound(t *testing.T) {
	cleanup := setupTestStorage(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	_, err := Get().GetRecordingPath("nonexistent")
	if err == nil {
		t.Error("GetRecordingPath() should error for nonexistent recording")
	}
}

func TestGetAudioPath_Found(t *testing.T) {
	cleanup := setupTestStorage(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	recordingID := "test-recording-123"
	audioFile := filepath.Join(globals.RecordingsPath, recordingID+"_audio.m4a")
	os.WriteFile(audioFile, []byte("audio data"), 0644)

	path, err := Get().GetAudioPath(recordingID)
	if err != nil {
		t.Fatalf("GetAudioPath() error = %v", err)
	}
	if path != audioFile {
		t.Errorf("GetAudioPath() = %s, want %s", path, audioFile)
	}
}

func TestGetAudioPath_NotFound(t *testing.T) {
	cleanup := setupTestStorage(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	_, err := Get().GetAudioPath("nonexistent")
	if err == nil {
		t.Error("GetAudioPath() should error for nonexistent audio")
	}
}

func TestGetThumbnailPath_Found(t *testing.T) {
	cleanup := setupTestStorage(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	recordingID := "test-recording-123"
	thumbFile := filepath.Join(globals.RecordingsPath, recordingID+".jpg")
	os.WriteFile(thumbFile, []byte("image data"), 0644)

	path, err := Get().GetThumbnailPath(recordingID)
	if err != nil {
		t.Fatalf("GetThumbnailPath() error = %v", err)
	}
	if path != thumbFile {
		t.Errorf("GetThumbnailPath() = %s, want %s", path, thumbFile)
	}
}

func TestGetThumbnailPath_NotFound(t *testing.T) {
	cleanup := setupTestStorage(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	_, err := Get().GetThumbnailPath("nonexistent")
	if err == nil {
		t.Error("GetThumbnailPath() should error for nonexistent thumbnail")
	}
}

func TestEventLogReadWrite(t *testing.T) {
	cleanup := setupTestStorage(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	s := Get()

	// Write events
	log := &EventLog{
		Events: []Event{
			{ID: "event-1", Timestamp: 1000, EventType: "person", Duration: 5.5},
			{ID: "event-2", Timestamp: 2000, EventType: "motion", Duration: 3.2},
		},
	}

	if err := s.writeEventLog(log); err != nil {
		t.Fatalf("writeEventLog() error = %v", err)
	}

	// Read back
	readLog, err := s.readEventLog()
	if err != nil {
		t.Fatalf("readEventLog() error = %v", err)
	}

	if len(readLog.Events) != 2 {
		t.Fatalf("readEventLog() = %d events, want 2", len(readLog.Events))
	}
	if readLog.Events[0].ID != "event-1" {
		t.Errorf("events[0].ID = %s, want event-1", readLog.Events[0].ID)
	}
	if readLog.Events[1].EventType != "motion" {
		t.Errorf("events[1].EventType = %s, want motion", readLog.Events[1].EventType)
	}
}

func TestIsEventDetectionEnabled(t *testing.T) {
	cleanup := setupTestStorage(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	if !IsEventDetectionEnabled() {
		t.Error("default should be true")
	}

	config.Get().SetKey("eventDetectionEnabled", false)
	if IsEventDetectionEnabled() {
		t.Error("should be false when disabled")
	}
}

func TestGetEnabledEventTypes(t *testing.T) {
	cleanup := setupTestStorage(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	if len(GetEnabledEventTypes()) != 0 {
		t.Error("default should be empty")
	}

	config.Get().SetKey("eventDetectionEnabledTypes", []string{"person", "vehicle"})
	types := GetEnabledEventTypes()
	if len(types) != 2 || types[0] != "person" || types[1] != "vehicle" {
		t.Errorf("GetEnabledEventTypes() = %v", types)
	}
}

func TestIsEventTypeEnabled(t *testing.T) {
	cleanup := setupTestStorage(t)
	defer cleanup()

	if err := Init(); err != nil {
		t.Fatal(err)
	}

	// No types configured - uses default
	if !IsEventTypeEnabled("person", true) {
		t.Error("should return defaultIfUnset when no types configured")
	}
	if IsEventTypeEnabled("person", false) {
		t.Error("should return defaultIfUnset when no types configured")
	}

	// With types configured
	config.Get().SetKey("eventDetectionEnabledTypes", []string{"person"})
	if !IsEventTypeEnabled("person", false) {
		t.Error("person should be enabled")
	}
	if IsEventTypeEnabled("vehicle", true) {
		t.Error("vehicle should not be enabled")
	}
}
