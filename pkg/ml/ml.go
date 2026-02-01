package ml

import (
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/globals"
	"root-firmware/pkg/record"
	"root-firmware/pkg/sfx"
)

const (
	checkInterval    = 2 * time.Second // Check for motion/events
	cooldownDuration = 6 * time.Second // Wait after recording stops
)

var modelPath = filepath.Join(globals.AssetsPath, "models", "nanodet-plus-m_416.onnx")

type ML struct {
	stopChan              chan struct{}
	objectDetector        *objectDetector
	motionDetector        *motionDetector
	recordingPath         string
	recordingEvent        string
	recordingStart        time.Time
	recordingStartPreview []byte
	recordingSplitAfter   time.Duration // wall-clock time until split
	lastRecordedAt        time.Time
	mu                    sync.Mutex
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
		m.stopRecording(true)
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

	// If event detection is disabled, stop any active recording and return
	if !IsEventDetectionEnabled() {
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
	hasMotion, err := m.motionDetector.detectMotion(frame)
	if err != nil {
		log.Printf("ML: Motion detection failed: %v", err)
		return
	}

	if !hasMotion {
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
	if eventType != "" && !isEventTypeEnabled(eventType, true) {
		eventType = ""
	}

	// Fall back to generic motion when nothing was classified & motion event is enabled
	if eventType == "" && isEventTypeEnabled("motion", false) {
		eventType = "motion"
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
		m.startRecording(eventType, frame, true)
		if val, ok := config.Get().GetKey("playRecordingSound"); ok && val.(bool) {
			sfx.Get().PlayRecording()
		}
	} else if time.Since(m.recordingStart) >= m.recordingSplitAfter {
		// Split recording if duration limit reached
		log.Printf("ML: Splitting recording (%.2fs elapsed)", time.Since(m.recordingStart).Seconds())
		m.stopRecording(false)
		m.startRecording(eventType, frame, false)
		m.motionDetector.reset(frame)
	}
}

func (m *ML) startRecording(eventType string, preview []byte, withLookback bool) {
	tempPath := filepath.Join(globals.RecordingsPath, fmt.Sprintf("temp-%d.mp4", time.Now().Unix()))

	record.Get().StartRecording(tempPath, withLookback)

	m.recordingPath = tempPath
	m.recordingEvent = eventType
	m.recordingStart = time.Now()
	m.recordingStartPreview = preview

	// When lookback is included, the flush adds LookbackDuration of pre-event
	// footage, so we split earlier to compensate & ensure the recording length doesn't exceed MaxRecordDuration
	m.recordingSplitAfter = globals.MaxRecordDuration
	if withLookback {
		m.recordingSplitAfter -= globals.LookbackDuration
	}
}

func (m *ML) stopRecording(applyCooldown bool) {
	_, err := record.Get().StopRecording(m.recordingEvent, m.recordingStartPreview)
	if err != nil {
		log.Printf("ML: Failed to stop recording: %v", err)
	}

	m.recordingPath = ""
	m.recordingStartPreview = nil
	if applyCooldown {
		m.lastRecordedAt = time.Now()
	}
}
