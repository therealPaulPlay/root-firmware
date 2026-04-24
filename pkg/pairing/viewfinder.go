package pairing

import "root-firmware/pkg/globals"

// GetViewfinderChunks returns chunked 2-bit grayscale bitmap from raw grayscale data
func GetViewfinderChunks(grayData []byte) ([]map[string]any, error) {
	width, height := globals.ViewfinderWidth, globals.ViewfinderHeight

	// Convert to 2-bit grayscale (4 shades) — 4 pixels per byte
	bitLen := (len(grayData) + 3) / 4
	bitData := make([]byte, bitLen)

	for i, v := range grayData {
		twoBit := v >> 6 // Map 0-255 to 0-3
		byteIdx := i / 4
		shift := 6 - (i%4)*2
		bitData[byteIdx] |= twoBit << shift
	}

	// Chunk raw bytes - minimum BLE MTU is ~128B, leave headroom for other fields
	const chunkSize = 90
	total := (len(bitData) + chunkSize - 1) / chunkSize
	chunks := make([]map[string]any, 0, total)

	for i := range total {
		start := i * chunkSize
		end := min(start+chunkSize, len(bitData))
		chunk := map[string]any{
			"data":  bitData[start:end],
			"index": i,
		}
		if i == 0 {
			chunk["width"] = width
			chunk["height"] = height
		}
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}
