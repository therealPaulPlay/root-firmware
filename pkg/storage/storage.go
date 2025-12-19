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

	// Create event log if it doesn't exist
	if _, err := os.Stat(globals.EventLogPath); os.IsNotExist(err) {
		eventLog := EventLog{Events: []Event{}}
		data, _ := json.Marshal(eventLog)
		if err := os.WriteFile(globals.EventLogPath, data, 0644); err != nil {
			return fmt.Errorf("failed to create event log: %w", err)
		}
	}

	return nil
}

func Get() *Storage {
	if instance == nil {
		panic("storage not initialized - call Init() first")
	}
	return instance
}

// SaveRecording saves a recording with event metadata and generates a thumbnail
// Handles cleanup automatically to ensure minFreeSpace
func (s *Storage) SaveRecording(filePath string, duration float64, eventType string) error {
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

	// Generate thumbnail from first frame of video
	thumbnailPath := filepath.Join(globals.RecordingsPath, fmt.Sprintf("%s.jpg", id.String()))
	if err := s.generateThumbnail(finalPath, thumbnailPath); err != nil {
		// Log error but don't fail - thumbnail is optional
		log.Printf("Failed to generate thumbnail for %s: %v", id.String(), err)
	}

	// Add to event log
	event := Event{
		ID:        id.String(),
		Timestamp: time.Now(),
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

	if err := os.WriteFile(globals.EventLogPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write event log: %w", err)
	}

	return nil
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
		thumbnailPath := filepath.Join(globals.RecordingsPath, fmt.Sprintf("%s.jpg", oldest.ID))

		// Permanently delete video file (log errors but continue)
		if err := os.Remove(videoPath); err != nil && !os.IsNotExist(err) {
			log.Printf("Failed to delete recording %s: %v", oldest.ID, err)
		}

		// Permanently delete thumbnail (log errors but continue)
		if err := os.Remove(thumbnailPath); err != nil && !os.IsNotExist(err) {
			log.Printf("Failed to delete thumbnail %s: %v", oldest.ID, err)
		}

		// Remove from eventLog and save
		eventLog.Events = eventLog.Events[1:]
		if err := s.writeEventLog(eventLog); err != nil {
			log.Printf("Failed to update event log after deleting %s: %v", oldest.ID, err)
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

// generateThumbnail extracts the first frame from a video as a JPEG thumbnail
func (s *Storage) generateThumbnail(videoPath, thumbnailPath string) error {
	cmd := exec.Command("ffmpeg",
		"-i", videoPath,
		"-vframes", "1",
		"-f", "image2",
		"-q:v", "2",
		"-y",
		thumbnailPath,
	)
	return cmd.Run()
}
