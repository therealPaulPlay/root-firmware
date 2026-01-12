package storage

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"root-firmware/pkg/fsutil"
	"root-firmware/pkg/globals"

	"github.com/gofrs/uuid"
)

const (
	minFreeSpace = 3 * 1024 * 1024 * 1024 // 3GB in bytes
)

type Event struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Duration  float64   `json:"duration"` // seconds
	EventType string    `json:"event_type"`
}

type EventLog struct {
	Events []Event `json:"events"`
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

	// MkdirAll is safe - it's a no-op if directory exists
	if err := os.MkdirAll(globals.RecordingsPath, 0755); err != nil {
		return fmt.Errorf("failed to create recordings directory: %w", err)
	}

	// Run fsck on data partition (non-blocking, best effort)
	exec.Command("fsck", "-p", globals.DataDir).Run()

	// Create or recover event log
	if err := recoverEventLog(); err != nil {
		return fmt.Errorf("failed to initialize event log: %w", err)
	}

	return nil
}

func recoverEventLog() error {
	data, err := os.ReadFile(globals.EventLogPath)
	if os.IsNotExist(err) {
		data, _ := json.Marshal(EventLog{Events: []Event{}})
		return fsutil.AtomicWrite(globals.EventLogPath, data, 0644)
	}

	var test EventLog
	if json.Unmarshal(data, &test) != nil {
		log.Printf("Storage: Corrupted event log, resetting")
		os.Rename(globals.EventLogPath, globals.EventLogPath+".corrupted")
		data, _ := json.Marshal(EventLog{Events: []Event{}})
		return fsutil.AtomicWrite(globals.EventLogPath, data, 0644)
	}

	return nil
}

func Get() *Storage {
	if instance == nil {
		panic("storage not initialized - call Init() first")
	}
	return instance
}

// SaveRecording saves a recording with event metadata and preview thumbnail
// Handles cleanup automatically to ensure minFreeSpace
func (s *Storage) SaveRecording(filePath string, duration float64, eventType string, preview []byte) error {
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

	// Generate ID and move recording to final location
	id, err := uuid.NewV4()
	if err != nil {
		return fmt.Errorf("failed to generate ID: %w", err)
	}

	finalPath := filepath.Join(globals.RecordingsPath, fmt.Sprintf("%s.mp4", id.String()))
	if err := os.Rename(filePath, finalPath); err != nil {
		return fmt.Errorf("failed to move recording: %w", err)
	}

	// Rename audio file if it exists
	audioTempPath := filePath[:len(filePath)-4] + "_audio.m4a"
	if _, err := os.Stat(audioTempPath); err == nil {
		audioFinalPath := filepath.Join(globals.RecordingsPath, fmt.Sprintf("%s_audio.m4a", id.String()))
		if err := os.Rename(audioTempPath, audioFinalPath); err != nil {
			log.Printf("Storage: Failed to rename audio file for %s: %v", id.String(), err)
		}
	}

	// Save preview as thumbnail
	if preview != nil {
		thumbnailPath := filepath.Join(globals.RecordingsPath, fmt.Sprintf("%s.jpg", id.String()))
		if err := fsutil.AtomicWrite(thumbnailPath, preview, 0644); err != nil {
			log.Printf("Storage: Failed to save thumbnail for %s: %v", id.String(), err)
		}
	}

	// Add to event log
	event := Event{
		ID:        id.String(),
		Timestamp: time.Now().UTC(),
		Duration:  duration,
		EventType: eventType,
	}

	eventLog, err := s.readEventLog()
	if err != nil {
		return err
	}

	eventLog.Events = append(eventLog.Events, event)
	return s.writeEventLog(eventLog)
}

// GetEventLog returns all events sorted by timestamp (newest first)
func (s *Storage) GetEventLog() ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	eventLog, err := s.readEventLog()
	if err != nil {
		return nil, err
	}

	// Return reversed (newest first)
	events := make([]Event, len(eventLog.Events))
	for i := range eventLog.Events {
		events[i] = eventLog.Events[len(eventLog.Events)-1-i]
	}

	return events, nil
}

