package relaycomm

import (
	"encoding/json"

	"root-firmware/pkg/devices"
	"root-firmware/pkg/record"
	"root-firmware/pkg/storage"
	"root-firmware/pkg/ups"
	"root-firmware/pkg/wifi"
)

// RegisterHandlers registers all relay message handlers
// TODO: Use end-2-end encryption here (from encryption package)
func RegisterHandlers() {
	relay := Get()

	// Device management
	relay.On("getDevices", handleGetDevices)
	relay.On("removeDevice", handleRemoveDevice)
	relay.On("kickDevice", handleKickDevice)

	// WiFi
	relay.On("wifiScan", handleWiFiScan)
	relay.On("wifiConnect", handleWiFiConnect)

	// Storage
	relay.On("getEvents", handleGetEvents)
	relay.On("getRecording", handleGetRecording)

	// Streaming
	relay.On("startStream", handleStartStream)
	relay.On("stopStream", handleStopStream)
	relay.On("toggleBabyphone", handleToggleBabyphone)

	// Settings
	relay.On("setMicrophone", handleSetMicrophone)
	relay.On("setRecordingSound", handleSetRecordingSound)

	// System
	relay.On("getHealth", handleGetHealth)
	relay.On("getPreview", handleGetPreview)
	relay.On("restart", handleRestart)
}

func handleGetDevices(payload json.RawMessage) {
	allDevices := devices.Get().GetAll()
	Get().Send("devicesResult", allDevices)
}

func handleRemoveDevice(payload json.RawMessage) {
	var req struct {
		DeviceID string `json:"deviceId"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	err := devices.Get().Remove(req.DeviceID)
	Get().Send("removeDeviceResult", map[string]interface{}{
		"success": err == nil,
	})
}

func handleKickDevice(payload json.RawMessage) {
	var req struct {
		DeviceID string `json:"deviceId"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	err := devices.Get().ScheduleKick(req.DeviceID)
	Get().Send("kickDeviceResult", map[string]interface{}{
		"success": err == nil,
	})
}

func handleWiFiScan(payload json.RawMessage) {
	networks, err := wifi.Get().Scan()
	Get().Send("wifiScanResult", map[string]interface{}{
		"success":  err == nil,
		"networks": networks,
	})
}

func handleWiFiConnect(payload json.RawMessage) {
	var req struct {
		SSID     string `json:"ssid"`
		Password string `json:"password"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	// TODO: Safely change WiFi - verify internet connection after switch
	// If new network doesn't work, revert to previous network
	err := wifi.Get().Connect(req.SSID, req.Password)
	Get().Send("wifiConnectResult", map[string]interface{}{
		"success": err == nil,
	})
}

func handleGetEvents(payload json.RawMessage) {
	events, err := storage.Get().GetEventLog()
	Get().Send("eventsResult", map[string]interface{}{
		"success": err == nil,
		"events":  events,
	})
}

func handleGetRecording(payload json.RawMessage) {
	var req struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	filePath, err := storage.Get().GetRecordingPath(req.ID)
	if err != nil {
		Get().Send("recordingResult", map[string]interface{}{
			"success": false,
		})
		return
	}

	// TODO: Stream file contents to relay server in chunks
	_ = filePath
	Get().Send("recordingResult", map[string]interface{}{
		"success": true,
	})
}

func handleStartStream(payload json.RawMessage) {
	stream, err := record.Get().StartStream()
	if err != nil {
		Get().Send("streamResult", map[string]interface{}{
			"success": false,
		})
		return
	}

	Get().Send("streamResult", map[string]interface{}{
		"success": true,
	})

	// TODO: Stream video/audio data to relay server
	// Read from stream.Video and stream.Audio, send chunks via WebSocket
	_ = stream
}

func handleStopStream(payload json.RawMessage) {
	err := record.Get().StopStream()
	Get().Send("stopStreamResult", map[string]interface{}{
		"success": err == nil,
	})
}

func handleToggleBabyphone(payload json.RawMessage) {
	var req struct {
		Enabled bool `json:"enabled"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	// TODO: Implement babyphone mode (2-way audio)
	// This would start audio streaming from device to camera
	Get().Send("babyphoneResult", map[string]interface{}{
		"success": true,
		"enabled": req.Enabled,
	})
}

func handleSetMicrophone(payload json.RawMessage) {
	var req struct {
		Enabled bool `json:"enabled"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	err := record.Get().SetMicrophoneEnabled(req.Enabled)
	Get().Send("microphoneResult", map[string]interface{}{
		"success": err == nil,
		"enabled": req.Enabled,
	})
}

func handleSetRecordingSound(payload json.RawMessage) {
	var req struct {
		PlayOnRecord bool `json:"playOnRecord"`
		PlayOnLive   bool `json:"playOnLive"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	// TODO: Store in config and implement sound playback
	Get().Send("recordingSoundResult", map[string]interface{}{
		"success": true,
	})
}

func handleGetHealth(payload json.RawMessage) {
	u := ups.Get()

	// TODO: Get error log from somewhere
	// TODO: Get performance metrics with gopsutil
	// TODO: Get firmware version from config
	// TODO: Get firmware update status

	health := map[string]interface{}{
		"battery": map[string]interface{}{
			"percent":   100,
			"onACPower": true,
			"lowPower":  false,
		},
		"wifi": map[string]interface{}{
			"connected": wifi.Get().IsConnected(),
			"ssid":      wifi.Get().GetCurrentNetwork(),
		},
		"firmwareVersion": "1.0.0", // TODO: Get from config
		"updateStatus":    "up-to-date",
		"relayUrl":        "", // TODO: Get from config
		"errors":          []string{},
		"performance":     map[string]interface{}{},
	}

	if u != nil {
		health["battery"] = map[string]interface{}{
			"percent":   u.GetBatteryPercent(),
			"onACPower": u.OnACPower(),
			"lowPower":  u.IsLowPower(),
		}
	}

	Get().Send("healthResult", health)
}

func handleGetPreview(payload json.RawMessage) {
	frameData, err := record.Get().CapturePreview()
	if err != nil {
		Get().Send("previewResult", map[string]interface{}{
			"success": false,
		})
		return
	}

	// TODO: Send JPEG data - encode as base64 or send as binary
	_ = frameData
	Get().Send("previewResult", map[string]interface{}{
		"success": true,
		// "image": base64.StdEncoding.EncodeToString(frameData),
	})
}

func handleRestart(payload json.RawMessage) {
	// TODO: Safely restart the camera
	// exec.Command("sudo", "reboot").Run()

	Get().Send("restartResult", map[string]interface{}{
		"success": true,
	})
}
