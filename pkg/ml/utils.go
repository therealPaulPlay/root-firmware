package ml

import (
	"math"
	"slices"

	"root-firmware/pkg/config"
)

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

// hasInitialRelayConnect returns whether the relay has ever connected successfully
func hasInitialRelayConnect() bool {
	if val, ok := config.Get().GetKey("initialRelayConnect"); ok {
		if b, ok := val.(bool); ok {
			return b
		}
	}
	return false
}

// IsEventDetectionEnabled returns whether event detection is enabled (default true)
func IsEventDetectionEnabled() bool {
	if val, ok := config.Get().GetKey("eventDetectionEnabled"); ok {
		if b, ok := val.(bool); ok {
			return b
		}
	}
	return true
}

// GetEnabledEventTypes returns the configured event types (empty = no filter set)
func GetEnabledEventTypes() []string {
	val, ok := config.Get().GetKey("eventDetectionEnabledTypes")
	if !ok {
		return []string{}
	}
	if arr, ok := val.([]any); ok {
		types := make([]string, 0, len(arr))
		for _, item := range arr {
			if str, ok := item.(string); ok {
				types = append(types, str)
			}
		}
		return types
	}
	return []string{}
}

// isEventTypeEnabled checks if event type should trigger recording
// defaultIfUnset controls behavior when no types are configured: true means enabled, false means disabled
func isEventTypeEnabled(eventType string, defaultIfUnset bool) bool {
	types := GetEnabledEventTypes()
	if len(types) == 0 {
		return defaultIfUnset
	}
	return slices.Contains(types, eventType)
}
