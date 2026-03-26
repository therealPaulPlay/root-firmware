package relaycomm

import (
	"log"
	"sync"
	"sync/atomic"
	"time"

	"root-firmware/pkg/globals"
	"root-firmware/pkg/record"
)

type stream struct {
	ctx        atomic.Pointer[HandlerContext]
	ch         chan []byte
	msgType    string
	endCh      chan struct{}
	startedAt  time.Time
	lastActive time.Time
	wg         sync.WaitGroup
}

type streamManager struct {
	mu          sync.Mutex
	muxerExited chan struct{}
	initSegment []byte
	video       map[string]*stream
	audio       map[string]*stream
}

var streams = &streamManager{
	video: make(map[string]*stream),
	audio: make(map[string]*stream),
}

func init() {
	go func() {
		for range time.Tick(2 * time.Second) {
			streams.mu.Lock()
			for id, v := range streams.video {
				if time.Since(v.lastActive) > 10*time.Second {
					log.Printf("RelayComm: Ending streams for device %s due to inactivity", id)
					streams.endVideoLocked(id, "")
					streams.endAudioLocked(id, "")
				}
			}
			streams.mu.Unlock()
		}
	}()
}

// Write implements io.Writer — called by the muxer goroutine with complete fMP4 segments
// Fans out to all viewer channels, caching the first segment as the init segment
func (sm *streamManager) Write(p []byte) (int, error) {
	out := make([]byte, len(p))
	copy(out, p)
	sm.mu.Lock()
	if sm.initSegment == nil {
		sm.initSegment = out
	}
	for _, v := range sm.video {
		select {
		case v.ch <- out:
		default:
			log.Println("RelayComm: Dropped video chunk, viewer channel full")
		}
	}
	sm.mu.Unlock()
	return len(p), nil
}

// stopStream signals a stream's sendLoop to exit and waits for it to finish
func stopStream(s *stream) {
	close(s.endCh)
	s.wg.Wait()
}

func (sm *streamManager) endVideoLocked(deviceID, errorMsg string) {
	s, ok := sm.video[deviceID]
	if !ok {
		return
	}
	delete(sm.video, deviceID)
	stopStream(s)
	if errorMsg != "" {
		SendEncryptedError(s.ctx.Load(), s.msgType, ErrStreamEnded, errorMsg)
	}
	// Tear down shared muxer when last viewer leaves
	// Must release mu: muxer goroutine may be in sm.Write() waiting for mu
	// Set muxerExited=nil before unlocking so concurrent callers see muxer as absent
	if len(sm.video) == 0 && sm.muxerExited != nil {
		log.Printf("RelayComm: Last viewer left, tearing down muxer")
		muxerExited := sm.muxerExited
		sm.muxerExited = nil
		sm.mu.Unlock()
		record.Get().StopVideoStream()
		<-muxerExited
		sm.mu.Lock()
		// Clear stale init — but only if no new muxer started during teardown
		if sm.muxerExited == nil {
			sm.initSegment = nil
		}
	}
}

func (sm *streamManager) endAudioLocked(deviceID, errorMsg string) {
	s, ok := sm.audio[deviceID]
	if !ok {
		return
	}
	delete(sm.audio, deviceID)
	stopStream(s)
	record.Get().StopAudioStream(s.ch)
	if errorMsg != "" {
		SendEncryptedError(s.ctx.Load(), MsgStreamAudioChunk, ErrStreamEnded, errorMsg)
	}
}

// evictOldest kicks the longest-connected device to make room for a new viewer (Must be called with sm.mu held)
func (sm *streamManager) evictOldest() {
	var oldestID string
	var oldest time.Time
	for id, v := range sm.video {
		if oldestID == "" || v.startedAt.Before(oldest) {
			oldestID = id
			oldest = v.startedAt
		}
	}
	if oldestID == "" {
		return
	}
	sm.endVideoLocked(oldestID, "Stream viewer limit reached")
	sm.endAudioLocked(oldestID, "Stream viewer limit reached")
}

