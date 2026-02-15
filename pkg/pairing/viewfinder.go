package pairing

const (
	viewfinderWidth  = 96
	viewfinderHeight = 54
)

// GetViewfinderChunks returns chunked 3-bit grayscale bitmap from raw grayscale data
func GetViewfinderChunks(grayData []byte) ([]map[string]any, error) {
	// Convert to 3-bit grayscale (8 shades)
	bitLen := (len(grayData)*3 + 7) / 8
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

	// Chunk raw bytes - minimum BLE MTU is ~128B, leave headroom for other fields
	const chunkSize = 90
	total := (len(bitData) + chunkSize - 1) / chunkSize
	chunks := make([]map[string]any, 0, total)

	for i := range total {
		start := i * chunkSize
		end := min(start+chunkSize, len(bitData))
		chunks = append(chunks, map[string]any{
			"data":  bitData[start:end],
			"index": i,
		})
	}

	return chunks, nil
}
