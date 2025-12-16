package ml

import (
	"bytes"
	"image"
	"math"
)

const (
	motionThreshold  = 12   // Pixel difference threshold (scaled for smaller resolution)
	minChangedPixels = 200  // Minimum pixels changed (160x120 = 19200 pixels, ~1% threshold)
	backgroundAlpha  = 0.05 // Background update rate (slower = more stable)
	scaledWidth      = 160  // Downscale for performance
	scaledHeight     = 120
)

type motionDetector struct {
	background []float32 // Grayscale background model
}

func newMotionDetector() *motionDetector {
	return &motionDetector{}
}

func (m *motionDetector) detectMotion(jpegData []byte) (bool, error) {
	gray, err := toGrayscale(jpegData)
	if err != nil {
		return false, err
	}

	// Initialize background on first frame
	if m.background == nil {
		m.background = gray
		return false, nil
	}

	// Count changed pixels and update background
	changedPixels := 0
	for i := range gray {
		diff := math.Abs(float64(gray[i] - m.background[i]))
		if diff > motionThreshold {
			changedPixels++
		}
		m.background[i] = m.background[i]*(1-backgroundAlpha) + gray[i]*backgroundAlpha
	}

	return changedPixels >= minChangedPixels, nil
}

func (m *motionDetector) reset(jpegData []byte) error {
	gray, err := toGrayscale(jpegData)
	if err != nil {
		return err
	}
	m.background = gray
	return nil
}

func toGrayscale(jpegData []byte) ([]float32, error) {
	img, _, err := image.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return nil, err
	}

	gray := make([]float32, scaledWidth*scaledHeight)
	bounds := img.Bounds()

	for y := 0; y < scaledHeight; y++ {
		for x := 0; x < scaledWidth; x++ {
			srcX := bounds.Min.X + (x * bounds.Dx() / scaledWidth)
			srcY := bounds.Min.Y + (y * bounds.Dy() / scaledHeight)
			r, g, b, _ := img.At(srcX, srcY).RGBA()
			// Weighted grayscale conversion
			gray[y*scaledWidth+x] = float32(0.299*float64(r)+0.587*float64(g)+0.114*float64(b)) / 256.0
		}
	}

	return gray, nil
}
