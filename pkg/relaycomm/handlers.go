package relaycomm

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"os"
	"os/exec"
	"strings"
	"time"

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
	"root-firmware/pkg/wifi"
)

// Message type constants
const (
	MsgRenewKey          = "renewKey"
	MsgRenewKeyAck       = "renewKeyAck"
	MsgGetDevices        = "getDevices"
	MsgRemoveDevice      = "removeDevice"
	MsgGetEvents         = "getEvents"
	MsgGetRecording      = "getRecording"
	MsgGetThumbnail      = "getThumbnail"
	MsgStartStream       = "startStream"
	MsgContinueStream    = "continueStream"
	MsgStreamVideoChunk  = "streamVideoChunk"
	MsgStreamAudioChunk  = "streamAudioChunk"
	MsgGetMicrophone     = "getMicrophone"
	MsgSetMicrophone     = "setMicrophone"
	MsgGetRecordingSound = "getRecordingSound"
	MsgSetRecordingSound = "setRecordingSound"
	MsgGetHealth         = "getHealth"
	MsgGetPreview        = "getPreview"
	MsgStartUpdate       = "startUpdate"
	MsgRestart           = "restart"
	MsgReset             = "reset"
)

// Error code constants
const (
	ErrDeviceNotPaired  = "DEVICE_NOT_PAIRED"
	ErrCameraNotInit    = "CAMERA_NOT_INITIALIZED"
	ErrInvalidKey       = "INVALID_KEY"
	ErrDecryptionFailed = "DECRYPTION_FAILED"
	ErrInvalidPayload   = "INVALID_PAYLOAD"
	ErrInternalError    = "INTERNAL_ERROR"
	ErrStreamSwitched   = "STREAM_SWITCHED"
)

// HandlerContext provides encryption context to handlers
type HandlerContext struct {
	DeviceID          string
	RequestID         string
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
		"payload": "encrypted base64..." // { ssid: "...", password: "..." } (No deviceId needed - if wrong device sent it, decryption would fail)
	}

Camera → Relay Server → Device:
	{
		"type": "wifiScanResult",
		"target": "device",
		"productId": "camera-uuid-123",    // ← Which camera sent this
		"deviceId": "device-uuid-456",     // ← Which device should receive this
		"payload": "encrypted base64..." // { success: true, networks: [...] } (No productId needed - if wrong camera sent it, decryption would fail)
	}
*/

// Middleware for e2e encryption
func useEncryption(messageType string, handler func(*HandlerContext, json.RawMessage)) func(Message) {
	return func(msg Message) {
		// Get device to verify it's paired
		device, ok := devices.Get().GetByID(msg.DeviceID)
		if !ok {
			sendError(msg.DeviceID, msg.RequestID, messageType, ErrDeviceNotPaired, "Device not paired!")
			return
		}

		// Get camera's private key (stored as base64 string)
		cameraPrivateKeyEncoded, ok := config.Get().GetKey("cameraPrivateKey")
		if !ok {
			sendError(msg.DeviceID, msg.RequestID, messageType, ErrCameraNotInit, "Camera not initialized!")
			return
		}

		privKeyStr, ok := cameraPrivateKeyEncoded.(string)
		if !ok {
			log.Printf("RelayComm: Camera private key has invalid type")
			sendError(msg.DeviceID, msg.RequestID, messageType, ErrInvalidKey, "Camera encryption key invalid!")
			return
		}

		// Decode from base64
		privKey, err := encryption.DecodeKey(privKeyStr)
		if err != nil {
			log.Printf("RelayComm: Failed to decode camera private key: %v", err)
			sendError(msg.DeviceID, msg.RequestID, messageType, ErrInvalidKey, "Camera encryption key invalid!")
			return
		}

		// Derive shared secret using camera's private key and device's public key
		sharedSecret, err := encryption.DeriveSharedSecret(privKey, device.PublicKey)
		if err != nil {
			sendError(msg.DeviceID, msg.RequestID, messageType, ErrInvalidKey, "Failed to derive encryption key!")
			return
		}

		// Create session for decryption
		session, err := encryption.FromSharedSecret(sharedSecret)
		if err != nil {
			sendError(msg.DeviceID, msg.RequestID, messageType, ErrInternalError, "Failed to create encryption session!")
			return
		}

		// Decrypt payload (try current key first, then previous key if available)
		decrypted, err := session.Decrypt(msg.Payload)
		if err != nil {
			// Retry with previous encryption session if available (during key renewal grace period)
			if prevSession, ok := GetPreviousEncryption(msg.DeviceID); ok {
				decrypted, err = prevSession.Decrypt(msg.Payload)
				if err != nil {
					sendError(msg.DeviceID, msg.RequestID, messageType, ErrDecryptionFailed, "Failed to decrypt payload!")
					return
				}
			} else {
				sendError(msg.DeviceID, msg.RequestID, messageType, ErrDecryptionFailed, "Failed to decrypt payload!")
				return
			}
		}

		// Create handler context with encryption info
		ctx := &HandlerContext{
			DeviceID:          msg.DeviceID,
			RequestID:         msg.RequestID,
			SharedSecret:      sharedSecret,
			EncryptionSession: session,
		}

		// Call the actual handler with context and decrypted payload
		handler(ctx, json.RawMessage(decrypted))
	}
}

