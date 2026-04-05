package ml

import (
	"bytes"
	"image"
	_ "image/jpeg"
	"math"
	"os"
	"testing"

	"root-firmware/pkg/config"
	"root-firmware/pkg/events"
	"root-firmware/pkg/testutil"
)

var testImage1 image.Image
var testImage2 image.Image

func TestMain(m *testing.M) {
	data1, err := os.ReadFile("testdata/test1.jpg")
	if err != nil {
		panic("failed to load test image 1: " + err.Error())
	}
	testImage1, _, err = image.Decode(bytes.NewReader(data1))
	if err != nil {
		panic("failed to decode test image 1: " + err.Error())
	}

	data2, err := os.ReadFile("testdata/test2.jpg")
	if err != nil {
		panic("failed to load test image 2: " + err.Error())
	}
	testImage2, _, err = image.Decode(bytes.NewReader(data2))
	if err != nil {
		panic("failed to decode test image 2: " + err.Error())
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
		{0, events.TypePerson},
		{1, events.TypeVehicle},  // bicycle
		{2, events.TypeVehicle},  // car
		{3, events.TypeVehicle},  // motorcycle
		{5, events.TypeVehicle},  // bus
		{7, events.TypeVehicle},  // truck
		{15, events.TypePet},     // cat
		{16, events.TypePet},     // dog
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

// --- Image processing tests ---

func TestToGrayscale(t *testing.T) {
	gray := toGrayscale(testImage1)

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

func TestMotionDetector_FirstFrameNoMotion(t *testing.T) {
	md := newMotionDetector()

	motion := md.detectMotion(testImage1)

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
	if md.detectMotion(testImage1) {
		t.Error("identical frames should not detect motion")
	}
}

func TestMotionDetector_DifferentFramesDetectMotion(t *testing.T) {
	md := newMotionDetector()

	// First frame - initializes background
	md.detectMotion(testImage1)

	// Different frame - should detect motion
	if !md.detectMotion(testImage2) {
		t.Error("different frames should detect motion")
	}
}

