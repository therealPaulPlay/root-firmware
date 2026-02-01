package relaycomm

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"root-firmware/pkg/record"
)

type stream struct {
	ctx        *HandlerContext
	reader     io.ReadCloser // For video (from ffmpeg)
	ch         chan []byte   // For audio (direct channel)
	msgType    string
	endCh      chan struct{}
	lastActive time.Time
	onEnd      func()
	wg         sync.WaitGroup
}

type streamManager struct {
	mu      sync.Mutex
	video   *stream
	audio   *stream
	monitor *time.Ticker
}

var streams = &streamManager{}

func (sm *streamManager) start(ctx *HandlerContext, reader io.ReadCloser, ch chan []byte, msgType string, onEnd func(), isVideo bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s := &stream{
		ctx:        ctx,
		reader:     reader,
		ch:         ch,
		msgType:    msgType,
		endCh:      make(chan struct{}),
		lastActive: time.Now(),
		onEnd:      onEnd,
	}

	s.wg.Add(1)
	if isVideo {
		go streamVideo(s)
		streams.video = s
	} else {
		go streamAudio(s)
		streams.audio = s
	}

	sm.startMonitor()
	log.Printf("RelayComm: Started stream for device %s (video: %v)", ctx.DeviceID, isVideo)
}

func (sm *streamManager) end(isVideo bool, errorMsg string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.endLocked(isVideo, errorMsg)
}

func (sm *streamManager) endLocked(isVideo bool, errorMsg string) {
	s := sm.video
	if !isVideo {
		s = sm.audio
	}

	if s != nil {
		close(s.endCh)
		if s.reader != nil {
			s.reader.Close()
		}
		s.wg.Wait() // Wait for stream loops to exit
		if s.onEnd != nil {
			s.onEnd()
		}

		if isVideo {
			sm.video = nil
		} else {
			sm.audio = nil
		}

		// Notify viewer of error after cleanup
		if errorMsg != "" {
			SendEncryptedError(s.ctx, s.msgType, ErrStreamEnded, errorMsg)
		}
	}

	sm.stopMonitorIfIdle()
}

func (sm *streamManager) updateActivity() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	now := time.Now()
	if sm.video != nil {
		sm.video.lastActive = now
	}
	if sm.audio != nil {
		sm.audio.lastActive = now
	}
}

func (sm *streamManager) updateContext(deviceID string, newCtx *HandlerContext) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.video != nil && sm.video.ctx.DeviceID == deviceID {
		sm.video.ctx = newCtx
	}
	if sm.audio != nil && sm.audio.ctx.DeviceID == deviceID {
		sm.audio.ctx = newCtx
	}
}

func (sm *streamManager) startMonitor() {
	if sm.monitor != nil {
		return
	}

	sm.monitor = time.NewTicker(1 * time.Second)
	go func() {
		for range sm.monitor.C {
			sm.mu.Lock()
			if sm.video != nil && time.Since(sm.video.lastActive) > 10*time.Second {
				log.Println("RelayComm: Ending video stream due to inactivity")
				sm.endLocked(true, "")
			}
			if sm.audio != nil && time.Since(sm.audio.lastActive) > 10*time.Second {
				log.Println("RelayComm: Ending audio stream due to inactivity")
				sm.endLocked(false, "")
			}
			sm.mu.Unlock()
		}
	}()
}

func (sm *streamManager) stopMonitorIfIdle() {
	if sm.video == nil && sm.audio == nil && sm.monitor != nil {
		sm.monitor.Stop()
		sm.monitor = nil
	}
}

func readMP4Box(r io.Reader) ([]byte, error) {
	header := make([]byte, 8)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}

	size := binary.BigEndian.Uint32(header[:4])
	if size < 8 {
		return nil, io.ErrUnexpectedEOF
	}
	if size > 10*1024*1024 {
		return nil, fmt.Errorf("MP4 box size exceeds safety limit: %d bytes", size)
	}

	boxData := make([]byte, size)
	copy(boxData[:8], header)
	if size > 8 {
		if _, err := io.ReadFull(r, boxData[8:]); err != nil {
			return nil, err
		}
	}

	return boxData, nil
}

func streamVideo(s *stream) {
	defer s.wg.Done()
	var pendingBox []byte
	chunkIndex := 0

	for {
		select {
		case <-s.endCh:
			return
		default:
		}

		boxData, err := readMP4Box(s.reader)
		if err != nil {
			select {
			case <-s.endCh:
				return // Stream already ended, error expected -> exit
			default:
			}
			log.Printf("RelayComm: Video stream read failed, ending stream: %v", err)
			go streams.end(true, "Video stream read failed")
			return
		}

		boxType := string(boxData[4:8])
		if boxType == "ftyp" || boxType == "moof" {
			pendingBox = boxData
			continue
		} else if (boxType == "moov" || boxType == "mdat") && pendingBox != nil {
			boxData = append(pendingBox, boxData...)
			pendingBox = nil
		}

		if err := SendEncryptedSuccessWithBinaryData(s.ctx, s.msgType, map[string]any{
			"chunkIndex": chunkIndex,
		}, boxData); err != nil {
			log.Printf("RelayComm: Video stream send failed, ending stream: %v", err)
			go streams.end(true, "Video stream send failed")
			return
		}
		chunkIndex++
	}
}

func streamAudio(s *stream) {
	defer s.wg.Done()
	chunkIndex := 0

	for {
		select {
		case <-s.endCh:
			return
		case data, ok := <-s.ch:
			if !ok {
				select {
				case <-s.endCh:
					return // Stream already ended, error expected -> exit
				default:
				}
				log.Println("RelayComm: Audio channel closed, ending stream")
				go streams.end(false, "Audio channel closed")
				return
			}

			if err := SendEncryptedSuccessWithBinaryData(s.ctx, s.msgType, map[string]any{
				"chunkIndex": chunkIndex,
			}, data); err != nil {
				log.Printf("RelayComm: Audio stream send failed, ending stream: %v", err)
				go streams.end(false, "Audio stream send failed")
				return
			}
			chunkIndex++
		}
	}
}

// Exported start functions
func StartVideoStreamForClient(ctx *HandlerContext, reader io.Reader, msgType string) {
	streams.start(ctx, reader.(io.ReadCloser), nil, msgType, func() {
		record.Get().StopVideoStream()
	}, true)
}

func StartAudioStreamForClient(ctx *HandlerContext, ch chan []byte) {
	streams.start(ctx, nil, ch, MsgStreamAudioChunk, func() {
		record.Get().StopAudioStream()
	}, false)
}

// Exported end functions
func EndVideoStream(errorMsg string) {
	streams.end(true, errorMsg)
}

func EndAudioStream(errorMsg string) {
	streams.end(false, errorMsg)
}

// GetVideoStreamDeviceID returns the device ID of the current video stream viewer, or empty if none
func GetVideoStreamDeviceID() string {
	streams.mu.Lock()
	defer streams.mu.Unlock()
	if streams.video != nil {
		return streams.video.ctx.DeviceID
	}
	return ""
}

// Update activity (heartbeat)
func UpdateStreamActivity() {
	streams.updateActivity()
}

// Update context (encryption session etc.)
func UpdateStreamContext(deviceID string, newCtx *HandlerContext) {
	streams.updateContext(deviceID, newCtx)
}
