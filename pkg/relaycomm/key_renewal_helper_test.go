package relaycomm

import (
	"sync"
	"testing"
	"time"

	"root-firmware/pkg/encryption"
)

// resetKeyRenewalManager resets the key renewal manager singleton for test isolation
func resetKeyRenewalManager() {
	if renewalManager != nil {
		close(renewalManager.stopCleanup)
		renewalManager.cleanupTicker.Stop()
	}
	renewalManager = nil
	renewalManagerOnce = sync.Once{}
}

func TestBufferAndGetPendingKeyRenewal(t *testing.T) {
	resetKeyRenewalManager()
	defer resetKeyRenewalManager()

	pubKey := []byte{0x01, 0x02, 0x03}
	session := &encryption.Session{}

	BufferPendingKeyRenewal("device-123", pubKey, session)

	gotPubKey, gotSession, ok := GetPendingKeyRenewal("device-123")
	if !ok {
		t.Fatal("GetPendingKeyRenewal() ok = false, want true")
	}
	if string(gotPubKey) != string(pubKey) {
		t.Errorf("pubKey = %v, want %v", gotPubKey, pubKey)
	}
	if gotSession != session {
		t.Error("session does not match")
	}

	// Non-existent device
	_, _, ok = GetPendingKeyRenewal("nonexistent")
	if ok {
		t.Error("GetPendingKeyRenewal() should return false for nonexistent device")
	}
}

func TestStoreAndGetPreviousEncryption(t *testing.T) {
	resetKeyRenewalManager()
	defer resetKeyRenewalManager()

	session := &encryption.Session{}
	StorePreviousEncryption("device-123", session)

	gotSession, ok := GetPreviousEncryption("device-123")
	if !ok {
		t.Fatal("GetPreviousEncryption() ok = false, want true")
	}
	if gotSession != session {
		t.Error("session does not match")
	}

	// Non-existent device
	_, ok = GetPreviousEncryption("nonexistent")
	if ok {
		t.Error("GetPreviousEncryption() should return false for nonexistent device")
	}
}

func TestRunCleanup(t *testing.T) {
	resetKeyRenewalManager()
	defer resetKeyRenewalManager()

	m := initKeyRenewalManager()

	// Add expired and fresh entries
	m.mu.Lock()
	m.pending["expired"] = &pendingKeyRenewal{
		CreatedAt: time.Now().Add(-60 * time.Second),
	}
	m.pending["fresh"] = &pendingKeyRenewal{
		CreatedAt: time.Now(),
	}
	m.previous["expiredPrev"] = &previousEncryption{
		CreatedAt: time.Now().Add(-60 * time.Second),
	}
	m.mu.Unlock()

	m.runCleanup()

	_, _, expiredOk := GetPendingKeyRenewal("expired")
	_, _, freshOk := GetPendingKeyRenewal("fresh")
	_, expiredPrevOk := GetPreviousEncryption("expiredPrev")

	if expiredOk {
		t.Error("expired pending entry should have been cleaned up")
	}
	if !freshOk {
		t.Error("fresh entry should not have been cleaned up")
	}
	if expiredPrevOk {
		t.Error("expired previous entry should have been cleaned up")
	}
}
