package relaycomm

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"time"

	rootproto "github.com/therealPaulPlay/root-e2ee-protocol/go-server"

	"root-firmware/pkg/config"
)

// decryptFileToReader decrypts a file and returns a reader for the plaintext content
func decryptFileToReader(filePath string, key []byte) (io.Reader, int64, error) {
	session, err := rootproto.SessionFromKey(key)
	if err != nil {
		return nil, 0, err
	}

	ciphertext, err := os.ReadFile(filePath)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read file: %w", err)
	}

	plaintext, err := session.Decrypt(ciphertext, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to decrypt: %w", err)
	}

	return bytes.NewReader(plaintext), int64(len(plaintext)), nil
}

// SendFileInChunksAsync reads a file and pushes it to the client in chunks
func SendFileInChunksAsync(clientID, filePath, fileType string, metadata map[string]any, onComplete func()) {
	go func() {
		const chunkSize = 1 * 1024 * 1024                 // 1MB chunks
		const delayBetweenChunks = 250 * time.Millisecond // 4/s rate limit = ~4MB/sec throughput

		productPrivateKey, err := config.Get().GetProductPrivateKey()
		if err != nil {
			pushFileError(clientID, fmt.Sprintf("Failed to get decryption key: %v", err))
			return
		}

		reader, fileSize, err := decryptFileToReader(filePath, productPrivateKey)
		if err != nil {
			pushFileError(clientID, fmt.Sprintf("Failed to decrypt file: %v", err))
			return
		}

		totalChunks := int((fileSize + chunkSize - 1) / chunkSize)
		buffer := make([]byte, chunkSize)

		for chunkIndex := range totalChunks {
			n, err := io.ReadFull(reader, buffer)
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				pushFileError(clientID, fmt.Sprintf("Failed to read chunk: %v", err))
				return
			}
			if n == 0 {
				break
			}

			payload := map[string]any{
				"success":     true,
				"fileType":    fileType,
				"chunkIndex":  chunkIndex,
				"totalChunks": totalChunks,
				"chunk":       buffer[:n],
			}
			if metadata != nil {
				payload["metadata"] = metadata
			}
			_ = Get().server.Push(clientID, MsgFileChunk, payload, lowPrioWriteFn())

			if chunkIndex < totalChunks-1 {
				time.Sleep(delayBetweenChunks)
			}
		}

		if onComplete != nil {
			onComplete()
		}
	}()
}

func pushFileError(clientID, errorMsg string) {
	_ = Get().server.Push(clientID, MsgFileChunk, map[string]any{
		"success": false,
		"error":   errorMsg,
	}, lowPrioWriteFn())
}
