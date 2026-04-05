package relaycomm

import (
	"fmt"
	"io"
	"log"
	"maps"
	"os/exec"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/shirou/gopsutil/v3/host"

	"root-firmware/pkg/config"
	"root-firmware/pkg/devices"
	"root-firmware/pkg/encryption"
	"root-firmware/pkg/globals"
	"root-firmware/pkg/logger"
	"root-firmware/pkg/metrics"
	"root-firmware/pkg/notifications"
	"root-firmware/pkg/record"
	"root-firmware/pkg/sfx"
	"root-firmware/pkg/events"
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
	MsgStartUpdate              = "startUpdate"
	MsgRestart                  = "restart"
	MsgReset                    = "reset"
	MsgGetEventDetectionConfig  = "getEventDetectionConfig"
	MsgSetEventDetectionEnabled = "setEventDetectionEnabled"
	MsgSetEventDetectionTypes   = "setEventDetectionTypes"
	MsgSetProductAlias          = "setProductAlias"
	MsgGetUpdateStatus          = "getUpdateStatus"
	MsgSetVersionDev            = "setVersionDev"
	MsgGetNotifications         = "getNotifications"
	MsgSetNotifications         = "setNotifications"
	MsgSetNotificationCooldown  = "setNotificationCooldown"
)

// Error code constants
const (
	ErrDeviceNotPaired  = "DEVICE_NOT_PAIRED"
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
	EncryptionSession *encryption.Session
}

