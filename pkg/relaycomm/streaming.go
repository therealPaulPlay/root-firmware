package relaycomm

import (
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"sync"
	"time"
)

const (
	chunkSize     = 64 * 1024 // 64KB chunks
	maxViewers    = 3
	channelBuffer = 30 // Buffer up to 30 chunks per viewer
)

type viewer struct {
	ctx     *HandlerContext
	chunks  chan map[string]any
	stopCh  chan struct{}
	msgType string
}

var (
	viewers   = make(map[string]*viewer)
	viewersMu sync.Mutex
)

// AddViewer adds a viewer. Returns (shouldStartCamera, error).
func AddViewer(ctx *HandlerContext, messageType string) (bool, error) {
	viewersMu.Lock()
	defer viewersMu.Unlock()

	if len(viewers) >= maxViewers {
		return false, fmt.Errorf("viewer limit reached (%d/%d)", len(viewers), maxViewers)
	}

	v := &viewer{
		ctx:     ctx,
		chunks:  make(chan map[string]any, channelBuffer),
		stopCh:  make(chan struct{}),
		msgType: messageType,
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

	wasEmpty := len(viewers) == 0
	viewers[ctx.DeviceID] = v

	return wasEmpty, nil // Start camera if this was first viewer
}

// RemoveViewer removes a viewer. Returns true if camera should stop (last viewer left).
func RemoveViewer(deviceID string) bool {
	viewersMu.Lock()
	defer viewersMu.Unlock()

	if v, ok := viewers[deviceID]; ok {
		close(v.stopCh)
		close(v.chunks)
		delete(viewers, deviceID)
	}
	return len(viewers) == 0
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

	// Rate limit: 5 chunks/sec (each chunk can contain multiple frames)
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
				"done":       false,
			})
			chunkIndex++
		}

		if err == io.EOF {
			log.Printf("RelayComm: %s reached EOF after %d chunks", messageType, chunkIndex)
			broadcastChunk(map[string]any{
				"done": true,
			})
			return nil
		}

		if err != nil {
			log.Printf("RelayComm: %s error after %d chunks: %v", messageType, chunkIndex, err)
			return fmt.Errorf("failed to read: %w", err)
		}
	}
}
