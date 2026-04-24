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
	rootproto "github.com/therealPaulPlay/root-e2ee-protocol/go-server"

	"root-firmware/pkg/config"
	"root-firmware/pkg/devices"
	"root-firmware/pkg/events"
	"root-firmware/pkg/globals"
	"root-firmware/pkg/logger"
	"root-firmware/pkg/metrics"
	"root-firmware/pkg/notifications"
	"root-firmware/pkg/record"
	"root-firmware/pkg/sfx"
	"root-firmware/pkg/updater"
	"root-firmware/pkg/wifi"
)

// Message type constants
const (
	MsgGetDevices               = "getDevices"
	MsgRemoveDevice             = "removeDevice"
	MsgGetEvents                = "getEvents"
	MsgGetRecording             = "getRecording"
	MsgFileChunk                = "fileChunk"
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

// successReply returns a {success: true, ...fields} payload
func successReply(fields map[string]any) map[string]any {
	out := map[string]any{"success": true}
	maps.Copy(out, fields)
	return out
}

// errorReply returns a {success: false, error, ...fields} payload
func errorReply(msg string, fields ...map[string]any) map[string]any {
	out := map[string]any{"success": false, "error": msg}
	if len(fields) > 0 {
		maps.Copy(out, fields[0])
	}
	return out
}

func registerHandlers(s *rootproto.Server) {
	s.OnRequest(MsgGetDevices, handleGetDevices)
	s.OnRequest(MsgRemoveDevice, handleRemoveDevice)
	s.OnRequest(MsgSetProductAlias, handleSetProductAlias)

	s.OnRequest(MsgGetEvents, handleGetEvents)
	s.OnRequest(MsgGetRecording, handleGetRecording)
	s.OnRequest(MsgGetThumbnail, handleGetThumbnail)

	s.OnRequest(MsgStartStream, handleStartStream)
	s.OnRequest(MsgContinueStream, handleContinueStream)

	s.OnRequest(MsgGetMicrophone, handleGetMicrophone)
	s.OnRequest(MsgSetMicrophone, handleSetMicrophone)
	s.OnRequest(MsgGetRecordingSound, handleGetRecordingSound)
	s.OnRequest(MsgSetRecordingSound, handleSetRecordingSound)
	s.OnRequest(MsgGetEventDetectionConfig, handleGetEventDetectionConfig)
	s.OnRequest(MsgSetEventDetectionEnabled, handleSetEventDetectionEnabled)
	s.OnRequest(MsgSetEventDetectionTypes, handleSetEventDetectionTypes)

	s.OnRequest(MsgGetNotifications, handleGetNotifications)
	s.OnRequest(MsgSetNotifications, handleSetNotifications)
	s.OnRequest(MsgSetNotificationCooldown, handleSetNotificationCooldown)

	s.OnRequest(MsgGetHealth, handleGetHealth)
	s.OnRequest(MsgStartUpdate, handleStartUpdate)
	s.OnRequest(MsgRestart, handleRestart)
	s.OnRequest(MsgReset, handleReset)
	s.OnRequest(MsgGetUpdateStatus, handleGetUpdateStatus)
	s.OnRequest(MsgSetVersionDev, handleSetVersionDev)
}

func handleGetDevices(clientID string, payload []byte, respond rootproto.RespondFn) any {
	return successReply(map[string]any{"devices": devices.Get().GetAll()})
}

func handleRemoveDevice(clientID string, payload []byte, respond rootproto.RespondFn) any {
	var req struct {
		TargetDeviceID string `cbor:"targetDeviceId"`
	}
	if err := cbor.Unmarshal(payload, &req); err != nil {
		return errorReply("Invalid payload")
	}
	if err := devices.Get().Remove(req.TargetDeviceID); err != nil {
		return errorReply(err.Error())
	}
	return successReply(map[string]any{"removedDeviceId": req.TargetDeviceID})
}

func handleSetProductAlias(clientID string, payload []byte, respond rootproto.RespondFn) any {
	var req struct {
		Alias string `cbor:"alias"`
	}
	if err := cbor.Unmarshal(payload, &req); err != nil {
		return errorReply("Invalid payload")
	}
	// Only allow clients to set their own product alias (authenticated sender = clientID)
	if err := devices.Get().SetProductAlias(clientID, req.Alias); err != nil {
		return errorReply(err.Error())
	}
	return successReply(map[string]any{"alias": req.Alias})
}

func handleGetEvents(clientID string, payload []byte, respond rootproto.RespondFn) any {
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
			return errorReply("Invalid payload")
		}
	}
	if req.Limit <= 0 {
		req.Limit = 200
	}
	eventList, nextCursor, total, err := events.Get().GetEventLogPaginated(
		req.Limit, req.Cursor, req.StartTime, req.EndTime, req.EventTypes, req.UntilEventID,
	)
	if err != nil {
		return errorReply(err.Error())
	}
	return successReply(map[string]any{
		"events":     eventList,
		"nextCursor": nextCursor,
		"total":      total,
	})
}

