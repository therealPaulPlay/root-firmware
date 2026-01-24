package relaycomm

import (
	"sync"
	"time"

	"root-firmware/pkg/encryption"
)

// pendingKeyRenewal stores temporary key renewal data before commit
type pendingKeyRenewal struct {
	NewPublicKey      []byte
	NewSharedSecret   []byte
	NewEncryptSession *encryption.Session
	CreatedAt         time.Time
}

// previousEncryption stores old encryption session during grace period
type previousEncryption struct {
	Session   *encryption.Session
	CreatedAt time.Time
}

type keyRenewalManager struct {
	pending       map[string]*pendingKeyRenewal  // deviceID -> future encryption session (discarded if client doesn't ACK)
	previous      map[string]*previousEncryption // deviceID -> old encryption session
	mu            sync.Mutex
	cleanupTicker *time.Ticker
	stopCleanup   chan struct{}
}

var renewalManager *keyRenewalManager
var renewalManagerOnce sync.Once

// initKeyRenewalManager initializes the key renewal manager with automatic cleanup
func initKeyRenewalManager() *keyRenewalManager {
	renewalManagerOnce.Do(func() {
		renewalManager = &keyRenewalManager{
			pending:       make(map[string]*pendingKeyRenewal),
			previous:      make(map[string]*previousEncryption),
			cleanupTicker: time.NewTicker(15 * time.Second),
			stopCleanup:   make(chan struct{}),
		}
		go renewalManager.cleanupExpired()
	})
	return renewalManager
}

// cleanupExpired removes stale pending renewals and expired previous encryptions (older than 30 seconds)
func (m *keyRenewalManager) cleanupExpired() {
	for {
		select {
		case <-m.cleanupTicker.C:
			m.mu.Lock()
			now := time.Now()
			// Clean up stale pending renewals
			for deviceID, renewal := range m.pending {
				if now.Sub(renewal.CreatedAt) > 30*time.Second {
					delete(m.pending, deviceID)
				}
			}
			// Clean up stale previous encryptions
			for deviceID, prev := range m.previous {
				if now.Sub(prev.CreatedAt) > 30*time.Second {
					delete(m.previous, deviceID)
				}
			}
			m.mu.Unlock()
		case <-m.stopCleanup:
			return
		}
	}
}

// BufferPendingKeyRenewal stores key renewal data before committing (auto-expires after 30s)
func BufferPendingKeyRenewal(deviceID string, newPublicKey, newSharedSecret []byte, newSession *encryption.Session) {
	m := initKeyRenewalManager()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending[deviceID] = &pendingKeyRenewal{
		NewPublicKey:      newPublicKey,
		NewSharedSecret:   newSharedSecret,
		NewEncryptSession: newSession,
		CreatedAt:         time.Now(),
	}
}

// GetPendingKeyRenewal retrieves buffered key renewal data
func GetPendingKeyRenewal(deviceID string) ([]byte, []byte, *encryption.Session, bool) {
	m := initKeyRenewalManager()
	m.mu.Lock()
	defer m.mu.Unlock()
	if renewal, ok := m.pending[deviceID]; ok {
		return renewal.NewPublicKey, renewal.NewSharedSecret, renewal.NewEncryptSession, true
	}
	return nil, nil, nil, false
}

// StorePreviousEncryption stores old encryption session for device (auto-expires after 30s)
func StorePreviousEncryption(deviceID string, session *encryption.Session) {
	m := initKeyRenewalManager()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.previous[deviceID] = &previousEncryption{
		Session:   session,
		CreatedAt: time.Now(),
	}
}

// GetPreviousEncryption retrieves old encryption session for device
func GetPreviousEncryption(deviceID string) (*encryption.Session, bool) {
	m := initKeyRenewalManager()
	m.mu.Lock()
	defer m.mu.Unlock()
	if prev, ok := m.previous[deviceID]; ok {
		return prev.Session, true
	}
	return nil, false
}
