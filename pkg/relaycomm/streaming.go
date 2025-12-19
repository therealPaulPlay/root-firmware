package relaycomm

import (
	"encoding/base64"
	"fmt"
	"io"
	"sync"
)

const (
	chunkSize  = 64 * 1024 // 64KB chunks
	maxViewers = 3
)

var (
	viewers   = make(map[string]*HandlerContext)
	viewersMu sync.Mutex
)

// addViewer adds a viewer and returns error if limit reached
func addViewer(ctx *HandlerContext) error {
	viewersMu.Lock()
	defer viewersMu.Unlock()

	if len(viewers) >= maxViewers {
		return fmt.Errorf("viewer limit reached (%d/%d)", len(viewers), maxViewers)
	}
	viewers[ctx.DeviceID] = ctx
	return nil
}

// removeViewer removes a viewer and returns true if no viewers remain
func removeViewer(deviceID string) bool {
	viewersMu.Lock()
	defer viewersMu.Unlock()
	delete(viewers, deviceID)
	return len(viewers) == 0
}

// broadcastChunk sends chunk to all viewers
func broadcastChunk(messageType string, payload map[string]any) {
	viewersMu.Lock()
	defer viewersMu.Unlock()

	for _, ctx := range viewers {
		SendEncrypted(ctx, messageType, payload)
	}
}

// StreamReader streams data from a reader to all active viewers
func StreamReader(reader io.Reader, messageType string) error {
	buffer := make([]byte, chunkSize)
	chunkIndex := 0

	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			encoded := base64.StdEncoding.EncodeToString(buffer[:n])
			broadcastChunk(messageType, map[string]any{
				"success":    true,
				"chunk":      encoded,
				"chunkIndex": chunkIndex,
				"done":       false,
			})
			chunkIndex++
		}

		if err == io.EOF {
			broadcastChunk(messageType, map[string]any{
				"success": true,
				"done":    true,
			})
			return nil
		}

		if err != nil {
			return fmt.Errorf("failed to read: %w", err)
		}
	}
}
