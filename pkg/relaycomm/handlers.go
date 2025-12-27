package relaycomm

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"os"
	"os/exec"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"

	"root-firmware/pkg/config"
	"root-firmware/pkg/devices"
	"root-firmware/pkg/encryption"
	"root-firmware/pkg/globals"
	"root-firmware/pkg/logger"
	"root-firmware/pkg/record"
	"root-firmware/pkg/storage"
	"root-firmware/pkg/updater"
	"root-firmware/pkg/ups"
	"root-firmware/pkg/wifi"
)

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
		"target": "product",
		"productId": "camera-uuid-123",    // ← Which camera should handle this
		"deviceId": "device-uuid-456",     // ← Which device sent this
		"encryptedPayload": "base64..." // { deviceId: "device-uuid-456", field1: ..., field 2:... } (Device ID is included here again to verify this payload was encrypted by this device)
	}

Camera → Relay Server → Device:
	{
		"type": "wifiScanResult",
		"target": "device",
		"productId": "camera-uuid-123",    // ← Which camera sent this
		"deviceId": "device-uuid-456",     // ← Which device should receive this
		"encryptedPayload": "base64..." // { productId: "camera-uuid-123", success: true, networks: [...] } (Product ID is included here to verify this payload was encrypted by this camera)
	}
*/

// Middleware for e2e encryption
func useEncryption(messageType string, handler func(*HandlerContext, json.RawMessage)) func(Message) {
	return func(msg Message) {
		// Get device to verify it's paired
		device, ok := devices.Get().GetByID(msg.DeviceID)
		if !ok {
			sendError(msg.DeviceID, messageType+"Result", "Device not paired!")
			return
		}

		// Get camera's private key (single key for all devices)
		cameraPrivateKey, ok := config.Get().GetKey("cameraPrivateKey")
		if !ok {
			sendError(msg.DeviceID, messageType+"Result", "Camera not initialized!")
			return
		}

		// Derive shared secret using camera's private key and device's public key
		sharedSecret, err := encryption.DeriveSharedSecret(cameraPrivateKey.([]byte), device.PublicKey)
		if err != nil {
			sendError(msg.DeviceID, messageType+"Result", "Failed to derive encryption key!")
			return
		}

		// Create session for decryption
		session, err := encryption.FromSharedSecret(sharedSecret)
		if err != nil {
			sendError(msg.DeviceID, messageType+"Result", "Failed to create encryption session!")
			return
		}

		// Decrypt payload
		decrypted, err := session.Decrypt(msg.EncryptedPayload)
		if err != nil {
			sendError(msg.DeviceID, messageType+"Result", "Failed to decrypt payload!")
			return
		}

		// Verify deviceID inside encrypted payload matches the outer claim
		var payloadCheck struct {
			DeviceID string `json:"deviceId"`
		}
		if err := json.Unmarshal(decrypted, &payloadCheck); err != nil {
			sendError(msg.DeviceID, messageType+"Result", "Invalid payload format!")
			return
		}

		if payloadCheck.DeviceID != msg.DeviceID {
			sendError(msg.DeviceID, messageType+"Result", "Device ID mismatch!")
			return
		}

		// Create handler context with encryption info
		ctx := &HandlerContext{
			DeviceID:          msg.DeviceID,
			SharedSecret:      sharedSecret,
			EncryptionSession: session,
		}

		// Call the actual handler with context and decrypted payload
		handler(ctx, json.RawMessage(decrypted))
	}
}

