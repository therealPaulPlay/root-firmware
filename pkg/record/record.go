package record

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/sfx"
)

// keyframeBufferSize controls H.264 keyframe capture starting from SPS (NAL type 7).
//
// Preview extraction: Must contain complete I-frame (SPS+PPS+IDR) for artifact-free decoding.
// Smaller values risk corrupted/stretched frames in CapturePreview().
//
// Stream chunking: Larger buffers → fewer, larger MP4 fragments sent to relay server.
// 500KB yields ~5-6 chunks/sec, 256KB yields ~10 chunks/sec (more relay overhead).
const keyframeBufferSize = 500 * 1024 // 500KB

type Recorder struct {
	cameraCmd *exec.Cmd
	broadcast *Broadcast
	mu        sync.RWMutex
	recording bool
	recordCmd *exec.Cmd
	streaming bool
	streamCmd *exec.Cmd
}

type Broadcast struct {
	consumers   []*Consumer
	consumersMu sync.RWMutex
	latestFrame []byte
	frameMu     sync.RWMutex
}

type Consumer struct {
	writer io.WriteCloser
}

var instance *Recorder
var once sync.Once

func Init() error {
	var initErr error
	once.Do(func() {
		for _, cmd := range []string{"rpicam-vid", "ffmpeg"} {
			if _, err := exec.LookPath(cmd); err != nil {
				initErr = fmt.Errorf("ffmpeg and/or rpicam-vid are missing - please install them")
				return
			}
		}
		instance = &Recorder{}

		if err := instance.startCamera(); err != nil {
			initErr = fmt.Errorf("failed to start camera: %w", err)
			return
		}
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
		return true
	}
	b, ok := val.(bool)
	return ok && b
}

func (r *Recorder) startCamera() error {
	exec.Command("pkill", "-9", "rpicam-vid").Run()
	time.Sleep(200 * time.Millisecond)

	cameraCmd := exec.Command("rpicam-vid",
		"-t", "0", "-o", "-",
		"--codec", "h264", "-n", "--inline", "--listen",
		"--framerate", "15",
		"--width", "1920", "--height", "1080",
	)

	cameraOut, err := cameraCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create camera pipe: %w", err)
	}

	if err := cameraCmd.Start(); err != nil {
		return fmt.Errorf("failed to start camera: %w", err)
	}

	r.cameraCmd = cameraCmd
	r.broadcast = &Broadcast{consumers: make([]*Consumer, 0)}

	go r.broadcastLoop(cameraOut)
	return nil
}

func (r *Recorder) broadcastLoop(cameraOut io.ReadCloser) {
	buffer := make([]byte, 64*1024)
	keyframeBuffer := &bytes.Buffer{}
	capturingKeyframe := false

	for {
		n, err := cameraOut.Read(buffer)
		if n > 0 {
			data := buffer[:n]

			// Look for SPS NAL unit (type 7) to start capturing a keyframe
			for i := 0; i < len(data)-4; i++ {
				// Check for NAL start code: 0x00 0x00 0x00 0x01
				if data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 && i+4 < len(data) {
					nalType := data[i+4] & 0x1F
					if nalType == 7 { // SPS - start of keyframe
						keyframeBuffer.Reset()
						capturingKeyframe = true
						break
					}
				}
			}

			// Accumulate keyframe data
			if capturingKeyframe {
				keyframeBuffer.Write(data)
				if keyframeBuffer.Len() > keyframeBufferSize {
					r.broadcast.frameMu.Lock()
					r.broadcast.latestFrame = append([]byte(nil), keyframeBuffer.Bytes()...)
					r.broadcast.frameMu.Unlock()
					capturingKeyframe = false
				}
			}

			// Broadcast to consumers
			r.broadcast.consumersMu.RLock()
			for _, consumer := range r.broadcast.consumers {
				consumer.writer.Write(data)
			}
			r.broadcast.consumersMu.RUnlock()
		}

		if err != nil {
			log.Printf("Recorder: Camera error: %v, restarting...", err)

			r.broadcast.consumersMu.Lock()
			for _, consumer := range r.broadcast.consumers {
				consumer.writer.Close()
			}
			r.broadcast.consumersMu.Unlock()

			time.Sleep(1 * time.Second)
			r.restartCamera()
			return
		}
	}
}

func (r *Recorder) restartCamera() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cameraCmd != nil && r.cameraCmd.Process != nil {
		r.cameraCmd.Process.Kill()
	}

	time.Sleep(500 * time.Millisecond)
	if err := r.startCamera(); err != nil {
		log.Printf("Recorder: Failed to restart camera: %v", err)
	}
}

func (r *Recorder) addConsumer(writer io.WriteCloser) *Consumer {
	consumer := &Consumer{writer: writer}
	r.broadcast.consumersMu.Lock()
	r.broadcast.consumers = append(r.broadcast.consumers, consumer)
	r.broadcast.consumersMu.Unlock()
	return consumer
}