// sendError sends an error response to a device
func sendError(deviceID, requestID, messageType, errorCode, errorMsg string) {
	productID, _ := config.Get().GetKey("id")

	prodIDStr, ok := productID.(string)
	if !ok {
		log.Printf("RelayComm: Cannot send error - product ID has invalid type")
		return
	}

	// Create error payload with error code
	errorPayload := map[string]any{
		"success":   false,
		"errorCode": errorCode,
		"error":     errorMsg,
	}
	payloadJSON, err := json.Marshal(errorPayload)
	if err != nil {
		log.Printf("RelayComm: Failed to marshal error payload: %v", err)
		return
	}

	Get().Send(Message{
		Type:      messageType + "Result",
		Target:    "device",
		ProductID: prodIDStr,
		DeviceID:  deviceID,
		RequestID: requestID,
		Payload:   string(payloadJSON), // Send as unencrypted JSON for errors
	})
}

// sendEncrypted sends an encrypted response to a specific device (private - use helpers instead)
func sendEncrypted(ctx *HandlerContext, messageType string, payload map[string]any) error {
	// Get product ID
	productID, ok := config.Get().GetKey("id")
	if !ok {
		return fmt.Errorf("product ID missing from config (trying to send encrypted WS message)")
	}

	prodIDStr, ok := productID.(string)
	if !ok {
		return fmt.Errorf("product ID has invalid type")
	}

	// Marshal payload to JSON
	payloadJSON, err := json.Marshal(payload)
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
		Type:      messageType + "Result",
		Target:    "device",
		ProductID: prodIDStr,
		DeviceID:  ctx.DeviceID,
		RequestID: ctx.RequestID,
		Payload:   encryptedPayload,
	})
}

// SendEncryptedSuccess sends an encrypted success response with optional data fields
func SendEncryptedSuccess(ctx *HandlerContext, messageType string, fields map[string]any) error {
	payload := map[string]any{
		"success": true,
	}
	maps.Copy(payload, fields)
	return sendEncrypted(ctx, messageType, payload)
}

// SendEncryptedError sends an encrypted error response with error code
func SendEncryptedError(ctx *HandlerContext, messageType, errorCode, errorMsg string) error {
	payload := map[string]any{
		"success":   false,
		"errorCode": errorCode,
		"error":     errorMsg,
	}
	return sendEncrypted(ctx, messageType, payload)
}

