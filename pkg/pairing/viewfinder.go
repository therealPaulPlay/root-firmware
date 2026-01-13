package pairing

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/jpeg"
)

const (
	viewfinderWidth  = 48
	viewfinderHeight = 27
)

// jpegTo3BitGray converts JPEG to 3-bit grayscale (8 shades)
func jpegTo3BitGray(jpegData []byte) (string, error) {
	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return "", err
	}

	bounds := img.Bounds()
	totalPixels := viewfinderWidth * viewfinderHeight
	out := make([]byte, (totalPixels*3+7)/8)

	outIdx := 0
	bitBuf := uint32(0)
	bitCount := 0

	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			gray := (r*299 + g*587 + b*114) / 1000 // 0-65535
			threeBit := uint32(gray / 8192)        // 0-7

			bitBuf = (bitBuf << 3) | threeBit
			bitCount += 3

			if bitCount >= 8 {
				bitCount -= 8
				out[outIdx] = uint8(bitBuf >> bitCount)
				outIdx++
				bitBuf &= (1 << bitCount) - 1
			}
		}
	}

	if bitCount > 0 {
		out[outIdx] = uint8(bitBuf << (8 - bitCount))
	}

	return base64.StdEncoding.EncodeToString(out), nil
}

// chunkData splits base64 string into chunks <110 bytes each
func chunkData(data string) []map[string]any {
	const size = 90 // Leave room for JSON overhead
	total := (len(data) + size - 1) / size
	chunks := make([]map[string]any, total)

	for i := range total {
		start := i * size
		end := min(start+size, len(data))
		chunks[i] = map[string]any{
			"data":  data[start:end],
			"index": i,
		}
	}

	return chunks
}

// GetViewfinderChunks returns chunked 3-bit grayscale bitmap from JPEG
func GetViewfinderChunks(jpegData []byte) ([]map[string]any, error) {
	bitmap, err := jpegTo3BitGray(jpegData)
	if err != nil {
		return nil, fmt.Errorf("convert failed: %w", err)
	}
	return chunkData(bitmap), nil
}
