package relaycomm

import (
	"encoding/base64"
	"encoding/json"
	"os/exec"

	"root-firmware/pkg/devices"
	"root-firmware/pkg/encryption"
	"root-firmware/pkg/record"
	"root-firmware/pkg/storage"
	"root-firmware/pkg/ups"
	"root-firmware/pkg/wifi"
)

// EncryptedRequest wraps encrypted payloads from devices
type EncryptedRequest struct {
	DeviceID         string `json:"deviceId"`
	EncryptedPayload string `json:"encryptedPayload"` // Base64 encrypted JSON
}

// useEncryption wraps a handler to decrypt device messages using E2E encryption
func useEncryption(messageType string, handler func(json.RawMessage)) func(json.RawMessage) {
	return func(payload json.RawMessage) {
		var req EncryptedRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			Get().Send(messageType+"Result", map[string]interface{}{
				"success": false,
				"error":   "Invalid request format",
			})
			return
		}

		// Get device to verify it's paired
		device, ok := devices.Get().GetByID(req.DeviceID)
		if !ok {
			Get().Send(messageType+"Result", map[string]interface{}{
				"success": false,
				"error":   "Device not paired",
			})
			return
		}

		// Derive shared secret using camera's private key and device's public key
		sharedSecret, err := encryption.DeriveSharedSecret(device.CameraPrivateKey, device.PublicKey)
		if err != nil {
			Get().Send(messageType+"Result", map[string]interface{}{
				"success": false,
				"error":   "Failed to derive encryption key",
			})
			return
		}

		// Create session for decryption
		session, err := encryption.FromSharedSecret(sharedSecret)
		if err != nil {
			Get().Send(messageType+"Result", map[string]interface{}{
				"success": false,
				"error":   "Failed to create encryption session",
			})
			return
		}

		// Decrypt payload
		decrypted, err := session.Decrypt(req.EncryptedPayload)
		if err != nil {
			Get().Send(messageType+"Result", map[string]interface{}{
				"success": false,
				"error":   "Failed to decrypt payload",
			})
			return
		}

		// Call the actual handler with decrypted payload
		handler(json.RawMessage(decrypted))
	}
}

// RegisterHandlers registers all relay message handlers with E2E encryption
func RegisterHandlers() {
	relay := Get()

	// Device management
	relay.On("getDevices", useEncryption("getDevices", handleGetDevices))
	relay.On("removeDevice", useEncryption("removeDevice", handleRemoveDevice))
	relay.On("kickDevice", useEncryption("kickDevice", handleKickDevice))

	// WiFi
	relay.On("wifiScan", useEncryption("wifiScan", handleWiFiScan))
	relay.On("wifiConnect", useEncryption("wifiConnect", handleWiFiConnect))

	// Storage
	relay.On("getEvents", useEncryption("getEvents", handleGetEvents))
	relay.On("getRecording", useEncryption("getRecording", handleGetRecording))

	// Streaming
	relay.On("startStream", useEncryption("startStream", handleStartStream))
	relay.On("stopStream", useEncryption("stopStream", handleStopStream))
	relay.On("toggleBabyphone", useEncryption("toggleBabyphone", handleToggleBabyphone))

	// Settings
	relay.On("setMicrophone", useEncryption("setMicrophone", handleSetMicrophone))
	relay.On("setRecordingSound", useEncryption("setRecordingSound", handleSetRecordingSound))

	// System
	relay.On("getHealth", useEncryption("getHealth", handleGetHealth))
	relay.On("getPreview", useEncryption("getPreview", handleGetPreview))
	relay.On("restart", useEncryption("restart", handleRestart))
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

	// Connect method verifies internet connectivity, otherwise rejects wifi
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

	// TODO: Stream video/audio data to relay server (see record package)
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

	Get().Send("previewResult", map[string]interface{}{
		"success": true,
		"image":   base64.StdEncoding.EncodeToString(frameData),
	})
}

func handleRestart(payload json.RawMessage) {
	// Send success response before rebooting
	Get().Send("restartResult", map[string]interface{}{
		"success": true,
	})

	// Reboot the system
	go func() {
		exec.Command("sudo", "reboot").Run()
	}()
}
