package relaycomm

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

type fileTransfer struct {
	mu       sync.RWMutex
	deviceID string
	filePath string
	ctx      *HandlerContext
}

var activeTransfers = struct {
	mu        sync.RWMutex
	transfers []*fileTransfer
}{
	transfers: []*fileTransfer{},
}

// UpdateFileTransferContext updates the encryption context for an ongoing file transfer
func UpdateFileTransferContext(deviceID string, newCtx *HandlerContext) {
	activeTransfers.mu.RLock()
	defer activeTransfers.mu.RUnlock()

	for _, transfer := range activeTransfers.transfers {
		if transfer.deviceID == deviceID {
			transfer.mu.Lock()
			transfer.ctx = newCtx
			transfer.mu.Unlock()
		}
	}
}

// SendFileInChunks reads a file and sends it in chunks
func SendFileInChunks(ctx *HandlerContext, msgType string, filePath string, fileType string, metadata map[string]any, onComplete func()) {
	go func() {
		const chunkSize = 1 * 1024 * 1024                 // 1MB chunks
		const delayBetweenChunks = 200 * time.Millisecond // 5/s rate limit = ~5MB/sec throughput

		// Register active transfer for context updates
		transfer := &fileTransfer{
			deviceID: ctx.DeviceID,
			filePath: filePath,
			ctx:      ctx,
		}
		activeTransfers.mu.Lock()
		activeTransfers.transfers = append(activeTransfers.transfers, transfer)
		activeTransfers.mu.Unlock()

		// Remove transfer from activeTransfers when complete
		defer func() {
			activeTransfers.mu.Lock()
			for i, t := range activeTransfers.transfers {
				if t == transfer {
					activeTransfers.transfers = append(activeTransfers.transfers[:i], activeTransfers.transfers[i+1:]...)
					break
				}
			}
			activeTransfers.mu.Unlock()
		}()

		file, err := os.Open(filePath)
		if err != nil {
			SendEncryptedError(ctx, msgType, ErrInternalError, fmt.Sprintf("Failed to open file: %v", err))
			return
		}
		defer file.Close()

		fileInfo, err := file.Stat()
		if err != nil {
			SendEncryptedError(ctx, msgType, ErrInternalError, fmt.Sprintf("Failed to stat file: %v", err))
			return
		}

		totalChunks := int((fileInfo.Size() + chunkSize - 1) / chunkSize)
		buffer := make([]byte, chunkSize)

		for chunkIndex := range totalChunks {
			n, err := file.Read(buffer)
			if err != nil && err != io.EOF {
				SendEncryptedError(ctx, msgType, ErrInternalError, fmt.Sprintf("Failed to read chunk: %v", err))
				return
			}
			if n == 0 {
				break
			}

			// Get current context (may have been updated by key renewal)
			transfer.mu.RLock()
			currentCtx := transfer.ctx
			transfer.mu.RUnlock()

			payload := map[string]any{
				"fileType":    fileType,
				"chunk":       base64.StdEncoding.EncodeToString(buffer[:n]),
				"chunkIndex":  chunkIndex,
				"totalChunks": totalChunks,
			}
			if metadata != nil {
				payload["metadata"] = metadata
			}
			SendEncryptedSuccess(currentCtx, msgType, payload)

			if chunkIndex < totalChunks-1 {
				time.Sleep(delayBetweenChunks)
			}
		}

		if onComplete != nil {
			onComplete()
		}
	}()
}