func handleGetRecording(clientID string, payload []byte, respond rootproto.RespondFn) any {
	var req struct {
		ID string `cbor:"id"`
	}
	if err := cbor.Unmarshal(payload, &req); err != nil {
		return errorReply("Invalid payload")
	}

	// Verify paths exist
	videoPath, err := events.Get().GetRecordingPath(req.ID)
	if err != nil {
		return errorReply(err.Error())
	}
	audioPath, err := events.Get().GetAudioPath(req.ID)
	hasAudio := err == nil

	// Respond before spawning file transfer, since chunk-pushing goroutines would otherwise
	// race the response on the wire and the client would see chunks before it's ready
	if err := respond(successReply(map[string]any{
		"hasAudio": hasAudio,
		"eventId":  req.ID,
	})); err != nil {
		log.Printf("RelayComm: Failed to respond to getRecording: %v", err)
		return nil
	}

	metadata := map[string]any{"eventId": req.ID}
	if hasAudio {
		SendFileInChunksAsync(clientID, audioPath, "audio", metadata, func() {
			SendFileInChunksAsync(clientID, videoPath, "video", metadata, nil)
		})
	} else {
		SendFileInChunksAsync(clientID, videoPath, "video", metadata, nil)
	}
	return nil
}

func handleGetThumbnail(clientID string, payload []byte, respond rootproto.RespondFn) any {
	var req struct {
		ID string `cbor:"id"`
	}
	if err := cbor.Unmarshal(payload, &req); err != nil {
		return errorReply("Invalid payload")
	}
	eventIdField := map[string]any{"eventId": req.ID}

	filePath, err := events.Get().GetThumbnailPath(req.ID)
	if err != nil {
		return errorReply(err.Error(), eventIdField)
	}
	productPrivateKey, err := config.Get().GetProductPrivateKey()
	if err != nil {
		return errorReply(fmt.Sprintf("Failed to get decryption key: %v", err), eventIdField)
	}
	reader, _, err := decryptFileToReader(filePath, productPrivateKey)
	if err != nil {
		return errorReply(fmt.Sprintf("Failed to decrypt thumbnail: %v", err), eventIdField)
	}
	fileData, err := io.ReadAll(reader)
	if err != nil {
		return errorReply(fmt.Sprintf("Failed to read thumbnail: %v", err), eventIdField)
	}
	return successReply(map[string]any{
		"eventId": req.ID,
		"data":    fileData,
	})
}

func handleStartStream(clientID string, payload []byte, respond rootproto.RespondFn) any {
	if val, ok := config.Get().GetKey("playRecordingSound"); ok {
		if b, ok := val.(bool); ok && b {
			sfx.Get().PlayStream()
		}
	}

	// Respond before kicking off streaming, since chunk-pushing goroutines would otherwise
	// race the response on the wire and the client would see chunks before it's ready
	if err := respond(successReply(nil)); err != nil {
		log.Printf("RelayComm: Failed to respond to startStream: %v", err)
		return nil
	}

	StartVideoStreamForClient(clientID, MsgStreamVideoChunk)
	if record.MicEnabled() {
		audioStream, err := record.Get().StartAudioStream()
		if err != nil {
			log.Printf("RelayComm: Failed to start audio stream: %v", err)
		} else {
			StartAudioStreamForClient(clientID, audioStream)
		}
	}

	return nil
}

// If clients do not send this, stream will be stopped after 5s (recommended interval is 2s)
func handleContinueStream(clientID string, payload []byte, respond rootproto.RespondFn) any {
	UpdateStreamActivity(clientID)
	return successReply(nil)
}

func handleGetMicrophone(clientID string, payload []byte, respond rootproto.RespondFn) any {
	return successReply(map[string]any{"enabled": record.MicEnabled()})
}

func handleSetMicrophone(clientID string, payload []byte, respond rootproto.RespondFn) any {
	var req struct {
		Enabled bool `cbor:"enabled"`
	}
	if err := cbor.Unmarshal(payload, &req); err != nil {
		return errorReply("Invalid payload")
	}
	if err := record.Get().SetMicrophoneEnabled(req.Enabled); err != nil {
		return errorReply(err.Error())
	}
	return successReply(map[string]any{"enabled": req.Enabled})
}

func handleGetRecordingSound(clientID string, payload []byte, respond rootproto.RespondFn) any {
	enabled := false
	if val, ok := config.Get().GetKey("playRecordingSound"); ok {
		if b, ok := val.(bool); ok {
			enabled = b
		}
	}
	return successReply(map[string]any{"enabled": enabled})
}

func handleSetRecordingSound(clientID string, payload []byte, respond rootproto.RespondFn) any {
	var req struct {
		Enabled bool `cbor:"enabled"`
	}
	if err := cbor.Unmarshal(payload, &req); err != nil {
		return errorReply("Invalid payload")
	}
	if err := config.Get().SetKey("playRecordingSound", req.Enabled); err != nil {
		return errorReply(err.Error())
	}
	return successReply(map[string]any{"enabled": req.Enabled})
}

