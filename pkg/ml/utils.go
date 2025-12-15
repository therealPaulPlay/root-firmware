package ml

func max(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func min(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
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
