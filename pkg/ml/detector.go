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
	confThresh  = 0.35
	nmsThresh   = 0.5
)

type Detection struct {
	HasPerson bool
	Count     int
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
	for y := 0; y < modelHeight; y++ {
		for x := 0; x < modelWidth; x++ {
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

	for c := 0; c < 3; c++ {
		for y := 0; y < modelHeight; y++ {
			for x := 0; x < modelWidth; x++ {
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

	// NanoDet output: [1, 2100, 84] where 84 = 4 bbox + 80 classes
	// Flatten to process: 2100 boxes * 84 values
	var boxes [][4]float32
	var scores []float32

	for i := 0; i < 2100; i++ {
		offset := i * 84
		if offset+84 > len(outputData) {
			break
		}

		personScore := outputData[offset+4] // class 0 = person

		if personScore >= confThresh {
			box := [4]float32{
				outputData[offset],
				outputData[offset+1],
				outputData[offset+2],
				outputData[offset+3],
			}
			boxes = append(boxes, box)
			scores = append(scores, personScore)
		}
	}

	// Apply NMS
	kept := nms(boxes, scores, nmsThresh)

	return &Detection{
		HasPerson: len(kept) > 0,
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

func (d *detector) close() error {
	if d.session != nil {
		return d.session.Destroy()
	}
	return nil
}
