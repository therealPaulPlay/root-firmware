package ml

import (
	"image"
	"log"
	"sort"

	"root-firmware/pkg/events"

	ort "github.com/yalue/onnxruntime_go"
)

const (
	modelWidth  = 416
	modelHeight = 416
	confThresh  = 0.35 // 0.35-0.45 suggested for people, lower is more sensitve
	nmsThresh   = 0.5
	numClasses  = 80   // COCO classes (classifiers)
	numAnchors  = 3598 // Anchor count from model
	outputSize  = 112  // 80 classes + 32 bbox distribution values (4 * 8)
)

type VideoDetection struct {
	EventType string // "person", "pet" etc.
	Count     int
	Result    *events.VideoDetectionResult // per-box detections with model size context
}

// decodeLabel maps COCO class IDs to event types
func decodeLabel(classID int) string {
	switch classID {
	case 0:
		return events.TypePerson
	case 1, 2, 3, 5, 7: // bicycle, car, motorcycle, bus, truck
		return events.TypeVehicle
	case 15, 16: // cat, dog
		return events.TypePet
	default:
		return "other"
	}
}

type videoDetector struct {
	session *ort.DynamicAdvancedSession
}

func newVideoDetector(modelPath string) (*videoDetector, error) {
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

	// Single thread for a low-power SBC
	opts.SetIntraOpNumThreads(1)
	opts.SetInterOpNumThreads(1)

	session, err := ort.NewDynamicAdvancedSession(modelPath, []string{"data"}, []string{"output"}, opts)
	if err != nil {
		log.Printf("ML: Failed to create ONNX session: %v", err)
		return nil, err
	}

	return &videoDetector{session: session}, nil
}

func (d *videoDetector) detect(img image.Image) (*VideoDetection, error) {
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

func (d *videoDetector) preprocess(img image.Image) ort.Value {
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

func (d *videoDetector) postprocess(outputTensor *ort.Tensor[float32]) *VideoDetection {
	outputData := outputTensor.GetData()

	// Output shape: [1, 3598, 112] = 80 class scores + 32 bbox distribution bins (4 edges × 8 bins)
	targetClasses := map[int]bool{0: true, 1: true, 2: true, 3: true, 5: true, 7: true, 15: true, 16: true} // person, bicycle, car, motorcycle, bus, truck, cat, dog

	var boxes [][4]float32
	var scores []float32
	var labels []int
	topScores := map[string]float32{} // best score per label for logging

	// Multi-scale feature map strides
	strides := []int{8, 16, 32}
	anchorIdx := 0

	// Process each stride level — collect ALL classes above threshold so NMS
	// can suppress weak target-class detections that overlap stronger non-target ones
	for _, stride := range strides {
		featureH := modelHeight / stride
		featureW := modelWidth / stride

		for row := range featureH {
			for col := range featureW {
				if anchorIdx >= numAnchors {
					break
				}

				offset := anchorIdx * outputSize

				// Find best class score across all 80 classes
				var bestClass int
				var bestScore float32

				for classID := range numClasses {
					score := outputData[offset+classID]
					if score > bestScore {
						bestScore = score
						bestClass = classID
					}
				}
				if label := decodeLabel(bestClass); bestScore > topScores[label] {
					topScores[label] = bestScore
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

	if len(boxes) == 0 {
		return &VideoDetection{EventType: "", Count: 0}
	}

	// Apply NMS across all classes — non-target detections suppress overlapping
	// weaker target detections, reducing false positives
	kept := nms(boxes, scores, nmsThresh)

	// Filter to target classes only
	var targetKept []int
	for _, idx := range kept {
		// Only keep if target classes contain this label (e.g. person -> kept, bench -> dropped)
		if targetClasses[labels[idx]] {
			targetKept = append(targetKept, idx)
		}
	}

	if len(targetKept) == 0 {
		return &VideoDetection{EventType: "", Count: 0}
	}

	// Build box results for visualization
	detectionBoxes := make([]events.DetectionBox, len(targetKept))
	for i, idx := range targetKept {
		detectionBoxes[i] = events.DetectionBox{
			Label:      decodeLabel(labels[idx]),
			Confidence: scores[idx],
			X1:         boxes[idx][0],
			Y1:         boxes[idx][1],
			X2:         boxes[idx][2],
			Y2:         boxes[idx][3],
		}
	}

	eventType := decodeLabel(labels[targetKept[0]]) // Set event type to highest confidence one

	log.Printf("ML: Detected %s (score=%.4f, count=%d after NMS)", eventType, scores[targetKept[0]], len(kept))

	return &VideoDetection{
		EventType: eventType,
		Count:     len(targetKept),
		Result: &events.VideoDetectionResult{
			Boxes:     detectionBoxes,
			ModelSize: [2]int{modelWidth, modelHeight},
		},
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
