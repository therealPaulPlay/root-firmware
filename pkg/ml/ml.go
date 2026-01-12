package ml

import (
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"root-firmware/pkg/globals"
	"root-firmware/pkg/record"
	"root-firmware/pkg/storage"
)

const (
	checkInterval    = 3 * time.Second  // Check for motion/events
	recordDuration   = 10 * time.Second // Fixed recording chunks
	cooldownDuration = 5 * time.Second  // Wait after recording stops
	motionTimeout    = 6 * time.Second  // Stop recording if no motion (must be > checkInterval)
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
	lastMotionAt          time.Time
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

func (m *ML) stopRecordingIfNoMotion() {
	if m.recordingPath != "" && time.Since(m.lastMotionAt) >= motionTimeout {
		log.Printf("ML: No motion detected, stopping recording")
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
		if m.recordingPath != "" {
			m.stopRecording(true)
		}
		m.mu.Unlock()
		return
	}

	// Capture frame
	frame, err := record.Get().CapturePreview()
	if err != nil {
		log.Printf("ML: Failed to capture frame: %v", err)
		return
	}

	// Gate 1: Motion detection
	hasMotion, err := m.motionDetector.detectMotion(frame)
	if err != nil {
		log.Printf("ML: Motion detection failed: %v", err)
		return
	}

	if !hasMotion {
		m.mu.Lock()
		m.stopRecordingIfNoMotion()
		m.mu.Unlock()
		return
	}

	// Gate 2: Object detection
	detection, err := m.objectDetector.detect(frame)
	if err != nil {
		log.Printf("ML: Object detection failed: %v", err)
		m.mu.Lock()
		m.stopRecordingIfNoMotion()
		m.mu.Unlock()
		return
	}

	if detection.EventType == "" || !isEventTypeEnabled(detection.EventType) {
		m.mu.Lock()
		m.stopRecordingIfNoMotion()
		m.mu.Unlock()
		return
	}

	// Motion + object detected
	log.Printf("ML: Detected %s (count: %d)", detection.EventType, detection.Count)
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastMotionAt = time.Now()

	if m.recordingPath == "" {
		log.Printf("ML: Starting recording for %s event", detection.EventType)
		m.startRecording(detection.EventType, frame)
	} else if time.Since(m.recordingStart) >= recordDuration {
		// Split recording if duration limit reached
		log.Printf("ML: Splitting recording (%.1fs)", time.Since(m.recordingStart).Seconds())
		m.stopRecording(false)
		m.startRecording(detection.EventType, frame)
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
	m.lastMotionAt = time.Now()
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
