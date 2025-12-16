package record

import (
	"fmt"
	"io"
	"os/exec"
	"sync"

	"root-firmware/pkg/config"
)

type Recorder struct {
	recording      bool
	streaming      bool
	recordCmd      *exec.Cmd
	videoStreamCmd *exec.Cmd
	audioStreamCmd *exec.Cmd
	mu             sync.Mutex
}

type StreamOutput struct {
	Video io.ReadCloser
	Audio io.ReadCloser // nil if microphone disabled
}

var instance *Recorder
var once sync.Once

func Init() {
	once.Do(func() {
		instance = &Recorder{}
	})
}

func Get() *Recorder {
	if instance == nil {
		panic("recorder not initialized - call Init() first")
	}
	return instance
}

func micEnabled() bool {
	val, ok := config.Get().GetKey("microphone_enabled")
	if !ok {
		return true // Default enabled
	}
	b, ok := val.(bool)
	return ok && b
}

// IsStreamingOrRecording returns true if camera is in use
func (r *Recorder) IsStreamingOrRecording() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recording || r.streaming
}

// StartRecording starts recording video (and audio if mic enabled) to file
func (r *Recorder) StartRecording(outputPath string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.recording {
		return fmt.Errorf("already recording")
	}

	if r.streaming {
		return fmt.Errorf("camera in use (streaming)")
	}

	var args []string
	if micEnabled() {
		args = []string{
			"-f", "v4l2", "-i", "/dev/video0",
			"-f", "alsa", "-i", "default",
			"-c:v", "h264_v4l2m2m",
			"-c:a", "aac",
			"-y", outputPath,
		}
	} else {
		args = []string{
			"-f", "v4l2", "-i", "/dev/video0",
			"-c:v", "h264_v4l2m2m",
			"-y", outputPath,
		}
	}

	cmd := exec.Command("ffmpeg", args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start recording: %w", err)
	}

	r.recording = true
	r.recordCmd = cmd
	return nil
}

func (r *Recorder) StopRecording() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.recording {
		return nil
	}

	if r.recordCmd != nil && r.recordCmd.Process != nil {
		r.recordCmd.Process.Kill()
		r.recordCmd.Wait()
	}

	r.recording = false
	r.recordCmd = nil
	return nil
}

// StartStream starts live streaming
func (r *Recorder) StartStream() (*StreamOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.streaming {
		return nil, fmt.Errorf("already streaming")
	}

	// Stop any active recording to give stream priority
	if r.recording && r.recordCmd != nil && r.recordCmd.Process != nil {
		r.recordCmd.Process.Kill()
		r.recordCmd.Wait()
		r.recording = false
		r.recordCmd = nil
	}

	videoCmd := exec.Command("ffmpeg",
		"-f", "v4l2", "-i", "/dev/video0",
		"-c:v", "h264_v4l2m2m",
		"-f", "h264",
		"pipe:1",
	)

	videoOut, err := videoCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create video pipe: %w", err)
	}

	if err := videoCmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start video stream: %w", err)
	}

	output := &StreamOutput{Video: videoOut}
	r.videoStreamCmd = videoCmd

	if micEnabled() {
		audioCmd := exec.Command("ffmpeg",
			"-f", "alsa", "-i", "default",
			"-c:a", "aac",
			"-f", "adts",
			"pipe:1",
		)

		audioOut, err := audioCmd.StdoutPipe()
		if err != nil {
			videoCmd.Process.Kill()
			return nil, fmt.Errorf("failed to create audio pipe: %w", err)
		}

		if err := audioCmd.Start(); err != nil {
			videoCmd.Process.Kill()
			return nil, fmt.Errorf("failed to start audio stream: %w", err)
		}

		output.Audio = audioOut
		r.audioStreamCmd = audioCmd
	}

	r.streaming = true
	return output, nil
}

func (r *Recorder) StopStream() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.streaming {
		return nil
	}

	if r.videoStreamCmd != nil && r.videoStreamCmd.Process != nil {
		r.videoStreamCmd.Process.Kill()
		r.videoStreamCmd.Wait()
	}

	if r.audioStreamCmd != nil && r.audioStreamCmd.Process != nil {
		r.audioStreamCmd.Process.Kill()
		r.audioStreamCmd.Wait()
	}

	r.streaming = false
	r.videoStreamCmd = nil
	r.audioStreamCmd = nil
	return nil
}

// SetMicrophoneEnabled enables or disables microphone
// Changes take effect on next recording/stream start
func (r *Recorder) SetMicrophoneEnabled(enabled bool) error {
	return config.Get().SetKey("microphone_enabled", enabled)
}

// CapturePreview captures a single frame as JPEG
func (r *Recorder) CapturePreview() ([]byte, error) {
	cmd := exec.Command("ffmpeg",
		"-f", "v4l2", "-i", "/dev/video0",
		"-frames:v", "1",
		"-f", "image2pipe",
		"-c:v", "mjpeg",
		"pipe:1",
	)

	return cmd.Output()
}