// Middleware for e2e encryption
func useEncryption(messageType string, handler func(*HandlerContext, []byte)) func(Message) {
	return func(msg Message) {
		// Get device to verify it's paired (OriginID is the device that sent the message)
		device, ok := devices.Get().GetByID(msg.OriginID)
		if !ok {
			sendUnencryptedError(msg.OriginID, msg.RequestID, messageType, ErrDeviceNotPaired, "Device not paired")
			return
		}

		// Get product's private key
		privKey, err := config.Get().GetProductPrivateKey()
		if err != nil {
			log.Printf("RelayComm: Failed to get product private key: %v", err)
			sendUnencryptedError(msg.OriginID, msg.RequestID, messageType, ErrInvalidKey, "Product private key invalid")
			return
		}

		// Derive shared secret using camera's private key and device's public key
		sharedSecret, err := encryption.DeriveSharedSecret(privKey, device.PublicKey)
		if err != nil {
			sendUnencryptedError(msg.OriginID, msg.RequestID, messageType, ErrInvalidKey, "Failed to derive shared secret")
			return
		}

		// Create session for decryption
		session, err := encryption.SessionFromKey(sharedSecret)
		if err != nil {
			sendUnencryptedError(msg.OriginID, msg.RequestID, messageType, ErrInternalError, "Failed to create encryption session")
			return
		}

		// Compute AAD for decryption
		aad := encryption.ComputeAAD(msg.Type, msg.OriginID, msg.TargetID)

		// Decrypt payload (try current key first, then previous key if available)
		decrypted, err := session.Decrypt(msg.Payload, aad)
		if err != nil {
			// Retry with previous encryption session if available (during key renewal grace period)
			if prevSession, ok := GetPreviousEncryption(msg.OriginID); ok {
				decrypted, err = prevSession.Decrypt(msg.Payload, aad)
				if err != nil {
					sendUnencryptedError(msg.OriginID, msg.RequestID, messageType, ErrDecryptionFailed, "Failed to decrypt payload")
					return
				}
			} else {
				sendUnencryptedError(msg.OriginID, msg.RequestID, messageType, ErrDecryptionFailed, "Failed to decrypt payload")
				return
			}
		}

		// Create handler context with encryption info
		ctx := &HandlerContext{
			DeviceID:          msg.OriginID,
			RequestID:         msg.RequestID,
			EncryptionSession: session,
		}

		// Call the actual handler with context and decrypted payload
		handler(ctx, decrypted)
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

	payloadCBOR, err := cbor.Marshal(errorPayload(errorCode, errorMsg))
	if err != nil {
		log.Printf("RelayComm: Failed to marshal error payload: %v", err)
		return
	}

	Get().Send(Message{
		Type:      messageType + "Result",
		OriginID:  prodIDStr,
		TargetID:  deviceID,
		RequestID: requestID,
		Payload:   payloadCBOR, // Unencrypted CBOR for errors
	})
}

// sendEncrypted sends an encrypted response to a device
func sendEncrypted(ctx *HandlerContext, messageType string, payload map[string]any) error {
	// Get product ID
	productID, ok := config.Get().GetKey("id")
	if !ok {
		return fmt.Errorf("product ID missing from config")
	}

	prodIDStr, ok := productID.(string)
	if !ok {
		return fmt.Errorf("product ID has invalid type")
	}

	// Compute AAD for encryption (origin is product, target is device)
	aad := encryption.ComputeAAD(messageType+"Result", prodIDStr, ctx.DeviceID)

	// Marshal payload to CBOR
	payloadCBOR, err := cbor.Marshal(payload)
	if err != nil {
		return err
	}

	// Encrypt the payload
	encryptedPayload, err := ctx.EncryptionSession.Encrypt(payloadCBOR, aad)
	if err != nil {
		return err
	}

	return Get().Send(Message{
		Type:      messageType + "Result",
		OriginID:  prodIDStr,
		TargetID:  ctx.DeviceID,
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

// SendEncryptedError sends an encrypted error response with error code and optional extra fields
func SendEncryptedError(ctx *HandlerContext, messageType, errorCode, errorMsg string, fields ...map[string]any) error {
	payload := errorPayload(errorCode, errorMsg)
	if len(fields) > 0 {
		maps.Copy(payload, fields[0])
	}
	return sendEncrypted(ctx, messageType, payload)
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

	// Notifications
	relay.On(MsgGetNotifications, useEncryption(MsgGetNotifications, handleGetNotifications))
	relay.On(MsgSetNotifications, useEncryption(MsgSetNotifications, handleSetNotifications))
	relay.On(MsgSetNotificationCooldown, useEncryption(MsgSetNotificationCooldown, handleSetNotificationCooldown))

	// System
	relay.On(MsgGetHealth, useEncryption(MsgGetHealth, handleGetHealth))
	relay.On(MsgStartUpdate, useEncryption(MsgStartUpdate, handleStartUpdate))
	relay.On(MsgRestart, useEncryption(MsgRestart, handleRestart))
	relay.On(MsgReset, useEncryption(MsgReset, handleReset))
	relay.On(MsgGetUpdateStatus, useEncryption(MsgGetUpdateStatus, handleGetUpdateStatus))
	relay.On(MsgSetVersionDev, useEncryption(MsgSetVersionDev, handleSetVersionDev))
}

func handleRenewKey(msg Message) {
	// Get device to verify it's paired (allow expired keys for renewal)
	device, ok := devices.Get().GetByID(msg.OriginID)
	if !ok {
		sendUnencryptedError(msg.OriginID, msg.RequestID, MsgRenewKey, ErrDeviceNotPaired, "Device not paired")
		return
	}

	// Get product's private key
	privKey, err := config.Get().GetProductPrivateKey()
	if err != nil {
		log.Printf("RelayComm: Failed to get product private key: %v", err)
		sendUnencryptedError(msg.OriginID, msg.RequestID, MsgRenewKey, ErrInvalidKey, "Product private key invalid")
		return
	}

	// Derive shared secret using current key (still stored in device)
	sharedSecret, err := encryption.DeriveSharedSecret(privKey, device.PublicKey)
	if err != nil {
		sendUnencryptedError(msg.OriginID, msg.RequestID, MsgRenewKey, ErrInvalidKey, "Failed to derive shared secret")
		return
	}

	// Create session for decryption
	session, err := encryption.SessionFromKey(sharedSecret)
	if err != nil {
		sendUnencryptedError(msg.OriginID, msg.RequestID, MsgRenewKey, ErrInternalError, "Failed to create encryption session")
		return
	}

	// Compute AAD and decrypt payload containing new public key
	aad := encryption.ComputeAAD(msg.Type, msg.OriginID, msg.TargetID)
	decrypted, err := session.Decrypt(msg.Payload, aad)
	if err != nil {
		sendUnencryptedError(msg.OriginID, msg.RequestID, MsgRenewKey, ErrDecryptionFailed, "Failed to decrypt payload")
		return
	}

	var req struct {
		NewPublicKey []byte `cbor:"newPublicKey"`
	}
	if err := cbor.Unmarshal(decrypted, &req); err != nil {
		sendUnencryptedError(msg.OriginID, msg.RequestID, MsgRenewKey, ErrInvalidPayload, "Invalid payload")
		return
	}

	// Derive NEW shared secret (but don't commit yet - wait for ACK)
	newSharedSecret, err := encryption.DeriveSharedSecret(privKey, req.NewPublicKey)
	if err != nil {
		sendUnencryptedError(msg.OriginID, msg.RequestID, MsgRenewKey, ErrInvalidKey, "Failed to derive new encryption key")
		return
	}

	newSession, err := encryption.SessionFromKey(newSharedSecret)
	if err != nil {
		sendUnencryptedError(msg.OriginID, msg.RequestID, MsgRenewKey, ErrInternalError, "Failed to create new encryption session")
		return
	}

	// Create context with current session for response (client hasn't switched yet)
	currentCtx := &HandlerContext{
		DeviceID:          msg.OriginID,
		RequestID:         msg.RequestID,
		EncryptionSession: session,
	}

	StorePreviousEncryption(msg.OriginID, session)                  // Store current encryption session for grace period
	BufferPendingKeyRenewal(msg.OriginID, req.NewPublicKey, newSession) // Buffer new key (don't commit yet)

	log.Printf("RelayComm: Key renewal prepared for device %s, waiting for ACK", msg.OriginID)
	SendEncryptedSuccess(currentCtx, MsgRenewKey, nil) // Send with current (to-be-replaced) encryption
}

func handleRenewKeyAck(msg Message) {
	newPublicKey, newSession, ok := GetPendingKeyRenewal(msg.OriginID)
	if !ok {
		log.Printf("RelayComm: No pending key renewal for device %s", msg.OriginID)
		sendUnencryptedError(msg.OriginID, msg.RequestID, MsgRenewKeyAck, ErrInternalError, "No pending key renewal")
		return
	}

	// Compute AAD and decrypt ACK with NEW key
	aad := encryption.ComputeAAD(msg.Type, msg.OriginID, msg.TargetID)
	decrypted, err := newSession.Decrypt(msg.Payload, aad)
	if err != nil {
		log.Printf("RelayComm: Failed to decrypt renewal ACK from device %s: %v", msg.OriginID, err)
		sendUnencryptedError(msg.OriginID, msg.RequestID, MsgRenewKeyAck, ErrDecryptionFailed, "Failed to decrypt payload")
		return
	}

	var req struct {
		Ack bool `cbor:"ack"`
	}
	if err := cbor.Unmarshal(decrypted, &req); err != nil || !req.Ack {
		log.Printf("RelayComm: Invalid renewal ACK from device %s", msg.OriginID)
		sendUnencryptedError(msg.OriginID, msg.RequestID, MsgRenewKeyAck, ErrInvalidPayload, "Invalid payload")
		return
	}

	if err := devices.Get().RenewKey(msg.OriginID, newPublicKey); err != nil { // NOW commit the new key
		log.Printf("RelayComm: Failed to commit renewed key for device %s: %v", msg.OriginID, err)
		sendUnencryptedError(msg.OriginID, msg.RequestID, MsgRenewKeyAck, ErrInternalError, "Failed to commit key")
		return
	}

	// Send success response BEFORE updating stream contexts, so the frontend
	// switches to the new key before receiving any chunks encrypted with it
	newCtx := &HandlerContext{
		DeviceID:          msg.OriginID,
		RequestID:         msg.RequestID,
		EncryptionSession: newSession,
	}
	SendEncryptedSuccess(newCtx, MsgRenewKeyAck, nil)

	// Now update contexts for ongoing operations (streaming, file transfers)
	UpdateStreamContext(msg.OriginID, newCtx)
	UpdateFileTransferContext(msg.OriginID, newCtx)
	log.Printf("RelayComm: Key renewal committed for device %s", msg.OriginID)
}

func handleGetDevices(ctx *HandlerContext, payload []byte) {
	allDevices := devices.Get().GetAll()
	SendEncryptedSuccess(ctx, MsgGetDevices, map[string]any{
		"devices": allDevices,
	})
}

func handleRemoveDevice(ctx *HandlerContext, payload []byte) {
	var req struct {
		TargetDeviceID string `cbor:"targetDeviceId"`
	}

	if err := cbor.Unmarshal(payload, &req); err != nil {
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

func handleSetProductAlias(ctx *HandlerContext, payload []byte) {
	var req struct {
		Alias string `cbor:"alias"`
	}

	if err := cbor.Unmarshal(payload, &req); err != nil {
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

func handleGetEvents(ctx *HandlerContext, payload []byte) {
	var req struct {
		Limit        int      `cbor:"limit"`
		Cursor       int      `cbor:"cursor"`
		StartTime    int64    `cbor:"startTime"`
		EndTime      int64    `cbor:"endTime"`
		EventTypes   []string `cbor:"eventTypes"`
		UntilEventID string   `cbor:"untilEventId"`
	}

	if len(payload) > 0 {
		if err := cbor.Unmarshal(payload, &req); err != nil {
			SendEncryptedError(ctx, MsgGetEvents, ErrInvalidPayload, "Invalid payload")
			return
		}
	}

	if req.Limit <= 0 {
		req.Limit = 200
	}

	eventList, nextCursor, total, err := events.Get().GetEventLogPaginated(
		req.Limit, req.Cursor, req.StartTime, req.EndTime, req.EventTypes, req.UntilEventID,
	)
	if err != nil {
		SendEncryptedError(ctx, MsgGetEvents, ErrInternalError, err.Error())
		return
	}

	SendEncryptedSuccess(ctx, MsgGetEvents, map[string]any{
		"events":     eventList,
		"nextCursor": nextCursor,
		"total":      total,
	})
}

func handleGetRecording(ctx *HandlerContext, payload []byte) {
	var req struct {
		ID string `cbor:"id"`
	}

	if err := cbor.Unmarshal(payload, &req); err != nil {
		SendEncryptedError(ctx, MsgGetRecording, ErrInvalidPayload, "Invalid payload")
		return
	}

	// Verify paths exist
	videoPath, err := events.Get().GetRecordingPath(req.ID)
	if err != nil {
		SendEncryptedError(ctx, MsgGetRecording, ErrInternalError, err.Error())
		return
	}

	audioPath, err := events.Get().GetAudioPath(req.ID)
	hasAudio := err == nil

	// Send immediate success ack
	SendEncryptedSuccess(ctx, MsgGetRecording, map[string]any{
		"hasAudio": hasAudio,
		"eventId":  req.ID,
	})

	// Send audio first (smaller), then video
	metadata := map[string]any{"eventId": req.ID}
	if hasAudio {
		SendFileInChunksAsync(ctx, MsgGetRecording, audioPath, "audio", metadata, func() {
			SendFileInChunksAsync(ctx, MsgGetRecording, videoPath, "video", metadata, nil)
		})
	} else {
		SendFileInChunksAsync(ctx, MsgGetRecording, videoPath, "video", metadata, nil)
	}
}

func handleGetThumbnail(ctx *HandlerContext, payload []byte) {
	var req struct {
		ID string `cbor:"id"`
	}

	if err := cbor.Unmarshal(payload, &req); err != nil {
		SendEncryptedError(ctx, MsgGetThumbnail, ErrInvalidPayload, "Invalid payload")
		return
	}

	eventIdField := map[string]any{"eventId": req.ID}

	filePath, err := events.Get().GetThumbnailPath(req.ID)
	if err != nil {
		SendEncryptedError(ctx, MsgGetThumbnail, ErrInternalError, err.Error(), eventIdField)
		return
	}

	productPrivateKey, err := config.Get().GetProductPrivateKey()
	if err != nil {
		SendEncryptedError(ctx, MsgGetThumbnail, ErrInternalError, fmt.Sprintf("Failed to get decryption key: %v", err), eventIdField)
		return
	}

	reader, _, err := decryptFileToReader(filePath, productPrivateKey)
	if err != nil {
		SendEncryptedError(ctx, MsgGetThumbnail, ErrInternalError, fmt.Sprintf("Failed to decrypt thumbnail: %v", err), eventIdField)
		return
	}

	fileData, err := io.ReadAll(reader)
	if err != nil {
		SendEncryptedError(ctx, MsgGetThumbnail, ErrInternalError, fmt.Sprintf("Failed to read thumbnail: %v", err), eventIdField)
		return
	}

	SendEncryptedSuccess(ctx, MsgGetThumbnail, map[string]any{
		"eventId": req.ID,
		"data":    fileData,
	})
}

func handleStartStream(ctx *HandlerContext, payload []byte) {
	if val, ok := config.Get().GetKey("playRecordingSound"); ok {
		if b, ok := val.(bool); ok && b {
			sfx.Get().PlayStream()
		}
	}

	// Acknowledge stream start before starting goroutines
	// since they might synchronously return the init frame
	SendEncryptedSuccess(ctx, MsgStartStream, nil)

	// Start streaming video to this client
	StartVideoStreamForClient(ctx, MsgStreamVideoChunk)

	// Start audio stream if microphone is enabled
	if record.MicEnabled() {
		audioStream, err := record.Get().StartAudioStream()
		if err != nil {
			log.Printf("RelayComm: Failed to start audio stream: %v", err)
		} else {
			StartAudioStreamForClient(ctx, audioStream)
		}
	}
}

// If clients do not send this, stream will be stopped after 5s (recommended interval is 2s)
func handleContinueStream(ctx *HandlerContext, payload []byte) {
	UpdateStreamActivity(ctx.DeviceID)
	SendEncryptedSuccess(ctx, MsgContinueStream, nil)
}

func handleGetMicrophone(ctx *HandlerContext, payload []byte) {
	enabled := record.MicEnabled()
	SendEncryptedSuccess(ctx, MsgGetMicrophone, map[string]any{
		"enabled": enabled,
	})
}

func handleSetMicrophone(ctx *HandlerContext, payload []byte) {
	var req struct {
		Enabled bool `cbor:"enabled"`
	}

	if err := cbor.Unmarshal(payload, &req); err != nil {
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

func handleGetRecordingSound(ctx *HandlerContext, payload []byte) {
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

func handleSetRecordingSound(ctx *HandlerContext, payload []byte) {
	var req struct {
		Enabled bool `cbor:"enabled"`
	}

	if err := cbor.Unmarshal(payload, &req); err != nil {
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

func handleGetHealth(ctx *HandlerContext, payload []byte) {
	relayDomain := ""
	if domain, ok := config.Get().GetKey("relayDomain"); ok {
		if domainStr, ok := domain.(string); ok {
			relayDomain = domainStr
		}
	}

	uptimeSeconds := uint64(0)
	if uptime, err := host.Uptime(); err == nil {
		uptimeSeconds = uptime
	}

	health := map[string]any{
		"wifi": map[string]any{
			"connectedSSID": wifi.Get().GetCurrentNetwork(),
		},
		"relayDomain":   relayDomain,
		"logs":          logger.GetLogs(),
		"uptimeSeconds": uptimeSeconds,
		"metrics":       metrics.GetPoints(),
	}

	SendEncryptedSuccess(ctx, MsgGetHealth, health)
}

func handleStartUpdate(ctx *HandlerContext, payload []byte) {
	if !updater.Get().StartUpdate() {
		SendEncryptedError(ctx, MsgStartUpdate, ErrInternalError, "No update available or already in progress")
		return
	}
	SendEncryptedSuccess(ctx, MsgStartUpdate, nil)
}

func handleGetUpdateStatus(ctx *HandlerContext, payload []byte) {
	status, availableVersion, updateError, scheduledFor := updater.Get().GetStatus()
	result := map[string]any{
		"status":         string(status),
		"currentVersion": globals.FirmwareVersion,
		"currentSlot":    updater.GetCurrentSlot(),
	}
	if availableVersion != "" {
		result["availableVersion"] = availableVersion
	}
	if updateError != "" {
		result["error"] = updateError
	}
	if !scheduledFor.IsZero() {
		result["scheduledFor"] = scheduledFor.UnixMilli()
	}

	SendEncryptedSuccess(ctx, MsgGetUpdateStatus, result)
}

func handleRestart(ctx *HandlerContext, payload []byte) {
	// Prevent restart while update is in progress
	status, _, _, _ := updater.Get().GetStatus()
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

func handleReset(ctx *HandlerContext, payload []byte) {
	// Prevent reset while update is in progress
	status, _, _, _ := updater.Get().GetStatus()
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

func handleGetEventDetectionConfig(ctx *HandlerContext, payload []byte) {
	SendEncryptedSuccess(ctx, MsgGetEventDetectionConfig, map[string]any{
		"enabled":             events.IsEventDetectionEnabled(),
		"enabledTypes":        events.GetEnabledEventTypes(),
		"availableEventTypes": events.AvailableEventTypes,
	})
}

func handleSetEventDetectionEnabled(ctx *HandlerContext, payload []byte) {
	var req struct {
		Enabled bool `cbor:"enabled"`
	}

	if err := cbor.Unmarshal(payload, &req); err != nil {
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

func handleSetEventDetectionTypes(ctx *HandlerContext, payload []byte) {
	var req struct {
		EnabledTypes []string `cbor:"enabledTypes"`
	}

	if err := cbor.Unmarshal(payload, &req); err != nil {
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

func handleSetVersionDev(ctx *HandlerContext, payload []byte) {
	globals.FirmwareVersion = "dev"

	// Remove scheduled update, since when version is dev auto updates are disabled
	u := updater.Get()
	u.RemoveScheduledUpdateWithLock()
	u.CheckForUpdates()

	SendEncryptedSuccess(ctx, MsgSetVersionDev, nil)
}

func handleGetNotifications(ctx *HandlerContext, payload []byte) {
	n := notifications.Get()
	SendEncryptedSuccess(ctx, MsgGetNotifications, map[string]any{
		"enabled":         n.IsEnabled(ctx.DeviceID),
		"cooldownMinutes": n.GetCooldownMinutes(),
	})
}

func handleSetNotifications(ctx *HandlerContext, payload []byte) {
	var req struct {
		Enabled  bool   `cbor:"enabled"`
		FCMToken string `cbor:"fcmToken"`
	}

	if err := cbor.Unmarshal(payload, &req); err != nil {
		SendEncryptedError(ctx, MsgSetNotifications, ErrInvalidPayload, "Invalid payload")
		return
	}

	if req.Enabled {
		if req.FCMToken == "" {
			SendEncryptedError(ctx, MsgSetNotifications, ErrInvalidPayload, "FCM token required")
			return
		}

		if err := notifications.Get().Enable(ctx.DeviceID, req.FCMToken); err != nil {
			SendEncryptedError(ctx, MsgSetNotifications, ErrInternalError, err.Error())
			return
		}

		SendEncryptedSuccess(ctx, MsgSetNotifications, map[string]any{
			"enabled": true,
		})
	} else {
		if err := notifications.Get().Disable(ctx.DeviceID); err != nil {
			SendEncryptedError(ctx, MsgSetNotifications, ErrInternalError, err.Error())
			return
		}

		SendEncryptedSuccess(ctx, MsgSetNotifications, map[string]any{
			"enabled": false,
		})
	}
}

func handleSetNotificationCooldown(ctx *HandlerContext, payload []byte) {
	var req struct {
		CooldownMinutes int `cbor:"cooldownMinutes"`
	}

	if err := cbor.Unmarshal(payload, &req); err != nil {
		SendEncryptedError(ctx, MsgSetNotificationCooldown, ErrInvalidPayload, "Invalid payload")
		return
	}

	if err := notifications.Get().SetCooldownMinutes(req.CooldownMinutes); err != nil {
		SendEncryptedError(ctx, MsgSetNotificationCooldown, ErrInvalidPayload, err.Error())
		return
	}

	SendEncryptedSuccess(ctx, MsgSetNotificationCooldown, map[string]any{
		"cooldownMinutes": req.CooldownMinutes,
	})
}
