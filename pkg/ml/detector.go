package ml

import (
	"bytes"
	"image"
	_ "image/jpeg"
	"log"
	"sort"

	ort "github.com/yalue/onnxruntime_go"
)

const (
	modelWidth  = 416
	modelHeight = 416
	confThresh  = 0.4
	nmsThresh   = 0.5
	regMax      = 7    // Distribution head bins [0-7]
	numClasses  = 80   // COCO classes (classifiers)
	numAnchors  = 3598 // Anchor count from model
	outputSize  = 112  // 80 classes + 32 bbox distribution values (4 * 8)
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

type objectDetector struct {
	session *ort.DynamicAdvancedSession
}

func newObjectDetector(modelPath string) (*objectDetector, error) {
	if err := ort.InitializeEnvironment(); err != nil {
		log.Printf("ML: Failed to initialize ONNX environment: %v", err)
		return nil, err
	}

	opts, err := ort.NewSessionOptions()
	if err != nil {
		log.Printf("ML: Failed to create session options: %v", err)
		return nil, err
	}
	defer opts.Destroy()

	// Single thread for Pi Zero 2
	opts.SetIntraOpNumThreads(1)
	opts.SetInterOpNumThreads(1)

	session, err := ort.NewDynamicAdvancedSession(modelPath, []string{"data"}, []string{"output"}, opts)
	if err != nil {
		log.Printf("ML: Failed to create ONNX session: %v", err)
		return nil, err
	}

	return &objectDetector{session: session}, nil
}

func (d *objectDetector) detect(jpegData []byte) (*Detection, error) {
	img, _, err := image.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return nil, err
	}

	inputTensor := d.preprocess(img)
	defer inputTensor.Destroy()

	outputTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(numAnchors), int64(outputSize)))
	if err != nil {
		return nil, err
	}
	defer outputTensor.Destroy()

	err = d.session.Run([]ort.Value{inputTensor}, []ort.Value{outputTensor})
	if err != nil {
		log.Printf("ML: Inference failed: %v", err)
		return nil, err
	}

	return d.postprocess(outputTensor), nil
}

func (d *objectDetector) preprocess(img image.Image) ort.Value {
	bounds := img.Bounds()
	srcWidth := bounds.Dx()
	srcHeight := bounds.Dy()

	// Calculate scaling to fit within modelWidth x modelHeight while preserving aspect ratio
	scaleX := float64(modelWidth) / float64(srcWidth)
	scaleY := float64(modelHeight) / float64(srcHeight)
	scale := scaleX
	if scaleY < scaleX {
		scale = scaleY
	}

	// Calculate scaled dimensions and padding (letterboxing)
	scaledWidth := int(float64(srcWidth) * scale)
	scaledHeight := int(float64(srcHeight) * scale)
	padX := (modelWidth - scaledWidth) / 2
	padY := (modelHeight - scaledHeight) / 2

	// Create black canvas
	resized := image.NewRGBA(image.Rect(0, 0, modelWidth, modelHeight))

	// Draw scaled image onto canvas with padding
	for y := range scaledHeight {
		for x := range scaledWidth {
			srcX := bounds.Min.X + (x * srcWidth / scaledWidth)
			srcY := bounds.Min.Y + (y * srcHeight / scaledHeight)
			resized.Set(padX+x, padY+y, img.At(srcX, srcY))
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

func (d *objectDetector) postprocess(outputTensor *ort.Tensor[float32]) *Detection {
	outputData := outputTensor.GetData()

	// Output shape: [1, 3598, 112] = 80 class scores + 32 bbox distribution bins (4 edges × 8 bins)
	classesToCheck := []int{0, 2, 15, 16} // person, car, cat, dog

	var boxes [][4]float32
	var scores []float32
	var labels []int

	// Track top scores per class for logging
	topScores := make(map[int]float32)
	for _, classID := range classesToCheck {
		topScores[classID] = 0
	}

	// Multi-scale feature map strides
	strides := []int{8, 16, 32}
	anchorIdx := 0

	// Process each stride level
	for _, stride := range strides {
		featureH := modelHeight / stride
		featureW := modelWidth / stride

		for row := range featureH {
			for col := range featureW {
				if anchorIdx >= numAnchors {
					break
				}

				offset := anchorIdx * outputSize

				// Find best class score from first 80 values
				var bestClass int
				var bestScore float32

				for _, classID := range classesToCheck {
					score := outputData[offset+classID]
					if score > bestScore {
						bestScore = score
						bestClass = classID
					}
					// Track top score per class
					if score > topScores[classID] {
						topScores[classID] = score
					}
				}

				if bestScore >= confThresh {
					// Decode distribution-based bbox prediction
					// Distribution values start at index 80, 8 values per edge (l, t, r, b)
					l := softmaxDistance(outputData[offset+numClasses : offset+numClasses+8])
					t := softmaxDistance(outputData[offset+numClasses+8 : offset+numClasses+16])
					r := softmaxDistance(outputData[offset+numClasses+16 : offset+numClasses+24])
					b := softmaxDistance(outputData[offset+numClasses+24 : offset+numClasses+32])

					// Calculate anchor center
					cx := (float32(col) + 0.5) * float32(stride)
					cy := (float32(row) + 0.5) * float32(stride)

					// Convert to absolute bbox coordinates
					x1 := max(cx-l*float32(stride), 0)
					y1 := max(cy-t*float32(stride), 0)
					x2 := min(cx+r*float32(stride), modelWidth)
					y2 := min(cy+b*float32(stride), modelHeight)

					boxes = append(boxes, [4]float32{x1, y1, x2, y2})
					scores = append(scores, bestScore)
					labels = append(labels, bestClass)
				}

				anchorIdx++
			}
		}
	}

	// Log top scores per class
	log.Printf("ML: top scores - person=%.4f car=%.4f cat=%.4f dog=%.4f | candidates=%d thresh=%.2f",
		topScores[0], topScores[2], topScores[15], topScores[16], len(boxes), confThresh)

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

	// Log detection result
	log.Printf("ML: detected %s (score=%.4f, count=%d after NMS)", eventType, scores[kept[0]], len(kept))

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
