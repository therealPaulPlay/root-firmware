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

const (
	chunkSize     = 64 * 1024 // 64KB chunks
	maxViewers    = 3
	channelBuffer = 30 // Buffer up to 30 chunks per viewer
)

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

// AddViewer adds a viewer for the given device
func AddViewer(ctx *HandlerContext, messageType string) error {
	viewersMu.Lock()
	defer viewersMu.Unlock()

	// Return if viewer for this device already exists
	if _, exists := viewers[ctx.DeviceID]; exists {
		return nil
	}

	if len(viewers) >= maxViewers {
		return fmt.Errorf("viewer limit reached (%d/%d)", len(viewers), maxViewers)
	}

	v := &viewer{
		ctx:        ctx,
		chunks:     make(chan map[string]any, channelBuffer),
		stopCh:     make(chan struct{}),
		msgType:    messageType,
		lastActive: time.Now(),
	}

	// Start sender goroutine for this viewer
	go func() {
		for {
			select {
			case chunk := <-v.chunks:
				// Send with timeout to prevent blocking indefinitely
				done := make(chan struct{})
				go func() {
					SendEncryptedSuccess(v.ctx, v.msgType, chunk)
					close(done)
				}()

				select {
				case <-done:
					// Success
				case <-time.After(5 * time.Second):
					log.Printf("RelayComm: Send timeout for viewer %s", v.ctx.DeviceID)
				}
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

	return nil
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
					log.Printf("RelayComm: Removing inactive viewer %s", deviceID)
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
	buffer := make([]byte, chunkSize)
	chunkIndex := 0

	log.Printf("RelayComm: StreamReader started for %s", messageType)

	// 5 chunks/sec (each chunk can contain multiple frames)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		<-ticker.C // Wait before reading next chunk

		n, err := reader.Read(buffer)
		if n > 0 {
			encoded := base64.StdEncoding.EncodeToString(buffer[:n])
			broadcastChunk(map[string]any{
				"chunk":      encoded,
				"chunkIndex": chunkIndex,
			})
			chunkIndex++
		}

		if err != nil {
			log.Printf("RelayComm: %s ended after %d chunks", messageType, chunkIndex)
			return nil
		}
	}
}
