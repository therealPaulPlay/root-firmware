package logger

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"root-firmware/pkg/globals"
)

const maxLogs = 1000

type Entry struct {
	Time string `json:"time"`
	Msg  string `json:"msg"`
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

	wr.logs = append(wr.logs, Entry{
		Time: time.Now().Format("15:04:05"),
		Msg:  string(p),
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
	os.WriteFile(globals.LogsPath, data, 0644)
}
