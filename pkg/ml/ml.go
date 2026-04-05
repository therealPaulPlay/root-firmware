package ml

import (
	"fmt"
	"image"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/uuid"

	"root-firmware/pkg/config"
	"root-firmware/pkg/globals"
	"root-firmware/pkg/notifications"
	"root-firmware/pkg/record"
	"root-firmware/pkg/sfx"
	"root-firmware/pkg/events"
)

const (
	checkInterval    = 2 * time.Second // Check for motion/events
	cooldownDuration = 6 * time.Second // Wait after recording stops
)

var modelPath = filepath.Join(globals.AssetsPath, "models", "nanodet-plus-m_416.onnx")

type ML struct {
	stopChan            chan struct{}
	objectDetector      *objectDetector
	motionDetector      *motionDetector
	recordingPath       string
	recordingID         string
	recordingEvent      string
	recordingStart      time.Time
	recordingPreview    []byte
	recordingDetection  *events.DetectionResult
	recordingSplitAfter time.Duration // wall-clock time until split
	lastRecordedAt      time.Time
	mu                  sync.Mutex
}

var instance *ML
var once sync.Once

func Init() error {
	var err error
	once.Do(func() {
		objDet, loadErr := newObjectDetector(modelPath)
		if loadErr != nil {
			err = fmt.Errorf("failed to load ML model: %w", loadErr)
			return
		}
		instance = &ML{
			objectDetector: objDet,
			motionDetector: newMotionDetector(),
			stopChan:       make(chan struct{}),
		}
		go instance.loop()
	})
	return err
}

func Get() *ML {
	if instance == nil {
		panic("ml not initialized - call Init() first")
	}
	return instance
}

func (m *ML) loop() {
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			m.check()
		}
	}
}

func (m *ML) stopRecordingIfActive(reason string) {
	if m.recordingPath != "" {
		log.Printf("ML: Stopping recording - %s", reason)
		if err := m.stopRecording(true); err != nil {
			log.Printf("ML: Failed to stop recording: %v", err)
		}
	}
}

func (m *ML) check() {
	// Check cooldown
	m.mu.Lock()
	if time.Since(m.lastRecordedAt) < cooldownDuration {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	// Don't record events until the relay has connected at least once
	if !hasInitialRelayConnect() {
		return
	}

	// If event detection is disabled, stop any active recording and return
	if !events.IsEventDetectionEnabled() {
		m.mu.Lock()
		m.stopRecordingIfActive("event detection disabled")
		m.mu.Unlock()
		return
	}

	// Capture frame
	frame, err := record.Get().CapturePreview(640, 360)
	if err != nil {
		log.Printf("ML: Failed to capture frame: %v", err)
		return
	}

	// Gate 1: Motion detection (motion detection works with a decay system – not a hard cut)
	if !m.motionDetector.detectMotion(frame) {
		m.mu.Lock()
		m.stopRecordingIfActive("no motion")
		m.mu.Unlock()
		return
	}

	// Gate 2: Object detection
	detection, err := m.objectDetector.detect(frame)
	if err != nil {
		log.Printf("ML: Object detection failed: %v", err)
		m.mu.Lock()
		m.stopRecordingIfActive("detection error")
		m.mu.Unlock()
		return
	}

	// Discard detected type if the user has disabled it (e.g. "pet" disabled should not record)
	eventType := detection.EventType
	if eventType != "" && !events.IsEventTypeEnabled(eventType, true) {
		eventType = ""
	}

	// Fall back to generic motion when nothing was classified & motion event is enabled
	if eventType == "" && events.IsEventTypeEnabled(events.TypeMotion, false) {
		eventType = events.TypeMotion
	}

	// No recordable event — stop any active recording
	if eventType == "" {
		m.mu.Lock()
		m.stopRecordingIfActive("no event detected")
		m.mu.Unlock()
		return
	}

	log.Printf("ML: New %s event", eventType)
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.recordingPath == "" {
		log.Printf("ML: Starting recording for %s event", eventType)
		if err := m.startRecording(eventType, frame, detection.Result, true); err != nil {
			log.Printf("ML: Failed to start recording: %v", err)
			return
		}
		if val, ok := config.Get().GetKey("playRecordingSound"); ok {
			if b, ok := val.(bool); ok && b {
				sfx.Get().PlayRecording()
			}
		}
		notifications.Get().SendEventToAll(eventType, m.recordingID, m.recordingPreview)
	} else if time.Since(m.recordingStart) >= m.recordingSplitAfter {
		// Split recording if duration limit reached
		log.Printf("ML: Splitting recording (%.2fs elapsed)", time.Since(m.recordingStart).Seconds())
		if err := m.stopRecording(false); err != nil {
			log.Printf("ML: Failed to stop recording for split: %v", err)
		}
		if err := m.startRecording(eventType, frame, detection.Result, false); err != nil {
			log.Printf("ML: Failed to start split recording: %v", err)
		}
	}
}

func (m *ML) startRecording(eventType string, preview image.Image, detection *events.DetectionResult, withLookback bool) error {
	tempPath := filepath.Join(globals.RecordingsPath, fmt.Sprintf("temp-%d.mp4", time.Now().Unix()))

	id, err := uuid.NewV4()
	if err != nil {
		return fmt.Errorf("failed to generate event ID: %w", err)
	}

	previewJPEG, err := record.PreviewToJPEG(preview)
	if err != nil {
		return fmt.Errorf("failed to encode preview: %w", err)
	}

	record.Get().StartRecording(tempPath, withLookback)

	m.recordingPath = tempPath
	m.recordingID = id.String()
	m.recordingEvent = eventType
	m.recordingStart = time.Now()
	m.recordingPreview = previewJPEG
	m.recordingDetection = detection

	// When lookback is included, the flush adds LookbackDuration of pre-event
	// footage, so we split earlier to compensate & ensure the recording length doesn't exceed MaxRecordDuration
	m.recordingSplitAfter = globals.MaxRecordDuration
	if withLookback {
		m.recordingSplitAfter -= globals.LookbackDuration
	}

	return nil
}

func (m *ML) stopRecording(applyCooldown bool) error {
	_, err := record.Get().StopRecording(m.recordingID, m.recordingEvent, m.recordingPreview, m.recordingDetection)

	m.recordingPath = ""
	m.recordingPreview = nil
	m.recordingDetection = nil
	if applyCooldown {
		m.lastRecordedAt = time.Now()
	}

	return err
}