// sendError sends an error response to a device
func sendError(deviceID, messageType, errorMsg string) {
	productID, _ := config.Get().GetKey("id")

	// Create error payload
	errorPayload := map[string]any{
		"productId": productID,
		"success":   false,
		"error":     errorMsg,
	}
	payloadJSON, _ := json.Marshal(errorPayload)

	Get().Send(Message{
		Type:             messageType,
		Target:           "device",
		ProductID:        productID.(string),
		DeviceID:         deviceID,
		EncryptedPayload: string(payloadJSON), // Send as unencrypted JSON for errors
	})
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
	// Get product ID
	productID, ok := config.Get().GetKey("id")
	if !ok {
		return fmt.Errorf("product ID missing from config (trying to send encrypted WS message)")
	}

	// Payload must be a map for valid JSON
	payloadMap, ok := payload.(map[string]any)
	if !ok {
		return fmt.Errorf("payload must be a map[string]any")
	}

	// Wrap payload with product ID for verification
	wrappedPayload := map[string]any{
		"productId": productID,
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
	return Get().Send(Message{
		Type:             messageType,
		Target:           "device",
		ProductID:        productID.(string),
		DeviceID:         ctx.DeviceID,
		EncryptedPayload: encryptedPayload,
	})
}

// RegisterHandlers registers all relay message handlers and starts connection if relay domain is configured
func RegisterHandlers() {
	// Check if relay domain is configured
	relayDomain, ok := config.Get().GetKey("relayDomain")
	if !ok || relayDomain == "" {
		log.Println("Relay handlers not registered - relay domain not configured")
		return
	}

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
	relay.On("getThumbnail", useEncryption("getThumbnail", handleGetThumbnail))

	// Streaming
	relay.On("startStream", useEncryption("startStream", handleStartStream))
	relay.On("stopStream", useEncryption("stopStream", handleStopStream))

	// Settings
	relay.On("setMicrophone", useEncryption("setMicrophone", handleSetMicrophone))
	relay.On("setRecordingSound", useEncryption("setRecordingSound", handleSetRecordingSound))

	// System
	relay.On("getHealth", useEncryption("getHealth", handleGetHealth))
	relay.On("getPreview", useEncryption("getPreview", handleGetPreview))
	relay.On("startUpdate", useEncryption("startUpdate", handleStartUpdate))
	relay.On("restart", useEncryption("restart", handleRestart))
	relay.On("reset", useEncryption("reset", handleReset))

	// Start the connection
	if err := relay.Start(); err != nil {
		log.Printf("Failed to start relay comm: %v", err)
	}
}

func handleGetDevices(ctx *HandlerContext, payload json.RawMessage) {
	allDevices := devices.Get().GetAll()
	SendEncrypted(ctx, "devicesResult", map[string]any{
		"success": true,
		"devices": allDevices,
	})
}

func handleRemoveDevice(ctx *HandlerContext, payload json.RawMessage) {
	// Device can only remove itself
	err := devices.Get().Remove(ctx.DeviceID)
	SendEncrypted(ctx, "removeDeviceResult", buildResult(err, nil))
}

func handleKickDevice(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		TargetDeviceID string `json:"targetDeviceId"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	// Device cannot kick itself
	if req.TargetDeviceID == ctx.DeviceID {
		SendEncrypted(ctx, "kickDeviceResult", buildResult(fmt.Errorf("cannot kick self"), nil))
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
		SSID     string `json:"ssid"`
		Password string `json:"password"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

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
		ID string `json:"id"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	filePath, err := storage.Get().GetRecordingPath(req.ID)
	if err != nil {
		SendEncrypted(ctx, "recordingResult", buildResult(err, nil))
		return
	}

	fileData, err := os.ReadFile(filePath)
	if err != nil {
		SendEncrypted(ctx, "recordingResult", buildResult(fmt.Errorf("failed to read file: %w", err), nil))
		return
	}

	SendEncrypted(ctx, "recordingResult", buildResult(nil, map[string]any{
		"data": base64.StdEncoding.EncodeToString(fileData),
	}))
}

func handleGetThumbnail(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	filePath, err := storage.Get().GetThumbnailPath(req.ID)
	if err != nil {
		SendEncrypted(ctx, "thumbnailResult", buildResult(err, nil))
		return
	}

	fileData, err := os.ReadFile(filePath)
	if err != nil {
		SendEncrypted(ctx, "thumbnailResult", buildResult(fmt.Errorf("failed to read thumbnail: %w", err), nil))
		return
	}

	SendEncrypted(ctx, "thumbnailResult", buildResult(nil, map[string]any{
		"data": base64.StdEncoding.EncodeToString(fileData),
	}))
}

func handleStartStream(ctx *HandlerContext, payload json.RawMessage) {
	// Add viewer (enforces limit)
	if err := addViewer(ctx); err != nil {
		SendEncrypted(ctx, "startStreamResult", buildResult(err, nil))
		return
	}

	// Start stream if not already running
	stream, err := record.Get().StartStream()
	if err != nil && err.Error() != "already streaming" {
		removeViewer(ctx.DeviceID)
		SendEncrypted(ctx, "startStreamResult", buildResult(err, nil))
		return
	}

	// If this is the first viewer, start streaming goroutines
	if err == nil {
		go func() {
			if err := StreamReader(stream.Video, "streamVideoChunkResult"); err != nil {
				broadcastChunk("streamVideoChunkResult", map[string]any{
					"success": false,
					"error":   err.Error(),
					"done":    true,
				})
			}
		}()

		if stream.Audio != nil {
			go func() {
				if err := StreamReader(stream.Audio, "streamAudioChunkResult"); err != nil {
					broadcastChunk("streamAudioChunkResult", map[string]any{
						"success": false,
						"error":   err.Error(),
						"done":    true,
					})
				}
			}()
		}
	}

	SendEncrypted(ctx, "startStreamResult", buildResult(nil, nil))
}

func handleStopStream(ctx *HandlerContext, payload json.RawMessage) {
	var err error
	if removeViewer(ctx.DeviceID) {
		err = record.Get().StopStream()
	}
	SendEncrypted(ctx, "stopStreamResult", buildResult(err, nil))
}

func handleSetMicrophone(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		Enabled bool `json:"enabled"`
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
		Enabled bool `json:"enabled"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}

	err := config.Get().SetKey("play_active_camera_sound", req.Enabled)
	SendEncrypted(ctx, "recordingSoundResult", buildResult(err, map[string]any{
		"enabled": req.Enabled,
	}))
}