func handleGetHealth(clientID string, payload []byte, respond rootproto.RespondFn) any {
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
	return successReply(map[string]any{
		"wifi":          map[string]any{"connectedSSID": wifi.Get().GetCurrentNetwork()},
		"relayDomain":   relayDomain,
		"logs":          logger.GetLogs(),
		"uptimeSeconds": uptimeSeconds,
		"metrics":       metrics.GetPoints(),
	})
}

func handleStartUpdate(clientID string, payload []byte, respond rootproto.RespondFn) any {
	if !updater.Get().StartUpdate() {
		return errorReply("No update available or already in progress")
	}
	return successReply(nil)
}

func handleGetUpdateStatus(clientID string, payload []byte, respond rootproto.RespondFn) any {
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
	return successReply(result)
}

func handleRestart(clientID string, payload []byte, respond rootproto.RespondFn) any {
	status, _, _, _ := updater.Get().GetStatus()
	if status == updater.StatusDownloading || status == updater.StatusInstalling {
		return errorReply("Cannot restart while update is in progress")
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		exec.Command("reboot").Run()
	}()
	return successReply(nil)
}

func handleReset(clientID string, payload []byte, respond rootproto.RespondFn) any {
	status, _, _, _ := updater.Get().GetStatus()
	if status == updater.StatusDownloading || status == updater.StatusInstalling {
		return errorReply("Cannot reset while update is in progress")
	}
	// Run as independent systemd unit so cleanup happens after firmware has stopped
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
	return successReply(nil)
}

func handleGetEventDetectionConfig(clientID string, payload []byte, respond rootproto.RespondFn) any {
	return successReply(map[string]any{
		"enabled":             events.IsEventDetectionEnabled(),
		"enabledTypes":        events.GetEnabledEventTypes(),
		"availableEventTypes": events.AvailableEventTypes,
	})
}

func handleSetEventDetectionEnabled(clientID string, payload []byte, respond rootproto.RespondFn) any {
	var req struct {
		Enabled bool `cbor:"enabled"`
	}
	if err := cbor.Unmarshal(payload, &req); err != nil {
		return errorReply("Invalid payload")
	}
	if err := config.Get().SetKey("eventDetectionEnabled", req.Enabled); err != nil {
		return errorReply(err.Error())
	}
	return successReply(map[string]any{"enabled": req.Enabled})
}

func handleSetEventDetectionTypes(clientID string, payload []byte, respond rootproto.RespondFn) any {
	var req struct {
		EnabledTypes []string `cbor:"enabledTypes"`
	}
	if err := cbor.Unmarshal(payload, &req); err != nil {
		return errorReply("Invalid payload")
	}
	for i := range req.EnabledTypes {
		req.EnabledTypes[i] = strings.ToLower(req.EnabledTypes[i])
	}
	if err := config.Get().SetKey("eventDetectionEnabledTypes", req.EnabledTypes); err != nil {
		return errorReply(err.Error())
	}
	return successReply(map[string]any{"enabledTypes": req.EnabledTypes})
}

func handleSetVersionDev(clientID string, payload []byte, respond rootproto.RespondFn) any {
	globals.FirmwareVersion = "dev"
	u := updater.Get()
	u.RemoveScheduledUpdateWithLock()
	u.CheckForUpdates()
	return successReply(nil)
}

func handleGetNotifications(clientID string, payload []byte, respond rootproto.RespondFn) any {
	n := notifications.Get()
	return successReply(map[string]any{
		"enabled":         n.IsEnabled(clientID),
		"cooldownMinutes": n.GetCooldownMinutes(),
	})
}

func handleSetNotifications(clientID string, payload []byte, respond rootproto.RespondFn) any {
	var req struct {
		Enabled  bool   `cbor:"enabled"`
		FCMToken string `cbor:"fcmToken"`
	}
	if err := cbor.Unmarshal(payload, &req); err != nil {
		return errorReply("Invalid payload")
	}
	if req.Enabled {
		if req.FCMToken == "" {
			return errorReply("FCM token required")
		}
		if err := notifications.Get().Enable(clientID, req.FCMToken); err != nil {
			return errorReply(err.Error())
		}
		return successReply(map[string]any{"enabled": true})
	}
	if err := notifications.Get().Disable(clientID); err != nil {
		return errorReply(err.Error())
	}
	return successReply(map[string]any{"enabled": false})
}

func handleSetNotificationCooldown(clientID string, payload []byte, respond rootproto.RespondFn) any {
	var req struct {
		CooldownMinutes int `cbor:"cooldownMinutes"`
	}
	if err := cbor.Unmarshal(payload, &req); err != nil {
		return errorReply("Invalid payload")
	}
	if err := notifications.Get().SetCooldownMinutes(req.CooldownMinutes); err != nil {
		return errorReply(err.Error())
	}
	return successReply(map[string]any{"cooldownMinutes": req.CooldownMinutes})
}