func sendLoop(s *stream) {
	defer s.wg.Done()
	chunkIndex := 0
	for {
		select {
		case <-s.endCh:
			return
		case data, ok := <-s.ch:
			if !ok {
				return
			}
			ctx := s.ctx.Load()
			if err := SendEncryptedSuccess(ctx, s.msgType, map[string]any{
				"chunkIndex": chunkIndex,
				"chunk":      data,
			}); err != nil {
				if chunkIndex == 0 {
					log.Printf("RelayComm: Failed to send init segment for %s: %v", ctx.DeviceID, err)
					go func() {
						streams.mu.Lock()
						// Ensure this is still the most recent stream, if so, end both video and audio
						if streams.video[ctx.DeviceID] == s {
							streams.endVideoLocked(ctx.DeviceID, "WebSocket overwhelmed")
							streams.endAudioLocked(ctx.DeviceID, "WebSocket overwhelmed")
						}
						streams.mu.Unlock()
					}()
					return
				}
				log.Printf("RelayComm: Dropped %s chunk message for %s: %v", s.msgType, ctx.DeviceID, err)
				continue
			}
			chunkIndex++
		}
	}
}

// Exported functions

func StartVideoStreamForClient(ctx *HandlerContext, msgType string) {
	streams.mu.Lock()
	defer streams.mu.Unlock()

	streams.endVideoLocked(ctx.DeviceID, "") // In case a stream is active for this device already, end it
	if len(streams.video) >= globals.MaxConcurrentStreams {
		streams.evictOldest()
	}

	// Start shared muxer on first viewer
	// Must release mu because StartVideoStream calls SeedKeyframe → Write() which needs mu
	// Set placeholder before unlocking so concurrent callers see muxer as starting
	if streams.muxerExited == nil {
		placeholder := make(chan struct{})
		streams.muxerExited = placeholder
		streams.mu.Unlock()
		done, err := record.Get().StartVideoStream(streams)
		streams.mu.Lock()
		if err != nil {
			streams.muxerExited = nil
			log.Printf("RelayComm: Failed to start video stream: %v", err)
			return
		}
		streams.muxerExited = done
	}

	now := time.Now()
	s := &stream{ch: make(chan []byte, 3), msgType: msgType, endCh: make(chan struct{}), startedAt: now, lastActive: now}
	s.ctx.Store(ctx)
	if streams.initSegment != nil {
		s.ch <- streams.initSegment
	}
	s.wg.Add(1)
	streams.video[ctx.DeviceID] = s
	go sendLoop(s)
	log.Printf("RelayComm: Started video stream for device %s (viewers: %d)", ctx.DeviceID, len(streams.video))
}

func StartAudioStreamForClient(ctx *HandlerContext, ch chan []byte) {
	streams.mu.Lock()
	defer streams.mu.Unlock()

	streams.endAudioLocked(ctx.DeviceID, "") // In case a stream is active for this device already, end it

	now := time.Now()
	s := &stream{ch: ch, msgType: MsgStreamAudioChunk, endCh: make(chan struct{}), startedAt: now, lastActive: now}
	s.ctx.Store(ctx)
	s.wg.Add(1)
	streams.audio[ctx.DeviceID] = s
	go sendLoop(s)
	log.Printf("RelayComm: Started audio stream for device %s", ctx.DeviceID)
}

// SyncAudioStreams starts or stops audio for all active video viewers based on current mic state
func SyncAudioStreams() {
	streams.mu.Lock()
	defer streams.mu.Unlock()

	if record.MicEnabled() {
		for deviceID, v := range streams.video {
			if _, hasAudio := streams.audio[deviceID]; hasAudio {
				continue
			}
			ch, err := record.Get().StartAudioStream()
			if err != nil {
				log.Printf("RelayComm: Failed to start audio stream for %s: %v", deviceID, err)
				continue
			}
			now := time.Now()
			s := &stream{ch: ch, msgType: MsgStreamAudioChunk, endCh: make(chan struct{}), startedAt: now, lastActive: now}
			s.ctx.Store(v.ctx.Load())
			s.wg.Add(1)
			streams.audio[deviceID] = s
			go sendLoop(s)
			log.Printf("RelayComm: Started audio stream for device %s (mic enabled)", deviceID)
		}
	} else {
		for deviceID := range streams.audio {
			streams.endAudioLocked(deviceID, "")
		}
	}
}

func UpdateStreamActivity(deviceID string) {
	streams.mu.Lock()
	defer streams.mu.Unlock()
	now := time.Now()
	if v, ok := streams.video[deviceID]; ok {
		v.lastActive = now
	}
	if v, ok := streams.audio[deviceID]; ok {
		v.lastActive = now
	}
}

func UpdateStreamContext(deviceID string, newCtx *HandlerContext) {
	streams.mu.Lock()
	defer streams.mu.Unlock()
	if v, ok := streams.video[deviceID]; ok {
		v.ctx.Store(newCtx)
	}
	if v, ok := streams.audio[deviceID]; ok {
		v.ctx.Store(newCtx)
	}
}
