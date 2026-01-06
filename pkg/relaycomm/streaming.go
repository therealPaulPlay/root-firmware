package relaycomm

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"root-firmware/pkg/record"
)

type currentStream struct {
	ctx        *HandlerContext
	stopCh     chan struct{}
	msgType    string
	reader     io.Reader
	wg         sync.WaitGroup
	lastActive time.Time
}

var (
	activeStream   *currentStream
	activeStreamMu sync.Mutex
	heartbeatTimer *time.Ticker
)

// StartStreamForClient starts a new stream for the given client
func StartStreamForClient(ctx *HandlerContext, reader io.Reader, messageType string) {
	activeStreamMu.Lock()
	defer activeStreamMu.Unlock()

	// Start new stream for this client
	stream := &currentStream{
		ctx:        ctx,
		stopCh:     make(chan struct{}),
		msgType:    messageType,
		reader:     reader,
		lastActive: time.Now(),
	}

	stream.wg.Add(1)
	go streamToClient(stream)

	activeStream = stream
	startHeartbeatMonitor()
	log.Printf("RelayComm: Started stream for device %s", ctx.DeviceID)
}

// StopCurrentStream stops the active stream if any
func StopCurrentStream() {
	activeStreamMu.Lock()
	defer activeStreamMu.Unlock()
	stopCurrentStreamLocked()
}

// stopCurrentStreamLocked must be called while holding activeStreamMu
func stopCurrentStreamLocked() {
	if activeStream != nil {
		close(activeStream.stopCh)
		activeStream.wg.Wait()

		// Stop the actual camera stream
		if err := record.Get().StopStream(); err != nil {
			log.Printf("RelayComm: Failed to stop stream: %v", err)
		}

		activeStream = nil
	}

	// Stop heartbeat monitor
	if heartbeatTimer != nil {
		heartbeatTimer.Stop()
		heartbeatTimer = nil
	}
}

// UpdateStreamContext updates the encryption context for the active stream (used during key renewal)
func UpdateStreamContext(deviceID string, newCtx *HandlerContext) {
	activeStreamMu.Lock()
	defer activeStreamMu.Unlock()

	if activeStream != nil && activeStream.ctx.DeviceID == deviceID {
		activeStream.ctx = newCtx
	}
}

// UpdateStreamActivity updates the last active timestamp (called by continueStream handler)
func UpdateStreamActivity() {
	activeStreamMu.Lock()
	defer activeStreamMu.Unlock()

	if activeStream != nil {
		activeStream.lastActive = time.Now()
	}
}

// startHeartbeatMonitor monitors client heartbeat and stops stream if inactive
func startHeartbeatMonitor() {
	// Stop existing monitor if any
	if heartbeatTimer != nil {
		heartbeatTimer.Stop()
	}

	heartbeatTimer = time.NewTicker(1 * time.Second)

	go func() {
		for range heartbeatTimer.C {
			activeStreamMu.Lock()

			// Stop stream if no heartbeat for 10 seconds
			if activeStream != nil && time.Since(activeStream.lastActive) > 10*time.Second {
				log.Printf("RelayComm: Stopping stream due to client inactivity")
				stopCurrentStreamLocked()
			}

			activeStreamMu.Unlock()
		}
	}()
}

// readMP4Box reads a single MP4 box from the reader
func readMP4Box(r io.Reader) ([]byte, error) {
	// Read 8-byte header (4 bytes size + 4 bytes type)
	header := make([]byte, 8)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}

	// Parse box size (big-endian)
	size := binary.BigEndian.Uint32(header[:4])
	if size < 8 {
		return nil, io.ErrUnexpectedEOF
	}

	// Sanity check: if size is unreasonably large, stream is corrupted
	maxSize := uint32(10 * 1024 * 1024) // 10MB max
	if size > maxSize {
		return nil, fmt.Errorf("invalid MP4 box size: %d bytes (corrupted stream)", size)
	}

	// Read entire box (already read 8 bytes of header)
	remainingSize := size - 8
	boxData := make([]byte, size)
	copy(boxData[:8], header)

	if remainingSize > 0 {
		if _, err := io.ReadFull(r, boxData[8:size]); err != nil {
			return nil, err
		}
	}

	return boxData, nil
}

// streamToClient reads MP4 fragments and sends them to the client
func streamToClient(stream *currentStream) {
	defer stream.wg.Done()

	chunkIndex := 0
	log.Printf("RelayComm: Stream sender started for %s", stream.msgType)

	var pendingBox []byte

	for {
		// Check for stop signal before reading
		select {
		case <-stream.stopCh:
			log.Printf("RelayComm: Stream sender stopped after %d chunks", chunkIndex)
			return
		default:
		}

		// Read next MP4 box
		boxData, err := readMP4Box(stream.reader)
		if err != nil {
			if err != io.EOF {
				log.Printf("RelayComm: %s error: %v", stream.msgType, err)
			}
			return
		}

		boxType := ""
		if len(boxData) >= 8 {
			boxType = string(boxData[4:8])
		}

		// Combine init segment (ftyp + moov) and fragments (moof + mdat)
		if boxType == "ftyp" || boxType == "moof" {
			pendingBox = boxData
			continue
		} else if (boxType == "moov" || boxType == "mdat") && pendingBox != nil {
			// Combine: ftyp+moov or moof+mdat
			combined := make([]byte, len(pendingBox)+len(boxData))
			copy(combined, pendingBox)
			copy(combined[len(pendingBox):], boxData)
			boxData = combined
			pendingBox = nil
		}

		// Send the chunk
		encoded := base64.StdEncoding.EncodeToString(boxData)

		if sendErr := SendEncryptedSuccess(stream.ctx, stream.msgType, map[string]any{
			"chunk":      encoded,
			"chunkIndex": chunkIndex,
		}); sendErr != nil {
			log.Printf("RelayComm: Error sending chunk %d: %v", chunkIndex, sendErr)
			return
		}

		chunkIndex++
	}
}
