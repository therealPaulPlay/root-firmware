package ml

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"root-firmware/pkg/globals"
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
	stopChan chan struct{}
	detector *detector
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
	// Skip if low power or camera already in use
	if ups.Get() != nil && ups.Get().IsLowPower() {
		return
	}

	if record.Get().IsStreamingOrRecording() {
		return
	}

	// Capture and detect
	frame, err := record.Get().CapturePreview()
	if err != nil {
		return
	}

	detection, err := m.detector.detect(frame)
	if err != nil || detection.EventType == "" {
		return
	}

	// Start recording with detected event type
	m.startRecording(detection.EventType)
}

func (m *ML) startRecording(eventType string) {
	tempPath := filepath.Join(globals.RecordingsPath, fmt.Sprintf("temp-%d.mp4", time.Now().Unix()))

	if err := record.Get().StartRecording(tempPath); err != nil {
		return
	}

	time.AfterFunc(recordDuration, func() {
		record.Get().StopRecording()
		storage.Get().SaveRecording(tempPath, recordDuration.Seconds(), eventType)
	})
}
