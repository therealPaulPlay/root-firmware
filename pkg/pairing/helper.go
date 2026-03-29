package pairing

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"sync"
	"time"

	"root-firmware/pkg/devices"
	"root-firmware/pkg/qr"
	"root-firmware/pkg/record"
	"root-firmware/pkg/sfx"
	"root-firmware/pkg/wifi"
)

const (
	codeExpiry     = 15 * time.Minute
	rateLimitDelay = 1 * time.Second
)

type PairingCode struct {
	Code         string
	ExpiresAt    time.Time
	CodeVerified bool
}

type Pairing struct {
	mu              sync.Mutex
	code            *PairingCode
	lastScanAttempt time.Time
	qrScanner       *qr.Scanner
}

var helperInstance *Pairing
var helperOnce sync.Once

func InitHelper() {
	helperOnce.Do(func() {
		helperInstance = &Pairing{
			qrScanner: qr.NewScanner(),
		}
	})
}

func GetHelper() *Pairing {
	if helperInstance == nil {
		panic("pairing helper not initialized - call InitHelper() first")
	}
	return helperInstance
}

// GenerateCode creates a new 8-digit pairing code
func (b *Pairing) GenerateCode() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	n, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
	if err != nil {
		log.Printf("Pairing: Failed to generate code: %v", err)
		return ""
	}
	b.code = &PairingCode{
		Code:         fmt.Sprintf("%08d", n.Int64()),
		ExpiresAt:    time.Now().Add(codeExpiry),
		CodeVerified: false,
	}

	return b.code.Code
}

// ScanQRCode captures a frame and attempts to read a QR code
// Verifies it matches the expected code and marks it as verified
func (b *Pairing) ScanQRCode() error {
	b.mu.Lock()

	// Rate limiting
	if time.Since(b.lastScanAttempt) < rateLimitDelay {
		b.mu.Unlock()
		return fmt.Errorf("rate limited: wait %v between scans", rateLimitDelay)
	}
	b.lastScanAttempt = time.Now()

	// Check if code exists and hasn't expired
	if b.code == nil || time.Now().After(b.code.ExpiresAt) {
		b.mu.Unlock()
		return fmt.Errorf("no active pairing code")
	}

	expectedCode := b.code.Code
	b.mu.Unlock()

	// Capture frame for QR code detection
	frame, err := record.Get().CapturePreview(1280, 720)
	if err != nil {
		return fmt.Errorf("failed to capture frame: %w", err)
	}

	// Scan for QR code
	scannedCode, err := b.qrScanner.Scan(frame)
	if err != nil {
		return fmt.Errorf("no QR code found")
	}

	// Verify code matches and mark as verified
	b.mu.Lock()
	defer b.mu.Unlock()

	// Re-check code still exists and matches what we read earlier
	if b.code == nil || b.code.Code != expectedCode {
		return fmt.Errorf("pairing code changed during scan")
	}

	if scannedCode != expectedCode {
		return fmt.Errorf("code mismatch")
	}

	// Mark code as verified
	b.code.CodeVerified = true
	log.Printf("Pairing: QR code verified successfully")
	return nil
}

// ScanWiFiNetworks scans for available WiFi networks with a timeout
func (b *Pairing) ScanWiFiNetworks() ([]wifi.Network, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type scanResult struct {
		networks []wifi.Network
		err      error
	}
	resultChan := make(chan scanResult, 1)
	go func() {
		networks, err := wifi.Get().Scan()
		select {
		case resultChan <- scanResult{networks, err}:
		case <-ctx.Done():
		}
	}()

	var result scanResult
	select {
	case result = <-resultChan:
		if result.err != nil {
			return nil, fmt.Errorf("failed to scan WiFi networks: %w", result.err)
		}
	case <-ctx.Done():
		return nil, fmt.Errorf("WiFi scan timed out after 10 seconds")
	}

	return result.networks, nil
}

// PairDevice pairs a device after QR code verification
func (b *Pairing) PairDevice(deviceID, deviceName string, devicePublicKey []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Verify code was scanned and verified via QR
	if b.code == nil || !b.code.CodeVerified || time.Now().After(b.code.ExpiresAt) {
		return fmt.Errorf("code not verified or expired")
	}

	// Add device with its public key
	if err := devices.Get().Add(deviceID, deviceName, devicePublicKey); err != nil {
		return fmt.Errorf("failed to add device: %w", err)
	}

	// Invalidate code after successful pairing
	b.code = nil

	// Play success sound
	sfx.Get().PlayPairingSuccess()

	return nil
}
