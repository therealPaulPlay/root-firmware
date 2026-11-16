package record

import (
	"sync"
	"testing"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/testutil"
)

func setupTestRecord(t *testing.T) func() {
	t.Helper()

	config.ResetForTesting()

	cleanupGlobals := testutil.SetupTempGlobals(t)

	if err := config.Init(); err != nil {
		t.Fatalf("config.Init() error = %v", err)
	}

	return func() {
		cleanupGlobals()
		config.ResetForTesting()
	}
}

// --- broadcast tests ---

func TestBroadcast_RemoveClosesChannel(t *testing.T) {
	b := &broadcast{consumers: make([]chan []byte, 0)}

	ch := make(chan []byte, 10)
	b.addConsumer(ch)
	b.removeConsumer(ch)

	// Channel should be closed
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel should be closed")
		}
	default:
		t.Error("channel should be closed, not blocking")
	}
}

func TestBroadcast_Write(t *testing.T) {
	b := &broadcast{consumers: make([]chan []byte, 0)}

	ch1 := make(chan []byte, 10)
	ch2 := make(chan []byte, 10)
	b.addConsumer(ch1)
	b.addConsumer(ch2)

	b.write([]byte{1, 2, 3})

	// Both consumers should receive
	data1 := <-ch1
	data2 := <-ch2

	if len(data1) != 3 || len(data2) != 3 {
		t.Error("both consumers should receive data")
	}
}

func TestBroadcast_WriteCopiesData(t *testing.T) {
	b := &broadcast{consumers: make([]chan []byte, 0)}

	ch := make(chan []byte, 10)
	b.addConsumer(ch)

	original := []byte{1, 2, 3}
	b.write(original)

	received := <-ch
	original[0] = 99

	if received[0] != 1 {
		t.Error("write should copy data, not reference it")
	}
}

func TestBroadcast_WriteDropsWhenFull(t *testing.T) {
	b := &broadcast{consumers: make([]chan []byte, 0)}

	ch := make(chan []byte, 1) // Small buffer
	b.addConsumer(ch)

	b.write([]byte{1})
	b.write([]byte{2}) // Should be dropped

	if len(ch) != 1 {
		t.Error("second write should be dropped when channel is full")
	}
}

func TestBroadcast_CloseAll(t *testing.T) {
	b := &broadcast{consumers: make([]chan []byte, 0)}

	ch1 := make(chan []byte, 10)
	ch2 := make(chan []byte, 10)
	b.addConsumer(ch1)
	b.addConsumer(ch2)

	b.closeAll()

	if len(b.consumers) != 0 {
		t.Error("closeAll should clear consumers")
	}

	// Both channels should be closed
	_, ok1 := <-ch1
	_, ok2 := <-ch2
	if ok1 || ok2 {
		t.Error("closeAll should close all channels")
	}
}

// --- MicEnabled tests ---

func TestMicEnabled_DefaultTrue(t *testing.T) {
	cleanup := setupTestRecord(t)
	defer cleanup()

	if !MicEnabled() {
		t.Error("MicEnabled() should default to true")
	}
}

func TestMicEnabled_ExplicitlyDisabled(t *testing.T) {
	cleanup := setupTestRecord(t)
	defer cleanup()

	config.Get().SetKey("microphoneEnabled", false)

	if MicEnabled() {
		t.Error("MicEnabled() should return false when disabled")
	}
}

// --- lock ordering tests ---

// lockedWriter mimics the relay's stream manager, whose Write takes its own mutex
type lockedWriter struct {
	mu      *sync.Mutex
	entered chan struct{} // Signalled when Write is about to wait for mu
}

func (w lockedWriter) Write(p []byte) (int, error) {
	select {
	case w.entered <- struct{}{}:
	default:
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(p), nil
}

// Regression test for a deadlock between starting a stream and stopping another viewer's audio
func TestSubscribeVideo_DoesNotHoldLockWhileWriting(t *testing.T) {
	r := &Recorder{
		videoBroadcast: &broadcast{latestFrame: makeTestGOP(1)},
		audioBroadcast: &broadcast{},
	}
	audioCh := make(chan []byte, 1)
	r.audioBroadcast.addConsumer(audioCh)

	var managerMu sync.Mutex
	managerMu.Lock() // Manager is busy tearing down a previous viewer

	writer := lockedWriter{&managerMu, make(chan struct{}, 1)}
	subscribed := make(chan struct{})
	go func() {
		defer close(subscribed)
		if _, err := r.SubscribeVideo(writer); err != nil {
			t.Errorf("SubscribeVideo() error = %v", err)
		}
	}()
	select {
	case <-writer.entered: // SubscribeVideo has reached the init segment write
	case <-time.After(2 * time.Second):
		t.Fatal("SubscribeVideo never wrote the init segment")
	}

	unsubscribed := make(chan struct{})
	go func() {
		defer close(unsubscribed)
		r.UnsubscribeAudio(audioCh)
	}()

	select {
	case <-unsubscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("UnsubscribeAudio blocked behind SubscribeVideo's init segment write (deadlock)")
	}

	managerMu.Unlock()
	select {
	case <-subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("SubscribeVideo did not finish")
	}
	r.UnsubscribeVideo()
}
