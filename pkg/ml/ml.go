package ml

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"root-firmware/pkg/globals"
	"root-firmware/pkg/record"
	"root-firmware/pkg/storage"
)

const (
	checkInterval    = 1 * time.Second  // Check frequently for motion
	recordDuration   = 10 * time.Second // Fixed recording chunks
	cooldownDuration = 5 * time.Second  // Wait after recording stops
	motionTimeout    = 3 * time.Second  // Stop recording if no motion
)

var modelPath = filepath.Join(globals.AssetsPath, "models", "nanodet-plus-m_416.onnx")

type ML struct {
	stopChan       chan struct{}
	objectDetector *objectDetector
	motionDetector *motionDetector
	recordingPath  string
	recordingEvent string
	recordingStart time.Time
	lastMotionAt   time.Time
	lastRecordedAt time.Time
	mu             sync.Mutex
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

func (m *ML) check() {
	// Check recording state and cooldown (need lock for timestamps)
	m.mu.Lock()
	isRecording := record.Get().IsStreamingOrRecording()
	if !isRecording && time.Since(m.lastRecordedAt) < cooldownDuration {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	// Capture frame without holding lock (this is a slow blocking call to ffmpeg)
	frame, err := record.Get().CapturePreview()
	if err != nil {
		return
	}

	// Gate 1: Motion detection (fast, cheap)
	hasMotion, err := m.motionDetector.detectMotion(frame)
	if err != nil {
		return
	}

	if !hasMotion {
		// Stop recording if no motion for timeout period
		m.mu.Lock()
		if isRecording && time.Since(m.lastMotionAt) >= motionTimeout {
			m.stopRecording()
		}
		m.mu.Unlock()
		return
	}

	// Gate 2: ML object detection (slow, expensive)
	detection, err := m.objectDetector.detect(frame)
	if err != nil || detection.EventType == "" {
		m.mu.Lock()
		if isRecording && time.Since(m.lastMotionAt) >= motionTimeout {
			m.stopRecording()
		}
		m.mu.Unlock()
		return
	}

	// Motion detected with relevant object
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	m.lastMotionAt = now

	if !isRecording {
		m.startRecording(detection.EventType)
		return
	}

	// Split recording if reached duration limit
	if time.Since(m.recordingStart) >= recordDuration {
		m.stopRecording()
		m.startRecording(detection.EventType)
		// Reset background to prevent re-detecting same stationary object
		m.motionDetector.reset(frame)
	}
}

func (m *ML) startRecording(eventType string) {
	tempPath := filepath.Join(globals.RecordingsPath, fmt.Sprintf("temp-%d.mp4", time.Now().Unix()))

	if err := record.Get().StartRecording(tempPath); err != nil {
		return
	}

	m.recordingPath = tempPath
	m.recordingEvent = eventType
	m.recordingStart = time.Now()
	m.lastMotionAt = time.Now()
}

func (m *ML) stopRecording() {
	record.Get().StopRecording()

	duration := time.Since(m.recordingStart).Seconds()
	storage.Get().SaveRecording(m.recordingPath, duration, m.recordingEvent)

	m.recordingPath = ""
	m.lastRecordedAt = time.Now()
}
