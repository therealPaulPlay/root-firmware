package ml

import (
	"bytes"
	"encoding/binary"
	"log"
	"math"
	"sync"

	"github.com/gunter-q12/resample"
	ort "github.com/yalue/onnxruntime_go"

	"root-firmware/pkg/events"
	"root-firmware/pkg/globals"
	"root-firmware/pkg/record"
)

const (
	alertWindowSeconds = 10
	alertModelSR       = 16000
	alertModelSamples  = alertWindowSeconds * alertModelSR
	alertBinaryThresh  = 0.5 // Don't adjust, model was trained wtih 0.5 as the threshold in mind
	alertRMSGate       = 200
)

// 10s of raw 48kHz S16_LE
const alertInputBytes = alertWindowSeconds * globals.AudioSampleRate * (globals.AudioBitsPerSample / 8)

type audioDetector struct {
	mu           sync.Mutex
	audioCh      chan []byte
	buffer       []byte
	session      *ort.DynamicAdvancedSession
	prevDetected bool                         // whether the prior inference tick crossed threshold
	stableResult *events.AudioDetectionResult // current debounced state returned to callers
}

func newAudioDetector(modelPath string) *audioDetector {
	ad := &audioDetector{}

	if !ort.IsInitialized() {
		if err := ort.InitializeEnvironment(); err != nil {
			log.Printf("ML: Failed to initialize ONNX environment for audio: %v", err)
			return ad
		}
	}

	opts, err := ort.NewSessionOptions()
	if err != nil {
		log.Printf("ML: Failed to create audio session options: %v", err)
		return ad
	}
	defer opts.Destroy()
	opts.SetIntraOpNumThreads(1)
	opts.SetInterOpNumThreads(1)

	session, err := ort.NewDynamicAdvancedSession(
		modelPath,
		[]string{"waveform"},
		[]string{"binary_logit", "subclass_logits"},
		opts,
	)
	if err != nil {
		log.Printf("ML: Failed to create audio ONNX session: %v", err)
		return ad
	}
	ad.session = session

	audioCh, err := record.Get().SubscribeAudio()
	if err != nil {
		log.Printf("ML: Audio detection unavailable (no microphone)")
		return ad
	}

	ad.audioCh = audioCh
	go ad.bufferLoop()
	return ad
}

func (ad *audioDetector) bufferLoop() {
	for chunk := range ad.audioCh {
		ad.mu.Lock()
		ad.buffer = append(ad.buffer, chunk...)
		if len(ad.buffer) > alertInputBytes {
			ad.buffer = ad.buffer[len(ad.buffer)-alertInputBytes:]
		}
		ad.mu.Unlock()
	}
	log.Printf("ML: Audio channel closed, draining buffer")
	ad.mu.Lock()
	ad.buffer = nil
	ad.mu.Unlock()
}

func (ad *audioDetector) detect() *events.AudioDetectionResult {
	ad.mu.Lock()
	if ad.session == nil || len(ad.buffer) < alertInputBytes {
		ad.mu.Unlock()
		return nil
	}
	raw := make([]byte, alertInputBytes)
	copy(raw, ad.buffer)
	ad.mu.Unlock()

	// Gate out near-silent windows, really quiet music often results in FPs
	if rawRMS(raw) < alertRMSGate {
		ad.mu.Lock()
		defer ad.mu.Unlock()
		ad.prevDetected = false
		ad.stableResult = nil
		return nil
	}

	// Model expects 16kHz
	resampled, err := resampleTo16k(raw)
	if err != nil {
		log.Printf("ML: Audio resample failed: %v", err)
		return nil
	}

	// Fit to exact model input size — silence-pads short windows, truncates long ones
	window := make([]float32, alertModelSamples)
	copy(window, resampled)

	inputTensor, err := ort.NewTensor(ort.NewShape(1, int64(alertModelSamples)), window)
	if err != nil {
		log.Printf("ML: Failed to create audio input tensor: %v", err)
		return nil
	}
	defer inputTensor.Destroy()

	binaryTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(1))
	if err != nil {
		log.Printf("ML: Failed to create binary output tensor: %v", err)
		return nil
	}
	defer binaryTensor.Destroy()

	subclassTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 2))
	if err != nil {
		log.Printf("ML: Failed to create subclass output tensor: %v", err)
		return nil
	}
	defer subclassTensor.Destroy()

	if err := ad.session.Run(
		[]ort.Value{inputTensor},
		[]ort.Value{binaryTensor, subclassTensor},
	); err != nil {
		log.Printf("ML: Audio inference failed: %v", err)
		return nil
	}

	prob := sigmoid(binaryTensor.GetData()[0])
	detected := prob >= alertBinaryThresh

	// Only change the stable state when two consecutive ticks agree
	ad.mu.Lock()
	defer ad.mu.Unlock()
	if detected == ad.prevDetected {
		if detected && ad.stableResult == nil {
			sub := subclassTensor.GetData()
			label := "siren"
			if sub[1] > sub[0] {
				label = "alarm"
			}
			ad.stableResult = &events.AudioDetectionResult{Label: label}
			log.Printf("ML: Detected %s (score=%.2f)", label, prob)
		} else if !detected {
			ad.stableResult = nil
		}
	}
	ad.prevDetected = detected
	return ad.stableResult
}

func resampleTo16k(raw []byte) ([]float32, error) {
	var buf bytes.Buffer
	r, err := resample.New(&buf, resample.FormatInt16, globals.AudioSampleRate, alertModelSR, globals.AudioChannels, resample.WithLinearFilter())
	if err != nil {
		return nil, err
	}
	if _, err := r.Write(raw); err != nil {
		return nil, err
	}

	out := buf.Bytes()
	n := len(out) / 2
	samples := make([]float32, n)
	const invScale = 1.0 / (math.MaxInt16 + 1.0)
	for i := range n {
		s := int16(binary.LittleEndian.Uint16(out[i*2:]))
		samples[i] = float32(s) * invScale
	}
	return samples, nil
}

func sigmoid(x float32) float32 {
	return float32(1.0 / (1.0 + math.Exp(-float64(x))))
}

// rawRMS estimates RMS over every 4th sample — 4x cheaper
func rawRMS(raw []byte) float64 {
	var sumSq float64
	var count int
	for i := 0; i+2 <= len(raw); i += 8 {
		s := float64(int16(binary.LittleEndian.Uint16(raw[i:])))
		sumSq += s * s
		count++
	}
	return math.Sqrt(sumSq / float64(count))
}
