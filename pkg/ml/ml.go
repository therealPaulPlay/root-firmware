package ml

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"root-firmware/pkg/record"
	"root-firmware/pkg/storage"
	"root-firmware/pkg/ups"
)

const (
	detectionInterval = 5 * time.Second
	recordDuration    = 10 * time.Second
)

// TODO: Handle actual detection, should handle Start and Stop atuomatically depending on if device is in low power mode
// No need to export Start and Stop!

type ML struct {
	mu        sync.Mutex
	running   bool
	stopChan  chan struct{}
	recording bool
}

var instance *ML
var once sync.Once

func Init() {
	once.Do(func() {
		instance = &ML{}
	})
}

func Get() *ML {
	if instance == nil {
		panic("ml not initialized - call Init() first")
	}
	return instance
}

// Start starts ML detection loop
func (m *ML) Start() error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("already running")
	}
	m.running = true
	m.stopChan = make(chan struct{})
	m.mu.Unlock()

	go m.detectLoop()
	return nil
}

// Stop stops ML detection loop
func (m *ML) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		return
	}

	close(m.stopChan)
	m.running = false
}

func (m *ML) detectLoop() {
	ticker := time.NewTicker(detectionInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			// Skip detection if in low power mode or already recording
			if ups.Get() != nil && ups.Get().IsLowPower() {
				continue
			}
			if m.recording {
				continue
			}

			// Run detection
			detected, eventType := m.runDetection()
			if detected {
				m.handleEvent(eventType)
			}
		}
	}
}

// runDetection captures frame and runs ML inference
func (m *ML) runDetection() (bool, string) {
	// Capture single frame
	frame, err := record.Get().CapturePreview()
	if err != nil {
		return false, ""
	}

	// TODO: Run ML model inference on frame
	// Options for Go ML:
	// 1. TensorFlow Lite Go bindings (github.com/mattn/go-tflite)
	// 2. ONNX Runtime Go (github.com/yalue/onnxruntime_go)
	// 3. OpenCV Go (gocv.io/x/gocv)
	//
	// For now, placeholder returns false (no detection)
	_ = frame

	// When implemented, return:
	// - true, "person" for person detection
	// - true, "motion" for motion detection
	// - true, "package" for package detection

	return false, ""
}

// handleEvent starts recording when event detected
func (m *ML) handleEvent(eventType string) {
	if m.recording {
		return
	}
	m.recording = true

	// Generate recording path in /data/recordings
	filename := fmt.Sprintf("temp-%d.mp4", time.Now().Unix())
	tempPath := filepath.Join("/data/recordings", filename)

	// Start recording
	if err := record.Get().StartRecording(tempPath); err != nil {
		m.recording = false
		return
	}

	// Stop recording after duration
	time.AfterFunc(recordDuration, func() {
		record.Get().StopRecording()
		m.recording = false

		// Save recording to storage (moves to permanent location)
		storage.Get().SaveRecording(tempPath, recordDuration.Seconds(), eventType)
	})
}
