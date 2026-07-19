package events

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/fxamacker/cbor/v2"
	rootproto "github.com/therealPaulPlay/root-e2ee-protocol/go-server"

	"root-firmware/pkg/config"
	"root-firmware/pkg/fsutil"
	"root-firmware/pkg/globals"
)

const (
	minFreeSpace = 4 * 1024 * 1024 * 1024 // 4GB in bytes (needed for update bundles, to maintain SD card performance etc.)
)

// encryptFileToPath reads a file, encrypts it, and writes to a destination path
func encryptFileToPath(srcPath, dstPath string, key []byte) error {
	session, err := rootproto.SessionFromKeyAES256GCM(key)
	if err != nil {
		return err
	}

	plaintext, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	ciphertext, err := session.Encrypt(plaintext, nil)
	if err != nil {
		return err
	}

	return fsutil.AtomicWrite(dstPath, ciphertext, 0644)
}

type DetectionBox struct {
	Label      string  `cbor:"label"`
	Confidence float32 `cbor:"confidence"`
	X1         float32 `cbor:"x1"`
	Y1         float32 `cbor:"y1"`
	X2         float32 `cbor:"x2"`
	Y2         float32 `cbor:"y2"`
}

type VideoDetectionResult struct {
	Boxes     []DetectionBox `cbor:"boxes"`
	ModelSize [2]int         `cbor:"modelSize"`
}

type AudioDetectionResult struct {
	Label string `cbor:"label"` // "siren" or "alarm"
}

type Event struct {
	ID             string                `cbor:"id"`
	Timestamp      int64                 `cbor:"timestamp"`
	Duration       float64               `cbor:"duration"`
	EventType      string                `cbor:"eventType"`
	DeletionAt     int64                 `cbor:"deletionAt,omitempty"`
	VideoDetection *VideoDetectionResult `cbor:"videoDetection,omitempty"`
	AudioDetection *AudioDetectionResult `cbor:"audioDetection,omitempty"`
}

type EventLog struct {
	Events []Event `cbor:"events"`
}

type Storage struct {
	mu sync.Mutex
}

var instance *Storage
var once sync.Once

func Init() error {
	once.Do(func() {
		instance = &Storage{}
	})

	// Ensure directories exist - no-op if they already exist
	if err := os.MkdirAll(globals.FirmwareDataDir, 0755); err != nil {
		return fmt.Errorf("failed to create firmware data directory: %w", err)
	}
	if err := os.MkdirAll(globals.RecordingsDir, 0755); err != nil {
		return fmt.Errorf("failed to create recordings directory: %w", err)
	}

	// Clean up orphaned temp files from interrupted mux jobs
	cleanupOrphanedFiles()

	// Create or recover event log
	if err := recoverEventLog(); err != nil {
		return fmt.Errorf("failed to initialize event log: %w", err)
	}

	// Run overdue scheduled deletions immediately, then check every 30s
	deletionLoopOnce.Do(func() {
		go func() {
			instance.processDueDeletions()
			for range time.Tick(30 * time.Second) {
				instance.processDueDeletions()
			}
		}()
	})

	return nil
}

func recoverEventLog() error {
	data, err := os.ReadFile(globals.EventLogPath)
	if os.IsNotExist(err) {
		empty, err := cbor.Marshal(EventLog{Events: []Event{}})
		if err != nil {
			return fmt.Errorf("failed to marshal empty event log: %w", err)
		}
		return fsutil.AtomicWrite(globals.EventLogPath, empty, 0644)
	}

	var test EventLog
	if cbor.Unmarshal(data, &test) != nil {
		log.Printf("Events: Corrupted event log, resetting")
		if err := os.Rename(globals.EventLogPath, globals.EventLogPath+".corrupted"); err != nil {
			log.Printf("Events: Failed to backup corrupted event log: %v", err)
		}
		empty, err := cbor.Marshal(EventLog{Events: []Event{}})
		if err != nil {
			return fmt.Errorf("failed to marshal empty event log: %w", err)
		}
		return fsutil.AtomicWrite(globals.EventLogPath, empty, 0644)
	}

	return nil
}

