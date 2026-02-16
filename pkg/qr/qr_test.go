package qr

import (
	"os"
	"testing"
)

var testQRImage []byte

func TestMain(m *testing.M) {
	var err error
	testQRImage, err = os.ReadFile("testdata/test.jpg")
	if err != nil {
		panic("failed to load test QR image: " + err.Error())
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

func TestScan_InvalidJPEG(t *testing.T) {
	scanner := NewScanner()

	_, err := scanner.Scan([]byte("not a jpeg"))
	if err == nil {
		t.Error("Scan() should error on invalid JPEG")
	}
}

func TestScan_EmptyData(t *testing.T) {
	scanner := NewScanner()

	_, err := scanner.Scan([]byte{})
	if err == nil {
		t.Error("Scan() should error on empty data")
	}
}