// RegisterHandlers registers all relay message handlers and starts connection if relay domain is configured
func RegisterHandlers() {
	// Check if relay domain is configured
	relayDomain, ok := config.Get().GetKey("relayDomain")
	if !ok || relayDomain == "" {
		log.Println("RelayComm: Relay handlers not registered - relay domain not configured")
		return
	}

	relay := Get()

	// Key renewal
	relay.On(MsgRenewKey, handleRenewKey)
	relay.On(MsgRenewKeyAck, handleRenewKeyAck)

	// Device management
	relay.On(MsgGetDevices, useEncryption(MsgGetDevices, handleGetDevices))
	relay.On(MsgRemoveDevice, useEncryption(MsgRemoveDevice, handleRemoveDevice))

	// Storage
	relay.On(MsgGetEvents, useEncryption(MsgGetEvents, handleGetEvents))
	relay.On(MsgGetRecording, useEncryption(MsgGetRecording, handleGetRecording))
	relay.On(MsgGetThumbnail, useEncryption(MsgGetThumbnail, handleGetThumbnail))

	// Streaming
	relay.On(MsgStartStream, useEncryption(MsgStartStream, handleStartStream))
	relay.On(MsgContinueStream, useEncryption(MsgContinueStream, handleContinueStream))

	// Settings
	relay.On(MsgGetMicrophone, useEncryption(MsgGetMicrophone, handleGetMicrophone))
	relay.On(MsgSetMicrophone, useEncryption(MsgSetMicrophone, handleSetMicrophone))
	relay.On(MsgGetRecordingSound, useEncryption(MsgGetRecordingSound, handleGetRecordingSound))
	relay.On(MsgSetRecordingSound, useEncryption(MsgSetRecordingSound, handleSetRecordingSound))

	// System
	relay.On(MsgGetHealth, useEncryption(MsgGetHealth, handleGetHealth))
	relay.On(MsgGetPreview, useEncryption(MsgGetPreview, handleGetPreview))
	relay.On(MsgStartUpdate, useEncryption(MsgStartUpdate, handleStartUpdate))
	relay.On(MsgRestart, useEncryption(MsgRestart, handleRestart))
	relay.On(MsgReset, useEncryption(MsgReset, handleReset))

	// Start the connection
	if err := relay.Start(); err != nil {
		log.Printf("Failed to start relay comm: %v", err)
	}
}

func handleRenewKey(msg Message) {
	// Get device to verify it's paired (allow expired keys for renewal)
	device, ok := devices.Get().GetByID(msg.DeviceID)
	if !ok {
		sendError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrDeviceNotPaired, "Device not paired!")
		return
	}

	// Get camera's private key (stored as base64 string)
	cameraPrivateKeyEncoded, ok := config.Get().GetKey("cameraPrivateKey")
	if !ok {
		sendError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrCameraNotInit, "Camera not initialized!")
		return
	}

	privKeyStr, ok := cameraPrivateKeyEncoded.(string)
	if !ok {
		log.Printf("RelayComm: Camera private key has invalid type")
		sendError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrInvalidKey, "Camera encryption key invalid!")
		return
	}

	// Decode from base64
	privKey, err := encryption.DecodeKey(privKeyStr)
	if err != nil {
		log.Printf("RelayComm: Failed to decode camera private key: %v", err)
		sendError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrInvalidKey, "Camera encryption key invalid!")
		return
	}

	// Derive shared secret using current key (still stored in device)
	sharedSecret, err := encryption.DeriveSharedSecret(privKey, device.PublicKey)
	if err != nil {
		sendError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrInvalidKey, "Failed to derive encryption key!")
		return
	}

	// Create session for decryption
	session, err := encryption.FromSharedSecret(sharedSecret)
	if err != nil {
		sendError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrInternalError, "Failed to create encryption session!")
		return
	}

	// Decrypt payload containing new public key
	decrypted, err := session.Decrypt(msg.Payload)
	if err != nil {
		sendError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrDecryptionFailed, "Failed to decrypt payload!")
		return
	}

	var req struct {
		NewPublicKey string `json:"newPublicKey"`
	}
	if err := json.Unmarshal(decrypted, &req); err != nil {
		sendError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrInvalidPayload, "Invalid payload!")
		return
	}

	// Decode new public key
	newPublicKey, err := encryption.DecodeKey(req.NewPublicKey)
	if err != nil {
		sendError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrInvalidKey, "Invalid public key!")
		return
	}

	// Derive NEW shared secret (but don't commit yet - wait for ACK)
	newSharedSecret, err := encryption.DeriveSharedSecret(privKey, newPublicKey)
	if err != nil {
		sendError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrInvalidKey, "Failed to derive new encryption key!")
		return
	}

	newSession, err := encryption.FromSharedSecret(newSharedSecret)
	if err != nil {
		sendError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrInternalError, "Failed to create new encryption session!")
		return
	}

	// Create context with current session for response (client hasn't switched yet)
	currentCtx := &HandlerContext{
		DeviceID:          msg.DeviceID,
		RequestID:         msg.RequestID,
		SharedSecret:      sharedSecret,
		EncryptionSession: session,
	}

	StorePreviousEncryption(msg.DeviceID, session)                                   // Store current encryption session for grace period
	BufferPendingKeyRenewal(msg.DeviceID, newPublicKey, newSharedSecret, newSession) // Buffer new key (don't commit yet)

	log.Printf("RelayComm: Key renewal prepared for device %s, waiting for ACK", msg.DeviceID)
	SendEncryptedSuccess(currentCtx, MsgRenewKey, nil) // Send with current (to-be-replaced) encryption
}

