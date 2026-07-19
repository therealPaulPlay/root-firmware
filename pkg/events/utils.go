package events

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"root-firmware/pkg/globals"
)

const deletionDelay = 7 * 24 * time.Hour

var deletionLoopOnce sync.Once

// deleteEventFiles removes the recording, audio and thumbnail files for an event
func deleteEventFiles(eventID string) {
	for _, path := range []string{
		filepath.Join(globals.RecordingsDir, fmt.Sprintf("%s.mp4", eventID)),
		filepath.Join(globals.RecordingsDir, fmt.Sprintf("%s_audio.m4a", eventID)),
		filepath.Join(globals.RecordingsDir, fmt.Sprintf("%s.jpg", eventID)),
	} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("Events: Failed to delete %s: %v", path, err)
		}
	}
}

// SetEventDeletion schedules or unschedules deletion for an event
// Returns the new deletion timestamp in ms (0 when unscheduled)
func (s *Storage) SetEventDeletion(eventID string, scheduled bool) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	eventLog, err := s.readEventLog()
	if err != nil {
		return 0, err
	}

	for i := range eventLog.Events {
		if eventLog.Events[i].ID != eventID {
			continue
		}
		var deletionAt int64
		if scheduled {
			deletionAt = time.Now().Add(deletionDelay).UnixMilli()
		}
		eventLog.Events[i].DeletionAt = deletionAt
		return deletionAt, s.writeEventLog(eventLog)
	}

	return 0, fmt.Errorf("event not found: %s", eventID)
}

// processDueDeletions deletes events whose scheduled deletion time has passed
func (s *Storage) processDueDeletions() {
	s.mu.Lock()
	defer s.mu.Unlock()

	eventLog, err := s.readEventLog()
	if err != nil {
		log.Printf("Events: Failed to read event log for scheduled deletions: %v", err)
		return
	}

	now := time.Now().UnixMilli()
	kept := eventLog.Events[:0]
	for _, e := range eventLog.Events {
		if e.DeletionAt > 0 && e.DeletionAt <= now {
			deleteEventFiles(e.ID)
			continue
		}
		kept = append(kept, e)
	}

	if len(kept) == len(eventLog.Events) {
		return
	}
	eventLog.Events = kept
	if err := s.writeEventLog(eventLog); err != nil {
		log.Printf("Events: Failed to update event log after scheduled deletions: %v", err)
	}
}
