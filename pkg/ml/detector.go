package ml

import (
	"bytes"
	"image"
	_ "image/jpeg"
	"sort"

	ort "github.com/yalue/onnxruntime_go"
)

const (
	modelWidth  = 416
	modelHeight = 416
	confThresh  = 0.4 // Match reference implementation
	nmsThresh   = 0.5
	regMax      = 7  // Distribution head bins [0-7]
	numClasses  = 80 // COCO classes
)

type Detection struct {
	EventType string // "person", "pet", "car", "other"
	Count     int
}

// decodeLabel maps COCO class IDs to event types
func decodeLabel(classID int) string {
	switch classID {
	case 0:
		return "person"
	case 2:
		return "car"
	case 15, 16: // cat, dog
		return "pet"
	default:
		return "other"
	}
}

type detector struct {
	session *ort.DynamicAdvancedSession
}

func newDetector(modelPath string) (*detector, error) {
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, err
	}

	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer opts.Destroy()

	// Single thread for Pi Zero 2
	opts.SetIntraOpNumThreads(1)
	opts.SetInterOpNumThreads(1)

	session, err := ort.NewDynamicAdvancedSession(modelPath, []string{"data"}, []string{"output"}, opts)
	if err != nil {
		return nil, err
	}

	return &detector{session: session}, nil
}

func (d *detector) detect(jpegData []byte) (*Detection, error) {
	img, _, err := image.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return nil, err
	}

	inputTensor := d.preprocess(img)
	defer inputTensor.Destroy()

	// Create output tensors placeholder
	outputTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 2100, 84))
	if err != nil {
		return nil, err
	}
	defer outputTensor.Destroy()

	err = d.session.Run([]ort.Value{inputTensor}, []ort.Value{outputTensor})
	if err != nil {
		return nil, err
	}

	return d.postprocess(outputTensor), nil
}

func (d *detector) preprocess(img image.Image) ort.Value {
	resized := image.NewRGBA(image.Rect(0, 0, modelWidth, modelHeight))
	bounds := img.Bounds()

	// Simple resize
	for y := range modelHeight {
		for x := range modelWidth {
			srcX := x * bounds.Dx() / modelWidth
			srcY := y * bounds.Dy() / modelHeight
			resized.Set(x, y, img.At(bounds.Min.X+srcX, bounds.Min.Y+srcY))
		}
	}

	// Convert to CHW tensor with COCO normalization
	data := make([]float32, 3*modelHeight*modelWidth)
	idx := 0

	// B, G, R channels
	means := [3]float32{103.53, 116.28, 123.675}
	scales := [3]float32{0.017429, 0.017507, 0.017125}

	for c := range 3 {
		for y := range modelHeight {
			for x := range modelWidth {
				r, g, b, _ := resized.At(x, y).RGBA()
				vals := [3]float32{float32(b >> 8), float32(g >> 8), float32(r >> 8)}
				data[idx] = (vals[c] - means[c]) * scales[c]
				idx++
			}
		}
	}

	shape := ort.NewShape(1, 3, int64(modelHeight), int64(modelWidth))
	tensor, _ := ort.NewTensor(shape, data)
	return tensor
}

func (d *detector) postprocess(outputTensor *ort.Tensor[float32]) *Detection {
	outputData := outputTensor.GetData()

	// NanoDet output: [1, 2100, 84] where 84 = 80 class logits + 4 bbox values
	// Layout per prediction: [class_0, class_1, ..., class_79, bbox_l, bbox_t, bbox_r, bbox_b]
	classesToCheck := []int{0, 2, 15, 16} // person, car, cat, dog

	var boxes [][4]float32
	var scores []float32
	var labels []int

	// Multi-scale feature map strides
	strides := []int{8, 16, 32, 64}
	totalIdx := 0

	// Process each stride level
	for _, stride := range strides {
		featureH := modelHeight / stride
		featureW := modelWidth / stride

		for row := range featureH {
			for col := range featureW {
				if totalIdx >= 2100 {
					break
				}

				offset := totalIdx * 84

				// Find best class score from first 80 values
				var bestClass int
				var bestScore float32

				for _, classID := range classesToCheck {
					score := outputData[offset+classID]
					if score > bestScore {
						bestScore = score
						bestClass = classID
					}
				}

				if bestScore >= confThresh {
					// Bbox values are at indices 80-83
					l := outputData[offset+numClasses]
					t := outputData[offset+numClasses+1]
					r := outputData[offset+numClasses+2]
					b := outputData[offset+numClasses+3]

					// Calculate anchor center
					cx := (float32(col) + 0.5) * float32(stride)
					cy := (float32(row) + 0.5) * float32(stride)

					// Convert to absolute bbox coordinates
					x1 := max(cx-l, 0)
					y1 := max(cy-t, 0)
					x2 := min(cx+r, modelWidth)
					y2 := min(cy+b, modelHeight)

					boxes = append(boxes, [4]float32{x1, y1, x2, y2})
					scores = append(scores, bestScore)
					labels = append(labels, bestClass)
				}

				totalIdx++
			}
		}
	}

	if len(boxes) == 0 {
		return &Detection{EventType: "", Count: 0}
	}

	// Apply NMS
	kept := nms(boxes, scores, nmsThresh)

	if len(kept) == 0 {
		return &Detection{EventType: "", Count: 0}
	}

	// Return event type of first kept detection
	eventType := decodeLabel(labels[kept[0]])

	return &Detection{
		EventType: eventType,
		Count:     len(kept),
	}
}

func nms(boxes [][4]float32, scores []float32, threshold float32) []int {
	if len(boxes) == 0 {
		return nil
	}

	// Sort indices by score
	indices := make([]int, len(scores))
	for i := range indices {
		indices[i] = i
	}
	sort.Slice(indices, func(i, j int) bool {
		return scores[indices[i]] > scores[indices[j]]
	})

	var keep []int
	suppressed := make([]bool, len(boxes))

	for _, i := range indices {
		if suppressed[i] {
			continue
		}
		keep = append(keep, i)

		for _, j := range indices {
			if !suppressed[j] && i != j {
				if iou(boxes[i], boxes[j]) > threshold {
					suppressed[j] = true
				}
			}
		}
	}

	return keep
}
