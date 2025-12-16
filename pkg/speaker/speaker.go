package speaker

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

type Speaker struct {
	streaming bool
	streamPipe io.WriteCloser
	streamCmd *exec.Cmd
	playCmd   *exec.Cmd
	mu        sync.Mutex
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

	cmd := exec.Command("aplay", "-D", "default", filePath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to play audio: %w", err)
	}

	return nil
}

// IsStreaming returns whether audio streaming is currently active
func (s *Speaker) IsStreaming() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streaming
}

// WriteChunk writes an AAC audio chunk (ADTS format) to the speaker
// Automatically starts streaming on first write
func (s *Speaker) WriteChunk(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Start streaming if not already active
	if !s.streaming {
		// Use ffmpeg to decode AAC and pipe to aplay
		cmd := exec.Command("ffmpeg",
			"-f", "adts",
			"-i", "pipe:0",
			"-f", "s16le",
			"-ar", "44100",
			"-ac", "1",
			"pipe:1",
		)

		// Connect stdin for AAC input
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return fmt.Errorf("failed to create pipe: %w", err)
		}

		// Connect stdout to aplay
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			stdin.Close()
			return fmt.Errorf("failed to create stdout pipe: %w", err)
		}

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start ffmpeg: %w", err)
		}

		// Start aplay to play the decoded audio
		playCmd := exec.Command("aplay", "-D", "default", "-f", "S16_LE", "-r", "44100", "-c", "1")
		playCmd.Stdin = stdout

		if err := playCmd.Start(); err != nil {
			cmd.Process.Kill()
			return fmt.Errorf("failed to start aplay: %w", err)
		}

		s.streaming = true
		s.streamPipe = stdin
		s.streamCmd = cmd
		s.playCmd = playCmd
	}

	// Write AAC chunk to ffmpeg
	if _, err := s.streamPipe.Write(data); err != nil {
		return fmt.Errorf("failed to write audio: %w", err)
	}

	return nil
}

// StopStream stops the audio stream
func (s *Speaker) StopStream() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.streaming {
		return nil
	}

	if s.streamPipe != nil {
		s.streamPipe.Close()
	}

	if s.streamCmd != nil && s.streamCmd.Process != nil {
		s.streamCmd.Process.Kill()
		s.streamCmd.Wait()
	}

	if s.playCmd != nil && s.playCmd.Process != nil {
		s.playCmd.Process.Kill()
		s.playCmd.Wait()
	}

	s.streaming = false
	s.streamPipe = nil
	s.streamCmd = nil
	s.playCmd = nil

	return nil
}
