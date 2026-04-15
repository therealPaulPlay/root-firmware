package ml

import (
	"encoding/binary"
	"log"
	"sync"
	"time"

	alertdetector "github.com/therealPaulPlay/go-alert-detector"

	"root-firmware/pkg/events"
	"root-firmware/pkg/globals"
	"root-firmware/pkg/record"
)

const alertWindowSeconds = 10

var alertInputSamples = alertWindowSeconds * globals.AudioSampleRate

type audioDetector struct {
	mu       sync.Mutex
	audioCh  chan []byte
	buffer   []int16
	detector *alertdetector.Detector
	active   bool
}

func newAudioDetector() *audioDetector {
	ad := &audioDetector{
		detector: alertdetector.New(globals.AudioSampleRate),
	}

	audioCh, err := record.Get().StartAudioStream()
	if err != nil {
		log.Printf("ML: Audio detection unavailable (no microphone)")
		return ad
	}

	ad.audioCh = audioCh
	ad.active = true
	go ad.bufferLoop()
	return ad
}

func (ad *audioDetector) bufferLoop() {
	for chunk := range ad.audioCh {
		ad.mu.Lock()
		ad.buffer = append(ad.buffer, decodePCM(chunk)...)
		if len(ad.buffer) > alertInputSamples {
			ad.buffer = ad.buffer[len(ad.buffer)-alertInputSamples:]
		}
		ad.mu.Unlock()
	}
	ad.mu.Lock()
	ad.active = false
	ad.mu.Unlock()
}

func (ad *audioDetector) detect() *events.AudioDetectionResult {
	ad.mu.Lock()
	defer ad.mu.Unlock()
	if !ad.active || len(ad.buffer) < alertInputSamples {
		return nil
	}

	start := time.Now()
	result := ad.detector.Analyze(ad.buffer)
	log.Printf("ML: Audio — analyzed in %dms", time.Since(start).Milliseconds())

	if result == nil {
		return nil
	}
	return &events.AudioDetectionResult{
		Label: result.Label,
	}
}

func decodePCM(data []byte) []int16 {
	bytesPerSample := globals.AudioBitsPerSample / 8
	n := len(data) / bytesPerSample
	samples := make([]int16, n)
	for i := range n {
		samples[i] = int16(binary.LittleEndian.Uint16(data[i*bytesPerSample:]))
	}
	return samples
}