func handleRenewKeyAck(msg Message) {
	newPublicKey, newSharedSecret, newSession, ok := GetAndClearPendingKeyRenewal(msg.DeviceID)
	if !ok {
		log.Printf("RelayComm: No pending key renewal for device %s", msg.DeviceID)
		sendError(msg.DeviceID, msg.RequestID, MsgRenewKeyAck, ErrInternalError, "No pending key renewal!")
		return
	}

	decrypted, err := newSession.Decrypt(msg.Payload) // Decrypt ACK with NEW key
	if err != nil {
		log.Printf("RelayComm: Failed to decrypt renewal ACK from device %s: %v", msg.DeviceID, err)
		sendError(msg.DeviceID, msg.RequestID, MsgRenewKeyAck, ErrDecryptionFailed, "Failed to decrypt payload!")
		return
	}

	var req struct {
		Ack bool `json:"ack"`
	}
	if err := json.Unmarshal(decrypted, &req); err != nil || !req.Ack {
		log.Printf("RelayComm: Invalid renewal ACK from device %s", msg.DeviceID)
		sendError(msg.DeviceID, msg.RequestID, MsgRenewKeyAck, ErrInvalidPayload, "Invalid payload!")
		return
	}

	log.Printf("RelayComm: Committing new key for device %s", msg.DeviceID)
	if err := devices.Get().RenewKey(msg.DeviceID, newPublicKey); err != nil { // NOW commit the new key
		log.Printf("RelayComm: Failed to commit renewed key for device %s: %v", msg.DeviceID, err)
		sendError(msg.DeviceID, msg.RequestID, MsgRenewKeyAck, ErrInternalError, "Failed to commit key!")
		return
	}

	// Update stream context if this device is streaming
	newCtx := &HandlerContext{
		DeviceID:          msg.DeviceID,
		RequestID:         msg.RequestID,
		SharedSecret:      newSharedSecret,
		EncryptionSession: newSession,
	}
	UpdateStreamContext(msg.DeviceID, newCtx)

	// Send success response to confirm commit
	SendEncryptedSuccess(newCtx, MsgRenewKeyAck, nil)
	log.Printf("RelayComm: Key renewal committed for device %s", msg.DeviceID)
}

