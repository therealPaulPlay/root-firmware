package relaycomm

import (
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
	"root-firmware/pkg/ml"
	"root-firmware/pkg/record"
	"root-firmware/pkg/sfx"
	"root-firmware/pkg/storage"
	"root-firmware/pkg/updater"
	"root-firmware/pkg/wifi"
)

// Message type constants
const (
	MsgRenewKey                 = "renewKey"
	MsgRenewKeyAck              = "renewKeyAck"
	MsgGetDevices               = "getDevices"
	MsgRemoveDevice             = "removeDevice"
	MsgGetEvents                = "getEvents"
	MsgGetRecording             = "getRecording"
	MsgGetThumbnail             = "getThumbnail"
	MsgStartStream              = "startStream"
	MsgContinueStream           = "continueStream"
	MsgStreamVideoChunk         = "streamVideoChunk"
	MsgStreamAudioChunk         = "streamAudioChunk"
	MsgGetMicrophone            = "getMicrophone"
	MsgSetMicrophone            = "setMicrophone"
	MsgGetRecordingSound        = "getRecordingSound"
	MsgSetRecordingSound        = "setRecordingSound"
	MsgGetHealth                = "getHealth"
	MsgGetPreview               = "getPreview"
	MsgStartUpdate              = "startUpdate"
	MsgRestart                  = "restart"
	MsgReset                    = "reset"
	MsgGetEventDetectionConfig  = "getEventDetectionConfig"
	MsgSetEventDetectionEnabled = "setEventDetectionEnabled"
	MsgSetEventDetectionTypes   = "setEventDetectionTypes"
	MsgSetProductAlias          = "setProductAlias"
	MsgGetUpdateStatus          = "getUpdateStatus"
	MsgSetVersionDev            = "setVersionDev"
)

// Error code constants
const (
	ErrDeviceNotPaired  = "DEVICE_NOT_PAIRED"
	ErrProductNotInit   = "PRODUCT_NOT_INITIALIZED"
	ErrInvalidKey       = "INVALID_KEY"
	ErrDecryptionFailed = "DECRYPTION_FAILED"
	ErrInvalidPayload   = "INVALID_PAYLOAD"
	ErrInternalError    = "INTERNAL_ERROR"
	ErrStreamEnded      = "STREAM_ENDED"
)

// HandlerContext provides encryption context to handlers
type HandlerContext struct {
	DeviceID          string
	RequestID         string
	SharedSecret      []byte
	EncryptionSession *encryption.Session
}

/* Flow example:
Device → Relay Server → Product:
	{
		"type": "wifiScan",
		"target": "product",
		"productId": "product-uuid-123",    // ← Which product should handle this
		"deviceId": "device-uuid-456",     // ← Which device sent this
		"payload": "encrypted base64..." // { ssid: "...", password: "..." } (No deviceId needed - if wrong device sent it, decryption would fail)
	}

Product → Relay Server → Device:
	{
		"type": "wifiScanResult",
		"target": "device",
		"productId": "product-uuid-123",    // ← Which product sent this
		"deviceId": "device-uuid-456",     // ← Which device should receive this
		"payload": "encrypted base64..." // { success: true, networks: [...] } (No productId needed - if wrong product sent it, decryption would fail)
		"binData?": "encrypted binary..." // Optional, used for transferring footage (to avoid encoding in base64 twice)
	}
*/

