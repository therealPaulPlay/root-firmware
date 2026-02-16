package relaycomm

import (
	"strings"
	"sync"
	"testing"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/testutil"
)

func resetSingletons() {
	if instance != nil {
		instance.Stop()
	}
	instance = nil
	once = sync.Once{}
	config.ResetForTesting()
}

func setupTestConfig(t *testing.T) func() {
	t.Helper()
	resetSingletons()

	cleanupGlobals := testutil.SetupTempGlobals(t)

	return func() {
		cleanupGlobals()
		resetSingletons()
	}
}

func TestGet_PanicsWithoutInit(t *testing.T) {
	resetSingletons()
	defer resetSingletons()

	defer func() {
		if r := recover(); r == nil {
			t.Error("Get() should panic when not initialized")
		}
	}()

	Get()
}

func TestRelayComm_CheckRateLimit(t *testing.T) {
	r := &RelayComm{
		rateMu:    sync.Mutex{},
		rateCount: 0,
		rateReset: time.Now(),
	}

	// Should allow up to maxMessagesPerSecond
	for i := 0; i < maxMessagesPerSecond; i++ {
		if !r.checkRateLimit() {
			t.Errorf("checkRateLimit() returned false at message %d, expected true", i+1)
		}
	}

	// Next one should be rate limited
	if r.checkRateLimit() {
		t.Error("checkRateLimit() should return false after exceeding limit")
	}
}

func TestRelayComm_CheckRateLimit_ResetsAfterSecond(t *testing.T) {
	r := &RelayComm{
		rateMu:    sync.Mutex{},
		rateCount: maxMessagesPerSecond,             // Already at limit
		rateReset: time.Now().Add(-2 * time.Second), // Reset time is in the past
	}

	// Should reset counter and allow
	if !r.checkRateLimit() {
		t.Error("checkRateLimit() should reset after 1 second and allow messages")
	}

	if r.rateCount != 1 {
		t.Errorf("rateCount = %d, want 1 after reset", r.rateCount)
	}
}

func TestRelayComm_Send_NotConnected(t *testing.T) {
	r := &RelayComm{sendChan: nil}

	err := r.Send(Message{Type: "test"})
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Errorf("Send() error = %v, want 'not connected'", err)
	}
}

func TestRelayComm_Send_QueueFull(t *testing.T) {
	r := &RelayComm{sendChan: make(chan Message, 1)}
	r.sendChan <- Message{Type: "first"} // Fill the queue

	err := r.Send(Message{Type: "second"})
	if err == nil || !strings.Contains(err.Error(), "queue full") {
		t.Errorf("Send() error = %v, want 'queue full'", err)
	}
}

func TestRelayComm_Send_Success(t *testing.T) {
	r := &RelayComm{sendChan: make(chan Message, 10)}

	msg := Message{Type: "test", OriginID: "origin", TargetID: "target"}
	if err := r.Send(msg); err != nil {
		t.Errorf("Send() error = %v", err)
	}

	select {
	case received := <-r.sendChan:
		if received.Type != msg.Type {
			t.Errorf("received Type = %s, want %s", received.Type, msg.Type)
		}
	default:
		t.Error("message was not queued")
	}
}

func TestRelayComm_On(t *testing.T) {
	r := &RelayComm{handlers: make(map[string]func(Message))}

	called := false
	r.On("testType", func(msg Message) { called = true })

	if handler, ok := r.handlers["testType"]; !ok {
		t.Fatal("handler was not registered")
	} else {
		handler(Message{})
		if !called {
			t.Error("handler was not called")
		}
	}
}

func TestRelayComm_Stop_WhenNotRunning(t *testing.T) {
	r := &RelayComm{stopChan: nil}
	r.Stop() // Should not panic
}

func TestRelayComm_Start_WithoutRelayDomain(t *testing.T) {
	cleanup := setupTestConfig(t)
	defer cleanup()

	if err := config.Init(); err != nil {
		t.Fatalf("config.Init() error = %v", err)
	}

	// Don't set relayDomain - Start should return without connecting
	Init()
	r := Get()
	r.Start()

	// Should not have created channels since relay domain is not set
	if r.sendChan != nil {
		t.Error("Start() should not create sendChan when relay domain is not configured")
	}
}