func handleGetDevices(ctx *HandlerContext, payload json.RawMessage) {
	allDevices := devices.Get().GetAll()
	SendEncryptedSuccess(ctx, MsgGetDevices, map[string]any{
		"devices": allDevices,
	})
}

func handleRemoveDevice(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		TargetDeviceID string `json:"targetDeviceId"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		SendEncryptedError(ctx, MsgRemoveDevice, ErrInvalidPayload, "Invalid payload")
		return
	}

	if err := devices.Get().Remove(req.TargetDeviceID); err != nil {
		SendEncryptedError(ctx, MsgRemoveDevice, ErrInternalError, err.Error())
		return
	}

	SendEncryptedSuccess(ctx, MsgRemoveDevice, map[string]any{
		"removedDeviceId": req.TargetDeviceID,
	})
}

func handleGetEvents(ctx *HandlerContext, payload json.RawMessage) {
	events, err := storage.Get().GetEventLog()
	if err != nil {
		SendEncryptedError(ctx, MsgGetEvents, ErrInternalError, err.Error())
		return
	}
	SendEncryptedSuccess(ctx, MsgGetEvents, map[string]any{
		"events": events,
	})
}

func handleGetRecording(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		SendEncryptedError(ctx, MsgGetRecording, ErrInvalidPayload, "Invalid payload")
		return
	}

	// Get video file
	videoPath, err := storage.Get().GetRecordingPath(req.ID)
	if err != nil {
		SendEncryptedError(ctx, MsgGetRecording, ErrInternalError, err.Error())
		return
	}

	videoData, err := os.ReadFile(videoPath)
	if err != nil {
		SendEncryptedError(ctx, MsgGetRecording, ErrInternalError, fmt.Sprintf("Failed to read video file: %v", err))
		return
	}

	response := map[string]any{
		"video": base64.StdEncoding.EncodeToString(videoData),
	}

	// Get audio file if it exists
	audioPath, err := storage.Get().GetAudioPath(req.ID)
	if err == nil {
		audioData, err := os.ReadFile(audioPath)
		if err == nil {
			response["audio"] = base64.StdEncoding.EncodeToString(audioData)
		}
	}

	SendEncryptedSuccess(ctx, MsgGetRecording, response)
}

func handleGetThumbnail(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		SendEncryptedError(ctx, MsgGetThumbnail, ErrInvalidPayload, "Invalid payload")
		return
	}

	filePath, err := storage.Get().GetThumbnailPath(req.ID)
	if err != nil {
		SendEncryptedError(ctx, MsgGetThumbnail, ErrInternalError, err.Error())
		return
	}

	fileData, err := os.ReadFile(filePath)
	if err != nil {
		SendEncryptedError(ctx, MsgGetThumbnail, ErrInternalError, fmt.Sprintf("Failed to read thumbnail: %v", err))
		return
	}

	SendEncryptedSuccess(ctx, MsgGetThumbnail, map[string]any{
		"data":    base64.StdEncoding.EncodeToString(fileData),
		"eventId": req.ID,
	})
}

func handleStartStream(ctx *HandlerContext, payload json.RawMessage) {
	StopVideoStream() // Stop existing video stream first (if any)
	StopAudioStream() // Stop existing audio stream first (if any)

	// Start fresh video stream
	stream, err := record.Get().StartVideoStream()
	if err != nil {
		SendEncryptedError(ctx, MsgStartStream, ErrInternalError, err.Error())
		return
	}

	// Start streaming video to this client
	StartVideoStreamForClient(ctx, stream, MsgStreamVideoChunk)

	// Start audio stream if microphone is enabled
	if record.MicEnabled() {
		audioStream, err := record.Get().StartAudioStream()
		if err != nil {
			log.Printf("RelayComm: Failed to start audio stream: %v", err)
		} else {
			StartAudioStreamForClient(ctx, audioStream)
		}
	}

	SendEncryptedSuccess(ctx, MsgStartStream, nil)
}

// If clients do not send this, stream will be stopped after 5s (recommended interval is 2s)
func handleContinueStream(ctx *HandlerContext, payload json.RawMessage) {
	UpdateStreamActivity()
	SendEncryptedSuccess(ctx, MsgContinueStream, nil)
}

func handleGetMicrophone(ctx *HandlerContext, payload json.RawMessage) {
	enabled := record.MicEnabled()
	SendEncryptedSuccess(ctx, MsgGetMicrophone, map[string]any{
		"enabled": enabled,
	})
}

func handleSetMicrophone(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		Enabled bool `json:"enabled"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		SendEncryptedError(ctx, MsgSetMicrophone, ErrInvalidPayload, "Invalid payload")
		return
	}

	if err := record.Get().SetMicrophoneEnabled(req.Enabled); err != nil {
		SendEncryptedError(ctx, MsgSetMicrophone, ErrInternalError, err.Error())
		return
	}

	SendEncryptedSuccess(ctx, MsgSetMicrophone, map[string]any{
		"enabled": req.Enabled,
	})
}

