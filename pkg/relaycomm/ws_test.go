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

func TestRateLimiter_PerDevice(t *testing.T) {
	rl := newRateLimiter()

	// Should allow up to rateBurst for a single device
	for i := range rateBurst {
		if !rl.allow("device1") {
			t.Errorf("allow() returned false at message %d, expected true", i+1)
		}
	}

	// Next one should be rate limited for device1
	if rl.allow("device1") {
		t.Error("allow() should return false after device burst exhausted")
	}

	// Different device should still be allowed
	if !rl.allow("device2") {
		t.Error("allow() should allow a different device")
	}
}

func TestRateLimiter_GlobalLimit(t *testing.T) {
	rl := rateLimiter{
		buckets:      make(map[string]*tokenBucket),
		globalBucket: tokenBucket{tokens: 2, last: time.Now()},
		lastCleanup:  time.Now(),
	}

	// Exhaust global tokens across different devices
	if !rl.allow("device1") {
		t.Error("first message should be allowed")
	}
	if !rl.allow("device2") {
		t.Error("second message should be allowed")
	}
	if rl.allow("device3") {
		t.Error("third message should be blocked by global limit")
	}
}

func TestRateLimiter_Refill(t *testing.T) {
	rl := rateLimiter{
		buckets: make(map[string]*tokenBucket),
		globalBucket: tokenBucket{
			tokens: 0,
			last:   time.Now().Add(-2 * time.Second), // 2s ago → should refill
		},
		lastCleanup: time.Now(),
	}

	if !rl.allow("device1") {
		t.Error("allow() should allow after refill")
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
