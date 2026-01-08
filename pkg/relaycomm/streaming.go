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
	heartbeatTimer *time.Ticker
	videoStream    *currentStream
	videoStreamMu  sync.Mutex
	audioStream    *currentStream
	audioStreamMu  sync.Mutex
)

// StartVideoStreamForClient starts a new stream for the given client
func StartVideoStreamForClient(ctx *HandlerContext, reader io.Reader, messageType string) {
	videoStreamMu.Lock()
	defer videoStreamMu.Unlock()

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

	videoStream = stream
	startHeartbeatMonitor()
	log.Printf("RelayComm: Started video stream for device %s", ctx.DeviceID)
}

// StopVideoStream stops the active stream if any
func StopVideoStream() {
	videoStreamMu.Lock()
	defer videoStreamMu.Unlock()
	stopVideoStreamLocked()
}

// stopVideoStreamLocked must be called while holding activeStreamMu
func stopVideoStreamLocked() {
	if videoStream != nil {
		close(videoStream.stopCh)
		videoStream.wg.Wait()

		// Stop the actual camera stream
		if err := record.Get().StopVideoStream(); err != nil {
			log.Printf("RelayComm: Failed to stop video stream: %v", err)
		}

		videoStream = nil
	}

	// Stop heartbeat monitor
	if heartbeatTimer != nil {
		heartbeatTimer.Stop()
		heartbeatTimer = nil
	}
}

// UpdateStreamContext updates the encryption context for the active stream (used during key renewal)
func UpdateStreamContext(deviceID string, newCtx *HandlerContext) {
	videoStreamMu.Lock()
	defer videoStreamMu.Unlock()

	if videoStream != nil && videoStream.ctx.DeviceID == deviceID {
		videoStream.ctx = newCtx
	}

	audioStreamMu.Lock()
	defer audioStreamMu.Unlock()

	if audioStream != nil && audioStream.ctx.DeviceID == deviceID {
		audioStream.ctx = newCtx
	}
}

// UpdateStreamActivity updates the last active timestamp (called by continueStream handler)
func UpdateStreamActivity() {
	videoStreamMu.Lock()
	defer videoStreamMu.Unlock()

	if videoStream != nil {
		videoStream.lastActive = time.Now()
	}

	audioStreamMu.Lock()
	defer audioStreamMu.Unlock()

	if audioStream != nil {
		audioStream.lastActive = time.Now()
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
			// Stop video stream if no heartbeat for 10 seconds
			videoStreamMu.Lock()
			if videoStream != nil && time.Since(videoStream.lastActive) > 10*time.Second {
				log.Printf("RelayComm: Stopping video stream due to client inactivity")
				stopVideoStreamLocked()
			}
			videoStreamMu.Unlock()

			// Stop audio stream if no heartbeat for 10 seconds
			audioStreamMu.Lock()
			if audioStream != nil && time.Since(audioStream.lastActive) > 10*time.Second {
				log.Printf("RelayComm: Stopping audio stream due to client inactivity")
				stopAudioStreamLocked()
			}
			audioStreamMu.Unlock()
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

// streamToClient reads MP4 fragments pre-chunked from broadcast and sends them to the client
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

// StartAudioStreamForClient starts audio streaming independently from video
func StartAudioStreamForClient(ctx *HandlerContext, reader io.Reader) {
	audioStreamMu.Lock()
	defer audioStreamMu.Unlock()

	// Start new audio stream
	stream := &currentStream{
		ctx:        ctx,
		stopCh:     make(chan struct{}),
		msgType:    MsgStreamAudioChunk,
		reader:     reader,
		lastActive: time.Now(),
	}

	stream.wg.Add(1)
	go streamAudioToClient(stream)

	audioStream = stream
	log.Printf("RelayComm: Started audio stream for device %s", ctx.DeviceID)
}

// StopAudioStream stops the active audio stream
func StopAudioStream() {
	audioStreamMu.Lock()
	defer audioStreamMu.Unlock()
	stopAudioStreamLocked()
}

// stopAudioStreamLocked must be called while holding audioStreamMu
func stopAudioStreamLocked() {
	if audioStream != nil {
		close(audioStream.stopCh)
		audioStream.wg.Wait()

		// Close the reader to trigger cleanup in recorder
		if closer, ok := audioStream.reader.(io.Closer); ok {
			closer.Close()
		}

		audioStream = nil
	}
}

// streamAudioToClient reads pre-chunked audio from broadcast and sends to client
func streamAudioToClient(stream *currentStream) {
	defer stream.wg.Done()

	chunkIndex := 0
	buffer := make([]byte, 64*1024)

	log.Println("RelayComm: Audio stream sender started")

	for {
		// Check for stop signal before reading
		select {
		case <-stream.stopCh:
			log.Printf("RelayComm: Audio stream stopped after %d chunks", chunkIndex)
			return
		default:
		}

		// Read audio data
		n, err := stream.reader.Read(buffer)
		if err != nil {
			if err != io.EOF {
				log.Printf("RelayComm: Audio stream error: %v", err)
			}
			return
		}

		if n > 0 {
			if sendErr := SendEncryptedSuccess(stream.ctx, stream.msgType, map[string]any{
				"chunk":      base64.StdEncoding.EncodeToString(buffer[:n]),
				"chunkIndex": chunkIndex,
			}); sendErr != nil {
				log.Printf("RelayComm: Error sending audio chunk %d: %v", chunkIndex, sendErr)
				return
			}
			chunkIndex++
		}
	}
}