func handleGetRecordingSound(ctx *HandlerContext, payload json.RawMessage) {
	enabled := false
	if val, ok := config.Get().GetKey("playActiveCameraSound"); ok {
		if b, ok := val.(bool); ok {
			enabled = b
		}
	}
	SendEncryptedSuccess(ctx, MsgGetRecordingSound, map[string]any{
		"enabled": enabled,
	})
}

func handleSetRecordingSound(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		Enabled bool `json:"enabled"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		SendEncryptedError(ctx, MsgSetRecordingSound, ErrInvalidPayload, "Invalid payload")
		return
	}

	if err := config.Get().SetKey("playActiveCameraSound", req.Enabled); err != nil {
		SendEncryptedError(ctx, MsgSetRecordingSound, ErrInternalError, err.Error())
		return
	}

	SendEncryptedSuccess(ctx, MsgSetRecordingSound, map[string]any{
		"enabled": req.Enabled,
	})
}

func handleGetHealth(ctx *HandlerContext, payload json.RawMessage) {
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

	SendEncryptedSuccess(ctx, MsgGetHealth, health)
}

func handleGetPreview(ctx *HandlerContext, payload json.RawMessage) {
	frameData, err := record.Get().CapturePreview()
	if err != nil {
		SendEncryptedError(ctx, MsgGetPreview, ErrInternalError, err.Error())
		return
	}

	SendEncryptedSuccess(ctx, MsgGetPreview, map[string]any{
		"image": base64.StdEncoding.EncodeToString(frameData),
	})
}

func handleStartUpdate(ctx *HandlerContext, payload json.RawMessage) {
	if err := updater.Get().StartUpdate(); err != nil {
		SendEncryptedError(ctx, MsgStartUpdate, ErrInternalError, err.Error())
		return
	}

	SendEncryptedSuccess(ctx, MsgStartUpdate, nil)
}

func handleRestart(ctx *HandlerContext, payload json.RawMessage) {
	SendEncryptedSuccess(ctx, MsgRestart, nil)

	// Reboot the system
	go func() {
		exec.Command("sudo", "reboot").Run()
	}()
}

func handleReset(ctx *HandlerContext, payload json.RawMessage) {
	SendEncryptedSuccess(ctx, MsgReset, nil)

	go func() {
		time.Sleep(500 * time.Millisecond)

		// Find data partition device from /proc/mounts
		var partition string
		if file, err := os.Open("/proc/mounts"); err == nil {
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				fields := strings.Fields(scanner.Text())
				if len(fields) >= 2 && fields[1] == globals.DataDir {
					partition = fields[0]
					break
				}
			}
			file.Close()
		}

		if partition != "" {
			exec.Command("umount", globals.DataDir).Run()
			exec.Command("mkfs.ext4", "-F", partition).Run()
			exec.Command("sudo", "reboot").Run()
		} else {
			log.Printf("Failed to find data partition for reset")
		}
	}()
}