// cleanupOrphanedFiles removes temp-* files left behind by interrupted mux jobs
func cleanupOrphanedFiles() {
	matches, err := filepath.Glob(filepath.Join(globals.RecordingsDir, "temp-*"))
	if err != nil {
		log.Printf("Events: Failed to scan for orphaned files: %v", err)
		return
	}
	for _, f := range matches {
		if err := os.Remove(f); err != nil {
			log.Printf("Events: Failed to remove orphaned file %s: %v", f, err)
		} else {
			log.Printf("Events: Removed orphaned file %s", f)
		}
	}
}

func Get() *Storage {
	if instance == nil {
		panic("events not initialized - call Init() first")
	}
	return instance
}

// SaveRecording saves a recording with event metadata and preview thumbnail
// Handles cleanup automatically to ensure minFreeSpace
func (s *Storage) SaveRecording(eventID string, filePath string, duration float64, eventType string, preview []byte, videoDetection *VideoDetectionResult, audioDetection *AudioDetectionResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get recording file size
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("failed to stat recording: %w", err)
	}

	// Cleanup old recordings until we have enough space
	if err := s.cleanupForRecording(info.Size()); err != nil {
		return fmt.Errorf("cleanup failed: %w", err)
	}

	// Add to event log first
	// If we crash immediately after this, the event log will reference non-existent files,
	// which is better than orphaned files that can never be cleaned up
	event := Event{
		ID:             eventID,
		Timestamp:      time.Now().UnixMilli(),
		Duration:       duration,
		EventType:      eventType,
		VideoDetection: videoDetection,
		AudioDetection: audioDetection,
	}

	eventLog, err := s.readEventLog()
	if err != nil {
		return err
	}

	eventLog.Events = append(eventLog.Events, event)
	if err := s.writeEventLog(eventLog); err != nil {
		return err
	}

	// Get the at-rest file encryption key and create a session
	fileEncryptionKey, err := config.Get().GetFileEncryptionKeyAES256()
	if err != nil {
		return fmt.Errorf("failed to get encryption key: %w", err)
	}

	// Encrypt and save video from temp to final location
	finalPath := filepath.Join(globals.RecordingsDir, fmt.Sprintf("%s.mp4", eventID))
	if err := encryptFileToPath(filePath, finalPath, fileEncryptionKey); err != nil {
		return fmt.Errorf("failed to encrypt recording: %w", err)
	}
	os.Remove(filePath) // Clean up temp file

	// Encrypt and save audio file if it exists
	audioTempPath := filePath[:len(filePath)-4] + "_audio.m4a"
	if _, err := os.Stat(audioTempPath); err == nil {
		audioFinalPath := filepath.Join(globals.RecordingsDir, fmt.Sprintf("%s_audio.m4a", eventID))
		if err := encryptFileToPath(audioTempPath, audioFinalPath, fileEncryptionKey); err != nil {
			log.Printf("Events: Failed to encrypt audio for %s: %v", eventID, err)
		}
		os.Remove(audioTempPath) // Clean up temp file
	}

	// Encrypt and save preview as thumbnail
	if preview != nil {
		thumbnailPath := filepath.Join(globals.RecordingsDir, fmt.Sprintf("%s.jpg", eventID))
		session, err := rootproto.SessionFromKeyAES256GCM(fileEncryptionKey)
		if err != nil {
			log.Printf("Events: Failed to create encryption session for %s: %v", eventID, err)
		} else if encryptedPreview, err := session.Encrypt(preview, nil); err != nil {
			log.Printf("Events: Failed to encrypt thumbnail for %s: %v", eventID, err)
		} else if err := fsutil.AtomicWrite(thumbnailPath, encryptedPreview, 0644); err != nil {
			log.Printf("Events: Failed to save thumbnail for %s: %v", eventID, err)
		}
	}

	return nil
}

