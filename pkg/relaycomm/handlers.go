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
	"root-firmware/pkg/ups"
	"root-firmware/pkg/wifi"
)

// Message type constants
const (
	MsgRenewKey          = "renewKey"
	MsgGetDevices        = "getDevices"
	MsgRemoveDevice      = "removeDevice"
	MsgKickDevice        = "kickDevice"
	MsgGetEvents         = "getEvents"
	MsgGetRecording      = "getRecording"
	MsgGetThumbnail      = "getThumbnail"
	MsgStartStream       = "startStream"
	MsgStopStream        = "stopStream"
	MsgStreamVideoChunk  = "streamVideoChunk"
	MsgStreamAudioChunk  = "streamAudioChunk"
	MsgSetMicrophone     = "setMicrophone"
	MsgSetRecordingSound = "setRecordingSound"
	MsgGetHealth         = "getHealth"
	MsgGetPreview        = "getPreview"
	MsgStartUpdate       = "startUpdate"
	MsgRestart           = "restart"
	MsgReset             = "reset"
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
		"encryptedPayload": "base64..." // { ssid: "...", password: "..." } (No deviceId needed - if wrong device sent it, decryption would fail)
	}

Camera → Relay Server → Device:
	{
		"type": "wifiScanResult",
		"target": "device",
		"productId": "camera-uuid-123",    // ← Which camera sent this
		"deviceId": "device-uuid-456",     // ← Which device should receive this
		"encryptedPayload": "base64..." // { success: true, networks: [...] } (No productId needed - if wrong camera sent it, decryption would fail)
	}
*/

