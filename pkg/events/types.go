package events

import (
	"slices"

	"root-firmware/pkg/config"
)

const (
	TypePerson  = "person"
	TypePet     = "pet"
	TypeVehicle = "vehicle"
	TypeMotion  = "motion"
	TypeAlert   = "alert"
)

type EventTypeInfo struct {
	Value          string `cbor:"value"`
	Label          string `cbor:"label"`
	DefaultEnabled bool   `cbor:"defaultEnabled"`
}

var AvailableEventTypes = []EventTypeInfo{
	{Value: TypePerson, Label: "Person", DefaultEnabled: true},
	{Value: TypePet, Label: "Pet", DefaultEnabled: true},
	{Value: TypeVehicle, Label: "Vehicle", DefaultEnabled: true},
	{Value: TypeAlert, Label: "Alert", DefaultEnabled: true},
	{Value: TypeMotion, Label: "Other motion", DefaultEnabled: false},
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

// GetEnabledEventTypes returns the configured event types, or defaults if not set
func GetEnabledEventTypes() []string {
	val, ok := config.Get().GetKey("eventDetectionEnabledTypes")
	if !ok {
		// Key not set, return defaults
		var types []string
		for _, t := range AvailableEventTypes {
			if t.DefaultEnabled {
				types = append(types, t.Value)
			}
		}
		return types
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
func IsEventTypeEnabled(eventType string) bool {
	return slices.Contains(GetEnabledEventTypes(), eventType)
}
