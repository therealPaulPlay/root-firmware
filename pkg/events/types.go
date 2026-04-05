package events

import (
	"slices"

	"root-firmware/pkg/config"
)

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

// IsEventTypeEnabled checks if event type should trigger recording
// defaultIfUnset controls behavior when no types are configured
func IsEventTypeEnabled(eventType string, defaultIfUnset bool) bool {
	types := GetEnabledEventTypes()
	if len(types) == 0 {
		return defaultIfUnset
	}
	return slices.Contains(types, eventType)
}
