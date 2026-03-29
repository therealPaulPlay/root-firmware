package qr

import (
	"fmt"
	"image"
	"math"

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

// Scan decodes a QR code from an image
// Tries the original first, then falls back to a contrast-stretched
// version to handle slight blur
func (s *Scanner) Scan(img image.Image) (string, error) {
	if text, err := s.decode(img); err == nil {
		return text, nil
	}

	// If regular decode fails, try on contrast-stretched image
	stretched, err := stretchContrast(img)
	if err != nil {
		return "", err
	}
	if text, err := s.decode(stretched); err == nil {
		return text, nil
	}

	return "", fmt.Errorf("no QR code found")
}

func (s *Scanner) decode(img image.Image) (string, error) {
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return "", err
	}
	result, err := s.reader.Decode(bmp, nil)
	if err != nil {
		return "", err
	}
	return result.GetText(), nil
}

// stretchContrast applies a sigmoidal contrast stretch to a grayscale
// conversion of the image, pushing pixels near mid-gray toward
// black or white to sharpen the distinction for the QR binarizer
func stretchContrast(img image.Image) (*image.Gray, error) {
	ycbcr, ok := img.(*image.YCbCr)
	if !ok {
		return nil, fmt.Errorf("unsupported image format %T, expected *image.YCbCr", img)
	}

	bounds := img.Bounds()
	out := image.NewGray(bounds)

	// Build a lookup table for the sigmoid stretch
	var lut [256]uint8
	for i := range lut {
		x := float64(i) / 255.0
		// Sigmoid centered at 0.5 with steepness 10
		s := 1.0 / (1.0 + math.Exp(-10*(x-0.5)))
		v := min(int(s*255.0), 255)
		lut[i] = uint8(v)
	}

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		yi := ycbcr.YOffset(bounds.Min.X, y)
		oi := out.PixOffset(bounds.Min.X, y)
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			out.Pix[oi] = lut[ycbcr.Y[yi]]
			yi++
			oi++
		}
	}

	return out, nil
}
