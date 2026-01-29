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
	"root-firmware/pkg/storage"
)

const (
	checkInterval    = 3 * time.Second  // Check for motion/events
	recordDuration   = 15 * time.Second // Max recording chunk duration
	cooldownDuration = 5 * time.Second  // Wait after recording stops
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

func (m *ML) stopRecordingIfActive() {
	if m.recordingPath != "" {
		log.Printf("ML: No event detected, stopping recording")
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
		m.stopRecordingIfActive()
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
		m.stopRecordingIfActive()
		m.mu.Unlock()
		return
	}

	// Gate 2: Object detection
	detection, err := m.objectDetector.detect(frame)
	if err != nil {
		log.Printf("ML: Object detection failed: %v", err)
		m.mu.Lock()
		m.stopRecordingIfActive()
		m.mu.Unlock()
		return
	}

	// Use classified event type if detected and enabled, otherwise fall back to "motion" if enabled
	eventType := detection.EventType
	if eventType == "" || !isEventTypeEnabled(eventType, true) {
		if !isEventTypeEnabled("motion", false) {
			m.mu.Lock()
			m.stopRecordingIfActive()
			m.mu.Unlock()
			return
		}
		eventType = "motion"
	}

	log.Printf("ML: New %s event", eventType)
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.recordingPath == "" {
		log.Printf("ML: Starting recording for %s event", eventType)
		m.startRecording(eventType, frame)
		if val, ok := config.Get().GetKey("playRecordingSound"); ok && val.(bool) {
			sfx.Get().PlayRecording()
		}
	} else if time.Since(m.recordingStart) >= recordDuration {
		// Split recording if duration limit reached
		log.Printf("ML: Splitting recording (%.1fs)", time.Since(m.recordingStart).Seconds())
		m.stopRecording(false)
		m.startRecording(eventType, frame)
		m.motionDetector.reset(frame)
	}
}

func (m *ML) startRecording(eventType string, preview []byte) {
	tempPath := filepath.Join(globals.RecordingsPath, fmt.Sprintf("temp-%d.mp4", time.Now().Unix()))

	if err := record.Get().StartRecording(tempPath); err != nil {
		return
	}

	m.recordingPath = tempPath
	m.recordingEvent = eventType
	m.recordingStart = time.Now()
	m.recordingStartPreview = preview
}

func (m *ML) stopRecording(applyCooldown bool) {
	record.Get().StopRecording()

	// Round duration to 2 decimal places
	duration := float64(int(time.Since(m.recordingStart).Seconds()*100)) / 100
	storage.Get().SaveRecording(m.recordingPath, duration, m.recordingEvent, m.recordingStartPreview)

	m.recordingPath = ""
	m.recordingStartPreview = nil
	if applyCooldown {
		m.lastRecordedAt = time.Now()
	}
}
