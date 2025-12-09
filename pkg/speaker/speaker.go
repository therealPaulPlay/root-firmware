package speaker

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

type Speaker struct {
	streaming  bool
	streamPipe io.WriteCloser
	streamCmd  *exec.Cmd
	mu         sync.Mutex
}

var instance *Speaker
var once sync.Once

func Init() {
	once.Do(func() {
		instance = &Speaker{}
	})
}

func Get() *Speaker {
	if instance == nil {
		panic("speaker not initialized - call Init() first")
	}
	return instance
}

// PlayFile plays an audio file (WAV format)
func (s *Speaker) PlayFile(filePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("audio file not found: %s", filePath)
	}

	cmd := exec.Command("aplay", "-D", "plughw:0,0", filePath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to play audio: %w", err)
	}

	return nil
}

// StartStreaming starts audio streaming for babyphone mode
// Returns a writer - write PCM audio data (16-bit, 44.1kHz, mono)
// Call Close() on the writer to stop streaming
func (s *Speaker) StartStreaming() (io.WriteCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.streaming {
		return nil, fmt.Errorf("already streaming")
	}

	cmd := exec.Command("aplay", "-D", "plughw:0,0", "-f", "S16_LE", "-r", "44100", "-c", "1")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start streaming: %w", err)
	}

	s.streaming = true
	s.streamPipe = stdin
	s.streamCmd = cmd

	return &streamWriter{speaker: s}, nil
}

func (s *Speaker) IsStreaming() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streaming
}

type streamWriter struct {
	speaker *Speaker
}

func (w *streamWriter) Write(p []byte) (int, error) {
	w.speaker.mu.Lock()
	defer w.speaker.mu.Unlock()

	if w.speaker.streamPipe == nil {
		return 0, fmt.Errorf("stream closed")
	}

	return w.speaker.streamPipe.Write(p)
}

func (w *streamWriter) Close() error {
	w.speaker.mu.Lock()
	defer w.speaker.mu.Unlock()

	if w.speaker.streamPipe != nil {
		w.speaker.streamPipe.Close()
	}

	if w.speaker.streamCmd != nil && w.speaker.streamCmd.Process != nil {
		w.speaker.streamCmd.Process.Kill()
		w.speaker.streamCmd.Wait()
	}

	w.speaker.streaming = false
	w.speaker.streamPipe = nil
	w.speaker.streamCmd = nil

	return nil
}