func handleGetHealth(ctx *HandlerContext, payload json.RawMessage) {
	u := ups.Get()

	// Get performance metrics using gopsutil
	performance := map[string]any{}

	// CPU usage (average over 500ms)
	if percentages, err := cpu.Percent(0, false); err == nil && len(percentages) > 0 {
		performance["cpuUsagePercent"] = percentages[0]
	}

	// CPU temperature
	if temps, err := host.SensorsTemperatures(); err == nil {
		for _, temp := range temps {
			// Look for CPU temp (common sensor names on Raspberry Pi)
			if temp.SensorKey == "cpu_thermal" || temp.SensorKey == "coretemp" {
				performance["cpuTempCelsius"] = temp.Temperature
				break
			}
		}
	}

	// Memory stats
	if vmStat, err := mem.VirtualMemory(); err == nil {
		performance["memoryUsedMB"] = vmStat.Used / (1024 * 1024)
		performance["memoryTotalMB"] = vmStat.Total / (1024 * 1024)
		performance["memoryUsagePercent"] = vmStat.UsedPercent
	}

	// Disk stats for data partition
	if diskStat, err := disk.Usage(globals.DataDir); err == nil {
		performance["diskUsedGB"] = diskStat.Used / (1024 * 1024 * 1024)
		performance["diskTotalGB"] = diskStat.Total / (1024 * 1024 * 1024)
		performance["diskUsagePercent"] = diskStat.UsedPercent
	}

	// Uptime
	if uptime, err := host.Uptime(); err == nil {
		performance["uptimeSeconds"] = uptime
	}

	// Get relay domain from config
	relayDomain := ""
	if domain, ok := config.Get().GetKey("relayDomain"); ok {
		if domainStr, ok := domain.(string); ok {
			relayDomain = domainStr
		}
	}

	// Get update status
	updateStatus, availableVersion, updateError := updater.Get().GetStatus()
	updateInfo := map[string]any{
		"status": string(updateStatus),
	}
	if availableVersion != "" {
		updateInfo["availableVersion"] = availableVersion
	}
	if updateError != "" {
		updateInfo["error"] = updateError
	}

	health := map[string]any{
		"battery": map[string]any{
			"percent":   0,
			"onACPower": true,
		},
		"wifi": map[string]any{
			"connected": wifi.Get().IsConnected(),
			"ssid":      wifi.Get().GetCurrentNetwork(),
		},
		"firmwareVersion": globals.FirmwareVersion,
		"update":          updateInfo,
		"relayDomain":     relayDomain,
		"logs":            logger.GetLogs(),
		"performance":     performance,
	}

	if u != nil {
		health["battery"] = map[string]any{
			"percent":   u.GetBatteryPercent(),
			"onACPower": u.OnACPower(),
		}
	}

	SendEncrypted(ctx, "healthResult", health)
}

// TODO: Make this use buildError (slight refactor)
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

func handleStartUpdate(ctx *HandlerContext, payload json.RawMessage) {
	err := updater.Get().StartUpdate()
	SendEncrypted(ctx, "startUpdateResult", buildResult(err, nil))
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

	// Remove all contents of data partition, then reboot
	go func() {
		// Note: Using wildcard requires shell expansion, so use sh -c
		exec.Command("sh", "-c", "rm -rf "+globals.DataDir+"/*").Run()
		// Reboot the system after deletion completes
		exec.Command("sudo", "reboot").Run()
	}()
}
