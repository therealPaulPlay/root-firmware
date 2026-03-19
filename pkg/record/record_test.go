package record

import (
	"testing"

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
