package relaycomm

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"os/exec"

	"root-firmware/pkg/config"
	"root-firmware/pkg/devices"
	"root-firmware/pkg/encryption"
	"root-firmware/pkg/record"
	"root-firmware/pkg/storage"
	"root-firmware/pkg/ups"
	"root-firmware/pkg/wifi"
)

// EncryptedRequest wraps encrypted payloads from devices
type EncryptedRequest struct {
	CameraID         string `json:"cameraId"`         // Target camera ID
	DeviceID         string `json:"deviceId"`         // Source device ID
	EncryptedPayload string `json:"encryptedPayload"` // Base64 encrypted JSON
}

// HandlerContext provides encryption context to handlers
type HandlerContext struct {
	DeviceID          string
	SharedSecret      []byte
	EncryptionSession *encryption.Session
}

/* Flow example:
Device → Relay Server → Camera:
	{
		"type": "wifiScan",
		"payload": {
			"cameraId": "camera-uuid-123",    // ← Which camera should handle this
			"deviceId": "device-uuid-456",     // ← Which device sent this
			"encryptedPayload": "base64..." // { deviceId: "device-uuid-456", field1: ..., field 2:... } (Device ID is included here again to verify this payload was encrypted by this device)
		}
	}

Camera → Relay Server → Device:
	{
		"type": "wifiScanResult",
		"payload": {
			"cameraId": "camera-uuid-123",    // ← Which camera sent this
			"deviceId": "device-uuid-456",     // ← Which device should receive this
			"encryptedPayload": "base64..." // { cameraId: "camera-uuid-123", success: true, networks: [...] } (Camera ID is included here to verify this payload was encrypted by this camera)
		}
	}
*/

// Middleware for e2e encryption
func useEncryption(messageType string, handler func(*HandlerContext, json.RawMessage)) func(json.RawMessage) {
	return func(payload json.RawMessage) {
		var req EncryptedRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			Get().Send(messageType+"Result", map[string]any{
				"success": false,
				"error":   "Invalid request format!",
			})
			return
		}

		// Get device to verify it's paired
		device, ok := devices.Get().GetByID(req.DeviceID)
		if !ok {
			Get().Send(messageType+"Result", map[string]any{
				"success": false,
				"error":   "Device not paired!",
			})
			return
		}

		// Get camera's private key (single key for all devices)
		cameraPrivateKey, ok := config.Get().GetKey("cameraPrivateKey")
		if !ok {
			Get().Send(messageType+"Result", map[string]any{
				"success": false,
				"error":   "Camera not initialized!",
			})
			return
		}

		// Derive shared secret using camera's private key and device's public key
		sharedSecret, err := encryption.DeriveSharedSecret(cameraPrivateKey.([]byte), device.PublicKey)
		if err != nil {
			Get().Send(messageType+"Result", map[string]any{
				"success": false,
				"error":   "Failed to derive encryption key!",
			})
			return
		}

		// Create session for decryption
		session, err := encryption.FromSharedSecret(sharedSecret)
		if err != nil {
			Get().Send(messageType+"Result", map[string]any{
				"success": false,
				"error":   "Failed to create encryption session!",
			})
			return
		}

		// Decrypt payload
		decrypted, err := session.Decrypt(req.EncryptedPayload)
		if err != nil {
			Get().Send(messageType+"Result", map[string]any{
				"success": false,
				"error":   "Failed to decrypt payload!",
			})
			return
		}

		// Verify deviceID inside encrypted payload matches the outer claim
		var payloadCheck struct {
			DeviceID string `json:"deviceId"`
		}
		if err := json.Unmarshal(decrypted, &payloadCheck); err != nil {
			Get().Send(messageType+"Result", map[string]any{
				"success": false,
				"error":   "Invalid payload format!",
			})
			return
		}

		if payloadCheck.DeviceID != req.DeviceID {
			Get().Send(messageType+"Result", map[string]any{
				"success": false,
				"error":   "Device ID mismatch!",
			})
			return
		}

		// Create handler context with encryption info
		ctx := &HandlerContext{
			DeviceID:          req.DeviceID,
			SharedSecret:      sharedSecret,
			EncryptionSession: session,
		}

		// Call the actual handler with context and decrypted payload
		handler(ctx, json.RawMessage(decrypted))
	}
}

