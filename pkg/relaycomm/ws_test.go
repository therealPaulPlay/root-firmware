package relaycomm

import (
	"strings"
	"sync"
	"testing"

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

func TestRelayComm_enqueueHighPrio_NotConnected(t *testing.T) {
	r := &RelayComm{sendChan: nil}
	err := r.enqueueHighPrio([]byte("x"))
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Errorf("enqueueHighPrio() error = %v, want 'not connected'", err)
	}
}

func TestRelayComm_enqueueHighPrio_QueueFull(t *testing.T) {
	r := &RelayComm{sendChan: make(chan []byte, 1)}
	r.sendChan <- []byte("first")

	err := r.enqueueHighPrio([]byte("second"))
	if err == nil || !strings.Contains(err.Error(), "queue full") {
		t.Errorf("enqueueHighPrio() error = %v, want 'queue full'", err)
	}
}

func TestRelayComm_enqueueHighPrio_Success(t *testing.T) {
	r := &RelayComm{sendChan: make(chan []byte, 10)}
	if err := r.enqueueHighPrio([]byte("hello")); err != nil {
		t.Errorf("enqueueHighPrio() error = %v", err)
	}
	select {
	case received := <-r.sendChan:
		if string(received) != "hello" {
			t.Errorf("received = %q, want 'hello'", string(received))
		}
	default:
		t.Error("message was not queued")
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

	if r.sendChan != nil {
		t.Error("Start() should not create sendChan when relay domain is not configured")
	}
}
