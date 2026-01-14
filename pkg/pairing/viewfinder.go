package pairing

import (
	"encoding/base64"
)

const (
	viewfinderWidth  = 96
	viewfinderHeight = 54
)

// GetViewfinderChunks returns chunked 3-bit grayscale bitmap from raw grayscale data
func GetViewfinderChunks(grayData []byte) ([]map[string]any, error) {
	// Convert to 3-bit grayscale (8 shades)
	bitLen := (len(grayData) * 3 + 7) / 8
	bitData := make([]byte, bitLen)

	for i := 0; i < len(grayData); i += 8 {
		// Process 8 pixels at a time (24 bits = 3 bytes)
		end := min(i+8, len(grayData))
		bitBuf := uint32(0)
		bitCount := 0
		outIdx := (i * 3) / 8

		for j := i; j < end; j++ {
			bitBuf = (bitBuf << 3) | uint32(grayData[j]>>5)
			bitCount += 3
		}

		// Write out complete bytes
		for bitCount >= 8 {
			bitCount -= 8
			bitData[outIdx] = uint8(bitBuf >> bitCount)
			outIdx++
			bitBuf &= (1 << bitCount) - 1
		}

		// Handle remaining bits
		if bitCount > 0 && outIdx < len(bitData) {
			bitData[outIdx] = uint8(bitBuf << (8 - bitCount))
		}
	}

	// Base64 encode the entire thing once
	encoded := base64.StdEncoding.EncodeToString(bitData)

	// Chunk the base64 string into 90-char pieces
	const size = 90 // Leave room for JSON overhead
	total := (len(encoded) + size - 1) / size
	chunks := make([]map[string]any, 0, total)

	for i := range total {
		start := i * size
		end := min(start+size, len(encoded))
		chunks = append(chunks, map[string]any{
			"data":  encoded[start:end],
			"index": i,
		})
	}

	return chunks, nil
}
