package record

import (
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/sfx"
)

type Recorder struct {
	recording      bool
	streaming      bool
	recordCmd      *exec.Cmd
	videoStreamCmd *exec.Cmd
	audioStreamCmd *exec.Cmd
	ffmpegCmd      *exec.Cmd
	mu             sync.Mutex
	captureMu      sync.Mutex // Serialize camera captures to prevent "in use" errors
}

type StreamOutput struct {
	Video io.ReadCloser
	Audio io.ReadCloser // nil if microphone disabled
}

var instance *Recorder
var once sync.Once

func Init() error {
	var initErr error
	once.Do(func() {
		for _, cmd := range []string{"rpicam-vid", "rpicam-still", "ffmpeg"} {
			if _, err := exec.LookPath(cmd); err != nil {
				initErr = fmt.Errorf("ffmpeg and/or rpicam-apps are missing - please install them")
				return
			}
		}
		instance = &Recorder{}
	})
	return initErr
}

func Get() *Recorder {
	if instance == nil {
		panic("recorder not initialized - call Init() first")
	}
	return instance
}

func micEnabled() bool {
	val, ok := config.Get().GetKey("microphoneEnabled")
	if !ok {
		return true // Default enabled
	}
	b, ok := val.(bool)
	return ok && b
}

// StartRecording starts recording video to file
func (r *Recorder) StartRecording(outputPath string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.recording {
		return fmt.Errorf("already recording")
	}
	if r.streaming {
		return fmt.Errorf("camera in use (streaming)")
	}

	// Kill any rpicam-still processes (ML might be using camera)
	exec.Command("pkill", "-9", "rpicam-still").Run()
	time.Sleep(200 * time.Millisecond)

	cmd := exec.Command("rpicam-vid",
		"-t", "0", "-o", outputPath,
		"--codec", "h264", "-n",
	)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start recording: %w", err)
	}

	// Play sound effect if key is true
	if val, ok := config.Get().GetKey("playActiveCameraSound"); !ok || val.(bool) {
		sfx.Get().PlayRecording()
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

	// Stop any active recording
	if r.recording {
		if r.recordCmd != nil && r.recordCmd.Process != nil {
			r.recordCmd.Process.Kill()
		}
		r.recording = false
		r.recordCmd = nil
	}

	// Kill any rpicam-still processes (ML might be using camera)
	exec.Command("pkill", "-9", "rpicam-still").Run()
	time.Sleep(200 * time.Millisecond)

	// Start rpicam-vid to capture H.264
	cameraCmd := exec.Command("rpicam-vid",
		"-t", "0", "-o", "-",
		"--codec", "h264", "-n", "--inline", "--listen",
		"--framerate", "15", // 15 FPS for streaming
		"--width", "1920", "--height", "1080", // 1080p
	)

	// Pipe camera output to ffmpeg for fragmented MP4 conversion
	ffmpegCmd := exec.Command("ffmpeg",
		"-i", "pipe:0", // Read from stdin
		"-c:v", "copy", // Copy video codec (no re-encoding)
		"-f", "mp4", // MP4 format
		"-movflags", "frag_keyframe+empty_moov+default_base_moof", // Fragmented MP4
		"-frag_duration", "200000", // Fragment duration in microseconds (~200ms)
		"pipe:1", // Output to stdout
	)

	cameraPipe, err := cameraCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create camera pipe: %w", err)
	}

	ffmpegCmd.Stdin = cameraPipe

	videoOut, err := ffmpegCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create ffmpeg pipe: %w", err)
	}

	if err := cameraCmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start camera: %w", err)
	}
	r.videoStreamCmd = cameraCmd

	if err := ffmpegCmd.Start(); err != nil {
		r.cleanupStream()
		return nil, fmt.Errorf("failed to start ffmpeg: %w", err)
	}
	r.ffmpegCmd = ffmpegCmd

	output := &StreamOutput{Video: videoOut}

	if micEnabled() {
		audioCmd := exec.Command("ffmpeg",
			"-f", "alsa", "-i", "default",
			"-c:a", "aac", "-f", "adts", "pipe:1",
		)

		audioOut, err := audioCmd.StdoutPipe()
		if err != nil {
			r.cleanupStream()
			return nil, err
		}

		if err := audioCmd.Start(); err != nil {
			r.cleanupStream()
			return nil, err
		}

		r.audioStreamCmd = audioCmd
		output.Audio = audioOut
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

	r.cleanupStream()
	return nil
}

func (r *Recorder) cleanupStream() {
	if r.ffmpegCmd != nil && r.ffmpegCmd.Process != nil {
		r.ffmpegCmd.Process.Kill()
		r.ffmpegCmd.Wait()
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
	r.ffmpegCmd = nil
}

// SetMicrophoneEnabled enables or disables microphone
// Changes take effect on next recording/stream start
func (r *Recorder) SetMicrophoneEnabled(enabled bool) error {
	return config.Get().SetKey("microphoneEnabled", enabled)
}

// CapturePreview captures a single frame as JPEG with configurable resolution
func (r *Recorder) CapturePreview() ([]byte, error) {
	return r.CapturePreviewWithResolution(640, 480)
}

// CapturePreviewWithResolution captures a single frame as JPEG at specified resolution
func (r *Recorder) CapturePreviewWithResolution(width, height int) ([]byte, error) {
	r.captureMu.Lock()
	defer r.captureMu.Unlock()

	// Skip if camera is busy
	r.mu.Lock()
	busy := r.streaming || r.recording
	r.mu.Unlock()
	if busy {
		return nil, fmt.Errorf("currently streaming or recording")
	}

	cmd := exec.Command("rpicam-still",
		"-o", "-", "-t", "2000",
		"--width", fmt.Sprintf("%d", width),
		"--height", fmt.Sprintf("%d", height),
		"-n", "--encoding", "jpg",
	)

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			log.Printf("Recorder: rpicam-still failed: %v, stderr: %s", err, string(exitErr.Stderr))
		} else {
			log.Printf("Recorder: rpicam-still failed: %v", err)
		}
		return nil, fmt.Errorf("camera capture failed")
	}
	return output, nil
}
