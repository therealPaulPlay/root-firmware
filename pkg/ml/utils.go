package ml

import "math"

// softmaxDistance applies softmax to distribution and computes expected value
func softmaxDistance(dist []float32) float32 {
	// Apply softmax
	var maxVal float32 = dist[0]
	for _, v := range dist {
		if v > maxVal {
			maxVal = v
		}
	}

	var sum float32
	softmax := make([]float32, len(dist))
	for i, v := range dist {
		exp := float32(math.Exp(float64(v - maxVal)))
		softmax[i] = exp
		sum += exp
	}

	// Normalize and compute expected distance
	var distance float32
	for i := range softmax {
		softmax[i] /= sum
		distance += softmax[i] * float32(i)
	}

	return distance
}

// iou calculates Intersection over Union for bounding boxes
func iou(box1, box2 [4]float32) float32 {
	x1 := max(box1[0], box2[0])
	y1 := max(box1[1], box2[1])
	x2 := min(box1[2], box2[2])
	y2 := min(box1[3], box2[3])

	if x2 < x1 || y2 < y1 {
		return 0
	}

	inter := (x2 - x1) * (y2 - y1)
	area1 := (box1[2] - box1[0]) * (box1[3] - box1[1])
	area2 := (box2[2] - box2[0]) * (box2[3] - box2[1])
	union := area1 + area2 - inter

	if union == 0 {
		return 0
	}

	return inter / union
}
