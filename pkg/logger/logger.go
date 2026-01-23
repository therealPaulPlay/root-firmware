package logger

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"root-firmware/pkg/fsutil"
	"root-firmware/pkg/globals"
)

const maxLogs = 1000
const maxLogMsgSize = 2500 // Max characters per log message

type Entry struct {
	Timestamp time.Time `json:"timestamp"`
	Msg       string    `json:"msg"`
}

type writer struct {
	mu   sync.Mutex
	logs []Entry
}

var w *writer

func Init() {
	w = &writer{logs: load()}
	log.SetOutput(io.MultiWriter(os.Stdout, w))
}

func (wr *writer) Write(p []byte) (int, error) {
	wr.mu.Lock()
	defer wr.mu.Unlock()

	msg := string(p)
	if len(msg) > maxLogMsgSize {
		msg = msg[:maxLogMsgSize] + "... (truncated)"
	}

	wr.logs = append(wr.logs, Entry{
		Timestamp: time.Now().UTC(),
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
	json.Unmarshal(data, &logs)
	return logs
}

func save(logs []Entry) {
	data, _ := json.Marshal(logs)
	fsutil.AtomicWrite(globals.LogsPath, data, 0644)
}
