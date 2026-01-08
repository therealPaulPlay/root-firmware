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

type stream struct {
	ctx        *HandlerContext
	reader     io.ReadCloser
	msgType    string
	stopCh     chan struct{}
	lastActive time.Time
	onStop     func()
	wg         sync.WaitGroup
}

type streamManager struct {
	mu      sync.Mutex
	video   *stream
	audio   *stream
	monitor *time.Ticker
}

var streams = &streamManager{}

func (sm *streamManager) start(isVideo bool, ctx *HandlerContext, reader io.ReadCloser, msgType string, onStop func()) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s := &stream{
		ctx:        ctx,
		reader:     reader,
		msgType:    msgType,
		stopCh:     make(chan struct{}),
		lastActive: time.Now(),
		onStop:     onStop,
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
	log.Printf("RelayComm: Started %s stream for device %s", msgType, ctx.DeviceID)
}

func (sm *streamManager) stop(isVideo bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.stopLocked(isVideo)
}

func (sm *streamManager) stopLocked(isVideo bool) {
	s := sm.video
	if !isVideo {
		s = sm.audio
	}

	if s != nil {
		close(s.stopCh)
		s.reader.Close()
		s.wg.Wait()
		if s.onStop != nil {
			s.onStop()
		}

		if isVideo {
			sm.video = nil
		} else {
			sm.audio = nil
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
				log.Println("RelayComm: Stopping video stream due to inactivity")
				sm.stopLocked(true)
			}
			if sm.audio != nil && time.Since(sm.audio.lastActive) > 10*time.Second {
				log.Println("RelayComm: Stopping audio stream due to inactivity")
				sm.stopLocked(false)
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
		return nil, fmt.Errorf("invalid MP4 box size: %d bytes", size)
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
		case <-s.stopCh:
			return
		default:
		}

		boxData, err := readMP4Box(s.reader)
		if err != nil {
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

		if err := SendEncryptedSuccess(s.ctx, s.msgType, map[string]any{
			"chunk":      base64.StdEncoding.EncodeToString(boxData),
			"chunkIndex": chunkIndex,
		}); err != nil {
			return
		}
		chunkIndex++
	}
}

func streamAudio(s *stream) {
	defer s.wg.Done()
	buffer := make([]byte, 64*1024)
	chunkIndex := 0

	for {
		select {
		case <-s.stopCh:
			return
		default:
		}

		n, err := s.reader.Read(buffer)
		if err != nil {
			return
		}

		if n > 0 {
			if err := SendEncryptedSuccess(s.ctx, s.msgType, map[string]any{
				"chunk":      base64.StdEncoding.EncodeToString(buffer[:n]),
				"chunkIndex": chunkIndex,
			}); err != nil {
				return
			}
			chunkIndex++
		}
	}
}

func StartVideoStreamForClient(ctx *HandlerContext, reader io.Reader, msgType string) {
	streams.start(true, ctx, reader.(io.ReadCloser), msgType, func() {
		record.Get().StopVideoStream()
	})
}

func StopVideoStream() {
	streams.stop(true)
}

func StartAudioStreamForClient(ctx *HandlerContext, reader io.Reader) {
	streams.start(false, ctx, reader.(io.ReadCloser), MsgStreamAudioChunk, nil)
}

func StopAudioStream() {
	streams.stop(false)
}

func UpdateStreamActivity() {
	streams.updateActivity()
}

func UpdateStreamContext(deviceID string, newCtx *HandlerContext) {
	streams.updateContext(deviceID, newCtx)
}