// GetEventLogPaginated returns a page of events (newest first) with optional filtering
// If untilEventId is set, returns at least limit events but extends to include the target event
func (s *Storage) GetEventLogPaginated(limit, cursor int, startTime, endTime int64, eventTypes []string, untilEventId string) ([]Event, int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	eventLog, err := s.readEventLog()
	if err != nil {
		return nil, 0, 0, err
	}

	// Build type set for fast lookup
	typeSet := make(map[string]bool, len(eventTypes))
	for _, t := range eventTypes {
		typeSet[t] = true
	}

	// Filter in reverse order (newest first)
	var filtered []Event
	for i := len(eventLog.Events) - 1; i >= 0; i-- {
		e := eventLog.Events[i]
		if startTime > 0 && e.Timestamp < startTime {
			continue
		}
		if endTime > 0 && e.Timestamp > endTime {
			continue
		}
		if len(typeSet) > 0 && !typeSet[e.EventType] {
			continue
		}
		filtered = append(filtered, e)
	}

	total := len(filtered)

	if cursor < 0 {
		cursor = 0
	}
	if cursor >= total {
		return []Event{}, 0, total, nil
	}

	// At least limit events, but extend to include untilEventId if it exists
	end := cursor + limit
	if untilEventId != "" {
		for i := cursor; i < total; i++ {
			if filtered[i].ID == untilEventId && i >= end {
				end = i + 1
				break
			}
		}
	}

	nextCursor := 0
	if end < total {
		nextCursor = end
	} else {
		end = total
	}

	return filtered[cursor:end], nextCursor, total, nil
}

// GetRecordingPath returns the file path for a recording by ID
func (s *Storage) GetRecordingPath(id string) (string, error) {
	filePath := filepath.Join(globals.RecordingsDir, fmt.Sprintf("%s.mp4", id))
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return "", fmt.Errorf("recording not found: %s", id)
	}
	return filePath, nil
}

// GetAudioPath returns the file path for an audio recording by ID
func (s *Storage) GetAudioPath(id string) (string, error) {
	filePath := filepath.Join(globals.RecordingsDir, fmt.Sprintf("%s_audio.m4a", id))
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return "", fmt.Errorf("audio not found: %s", id)
	}
	return filePath, nil
}

// GetThumbnailPath returns the file path for a thumbnail by ID
func (s *Storage) GetThumbnailPath(id string) (string, error) {
	filePath := filepath.Join(globals.RecordingsDir, fmt.Sprintf("%s.jpg", id))
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return "", fmt.Errorf("thumbnail not found: %s", id)
	}
	return filePath, nil
}

func (s *Storage) readEventLog() (*EventLog, error) {
	data, err := os.ReadFile(globals.EventLogPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read event log: %w", err)
	}

	var eventLog EventLog
	if err := cbor.Unmarshal(data, &eventLog); err != nil {
		return nil, fmt.Errorf("failed to parse event log: %w", err)
	}

	return &eventLog, nil
}

func (s *Storage) writeEventLog(eventLog *EventLog) error {
	data, err := cbor.Marshal(eventLog)
	if err != nil {
		return fmt.Errorf("failed to marshal event log: %w", err)
	}

	return fsutil.AtomicWrite(globals.EventLogPath, data, 0644)
}

// cleanupForRecording deletes old recordings until we have enough space
func (s *Storage) cleanupForRecording(recordingSize int64) error {
	needed := recordingSize + minFreeSpace
	maxIterations := 50

	// Read event log once upfront
	eventLog, err := s.readEventLog()
	if err != nil {
		return err
	}

	initialCount := len(eventLog.Events)
	flush := func() error {
		if len(eventLog.Events) < initialCount {
			if err := s.writeEventLog(eventLog); err != nil {
				log.Printf("Events: Failed to update event log after cleanup: %v", err)
				return err
			}
		}
		return nil
	}

	deletions := 0
	for deletions < maxIterations {
		free, err := s.getFreeSpace()
		if err != nil {
			flush()
			return err
		}

		if free >= needed {
			return flush()
		}

		if len(eventLog.Events) == 0 {
			flush()
			return fmt.Errorf("insufficient space and no recordings to delete")
		}

		// Remove oldest event (first in array)
		deleteEventFiles(eventLog.Events[0].ID)
		eventLog.Events = eventLog.Events[1:]
		deletions++
	}

	flush()
	return fmt.Errorf("failed to free enough space after %d deletions", maxIterations)
}

// getFreeSpace returns free space in bytes on data partition
func (s *Storage) getFreeSpace() (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(globals.RecordingsDir, &stat); err != nil {
		return 0, fmt.Errorf("failed to get filesystem stats: %w", err)
	}

	return int64(stat.Bavail) * int64(stat.Bsize), nil
}