// Middleware for e2e encryption
func useEncryption(messageType string, handler func(*HandlerContext, json.RawMessage)) func(Message) {
	return func(msg Message) {
		// Get device to verify it's paired
		device, ok := devices.Get().GetByID(msg.DeviceID)
		if !ok {
			sendError(msg.DeviceID, messageType, "Device not paired!")
			return
		}

		// Check if key is expired (forward secrecy enforcement)
		if time.Now().After(device.KeyExpiresAt) {
			sendError(msg.DeviceID, messageType, "Session key expired!")
			return
		}

		// Get camera's private key (single key for all devices)
		cameraPrivateKey, ok := config.Get().GetKey("cameraPrivateKey")
		if !ok {
			sendError(msg.DeviceID, messageType, "Camera not initialized!")
			return
		}

		privKey, ok := cameraPrivateKey.([]byte)
		if !ok {
			log.Printf("RelayComm: Camera private key has invalid type")
			sendError(msg.DeviceID, messageType, "Camera encryption key invalid!")
			return
		}

		// Derive shared secret using camera's private key and device's public key
		sharedSecret, err := encryption.DeriveSharedSecret(privKey, device.PublicKey)
		if err != nil {
			sendError(msg.DeviceID, messageType, "Failed to derive encryption key!")
			return
		}

		// Create session for decryption
		session, err := encryption.FromSharedSecret(sharedSecret)
		if err != nil {
			sendError(msg.DeviceID, messageType, "Failed to create encryption session!")
			return
		}

		// Decrypt payload
		decrypted, err := session.Decrypt(msg.EncryptedPayload)
		if err != nil {
			sendError(msg.DeviceID, messageType, "Failed to decrypt payload!")
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

	prodIDStr, ok := productID.(string)
	if !ok {
		log.Printf("RelayComm: Cannot send error - product ID has invalid type")
		return
	}

	// Create error payload
	errorPayload := map[string]any{
		"productId": prodIDStr,
		"success":   false,
		"error":     errorMsg,
	}
	payloadJSON, err := json.Marshal(errorPayload)
	if err != nil {
		log.Printf("RelayComm: Failed to marshal error payload: %v", err)
		return
	}

	Get().Send(Message{
		Type:             messageType + "Result",
		Target:           "device",
		ProductID:        prodIDStr,
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

	prodIDStr, ok := productID.(string)
	if !ok {
		return fmt.Errorf("product ID has invalid type")
	}

	// Payload must be a map for valid JSON
	payloadMap, ok := payload.(map[string]any)
	if !ok {
		return fmt.Errorf("payload must be a map[string]any")
	}

	// Marshal payload to JSON
	payloadJSON, err := json.Marshal(payloadMap)
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
		Type:             messageType + "Result",
		Target:           "device",
		ProductID:        prodIDStr,
		DeviceID:         ctx.DeviceID,
		EncryptedPayload: encryptedPayload,
	})
}

// RegisterHandlers registers all relay message handlers and starts connection if relay domain is configured
func RegisterHandlers() {
	// Check if relay domain is configured
	relayDomain, ok := config.Get().GetKey("relayDomain")
	if !ok || relayDomain == "" {
		log.Println("Relaycomm: Relay handlers not registered - relay domain not configured")
		return
	}

	relay := Get()

	// Key renewal (no encryption middleware - uses old key to decrypt renewal request)
	relay.On(MsgRenewKey, handleRenewKey)

	// Device management
	relay.On(MsgGetDevices, useEncryption(MsgGetDevices, handleGetDevices))
	relay.On(MsgRemoveDevice, useEncryption(MsgRemoveDevice, handleRemoveDevice))
	relay.On(MsgKickDevice, useEncryption(MsgKickDevice, handleKickDevice))

	// Storage
	relay.On(MsgGetEvents, useEncryption(MsgGetEvents, handleGetEvents))
	relay.On(MsgGetRecording, useEncryption(MsgGetRecording, handleGetRecording))
	relay.On(MsgGetThumbnail, useEncryption(MsgGetThumbnail, handleGetThumbnail))

	// Streaming
	relay.On(MsgStartStream, useEncryption(MsgStartStream, handleStartStream))
	relay.On(MsgStopStream, useEncryption(MsgStopStream, handleStopStream))

	// Settings
	relay.On(MsgSetMicrophone, useEncryption(MsgSetMicrophone, handleSetMicrophone))
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
		sendError(msg.DeviceID, MsgRenewKey, "Device not paired!")
		return
	}

	// Get camera's private key
	cameraPrivateKey, ok := config.Get().GetKey("cameraPrivateKey")
	if !ok {
		sendError(msg.DeviceID, MsgRenewKey, "Camera not initialized!")
		return
	}

	privKey, ok := cameraPrivateKey.([]byte)
	if !ok {
		log.Printf("RelayComm: Camera private key has invalid type")
		sendError(msg.DeviceID, MsgRenewKey, "Camera encryption key invalid!")
		return
	}

	// Derive shared secret using OLD key (still stored in device)
	sharedSecret, err := encryption.DeriveSharedSecret(privKey, device.PublicKey)
	if err != nil {
		sendError(msg.DeviceID, MsgRenewKey, "Failed to derive encryption key!")
		return
	}

	// Create session for decryption
	session, err := encryption.FromSharedSecret(sharedSecret)
	if err != nil {
		sendError(msg.DeviceID, MsgRenewKey, "Failed to create encryption session!")
		return
	}

	// Decrypt payload containing new public key
	decrypted, err := session.Decrypt(msg.EncryptedPayload)
	if err != nil {
		sendError(msg.DeviceID, MsgRenewKey, "Failed to decrypt payload!")
		return
	}

	var req struct {
		NewPublicKey string `json:"newPublicKey"`
	}
	if err := json.Unmarshal(decrypted, &req); err != nil {
		sendError(msg.DeviceID, MsgRenewKey, "Invalid payload!")
		return
	}

	// Decode new public key
	newPublicKey, err := encryption.DecodePublicKey(req.NewPublicKey)
	if err != nil {
		sendError(msg.DeviceID, MsgRenewKey, "Invalid public key!")
		return
	}

	// Update device with new key
	if err := devices.Get().RenewKey(msg.DeviceID, newPublicKey); err != nil {
		sendError(msg.DeviceID, MsgRenewKey, "Failed to update key!")
		return
	}

	// Derive NEW shared secret for response
	newSharedSecret, err := encryption.DeriveSharedSecret(privKey, newPublicKey)
	if err != nil {
		sendError(msg.DeviceID, MsgRenewKey, "Failed to derive new encryption key!")
		return
	}

	newSession, err := encryption.FromSharedSecret(newSharedSecret)
	if err != nil {
		sendError(msg.DeviceID, MsgRenewKey, "Failed to create new encryption session!")
		return
	}

	// Create context with NEW session for response
	ctx := &HandlerContext{
		DeviceID:          msg.DeviceID,
		SharedSecret:      newSharedSecret,
		EncryptionSession: newSession,
	}

	log.Printf("Relaycomm: Key renewed for device %s", msg.DeviceID)
	SendEncrypted(ctx, MsgRenewKey, map[string]any{
		"success": true,
	})
}

func handleGetDevices(ctx *HandlerContext, payload json.RawMessage) {
	allDevices := devices.Get().GetAll()
	SendEncrypted(ctx, MsgGetDevices, map[string]any{
		"success": true,
		"devices": allDevices,
	})
}

func handleRemoveDevice(ctx *HandlerContext, payload json.RawMessage) {
	// Device can only remove itself
	err := devices.Get().Remove(ctx.DeviceID)
	SendEncrypted(ctx, MsgRemoveDevice, buildResult(err, nil))
}

func handleKickDevice(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		TargetDeviceID string `json:"targetDeviceId"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		SendEncrypted(ctx, MsgKickDevice, buildResult(fmt.Errorf("invalid payload"), nil))
		return
	}

	// Device cannot kick itself
	if req.TargetDeviceID == ctx.DeviceID {
		SendEncrypted(ctx, MsgKickDevice, buildResult(fmt.Errorf("cannot kick self"), nil))
		return
	}

	err := devices.Get().ScheduleKick(req.TargetDeviceID)
	SendEncrypted(ctx, MsgKickDevice, buildResult(err, nil))
}

func handleGetEvents(ctx *HandlerContext, payload json.RawMessage) {
	events, err := storage.Get().GetEventLog()
	SendEncrypted(ctx, MsgGetEvents, buildResult(err, map[string]any{
		"events": events,
	}))
}

func handleGetRecording(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		SendEncrypted(ctx, MsgGetRecording, buildResult(fmt.Errorf("invalid payload"), nil))
		return
	}

	filePath, err := storage.Get().GetRecordingPath(req.ID)
	if err != nil {
		SendEncrypted(ctx, MsgGetRecording, buildResult(err, nil))
		return
	}

	fileData, err := os.ReadFile(filePath)
	if err != nil {
		SendEncrypted(ctx, MsgGetRecording, buildResult(fmt.Errorf("failed to read file: %w", err), nil))
		return
	}

	SendEncrypted(ctx, MsgGetRecording, buildResult(nil, map[string]any{
		"data": base64.StdEncoding.EncodeToString(fileData),
	}))
}

func handleGetThumbnail(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		SendEncrypted(ctx, MsgGetThumbnail, buildResult(fmt.Errorf("invalid payload"), nil))
		return
	}

	filePath, err := storage.Get().GetThumbnailPath(req.ID)
	if err != nil {
		SendEncrypted(ctx, MsgGetThumbnail, buildResult(err, nil))
		return
	}

	fileData, err := os.ReadFile(filePath)
	if err != nil {
		SendEncrypted(ctx, MsgGetThumbnail, buildResult(fmt.Errorf("failed to read thumbnail: %w", err), nil))
		return
	}

	SendEncrypted(ctx, MsgGetThumbnail, buildResult(nil, map[string]any{
		"data": base64.StdEncoding.EncodeToString(fileData),
	}))
}

func handleStartStream(ctx *HandlerContext, payload json.RawMessage) {
	// Add viewer (enforces limit)
	if err := addViewer(ctx); err != nil {
		SendEncrypted(ctx, MsgStartStream, buildResult(err, nil))
		return
	}

	// Start stream if not already running
	stream, err := record.Get().StartStream()
	if err != nil && err.Error() != "already streaming" {
		removeViewer(ctx.DeviceID)
		SendEncrypted(ctx, MsgStartStream, buildResult(err, nil))
		return
	}

	// If this is the first viewer, start streaming goroutines
	if err == nil {
		go func() {
			if err := StreamReader(stream.Video, MsgStreamVideoChunk); err != nil {
				broadcastChunk(MsgStreamVideoChunk, map[string]any{
					"success": false,
					"error":   err.Error(),
					"done":    true,
				})
			}
		}()

		if stream.Audio != nil {
			go func() {
				if err := StreamReader(stream.Audio, MsgStreamAudioChunk); err != nil {
					broadcastChunk(MsgStreamAudioChunk, map[string]any{
						"success": false,
						"error":   err.Error(),
						"done":    true,
					})
				}
			}()
		}
	}

	SendEncrypted(ctx, MsgStartStream, buildResult(nil, nil))
}

func handleStopStream(ctx *HandlerContext, payload json.RawMessage) {
	var err error
	if removeViewer(ctx.DeviceID) {
		err = record.Get().StopStream()
	}
	SendEncrypted(ctx, MsgStopStream, buildResult(err, nil))
}

func handleSetMicrophone(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		Enabled bool `json:"enabled"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		SendEncrypted(ctx, MsgSetMicrophone, buildResult(fmt.Errorf("invalid payload"), nil))
		return
	}

	err := record.Get().SetMicrophoneEnabled(req.Enabled)
	SendEncrypted(ctx, MsgSetMicrophone, buildResult(err, map[string]any{
		"enabled": req.Enabled,
	}))
}