// buildResult creates a result map with optional error
func buildResult(err error, fields map[string]any) map[string]any {
	result := map[string]any{
		"success": err == nil,
	}
	if err != nil {
		result["error"] = err.Error()
	}
	maps.Copy(result, fields)
	return result
}

// SendEncrypted sends an encrypted response to a specific device
func SendEncrypted(ctx *HandlerContext, messageType string, payload any) error {
	// Get camera ID
	cameraID, ok := config.Get().GetKey("id")
	if !ok {
		return fmt.Errorf("camera ID missing from config (trying to send encrypted WS message)")
	}

	// Payload must be a map for valid JSON
	payloadMap, ok := payload.(map[string]any)
	if !ok {
		return fmt.Errorf("payload must be a map[string]any")
	}

	// Wrap payload with camera ID for verification
	wrappedPayload := map[string]any{
		"cameraId": cameraID,
	}

	// Merge the actual payload into the wrapped payload
	maps.Copy(wrappedPayload, payloadMap)

	// Marshal wrapped payload to JSON
	payloadJSON, err := json.Marshal(wrappedPayload)
	if err != nil {
		return err
	}

	// Encrypt the payload
	encryptedPayload, err := ctx.EncryptionSession.Encrypt(payloadJSON)
	if err != nil {
		return err
	}

	// Send encrypted response
	return Get().Send(messageType, map[string]any{
		"cameraId":         cameraID,     // Source camera ID (outer)
		"deviceId":         ctx.DeviceID, // Target device ID
		"encryptedPayload": encryptedPayload,
	})
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
	relay.On("reset", useEncryption("reset", handleReset))
}

func handleGetDevices(ctx *HandlerContext, payload json.RawMessage) {
	allDevices := devices.Get().GetAll()
	SendEncrypted(ctx, "devicesResult", map[string]any{
		"success": true,
		"devices": allDevices,
	})
}

func handleRemoveDevice(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		DeviceID string `json:"deviceId"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	// Device can only remove itself
	err := devices.Get().Remove(ctx.DeviceID)
	SendEncrypted(ctx, "removeDeviceResult", buildResult(err, nil))
}

func handleKickDevice(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		DeviceID       string `json:"deviceId"`
		TargetDeviceID string `json:"targetDeviceId"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	// Device cannot kick itself
	if req.TargetDeviceID == ctx.DeviceID {
		SendEncrypted(ctx, "kickDeviceResult", map[string]any{
			"success": false,
			"error":   "Cannot kick self!",
		})
		return
	}

	err := devices.Get().ScheduleKick(req.TargetDeviceID)
	SendEncrypted(ctx, "kickDeviceResult", buildResult(err, nil))
}

func handleWiFiScan(ctx *HandlerContext, payload json.RawMessage) {
	networks, err := wifi.Get().Scan()
	SendEncrypted(ctx, "wifiScanResult", buildResult(err, map[string]any{
		"networks": networks,
	}))
}