// GetRecordingPath returns the file path for a recording by ID
func (s *Storage) GetRecordingPath(id string) (string, error) {
	filePath := filepath.Join(globals.RecordingsPath, fmt.Sprintf("%s.mp4", id))
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return "", fmt.Errorf("recording not found: %s", id)
	}
	return filePath, nil
}

// GetAudioPath returns the file path for an audio recording by ID
func (s *Storage) GetAudioPath(id string) (string, error) {
	filePath := filepath.Join(globals.RecordingsPath, fmt.Sprintf("%s_audio.m4a", id))
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return "", fmt.Errorf("audio not found: %s", id)
	}
	return filePath, nil
}

// GetThumbnailPath returns the file path for a thumbnail by ID
func (s *Storage) GetThumbnailPath(id string) (string, error) {
	filePath := filepath.Join(globals.RecordingsPath, fmt.Sprintf("%s.jpg", id))
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

	var log EventLog
	if err := json.Unmarshal(data, &log); err != nil {
		return nil, fmt.Errorf("failed to parse event log: %w", err)
	}

	return &log, nil
}

func (s *Storage) writeEventLog(log *EventLog) error {
	data, err := json.MarshalIndent(log, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal event log: %w", err)
	}

	return fsutil.AtomicWrite(globals.EventLogPath, data, 0644)
}

// cleanupForRecording deletes old recordings until we have enough space
func (s *Storage) cleanupForRecording(recordingSize int64) error {
	needed := recordingSize + minFreeSpace
	maxIterations := 50
	iterations := 0

	for {
		iterations++
		if iterations > maxIterations {
			return fmt.Errorf("failed to free enough space after %d deletions", maxIterations)
		}

		free, err := s.getFreeSpace()
		if err != nil {
			return err
		}

		if free >= needed {
			return nil // Enough space available
		}

		// Need to delete oldest recording
		eventLog, err := s.readEventLog()
		if err != nil {
			return err
		}

		if len(eventLog.Events) == 0 {
			return fmt.Errorf("insufficient space and no recordings to delete")
		}

		// Remove oldest event (first in array)
		oldest := eventLog.Events[0]
		videoPath := filepath.Join(globals.RecordingsPath, fmt.Sprintf("%s.mp4", oldest.ID))
		audioPath := filepath.Join(globals.RecordingsPath, fmt.Sprintf("%s_audio.m4a", oldest.ID))
		thumbnailPath := filepath.Join(globals.RecordingsPath, fmt.Sprintf("%s.jpg", oldest.ID))

		// Permanently delete video file (log errors but continue)
		if err := os.Remove(videoPath); err != nil && !os.IsNotExist(err) {
			log.Printf("Storage: Failed to delete recording %s: %v", oldest.ID, err)
		}

		// Permanently delete audio file if it exists (log errors but continue)
		if err := os.Remove(audioPath); err != nil && !os.IsNotExist(err) {
			log.Printf("Storage: Failed to delete audio file %s: %v", oldest.ID, err)
		}

		// Permanently delete thumbnail (log errors but continue)
		if err := os.Remove(thumbnailPath); err != nil && !os.IsNotExist(err) {
			log.Printf("Storage: Failed to delete thumbnail %s: %v", oldest.ID, err)
		}

		// Remove from eventLog and save
		eventLog.Events = eventLog.Events[1:]
		if err := s.writeEventLog(eventLog); err != nil {
			log.Printf("Storage: Failed to update event log after deleting %s: %v", oldest.ID, err)
		}
	}
}

// getFreeSpace returns free space in bytes on data partition
func (s *Storage) getFreeSpace() (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(globals.RecordingsPath, &stat); err != nil {
		return 0, fmt.Errorf("failed to get filesystem stats: %w", err)
	}

	return int64(stat.Bavail) * int64(stat.Bsize), nil
}
