package qr

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg" // Register JPEG decoder

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
)

// Scanner decodes QR codes from image data
type Scanner struct {
	reader gozxing.Reader
}

// NewScanner creates a new QR code scanner
func NewScanner() *Scanner {
	return &Scanner{
		reader: qrcode.NewQRCodeReader(),
	}
}

// Scan attempts to decode a QR code from JPEG image data
// Returns the decoded string or error if no QR code found
func (s *Scanner) Scan(jpegData []byte) (string, error) {
	// Decode JPEG to image.Image
	img, _, err := image.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return "", fmt.Errorf("failed to decode image: %w", err)
	}

	// Convert to gozxing BinaryBitmap
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return "", fmt.Errorf("failed to create bitmap: %w", err)
	}

	// Decode QR code
	result, err := s.reader.Decode(bmp, nil)
	if err != nil {
		return "", fmt.Errorf("no QR code found: %w", err)
	}

	return result.GetText(), nil
}