func handleWiFiConnect(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		DeviceID string `json:"deviceId"`
		SSID     string `json:"ssid"`
		Password string `json:"password"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	// Connect method verifies internet connectivity, otherwise rejects wifi
	err := wifi.Get().Connect(req.SSID, req.Password)
	SendEncrypted(ctx, "wifiConnectResult", buildResult(err, nil))
}

func handleGetEvents(ctx *HandlerContext, payload json.RawMessage) {
	events, err := storage.Get().GetEventLog()
	SendEncrypted(ctx, "eventsResult", buildResult(err, map[string]any{
		"events": events,
	}))
}

func handleGetRecording(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		DeviceID string `json:"deviceId"`
		ID       string `json:"id"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	filePath, err := storage.Get().GetRecordingPath(req.ID)
	if err != nil {
		SendEncrypted(ctx, "recordingResult", map[string]any{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// TODO: Stream file contents to relay server in chunks
	_ = filePath
	SendEncrypted(ctx, "recordingResult", map[string]any{
		"success": true,
	})
}

func handleStartStream(ctx *HandlerContext, payload json.RawMessage) {
	stream, err := record.Get().StartStream()
	if err != nil {
		SendEncrypted(ctx, "streamResult", map[string]any{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	SendEncrypted(ctx, "streamResult", map[string]any{
		"success": true,
	})

	// TODO: Stream video/audio data to relay server (see record package)
	// Read from stream.Video and stream.Audio, send chunks via WebSocket
	_ = stream
}

func handleStopStream(ctx *HandlerContext, payload json.RawMessage) {
	err := record.Get().StopStream()
	SendEncrypted(ctx, "stopStreamResult", buildResult(err, nil))
}

func handleToggleBabyphone(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		DeviceID string `json:"deviceId"`
		Enabled  bool   `json:"enabled"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	// TODO: Implement babyphone mode (2-way audio)
	// This would start audio streaming from device to camera
	SendEncrypted(ctx, "babyphoneResult", map[string]any{
		"success": true,
		"enabled": req.Enabled,
	})
}

func handleSetMicrophone(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		DeviceID string `json:"deviceId"`
		Enabled  bool   `json:"enabled"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	err := record.Get().SetMicrophoneEnabled(req.Enabled)
	SendEncrypted(ctx, "microphoneResult", buildResult(err, map[string]any{
		"enabled": req.Enabled,
	}))
}

func handleSetRecordingSound(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		DeviceID     string `json:"deviceId"`
		PlayOnRecord bool   `json:"playOnRecord"`
		PlayOnLive   bool   `json:"playOnLive"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	// TODO: Store in config and implement sound playback
	SendEncrypted(ctx, "recordingSoundResult", map[string]any{
		"success": true,
	})
}

func handleGetHealth(ctx *HandlerContext, payload json.RawMessage) {
	u := ups.Get()

	// TODO: Get error log from somewhere
	// TODO: Get performance metrics with gopsutil
	// TODO: Get firmware version from config
	// TODO: Get firmware update status

	health := map[string]any{
		"battery": map[string]any{
			"percent":   100,
			"onACPower": true,
		},
		"wifi": map[string]any{
			"connected": wifi.Get().IsConnected(),
			"ssid":      wifi.Get().GetCurrentNetwork(),
		},
		"firmwareVersion": "1.0.0", // TODO: Get from config
		"updateStatus":    "up-to-date",
		"relayUrl":        "", // TODO: Get from config
		"errors":          []string{},
		"performance":     map[string]any{},
	}

	if u != nil {
		health["battery"] = map[string]any{
			"percent":   u.GetBatteryPercent(),
			"onACPower": u.OnACPower(),
		}
	}

	SendEncrypted(ctx, "healthResult", health)
}

func handleGetPreview(ctx *HandlerContext, payload json.RawMessage) {
	frameData, err := record.Get().CapturePreview()
	if err != nil {
		SendEncrypted(ctx, "previewResult", map[string]any{
			"success": false,
		})
		return
	}

	SendEncrypted(ctx, "previewResult", map[string]any{
		"success": true,
		"image":   base64.StdEncoding.EncodeToString(frameData),
	})
}

func handleRestart(ctx *HandlerContext, payload json.RawMessage) {
	// Send success response before rebooting
	SendEncrypted(ctx, "restartResult", map[string]any{
		"success": true,
	})

	// Reboot the system
	go func() {
		exec.Command("sudo", "reboot").Run()
	}()
}

func handleReset(ctx *HandlerContext, payload json.RawMessage) {
	// Send success response before resetting
	SendEncrypted(ctx, "resetResult", map[string]any{
		"success": true,
	})

	// Remove all contents of /data partition, then reboot
	go func() {
		// Note: Using /data/* requires shell expansion, so use sh -c
		exec.Command("sh", "-c", "rm -rf /data/*").Run()
		// Reboot the system after deletion completes
		exec.Command("sudo", "reboot").Run()
	}()
}
