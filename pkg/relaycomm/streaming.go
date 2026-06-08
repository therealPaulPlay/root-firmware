package relaycomm

import (
	"log"
	"sync"
	"time"

	"root-firmware/pkg/globals"
	"root-firmware/pkg/record"
)

type stream struct {
	clientID   string
	ch         chan []byte
	msgType    string
	stopCh     chan struct{}
	startedAt  time.Time
	lastActive time.Time
	wg         sync.WaitGroup
}

type streamManager struct {
	mu          sync.Mutex
	muxerDoneCh chan struct{}
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
					log.Printf("RelayComm: Stopping streams for device %s due to inactivity", id)
					streams.stopVideoLocked(id, "")
					streams.stopAudioLocked(id, "")
				}
			}
			streams.mu.Unlock()
		}
	}()
}

// Write is the muxer's io.Writer sink, fanning each fMP4 segment out to every viewer
// The first segment is the init segment, cached to bootstrap late joiners
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
	close(s.stopCh)
	s.wg.Wait()
}

func (sm *streamManager) stopVideoLocked(deviceID, errorMsg string) {
	s, ok := sm.video[deviceID]
	if !ok {
		return
	}
	delete(sm.video, deviceID)
	stopStream(s)
	if errorMsg != "" {
		pushStreamError(s.clientID, s.msgType, errorMsg)
	}
	// Last viewer left, release the shared video subscription
	// Clear muxerDoneCh and unlock first so a concurrent start sees none and sm.Write can still take mu
	if len(sm.video) == 0 && sm.muxerDoneCh != nil {
		log.Printf("RelayComm: Last viewer left, releasing video subscription")
		muxerDoneCh := sm.muxerDoneCh
		sm.muxerDoneCh = nil
		sm.mu.Unlock()
		record.Get().UnsubscribeVideo()
		<-muxerDoneCh
		sm.mu.Lock()
		// Clear stale init (only if no new subscription started during teardown)
		if sm.muxerDoneCh == nil {
			sm.initSegment = nil
		}
	}
}

func (sm *streamManager) stopAudioLocked(deviceID, errorMsg string) {
	s, ok := sm.audio[deviceID]
	if !ok {
		return
	}
	delete(sm.audio, deviceID)
	stopStream(s)
	record.Get().UnsubscribeAudio(s.ch)
	if errorMsg != "" {
		pushStreamError(s.clientID, MsgStreamAudioChunk, errorMsg)
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
	sm.stopVideoLocked(oldestID, "Stream viewer limit reached")
	sm.stopAudioLocked(oldestID, "Stream viewer limit reached")
}

func sendLoop(s *stream) {
	defer s.wg.Done()
	chunkIndex := 0
	for {
		select {
		case <-s.stopCh:
			return
		case data, ok := <-s.ch:
			if !ok {
				return
			}
			if err := pushStreamChunk(s.clientID, s.msgType, chunkIndex, data); err != nil {
				if chunkIndex == 0 {
					log.Printf("RelayComm: Failed to send init segment for %s: %v", s.clientID, err)
					go func() {
						streams.mu.Lock()
						// Ensure this is still the most recent stream, if so, end both video and audio
						if streams.video[s.clientID] == s {
							streams.stopVideoLocked(s.clientID, "WebSocket overwhelmed")
							streams.stopAudioLocked(s.clientID, "WebSocket overwhelmed")
						}
						streams.mu.Unlock()
					}()
					return
				}
				log.Printf("RelayComm: Dropped %s chunk message for %s: %v", s.msgType, s.clientID, err)
				continue
			}
			chunkIndex++
		}
	}
}

func pushStreamChunk(clientID, msgType string, chunkIndex int, data []byte) error {
	return Get().server.Push(clientID, msgType, map[string]any{
		"success":    true,
		"chunkIndex": chunkIndex,
		"chunk":      data,
	}, lowPrioWriteFn())
}

func pushStreamError(clientID, msgType, errorMsg string) {
	_ = Get().server.Push(clientID, msgType, map[string]any{
		"success": false,
		"error":   errorMsg,
	}, lowPrioWriteFn())
}

func StartVideoStreamForClient(clientID, msgType string) {
	streams.mu.Lock()
	defer streams.mu.Unlock()

	streams.stopVideoLocked(clientID, "") // Stop any existing stream for this client if active
	if len(streams.video) >= globals.MaxConcurrentStreams {
		streams.evictOldest()
	}

	// First viewer starts the shared subscription, set the placeholder so concurrent starts back off
	// Unlock while subscribing since SubscribeVideo seeds a keyframe through Write which needs mu
	if streams.muxerDoneCh == nil {
		placeholder := make(chan struct{})
		streams.muxerDoneCh = placeholder
		streams.mu.Unlock()
		done, err := record.Get().SubscribeVideo(streams)
		streams.mu.Lock()
		if err != nil {
			streams.muxerDoneCh = nil
			log.Printf("RelayComm: Failed to start video stream: %v", err)
			return
		}
		streams.muxerDoneCh = done
	}

	now := time.Now()
	s := &stream{clientID: clientID, ch: make(chan []byte, 3), msgType: msgType, stopCh: make(chan struct{}), startedAt: now, lastActive: now}
	if streams.initSegment != nil {
		s.ch <- streams.initSegment
	}
	s.wg.Add(1)
	streams.video[clientID] = s
	go sendLoop(s)
	log.Printf("RelayComm: Started video stream for device %s (viewers: %d)", clientID, len(streams.video))
}

func StartAudioStreamForClient(clientID string) {
	streams.mu.Lock()
	defer streams.mu.Unlock()
	streams.startAudioLocked(clientID)
}

// startAudioLocked opens a recorder audio channel for the client and registers the stream
// Must be called with sm.mu held
func (sm *streamManager) startAudioLocked(clientID string) {
	sm.stopAudioLocked(clientID, "")

	ch, err := record.Get().SubscribeAudio()
	if err != nil {
		log.Printf("RelayComm: Failed to start audio stream for %s: %v", clientID, err)
		return
	}

	now := time.Now()
	s := &stream{clientID: clientID, ch: ch, msgType: MsgStreamAudioChunk, stopCh: make(chan struct{}), startedAt: now, lastActive: now}
	s.wg.Add(1)
	sm.audio[clientID] = s
	go sendLoop(s)
	log.Printf("RelayComm: Started audio stream for device %s", clientID)
}

// SyncAudioStreams starts or stops audio for all active video viewers based on current mic state
func SyncAudioStreams() {
	streams.mu.Lock()
	defer streams.mu.Unlock()

	if record.MicEnabled() {
		for clientID := range streams.video {
			if _, hasAudio := streams.audio[clientID]; hasAudio {
				continue
			}
			streams.startAudioLocked(clientID)
		}
	} else {
		for clientID := range streams.audio {
			streams.stopAudioLocked(clientID, "")
		}
	}
}

func UpdateStreamActivity(clientID string) {
	streams.mu.Lock()
	defer streams.mu.Unlock()
	now := time.Now()
	if v, ok := streams.video[clientID]; ok {
		v.lastActive = now
	}
	if v, ok := streams.audio[clientID]; ok {
		v.lastActive = now
	}
}