// Middleware for e2e encryption
func useEncryption(messageType string, handler func(*HandlerContext, json.RawMessage)) func(Message) {
	return func(msg Message) {
		// Get device to verify it's paired
		device, ok := devices.Get().GetByID(msg.DeviceID)
		if !ok {
			sendUnencryptedError(msg.DeviceID, msg.RequestID, messageType, ErrDeviceNotPaired, "Device not paired")
			return
		}

		// Get product's private key (stored as base64 string)
		productPrivateKeyEncoded, ok := config.Get().GetKey("productPrivateKey")
		if !ok {
			sendUnencryptedError(msg.DeviceID, msg.RequestID, messageType, ErrProductNotInit, "Product private key not initialized")
			return
		}

		privKeyStr, ok := productPrivateKeyEncoded.(string)
		if !ok {
			log.Printf("RelayComm: Product private key has invalid type")
			sendUnencryptedError(msg.DeviceID, msg.RequestID, messageType, ErrInvalidKey, "Product encryption key invalid")
			return
		}

		// Decode from base64
		privKey, err := encryption.DecodeKey(privKeyStr)
		if err != nil {
			log.Printf("RelayComm: Failed to decode product private key: %v", err)
			sendUnencryptedError(msg.DeviceID, msg.RequestID, messageType, ErrInvalidKey, "Product encryption key invalid")
			return
		}

		// Derive shared secret using camera's private key and device's public key
		sharedSecret, err := encryption.DeriveSharedSecret(privKey, device.PublicKey)
		if err != nil {
			sendUnencryptedError(msg.DeviceID, msg.RequestID, messageType, ErrInvalidKey, "Failed to derive encryption key")
			return
		}

		// Create session for decryption
		session, err := encryption.FromSharedSecret(sharedSecret)
		if err != nil {
			sendUnencryptedError(msg.DeviceID, msg.RequestID, messageType, ErrInternalError, "Failed to create encryption session")
			return
		}

		// Decrypt payload (try current key first, then previous key if available)
		decrypted, err := session.Decrypt(msg.Payload)
		if err != nil {
			// Retry with previous encryption session if available (during key renewal grace period)
			if prevSession, ok := GetPreviousEncryption(msg.DeviceID); ok {
				decrypted, err = prevSession.Decrypt(msg.Payload)
				if err != nil {
					sendUnencryptedError(msg.DeviceID, msg.RequestID, messageType, ErrDecryptionFailed, "Failed to decrypt payload")
					return
				}
			} else {
				sendUnencryptedError(msg.DeviceID, msg.RequestID, messageType, ErrDecryptionFailed, "Failed to decrypt payload")
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

func errorPayload(errorCode, errorMsg string) map[string]any {
	return map[string]any{
		"success":   false,
		"errorCode": errorCode,
		"error":     errorMsg,
	}
}

// sendUnencryptedError sends an unencrypted error response to a device (only used for encryption-related errors)
func sendUnencryptedError(deviceID, requestID, messageType, errorCode, errorMsg string) {
	productID, _ := config.Get().GetKey("id")

	prodIDStr, ok := productID.(string)
	if !ok {
		log.Printf("RelayComm: Cannot send error - product ID has invalid type")
		return
	}

	payloadJSON, err := json.Marshal(errorPayload(errorCode, errorMsg))
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

// sendEncrypted sends an encrypted response to a device
// If binaryData is non-nil, it is encrypted separately and sent in the BinData field
func sendEncrypted(ctx *HandlerContext, messageType string, payload map[string]any, binaryData []byte) error {
	// Get product ID
	productID, ok := config.Get().GetKey("id")
	if !ok {
		return fmt.Errorf("product ID missing from config")
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

	// Encrypt binary data if provided
	var encryptedBinData string
	if binaryData != nil {
		encryptedBinData, err = ctx.EncryptionSession.Encrypt(binaryData)
		if err != nil {
			return err
		}
	}

	return Get().Send(Message{
		Type:      messageType + "Result",
		Target:    "device",
		ProductID: prodIDStr,
		DeviceID:  ctx.DeviceID,
		RequestID: ctx.RequestID,
		Payload:   encryptedPayload,
		BinData:   encryptedBinData,
	})
}

// SendEncryptedSuccess sends an encrypted success response with optional data fields
func SendEncryptedSuccess(ctx *HandlerContext, messageType string, fields map[string]any) error {
	payload := map[string]any{
		"success": true,
	}
	maps.Copy(payload, fields)
	return sendEncrypted(ctx, messageType, payload, nil)
}

// SendEncryptedSuccessWithBinaryData sends an encrypted success response with separately encrypted binary data
func SendEncryptedSuccessWithBinaryData(ctx *HandlerContext, messageType string, fields map[string]any, data []byte) error {
	payload := map[string]any{
		"success": true,
	}
	maps.Copy(payload, fields)
	return sendEncrypted(ctx, messageType, payload, data)
}

// SendEncryptedError sends an encrypted error response with error code
func SendEncryptedError(ctx *HandlerContext, messageType, errorCode, errorMsg string) error {
	return sendEncrypted(ctx, messageType, errorPayload(errorCode, errorMsg), nil)
}

func registerHandlers(relay *RelayComm) {
	// Key renewal
	relay.On(MsgRenewKey, handleRenewKey)
	relay.On(MsgRenewKeyAck, handleRenewKeyAck)

	// Device management
	relay.On(MsgGetDevices, useEncryption(MsgGetDevices, handleGetDevices))
	relay.On(MsgRemoveDevice, useEncryption(MsgRemoveDevice, handleRemoveDevice))
	relay.On(MsgSetProductAlias, useEncryption(MsgSetProductAlias, handleSetProductAlias))

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
	relay.On(MsgGetEventDetectionConfig, useEncryption(MsgGetEventDetectionConfig, handleGetEventDetectionConfig))
	relay.On(MsgSetEventDetectionEnabled, useEncryption(MsgSetEventDetectionEnabled, handleSetEventDetectionEnabled))
	relay.On(MsgSetEventDetectionTypes, useEncryption(MsgSetEventDetectionTypes, handleSetEventDetectionTypes))

	// System
	relay.On(MsgGetHealth, useEncryption(MsgGetHealth, handleGetHealth))
	relay.On(MsgGetPreview, useEncryption(MsgGetPreview, handleGetPreview))
	relay.On(MsgStartUpdate, useEncryption(MsgStartUpdate, handleStartUpdate))
	relay.On(MsgRestart, useEncryption(MsgRestart, handleRestart))
	relay.On(MsgReset, useEncryption(MsgReset, handleReset))
	relay.On(MsgGetUpdateStatus, useEncryption(MsgGetUpdateStatus, handleGetUpdateStatus))
	relay.On(MsgSetVersionDev, useEncryption(MsgSetVersionDev, handleSetVersionDev))
}

func handleRenewKey(msg Message) {
	// Get device to verify it's paired (allow expired keys for renewal)
	device, ok := devices.Get().GetByID(msg.DeviceID)
	if !ok {
		sendUnencryptedError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrDeviceNotPaired, "Device not paired")
		return
	}

	// Get product's private key (stored as base64 string)
	productPrivateKeyEncoded, ok := config.Get().GetKey("productPrivateKey")
	if !ok {
		sendUnencryptedError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrProductNotInit, "Camera not initialized")
		return
	}

	privKeyStr, ok := productPrivateKeyEncoded.(string)
	if !ok {
		log.Printf("RelayComm: Camera private key has invalid type")
		sendUnencryptedError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrInvalidKey, "Camera encryption key invalid")
		return
	}

	// Decode from base64
	privKey, err := encryption.DecodeKey(privKeyStr)
	if err != nil {
		log.Printf("RelayComm: Failed to decode camera private key: %v", err)
		sendUnencryptedError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrInvalidKey, "Camera encryption key invalid")
		return
	}

	// Derive shared secret using current key (still stored in device)
	sharedSecret, err := encryption.DeriveSharedSecret(privKey, device.PublicKey)
	if err != nil {
		sendUnencryptedError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrInvalidKey, "Failed to derive encryption key")
		return
	}

	// Create session for decryption
	session, err := encryption.FromSharedSecret(sharedSecret)
	if err != nil {
		sendUnencryptedError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrInternalError, "Failed to create encryption session")
		return
	}

	// Decrypt payload containing new public key
	decrypted, err := session.Decrypt(msg.Payload)
	if err != nil {
		sendUnencryptedError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrDecryptionFailed, "Failed to decrypt payload")
		return
	}

	var req struct {
		NewPublicKey string `json:"newPublicKey"`
	}
	if err := json.Unmarshal(decrypted, &req); err != nil {
		sendUnencryptedError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrInvalidPayload, "Invalid payload")
		return
	}

	// Decode new public key
	newPublicKey, err := encryption.DecodeKey(req.NewPublicKey)
	if err != nil {
		sendUnencryptedError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrInvalidKey, "Invalid public key")
		return
	}

	// Derive NEW shared secret (but don't commit yet - wait for ACK)
	newSharedSecret, err := encryption.DeriveSharedSecret(privKey, newPublicKey)
	if err != nil {
		sendUnencryptedError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrInvalidKey, "Failed to derive new encryption key")
		return
	}

	newSession, err := encryption.FromSharedSecret(newSharedSecret)
	if err != nil {
		sendUnencryptedError(msg.DeviceID, msg.RequestID, MsgRenewKey, ErrInternalError, "Failed to create new encryption session")
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
	newPublicKey, newSharedSecret, newSession, ok := GetPendingKeyRenewal(msg.DeviceID)
	if !ok {
		log.Printf("RelayComm: No pending key renewal for device %s", msg.DeviceID)
		sendUnencryptedError(msg.DeviceID, msg.RequestID, MsgRenewKeyAck, ErrInternalError, "No pending key renewal")
		return
	}

	decrypted, err := newSession.Decrypt(msg.Payload) // Decrypt ACK with NEW key
	if err != nil {
		log.Printf("RelayComm: Failed to decrypt renewal ACK from device %s: %v", msg.DeviceID, err)
		sendUnencryptedError(msg.DeviceID, msg.RequestID, MsgRenewKeyAck, ErrDecryptionFailed, "Failed to decrypt payload")
		return
	}

	var req struct {
		Ack bool `json:"ack"`
	}
	if err := json.Unmarshal(decrypted, &req); err != nil || !req.Ack {
		log.Printf("RelayComm: Invalid renewal ACK from device %s", msg.DeviceID)
		sendUnencryptedError(msg.DeviceID, msg.RequestID, MsgRenewKeyAck, ErrInvalidPayload, "Invalid payload")
		return
	}

	log.Printf("RelayComm: Committing new key for device %s", msg.DeviceID)
	if err := devices.Get().RenewKey(msg.DeviceID, newPublicKey); err != nil { // NOW commit the new key
		log.Printf("RelayComm: Failed to commit renewed key for device %s: %v", msg.DeviceID, err)
		sendUnencryptedError(msg.DeviceID, msg.RequestID, MsgRenewKeyAck, ErrInternalError, "Failed to commit key")
		return
	}

	// Update contexts for ongoing operations (streaming, file transfers)
	newCtx := &HandlerContext{
		DeviceID:          msg.DeviceID,
		RequestID:         msg.RequestID,
		SharedSecret:      newSharedSecret,
		EncryptionSession: newSession,
	}
	UpdateStreamContext(msg.DeviceID, newCtx)
	UpdateFileTransferContext(msg.DeviceID, newCtx)

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

func handleSetProductAlias(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		Alias string `json:"alias"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		SendEncryptedError(ctx, MsgSetProductAlias, ErrInvalidPayload, "Invalid payload")
		return
	}

	// Only allow devices to set their own product alias (ctx.DeviceID is the authenticated sender)
	if err := devices.Get().SetProductAlias(ctx.DeviceID, req.Alias); err != nil {
		SendEncryptedError(ctx, MsgSetProductAlias, ErrInternalError, err.Error())
		return
	}

	SendEncryptedSuccess(ctx, MsgSetProductAlias, map[string]any{
		"alias": req.Alias,
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

	// Verify paths exist
	videoPath, err := storage.Get().GetRecordingPath(req.ID)
	if err != nil {
		SendEncryptedError(ctx, MsgGetRecording, ErrInternalError, err.Error())
		return
	}

	audioPath, err := storage.Get().GetAudioPath(req.ID)
	hasAudio := err == nil

	// Send immediate success ack
	SendEncryptedSuccess(ctx, MsgGetRecording, map[string]any{
		"hasAudio": hasAudio,
		"eventId":  req.ID,
	})

	// Send video, then audio sequentially
	metadata := map[string]any{"eventId": req.ID}
	SendFileInChunks(ctx, MsgGetRecording, videoPath, "video", metadata, func() {
		if hasAudio {
			SendFileInChunks(ctx, MsgGetRecording, audioPath, "audio", metadata, nil)
		}
	})
}

func handleGetThumbnail(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		SendEncryptedError(ctx, MsgGetThumbnail, ErrInvalidPayload, "Invalid payload")
		return
	}

	// Read and encode thumbnail in goroutine to avoid blocking
	go func() {
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

		SendEncryptedSuccessWithBinaryData(ctx, MsgGetThumbnail, map[string]any{
			"eventId": req.ID,
		}, fileData)
	}()
}

func handleStartStream(ctx *HandlerContext, payload json.RawMessage) {
	// End existing streams (if any are active) and notify if a different device was streaming
	errorMsg := ""
	if currentViewer := GetVideoStreamDeviceID(); currentViewer != "" && currentViewer != ctx.DeviceID {
		errorMsg = "Another viewer started streaming"
	}
	EndVideoStream(errorMsg)
	EndAudioStream(errorMsg)

	// Start fresh video stream
	stream, err := record.Get().StartVideoStream()
	if err != nil {
		SendEncryptedError(ctx, MsgStartStream, ErrInternalError, err.Error())
		return
	}

	if val, ok := config.Get().GetKey("playRecordingSound"); ok && val.(bool) {
		sfx.Get().PlayStream()
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
	if val, ok := config.Get().GetKey("playRecordingSound"); ok {
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

	if err := config.Get().SetKey("playRecordingSound", req.Enabled); err != nil {
		SendEncryptedError(ctx, MsgSetRecordingSound, ErrInternalError, err.Error())
		return
	}

	SendEncryptedSuccess(ctx, MsgSetRecordingSound, map[string]any{
		"enabled": req.Enabled,
	})
}

func handleGetHealth(ctx *HandlerContext, payload json.RawMessage) {
	// Collect health metrics in a goroutine to avoid blocking the handler
	go func() {
		performance := map[string]any{}

		// CPU usage - use 100ms sampling to avoid long blocks
		if percentages, err := cpu.Percent(100*time.Millisecond, false); err == nil && len(percentages) > 0 {
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

		health := map[string]any{
			"battery": map[string]any{
				"percent":   0,
				"onACPower": true,
			},
			"wifi": map[string]any{
				"connected": wifi.Get().IsConnected(),
				"ssid":      wifi.Get().GetCurrentNetwork(),
			},
			"relayDomain": relayDomain,
			"logs":        logger.GetLogs(),
			"performance": performance,
		}

		SendEncryptedSuccess(ctx, MsgGetHealth, health)
	}()
}

func handleGetPreview(ctx *HandlerContext, payload json.RawMessage) {
	// Capture and encode preview in goroutine to avoid blocking
	go func() {
		frameData, err := record.Get().CapturePreview(640, 360)
		if err != nil {
			SendEncryptedError(ctx, MsgGetPreview, ErrInternalError, err.Error())
			return
		}

		SendEncryptedSuccessWithBinaryData(ctx, MsgGetPreview, map[string]any{}, frameData)
	}()
}

func handleStartUpdate(ctx *HandlerContext, payload json.RawMessage) {
	if err := updater.Get().StartUpdate(); err != nil {
		SendEncryptedError(ctx, MsgStartUpdate, ErrInternalError, err.Error())
		return
	}
	SendEncryptedSuccess(ctx, MsgStartUpdate, nil)
}

func handleGetUpdateStatus(ctx *HandlerContext, payload json.RawMessage) {
	status, availableVersion, updateError := updater.Get().GetStatus()
	result := map[string]any{
		"status":         string(status),
		"currentVersion": globals.FirmwareVersion,
	}
	if availableVersion != "" {
		result["availableVersion"] = availableVersion
	}
	if updateError != "" {
		result["error"] = updateError
	}

	SendEncryptedSuccess(ctx, MsgGetUpdateStatus, result)
}

func handleRestart(ctx *HandlerContext, payload json.RawMessage) {
	// Prevent restart while update is in progress
	status, _, _ := updater.Get().GetStatus()
	if status == updater.StatusDownloading || status == updater.StatusInstalling {
		SendEncryptedError(ctx, MsgRestart, ErrInternalError, "Cannot restart while update is in progress")
		return
	}

	SendEncryptedSuccess(ctx, MsgRestart, nil)
	go func() {
		time.Sleep(500 * time.Millisecond)
		exec.Command("reboot").Run()
	}()
}

func handleReset(ctx *HandlerContext, payload json.RawMessage) {
	// Prevent reset while update is in progress
	status, _, _ := updater.Get().GetStatus()
	if status == updater.StatusDownloading || status == updater.StatusInstalling {
		SendEncryptedError(ctx, MsgReset, ErrInternalError, "Cannot reset while update is in progress")
		return
	}

	SendEncryptedSuccess(ctx, MsgReset, nil)

	// Run as independent systemd unit so cleanup happens after firmware has stopped
	// Remove everything in /data (and explicitly the firmware dir since it starts with a '.') and remove WiFi connections
	go func() {
		time.Sleep(500 * time.Millisecond)
		exec.Command("systemd-run", "--unit=factory-reset", "--no-block", "bash", "-c",
			`systemctl stop root-firmware
			nmcli -t -f UUID,TYPE connection show 2>/dev/null | while IFS=: read -r uuid type; do
				[ "$type" = "802-11-wireless" ] && nmcli connection delete uuid "$uuid" 2>/dev/null
			done
			rm -rf `+globals.DataDir+`/* `+globals.FirmwareDataDir+`
			reboot`,
		).Run()
	}()
}

func handleGetEventDetectionConfig(ctx *HandlerContext, payload json.RawMessage) {
	SendEncryptedSuccess(ctx, MsgGetEventDetectionConfig, map[string]any{
		"enabled":      ml.IsEventDetectionEnabled(),
		"enabledTypes": ml.GetEnabledEventTypes(),
	})
}

func handleSetEventDetectionEnabled(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		Enabled bool `json:"enabled"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		SendEncryptedError(ctx, MsgSetEventDetectionEnabled, ErrInvalidPayload, "Invalid payload")
		return
	}

	if err := config.Get().SetKey("eventDetectionEnabled", req.Enabled); err != nil {
		SendEncryptedError(ctx, MsgSetEventDetectionEnabled, ErrInternalError, err.Error())
		return
	}

	SendEncryptedSuccess(ctx, MsgSetEventDetectionEnabled, map[string]any{
		"enabled": req.Enabled,
	})
}

func handleSetEventDetectionTypes(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		EnabledTypes []string `json:"enabledTypes"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		SendEncryptedError(ctx, MsgSetEventDetectionTypes, ErrInvalidPayload, "Invalid payload")
		return
	}

	// Lowercase all types for consistency
	for i := range req.EnabledTypes {
		req.EnabledTypes[i] = strings.ToLower(req.EnabledTypes[i])
	}

	if err := config.Get().SetKey("eventDetectionEnabledTypes", req.EnabledTypes); err != nil {
		SendEncryptedError(ctx, MsgSetEventDetectionTypes, ErrInternalError, err.Error())
		return
	}

	SendEncryptedSuccess(ctx, MsgSetEventDetectionTypes, map[string]any{
		"enabledTypes": req.EnabledTypes,
	})
}

func handleSetVersionDev(ctx *HandlerContext, payload json.RawMessage) {
	globals.FirmwareVersion = "dev"
	updater.Get().CheckForUpdates()
	SendEncryptedSuccess(ctx, MsgSetVersionDev, nil)
}
