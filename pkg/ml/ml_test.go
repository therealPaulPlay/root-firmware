package ml

import (
	"math"
	"os"
	"testing"

	"root-firmware/pkg/config"
	"root-firmware/pkg/testutil"
)

var testImage1 []byte
var testImage2 []byte

func TestMain(m *testing.M) {
	var err error
	testImage1, err = os.ReadFile("testdata/test1.jpg")
	if err != nil {
		panic("failed to load test image 1: " + err.Error())
	}
	testImage2, err = os.ReadFile("testdata/test2.jpg")
	if err != nil {
		panic("failed to load test image 2: " + err.Error())
	}
	os.Exit(m.Run())
}

func setupTestML(t *testing.T) func() {
	t.Helper()

	config.ResetForTesting()

	cleanupGlobals := testutil.SetupTempGlobals(t)

	if err := config.Init(); err != nil {
		t.Fatalf("config.Init() error = %v", err)
	}

	return func() {
		cleanupGlobals()
		config.ResetForTesting()
	}
}

// --- IOU tests ---

func TestIOU(t *testing.T) {
	tests := []struct {
		name     string
		box1     [4]float32
		box2     [4]float32
		expected float32
	}{
		{"identical", [4]float32{0, 0, 10, 10}, [4]float32{0, 0, 10, 10}, 1.0},
		{"no overlap", [4]float32{0, 0, 10, 10}, [4]float32{20, 20, 30, 30}, 0.0},
		{"partial overlap", [4]float32{0, 0, 10, 10}, [4]float32{5, 5, 15, 15}, 25.0 / 175.0},
		{"one inside other", [4]float32{0, 0, 20, 20}, [4]float32{5, 5, 15, 15}, 0.25},
		{"touching edges", [4]float32{0, 0, 10, 10}, [4]float32{10, 0, 20, 10}, 0.0},
		{"zero area", [4]float32{5, 5, 5, 5}, [4]float32{0, 0, 10, 10}, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := iou(tt.box1, tt.box2)
			if math.Abs(float64(result-tt.expected)) > 0.001 {
				t.Errorf("iou() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// --- Softmax distance tests ---

func TestSoftmaxDistance(t *testing.T) {
	tests := []struct {
		name     string
		dist     []float32
		expected float32
		delta    float32
	}{
		{"uniform distribution", []float32{1, 1, 1, 1, 1}, 2.0, 0.001},
		{"peak at start", []float32{10, 0, 0, 0, 0}, 0.0, 0.1},
		{"peak at end", []float32{0, 0, 0, 0, 10}, 4.0, 0.1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := softmaxDistance(tt.dist)
			if math.Abs(float64(result-tt.expected)) > float64(tt.delta) {
				t.Errorf("softmaxDistance() = %v, want ~%v", result, tt.expected)
			}
		})
	}
}

// --- Label decoding tests ---

func TestDecodeLabel(t *testing.T) {
	tests := []struct {
		classID  int
		expected string
	}{
		{0, "person"},
		{2, "car"},
		{15, "pet"},  // cat
		{16, "pet"},  // dog
		{1, "other"}, // bicycle
		{99, "other"},
	}

	for _, tt := range tests {
		result := decodeLabel(tt.classID)
		if result != tt.expected {
			t.Errorf("decodeLabel(%d) = %s, want %s", tt.classID, result, tt.expected)
		}
	}
}

// --- NMS tests ---

func TestNMS_EmptyInput(t *testing.T) {
	result := nms(nil, nil, 0.5)
	if result != nil {
		t.Errorf("nms() = %v, want nil", result)
	}
}

func TestNMS_SingleBox(t *testing.T) {
	boxes := [][4]float32{{0, 0, 10, 10}}
	scores := []float32{0.9}

	result := nms(boxes, scores, 0.5)
	if len(result) != 1 || result[0] != 0 {
		t.Errorf("nms() = %v, want [0]", result)
	}
}

func TestNMS_SuppressesOverlapping(t *testing.T) {
	boxes := [][4]float32{
		{0, 0, 10, 10},   // high score
		{1, 1, 11, 11},   // overlaps heavily, should be suppressed
		{50, 50, 60, 60}, // no overlap, should be kept
	}
	scores := []float32{0.9, 0.8, 0.7}

	result := nms(boxes, scores, 0.5)

	// Should keep boxes 0 and 2, suppress 1
	if len(result) != 2 {
		t.Fatalf("nms() kept %d boxes, want 2", len(result))
	}
	if result[0] != 0 || result[1] != 2 {
		t.Errorf("nms() = %v, want [0 2]", result)
	}
}

func TestNMS_KeepsNonOverlapping(t *testing.T) {
	boxes := [][4]float32{
		{0, 0, 10, 10},
		{20, 20, 30, 30},
		{40, 40, 50, 50},
	}
	scores := []float32{0.9, 0.8, 0.7}

	result := nms(boxes, scores, 0.5)

	if len(result) != 3 {
		t.Errorf("nms() kept %d boxes, want 3 (no overlap)", len(result))
	}
}

// --- Config-dependent tests ---

func TestIsEventDetectionEnabled(t *testing.T) {
	cleanup := setupTestML(t)
	defer cleanup()

	// Default should be true
	if !IsEventDetectionEnabled() {
		t.Error("default should be true")
	}

	// Explicitly disabled
	config.Get().SetKey("eventDetectionEnabled", false)
	if IsEventDetectionEnabled() {
		t.Error("should be false when disabled")
	}
}

func TestGetEnabledEventTypes(t *testing.T) {
	cleanup := setupTestML(t)
	defer cleanup()

	// Default empty
	if len(GetEnabledEventTypes()) != 0 {
		t.Error("default should be empty")
	}

	// With types set
	config.Get().SetKey("eventDetectionEnabledTypes", []string{"person", "car"})
	types := GetEnabledEventTypes()
	if len(types) != 2 || types[0] != "person" || types[1] != "car" {
		t.Errorf("GetEnabledEventTypes() = %v", types)
	}
}

func TestIsEventTypeEnabled(t *testing.T) {
	cleanup := setupTestML(t)
	defer cleanup()

	// No types configured - uses default
	if !isEventTypeEnabled("person", true) {
		t.Error("should return defaultIfUnset when no types configured")
	}
	if isEventTypeEnabled("person", false) {
		t.Error("should return defaultIfUnset when no types configured")
	}

	// With types configured
	config.Get().SetKey("eventDetectionEnabledTypes", []string{"person"})
	if !isEventTypeEnabled("person", false) {
		t.Error("person should be enabled")
	}
	if isEventTypeEnabled("car", true) {
		t.Error("car should not be enabled")
	}
}

// --- Image processing tests ---

func TestToGrayscale(t *testing.T) {
	gray, err := toGrayscale(testImage1)
	if err != nil {
		t.Fatalf("toGrayscale() error = %v", err)
	}

	expectedLen := scaledWidth * scaledHeight
	if len(gray) != expectedLen {
		t.Errorf("toGrayscale() length = %d, want %d", len(gray), expectedLen)
	}

	// Check values are in valid range (0-255 scaled by /256.0, so roughly 0-255)
	for i, v := range gray {
		if v < 0 || v > 65535 { // RGBA returns 16-bit values, /256 gives ~0-255
			t.Errorf("pixel %d value %f out of range", i, v)
			break
		}
	}
}

func TestToGrayscale_InvalidJPEG(t *testing.T) {
	_, err := toGrayscale([]byte("not a jpeg"))
	if err == nil {
		t.Error("toGrayscale() should error on invalid JPEG")
	}
}

func TestMotionDetector_FirstFrameNoMotion(t *testing.T) {
	md := newMotionDetector()

	motion, err := md.detectMotion(testImage1)
	if err != nil {
		t.Fatalf("detectMotion() error = %v", err)
	}

	// First frame initializes background, no motion
	if motion {
		t.Error("first frame should not detect motion")
	}

	if md.background == nil {
		t.Error("background should be initialized after first frame")
	}
}

func TestMotionDetector_SameFrameNoMotion(t *testing.T) {
	md := newMotionDetector()

	// First frame - initializes
	md.detectMotion(testImage1)

	// Same frame again - no motion
	motion, err := md.detectMotion(testImage1)
	if err != nil {
		t.Fatalf("detectMotion() error = %v", err)
	}

	if motion {
		t.Error("identical frames should not detect motion")
	}
}

func TestMotionDetector_DifferentFramesDetectMotion(t *testing.T) {
	md := newMotionDetector()

	// First frame - initializes background
	_, err := md.detectMotion(testImage1)
	if err != nil {
		t.Fatalf("detectMotion(testImage1) error = %v", err)
	}

	// Different frame - should detect motion
	motion, err := md.detectMotion(testImage2)
	if err != nil {
		t.Fatalf("detectMotion(testImage2) error = %v", err)
	}

	if !motion {
		t.Error("different frames should detect motion")
	}
}

func TestMotionDetector_Reset(t *testing.T) {
	md := newMotionDetector()

	// Initialize with first frame
	md.detectMotion(testImage1)
	originalBackground := make([]float32, len(md.background))
	copy(originalBackground, md.background)

	// Reset should reinitialize background
	err := md.reset(testImage1)
	if err != nil {
		t.Fatalf("reset() error = %v", err)
	}

	if md.background == nil {
		t.Error("background should exist after reset")
	}
}

func TestMotionDetector_Reset_InvalidJPEG(t *testing.T) {
	md := newMotionDetector()

	err := md.reset([]byte("not a jpeg"))
	if err == nil {
		t.Error("reset() should error on invalid JPEG")
	}
}
