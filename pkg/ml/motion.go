package ml

import (
	"image"
	"math"
)

const (
	pixelChangeThreshold = 12   // Min brightness difference for a pixel to count as "changed"
	minChangedPixels     = 225  // Min changed pixels to trigger motion (160x90 = 14400 total, ~1.5% / ~20x12px)
	backgroundAlpha      = 0.20 // Background update rate per frame (higher = faster adaptation)
	scaledWidth          = 160  // Downscale for performance
	scaledHeight         = 90   // 16:9 aspect ratio
)

type motionDetector struct {
	background []float32 // Grayscale background model
}

func newMotionDetector() *motionDetector {
	return &motionDetector{}
}

func (m *motionDetector) detectMotion(img image.Image) bool {
	gray := toGrayscale(img)

	// Initialize background on first frame
	if m.background == nil {
		m.background = gray
		return false
	}

	// Count changed pixels and update background
	changedPixels := 0
	for i := range gray {
		diff := math.Abs(float64(gray[i] - m.background[i]))
		if diff > pixelChangeThreshold {
			changedPixels++
		}
		m.background[i] = m.background[i]*(1-backgroundAlpha) + gray[i]*backgroundAlpha
	}

	return changedPixels >= minChangedPixels
}

func toGrayscale(img image.Image) []float32 {
	gray := make([]float32, scaledWidth*scaledHeight)
	bounds := img.Bounds()

	for y := range scaledHeight {
		for x := range scaledWidth {
			srcX := bounds.Min.X + (x * bounds.Dx() / scaledWidth)
			srcY := bounds.Min.Y + (y * bounds.Dy() / scaledHeight)
			r, g, b, _ := img.At(srcX, srcY).RGBA()
			// Weighted grayscale conversion
			gray[y*scaledWidth+x] = float32(0.299*float64(r)+0.587*float64(g)+0.114*float64(b)) / 256.0
		}
	}

	return gray
}
