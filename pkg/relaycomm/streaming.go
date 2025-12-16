package relaycomm

import (
	"encoding/base64"
	"fmt"
	"io"
)

const chunkSize = 64 * 1024 // 64KB chunks

// StreamReader streams data from a reader to the relay server in base64-encoded chunks
func StreamReader(ctx *HandlerContext, reader io.Reader, messageType string) error {
	buffer := make([]byte, chunkSize)
	chunkIndex := 0

	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			encoded := base64.StdEncoding.EncodeToString(buffer[:n])
			if sendErr := SendEncrypted(ctx, messageType, map[string]any{
				"success":    true,
				"chunk":      encoded,
				"chunkIndex": chunkIndex,
				"done":       false,
			}); sendErr != nil {
				return fmt.Errorf("failed to send chunk: %w", sendErr)
			}
			chunkIndex++
		}

		if err == io.EOF {
			SendEncrypted(ctx, messageType, map[string]any{
				"success": true,
				"done":    true,
			})
			return nil
		}

		if err != nil {
			return fmt.Errorf("failed to read: %w", err)
		}
	}
}