func (r *Recorder) removeConsumer(consumer *Consumer) {
	r.broadcast.consumersMu.Lock()
	defer r.broadcast.consumersMu.Unlock()

	for i, c := range r.broadcast.consumers {
		if c == consumer {
			r.broadcast.consumers = append(r.broadcast.consumers[:i], r.broadcast.consumers[i+1:]...)
			break
		}
	}
}

// startFFmpeg creates an ffmpeg process with optional output file
func startFFmpeg(input io.Reader, outputFile string) (*exec.Cmd, io.ReadCloser, error) {
	var cmd *exec.Cmd
	var outputReader io.ReadCloser

	if outputFile != "" {
		// Recording to file
		if micEnabled() {
			cmd = exec.Command("ffmpeg",
				"-f", "h264", "-i", "pipe:0",
				"-f", "alsa", "-i", "default",
				"-c:v", "copy", "-c:a", "aac",
				"-f", "mp4", outputFile,
			)
		} else {
			cmd = exec.Command("ffmpeg",
				"-f", "h264", "-i", "pipe:0",
				"-c:v", "copy",
				"-f", "mp4", outputFile,
			)
		}
		cmd.Stdin = input
	} else {
		// Streaming to pipe
		if micEnabled() {
			cmd = exec.Command("ffmpeg",
				"-f", "h264", "-i", "pipe:0",
				"-f", "alsa", "-i", "default",
				"-c:v", "copy", "-c:a", "aac",
				"-f", "mp4",
				"-movflags", "frag_keyframe+empty_moov+default_base_moof",
				"-frag_duration", "200000",
				"pipe:1",
			)
		} else {
			cmd = exec.Command("ffmpeg",
				"-f", "h264", "-i", "pipe:0",
				"-c:v", "copy",
				"-f", "mp4",
				"-movflags", "frag_keyframe+empty_moov+default_base_moof",
				"-frag_duration", "200000",
				"pipe:1",
			)
		}

		var err error
		outputReader, err = cmd.StdoutPipe()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create stdout pipe: %w", err)
		}
		cmd.Stdin = input
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	return cmd, outputReader, nil
}

func (r *Recorder) StartStream() (io.ReadCloser, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.streaming {
		return nil, fmt.Errorf("already streaming")
	}

	reader, writer := io.Pipe()
	consumer := r.addConsumer(writer)

	cmd, outputReader, err := startFFmpeg(reader, "")
	if err != nil {
		r.removeConsumer(consumer)
		writer.Close()
		return nil, err
	}

	go func() {
		cmd.Wait()
		writer.Close()
		r.removeConsumer(consumer)
	}()

	r.streamCmd = cmd
	r.streaming = true

	log.Println("Recorder: Started streaming")
	return outputReader, nil
}

func (r *Recorder) StopStream() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.streaming {
		return nil
	}

	if r.streamCmd != nil && r.streamCmd.Process != nil {
		r.streamCmd.Process.Kill()
	}

	r.streaming = false
	r.streamCmd = nil

	log.Println("Recorder: Stopped streaming")
	return nil
}

func (r *Recorder) StartRecording(outputPath string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.recording {
		return fmt.Errorf("already recording")
	}

	reader, writer := io.Pipe()
	consumer := r.addConsumer(writer)

	cmd, _, err := startFFmpeg(reader, outputPath)
	if err != nil {
		r.removeConsumer(consumer)
		writer.Close()
		return err
	}

	go func() {
		cmd.Wait()
		writer.Close()
		r.removeConsumer(consumer)
	}()

	r.recordCmd = cmd
	r.recording = true

	if val, ok := config.Get().GetKey("playActiveCameraSound"); !ok || val.(bool) {
		sfx.Get().PlayRecording()
	}

	log.Printf("Recorder: Started recording to %s", outputPath)
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
	}

	r.recording = false
	r.recordCmd = nil

	log.Println("Recorder: Stopped recording")
	return nil
}

func (r *Recorder) CapturePreview() ([]byte, error) {
	r.broadcast.frameMu.RLock()
	frame := r.broadcast.latestFrame
	r.broadcast.frameMu.RUnlock()

	if len(frame) == 0 {
		return nil, fmt.Errorf("no frame available yet")
	}

	cmd := exec.Command("ffmpeg",
		"-err_detect", "ignore_err",
		"-f", "h264",
		"-i", "pipe:0",
		"-frames:v", "1",
		"-vf", "scale=640:480",
		"-f", "image2",
		"-c:v", "mjpeg",
		"-q:v", "5",
		"pipe:1",
	)

	cmd.Stdin = bytes.NewReader(frame)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to extract preview frame: %w (stderr: %s)", err, stderr.String())
	}

	return output, nil
}

func (r *Recorder) SetMicrophoneEnabled(enabled bool) error {
	return config.Get().SetKey("microphoneEnabled", enabled)
}
