package relaycomm

import (
	"testing"
	"time"
)

func newTestStream() *stream {
	s := &stream{
		ch:    make(chan []byte, 3),
		endCh: make(chan struct{}),
	}
	s.wg.Go(func() { <-s.endCh })
	return s
}

func newTestManager() *streamManager {
	return &streamManager{
		video: make(map[string]*stream),
		audio: make(map[string]*stream),
	}
}

func TestWriteFansOutToViewers(t *testing.T) {
	sm := newTestManager()
	s1 := newTestStream()
	s2 := newTestStream()
	defer func() { close(s1.endCh); close(s2.endCh) }()

	sm.video["a"] = s1
	sm.video["b"] = s2
	sm.Write([]byte("segment1"))

	if got := string(<-s1.ch); got != "segment1" {
		t.Errorf("viewer a: got %q, want segment1", got)
	}
	if got := string(<-s2.ch); got != "segment1" {
		t.Errorf("viewer b: got %q, want segment1", got)
	}
}

func TestWriteCachesInitSegment(t *testing.T) {
	sm := newTestManager()
	sm.Write([]byte("init"))
	sm.Write([]byte("fragment"))

	if string(sm.initSegment) != "init" {
		t.Errorf("init segment = %q, want 'init'", sm.initSegment)
	}
}

func TestWriteDropsWhenChannelFull(t *testing.T) {
	sm := newTestManager()
	s := newTestStream()
	defer close(s.endCh)

	sm.video["a"] = s
	sm.Write([]byte("1"))
	sm.Write([]byte("2"))
	sm.Write([]byte("3"))

	// 4th write must not block (channel buffer is 3)
	done := make(chan struct{})
	go func() { sm.Write([]byte("4")); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Write blocked on full channel")
	}
}

func TestInitSegmentResetBetweenSessions(t *testing.T) {
	sm := newTestManager()

	// Session 1: muxer writes init
	sm.Write([]byte("init-v1"))
	if string(sm.initSegment) != "init-v1" {
		t.Fatalf("session 1: initSegment = %q, want init-v1", sm.initSegment)
	}

	// Teardown clears init
	sm.initSegment = nil

	// Session 2: new muxer writes fresh init
	sm.Write([]byte("init-v2"))
	if string(sm.initSegment) != "init-v2" {
		t.Errorf("session 2: initSegment = %q, want init-v2", sm.initSegment)
	}
}

func TestWriteCopiesData(t *testing.T) {
	sm := newTestManager()
	s := newTestStream()
	defer close(s.endCh)

	sm.video["a"] = s
	data := []byte("hello")
	sm.Write(data)
	data[0] = 'X'

	if got := string(<-s.ch); got != "hello" {
		t.Errorf("got %q after mutation, want 'hello'", got)
	}
}
