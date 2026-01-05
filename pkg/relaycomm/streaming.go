package relaycomm

import (
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"root-firmware/pkg/record"
)

const maxViewers = 3

type viewer struct {
	ctx        *HandlerContext
	chunks     chan map[string]any
	stopCh     chan struct{}
	msgType    string
	lastActive time.Time
}

var (
	viewers       = make(map[string]*viewer)
	viewersMu     sync.Mutex
	cleanupTicker *time.Ticker
)

// AddViewer adds a viewer for the given device.
// Returns true if this is the first viewer (stream should start), false otherwise.
func AddViewer(ctx *HandlerContext, messageType string) (bool, error) {
	viewersMu.Lock()

	// Update existing viewer if device ID already viewing
	if _, exists := viewers[ctx.DeviceID]; exists {
		viewersMu.Unlock()
		UpdateViewerActivity(ctx.DeviceID)
		return false, nil
	}

	if len(viewers) >= maxViewers {
		viewersMu.Unlock()
		return false, fmt.Errorf("viewer limit reached (%d/%d)", len(viewers), maxViewers)
	}

	defer viewersMu.Unlock()

	v := &viewer{
		ctx:        ctx,
		chunks:     make(chan map[string]any, 30), // Max. 15 chunks in buffer before dropping
		stopCh:     make(chan struct{}),
		msgType:    messageType,
		lastActive: time.Now(),
	}

	// Start sender goroutine for viewer
	go func() {
		for {
			select {
			case chunk := <-v.chunks:
				// Send in separate goroutine to avoid blocking channel drain
				go func() {
					viewersMu.Lock()
					ctx := v.ctx
					viewersMu.Unlock()

					if err := SendEncryptedSuccess(ctx, v.msgType, chunk); err != nil {
						log.Printf("RelayComm: Error sending chunk to viewer %s: %v", ctx.DeviceID, err)
					}
				}()
			case <-v.stopCh:
				return
			}
		}
	}()

	isFirstViewer := len(viewers) == 0
	viewers[ctx.DeviceID] = v

	// Start cleanup goroutine if first viewer
	if isFirstViewer {
		startViewerCleanup()
	}

	return isFirstViewer, nil
}

// UpdateViewerActivity updates the last active timestamp for a viewer
func UpdateViewerActivity(deviceID string) {
	viewersMu.Lock()
	defer viewersMu.Unlock()

	if v, ok := viewers[deviceID]; ok {
		v.lastActive = time.Now()
	}
}

// UpdateViewerContext updates the encryption context for a viewer (used during key renewal)
func UpdateViewerContext(deviceID string, newCtx *HandlerContext) {
	viewersMu.Lock()
	defer viewersMu.Unlock()

	if v, ok := viewers[deviceID]; ok {
		v.ctx = newCtx
	}
}

// startViewerCleanup starts a background goroutine to remove inactive viewers
func startViewerCleanup() {
	cleanupTicker = time.NewTicker(1 * time.Second)

	go func() {
		for range cleanupTicker.C {
			viewersMu.Lock()
			now := time.Now()

			for deviceID, v := range viewers {
				// Remove viewer if no heartbeat for 5s
				if now.Sub(v.lastActive) > 5*time.Second {
					log.Printf("RelayComm: Removing inactive viewer with device ID %s", deviceID)
					close(v.stopCh)
					close(v.chunks)
					delete(viewers, deviceID)
				}
			}

			// Stop cleanup interval if no viewers left
			if len(viewers) == 0 {
				viewersMu.Unlock()
				cleanupTicker.Stop()

				// Stop camera stream
				if err := record.Get().StopStream(); err != nil {
					log.Printf("RelayComm: Failed to stop stream: %v", err)
				}
				return
			}

			viewersMu.Unlock()
		}
	}()
}

// broadcastChunk sends chunk data to all viewers (non-blocking, drops if buffer full)
func broadcastChunk(fields map[string]any) {
	viewersMu.Lock()
	defer viewersMu.Unlock()

	for deviceID, v := range viewers {
		select {
		case v.chunks <- fields:
			// Successfully queued
		default:
			log.Printf("RelayComm: Dropping chunk for viewer %s (buffer full)", deviceID)
		}
	}
}

// StreamReader streams data from a reader to all active viewers
func StreamReader(reader io.Reader, messageType string) error {
	buffer := make([]byte, 128*1024) // 128KB chunks (dynamic chunking?)
	chunkIndex := 0

	log.Printf("RelayComm: StreamReader started for %s", messageType)

	for {
		n, err := reader.Read(buffer)

		if n > 0 {
			encoded := base64.StdEncoding.EncodeToString(buffer[:n])
			broadcastChunk(map[string]any{
				"chunk":      encoded,
				"chunkIndex": chunkIndex,
			})
			chunkIndex++
			time.Sleep(200 * time.Millisecond) // Small delay to prevent flooding the channel (5 messages / s)
		}

		if err != nil {
			if err == io.EOF {
				log.Printf("RelayComm: %s ended after %d chunks", messageType, chunkIndex)
			} else {
				log.Printf("RelayComm: %s error: %v", messageType, err)
			}
			return err
		}
	}
}
