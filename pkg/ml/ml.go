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
	checkInterval  = 5 * time.Second
	recordDuration = 10 * time.Second
	modelPath      = "pkg/ml/models/nanodet-plus-m_416.onnx"
)

type ML struct {
	mu        sync.Mutex
	stopChan  chan struct{}
	recording bool
	detector  *detector
}

var instance *ML
var once sync.Once

func Init() error {
	var err error
	once.Do(func() {
		det, loadErr := newDetector(modelPath)
		if loadErr != nil {
			err = fmt.Errorf("failed to load ML model: %w", loadErr)
			return
		}
		instance = &ML{
			detector: det,
			stopChan: make(chan struct{}),
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
	// Skip if low power or already recording
	if ups.Get() != nil && ups.Get().IsLowPower() {
		return
	}
	if m.recording {
		return
	}

	// Capture and detect
	frame, err := record.Get().CapturePreview()
	if err != nil {
		return
	}

	detection, err := m.detector.detect(frame)
	if err != nil || !detection.HasPerson {
		return
	}

	// Start recording
	m.startRecording()
}

func (m *ML) startRecording() {
	m.mu.Lock()
	if m.recording {
		m.mu.Unlock()
		return
	}
	m.recording = true
	m.mu.Unlock()

	tempPath := filepath.Join("/data/recordings", fmt.Sprintf("temp-%d.mp4", time.Now().Unix()))

	if err := record.Get().StartRecording(tempPath); err != nil {
		m.recording = false
		return
	}

	time.AfterFunc(recordDuration, func() {
		record.Get().StopRecording()
		storage.Get().SaveRecording(tempPath, recordDuration.Seconds(), "person")
		m.recording = false
	})
}
