package logger

import (
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/fxamacker/cbor/v2"

	"root-firmware/pkg/fsutil"
	"root-firmware/pkg/globals"
)

const maxLogs = 250
const maxLogMsgSize = 1500 // Max characters per log message

type Entry struct {
	Timestamp int64  `cbor:"timestamp"`
	Msg       string `cbor:"msg"`
}

type writer struct {
	mu   sync.Mutex
	logs []Entry
}

var w *writer

func Init() {
	os.MkdirAll(globals.FirmwareDataDir, 0755) // Ensure firmware data dir exists
	w = &writer{logs: load()}
	log.SetOutput(io.MultiWriter(os.Stdout, w))
	log.SetFlags(0) // Disable default timestamp prefix
}

func (wr *writer) Write(p []byte) (int, error) {
	wr.mu.Lock()
	defer wr.mu.Unlock()

	msg := string(p)
	if len(msg) > maxLogMsgSize {
		msg = msg[:maxLogMsgSize] + "... (truncated)"
	}

	wr.logs = append(wr.logs, Entry{
		Timestamp: time.Now().UnixMilli(),
		Msg:       msg,
	})

	if len(wr.logs) > maxLogs {
		wr.logs = wr.logs[1:]
	}

	save(wr.logs)
	return len(p), nil
}

func GetLogs() []Entry {
	if w == nil {
		return []Entry{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Entry{}, w.logs...)
}

func load() []Entry {
	data, err := os.ReadFile(globals.LogsPath)
	if err != nil {
		return []Entry{}
	}
	var logs []Entry
	if err := cbor.Unmarshal(data, &logs); err != nil {
		log.Printf("Logger: Failed to parse logs file: %v", err)
		return []Entry{}
	}
	return logs
}

// save persists logs to disk, errors are silently ignored because this is called
// from within the log writer — using log.Printf here would deadlock on the log mutex
func save(logs []Entry) {
	data, err := cbor.Marshal(logs)
	if err != nil {
		return
	}
	fsutil.AtomicWrite(globals.LogsPath, data, 0644)
}