func handleSetRecordingSound(ctx *HandlerContext, payload json.RawMessage) {
	var req struct {
		Enabled bool `json:"enabled"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		SendEncrypted(ctx, MsgSetRecordingSound, buildResult(fmt.Errorf("invalid payload"), nil))
		return
	}

	err := config.Get().SetKey("playActiveCameraSound", req.Enabled)
	SendEncrypted(ctx, MsgSetRecordingSound, buildResult(err, map[string]any{
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

	SendEncrypted(ctx, MsgGetHealth, health)
}

func handleGetPreview(ctx *HandlerContext, payload json.RawMessage) {
	frameData, err := record.Get().CapturePreview()
	if err != nil {
		SendEncrypted(ctx, MsgGetPreview, buildResult(err, nil))
		return
	}

	SendEncrypted(ctx, MsgGetPreview, buildResult(nil, map[string]any{
		"image": base64.StdEncoding.EncodeToString(frameData),
	}))
}

func handleStartUpdate(ctx *HandlerContext, payload json.RawMessage) {
	err := updater.Get().StartUpdate()
	SendEncrypted(ctx, MsgStartUpdate, buildResult(err, nil))
}

func handleRestart(ctx *HandlerContext, payload json.RawMessage) {
	// Send success response before rebooting
	SendEncrypted(ctx, MsgRestart, map[string]any{
		"success": true,
	})

	// Reboot the system
	go func() {
		exec.Command("sudo", "reboot").Run()
	}()
}

func handleReset(ctx *HandlerContext, payload json.RawMessage) {
	SendEncrypted(ctx, MsgReset, map[string]any{"success": true})

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
