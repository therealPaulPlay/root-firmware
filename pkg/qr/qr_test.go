package qr

import (
	"bytes"
	"image"
	_ "image/jpeg"
	"os"
	"testing"
)

var testQRImage image.Image

func TestMain(m *testing.M) {
	data, err := os.ReadFile("testdata/test.jpg")
	if err != nil {
		panic("failed to load test QR image: " + err.Error())
	}
	testQRImage, _, err = image.Decode(bytes.NewReader(data))
	if err != nil {
		panic("failed to decode test QR image: " + err.Error())
	}
	os.Exit(m.Run())
}

func TestNewScanner(t *testing.T) {
	scanner := NewScanner()
	if scanner == nil {
		t.Error("NewScanner() returned nil")
	}
	if scanner.reader == nil {
		t.Error("scanner.reader is nil")
	}
}

func TestScan_ValidQRCode(t *testing.T) {
	scanner := NewScanner()

	result, err := scanner.Scan(testQRImage)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	expected := "ABC123"
	if result != expected {
		t.Errorf("Scan() = %q, want %q", result, expected)
	}
}

func TestScan_NoQRCode(t *testing.T) {
	scanner := NewScanner()

	// Blank image with no QR code
	blank := image.NewRGBA(image.Rect(0, 0, 100, 100))
	_, err := scanner.Scan(blank)
	if err == nil {
		t.Error("Scan() should error when no QR code is present")
	}
}
