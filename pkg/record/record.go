package record

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"sync"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/sfx"
)

const (
	maxKeyframeBufferSize = 256 * 1024 // 256KB - must be big enough to contain full I-frame (SPS+PPS+IDR) for preview extraction
	audioChunkSize        = 48 * 1024  // 48KB - yields ~2 audio chunks/sec (500ms at 48kHz mono)
)

type Recorder struct {
	mu             sync.RWMutex
	videoCmd       *exec.Cmd
	videoBroadcast *broadcast
	audioCmd       *exec.Cmd
	audioBroadcast *broadcast
	recording      bool
	recordVideoCmd *exec.Cmd
	recordAudioCmd *exec.Cmd
	videoStreamCmd *exec.Cmd
}

type broadcast struct {
	consumers   []*io.PipeWriter
	mu          sync.RWMutex
	latestFrame []byte // video only
	frameMu     sync.RWMutex
}

func (b *broadcast) addConsumer(w *io.PipeWriter) {
	b.mu.Lock()
	b.consumers = append(b.consumers, w)
	b.mu.Unlock()
}

func (b *broadcast) removeConsumer(w *io.PipeWriter) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, c := range b.consumers {
		if c == w {
			b.consumers = append(b.consumers[:i], b.consumers[i+1:]...)
			return
		}
	}
}

func (b *broadcast) write(data []byte) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, w := range b.consumers {
		w.Write(data)
	}
}

func (b *broadcast) closeAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, w := range b.consumers {
		w.Close()
	}
	b.consumers = nil
}

var instance *Recorder
var once sync.Once

func Init() error {
	var initErr error
	once.Do(func() {
		for _, cmd := range []string{"rpicam-vid", "ffmpeg"} {
			if _, err := exec.LookPath(cmd); err != nil {
				initErr = fmt.Errorf("ffmpeg and/or rpicam-vid are missing (need to install via apt!)")
				return
			}
		}
		instance = &Recorder{}

		if err := instance.startCamera(); err != nil {
			initErr = fmt.Errorf("failed to start camera: %w", err)
			return
		}

		if err := instance.startMicrophone(); err != nil {
			log.Printf("Recorder: Failed to start audio: %v", err)
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

func MicEnabled() bool {
	val, ok := config.Get().GetKey("microphoneEnabled")
	if !ok {
		return true
	}
	b, ok := val.(bool)
	return ok && b
}

// getMicDevice returns the plughw device string for the first available microphone, or empty string if none
func getMicDevice() string {
	output, err := exec.Command("arecord", "-l").CombinedOutput()
	if err != nil {
		log.Printf("Recorder: No microphone available (arecord failed): %v", err)
		return ""
	}

	matches := regexp.MustCompile(`card (\d+):.*device (\d+):`).FindSubmatch(output)
	if len(matches) >= 3 {
		return fmt.Sprintf("plughw:%s,%s", matches[1], matches[2])
	}

	log.Println("Recorder: No microphone available")
	return ""
}

func (r *Recorder) startCamera() error {
	exec.Command("pkill", "-9", "rpicam-vid").Run()
	time.Sleep(200 * time.Millisecond)

	cmd := exec.Command("rpicam-vid",
		"-t", "0", "-o", "-",
		"--codec", "h264", "-n", "--inline", "--listen",
		"--framerate", "15",
		"--width", "1920", "--height", "1080",
		"-b", "3000000",
		"-g", "15",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	r.videoCmd = cmd
	r.videoBroadcast = &broadcast{consumers: make([]*io.PipeWriter, 0)}

	go r.videoBroadcastLoop(stdout)
	return nil
}

func (r *Recorder) videoBroadcastLoop(stdout io.ReadCloser) {
	defer stdout.Close()

	buffer := make([]byte, 64*1024)
	keyframeBuffer := &bytes.Buffer{}
	capturingKeyframe := false

	for {
		n, err := stdout.Read(buffer)
		if n > 0 {
			data := buffer[:n]

			// Detect SPS NAL (type 7) to start keyframe capture
			for i := 0; i < len(data)-4; i++ {
				// Check for NAL start code: 0x00 0x00 0x00 0x01
				if data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 && i+4 < len(data) {
					if data[i+4]&0x1F == 7 { // SPS - start of keyframe
						// If we were already capturing, save the previous keyframe
						if capturingKeyframe && keyframeBuffer.Len() > 0 {
							r.videoBroadcast.frameMu.Lock()
							r.videoBroadcast.latestFrame = append([]byte(nil), keyframeBuffer.Bytes()...)
							r.videoBroadcast.frameMu.Unlock()
						}
						keyframeBuffer.Reset()
						capturingKeyframe = true
						break
					}
				}
			}

			if capturingKeyframe {
				keyframeBuffer.Write(data)
				// Safety: cap buffer size to prevent unbounded memory growth
				if keyframeBuffer.Len() > maxKeyframeBufferSize {
					r.videoBroadcast.frameMu.Lock()
					r.videoBroadcast.latestFrame = append([]byte(nil), keyframeBuffer.Bytes()...)
					r.videoBroadcast.frameMu.Unlock()
					capturingKeyframe = false
				}
			}

			r.videoBroadcast.write(data)
		}

		if err != nil {
			r.videoBroadcast.closeAll()
			return
		}
	}
}

func (r *Recorder) startMicrophone() error {
	if !MicEnabled() {
		return fmt.Errorf("microphone disabled")
	}

	micDevice := getMicDevice()
	if micDevice == "" {
		return fmt.Errorf("no microphone available")
	}

	// Apply 2x volume gain to boost microphone input
	exec.Command("amixer", "-D", "hw:0", "sset", "Mic", "100%").Run()

	cmd := exec.Command("arecord",
		"-D", "hw:0,0",
		"-f", "S16_LE",
		"-r", "48000",
		"-c", "1",
		"-t", "raw",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	r.audioCmd = cmd
	r.audioBroadcast = &broadcast{consumers: make([]*io.PipeWriter, 0)}

	go r.audioBroadcastLoop(stdout)
	log.Println("Recorder: Audio broadcast started")
	return nil
}

func (r *Recorder) audioBroadcastLoop(stdout io.ReadCloser) {
	defer stdout.Close()

	chunkBuffer := make([]byte, 0, audioChunkSize)
	readBuffer := make([]byte, 4096)

	for {
		n, err := stdout.Read(readBuffer)
		if n > 0 {
			chunkBuffer = append(chunkBuffer, readBuffer[:n]...)
			if len(chunkBuffer) >= audioChunkSize {
				r.audioBroadcast.write(chunkBuffer)
				chunkBuffer = chunkBuffer[:0]
			}
		}

		if err != nil {
			r.audioBroadcast.closeAll()
			return
		}
	}
}

func (r *Recorder) stopMicrophone() {
	if r.audioCmd != nil && r.audioCmd.Process != nil {
		r.audioCmd.Process.Kill()
		r.audioCmd.Wait()
	}

	if r.audioBroadcast != nil {
		r.audioBroadcast.closeAll()
		r.audioBroadcast = nil
	}

	r.audioCmd = nil
	log.Println("Recorder: Stopped audio broadcast")
}

func (r *Recorder) StartVideoStream() (io.ReadCloser, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	reader, writer := io.Pipe()
	r.videoBroadcast.addConsumer(writer)

	cmd := exec.Command("ffmpeg",
		"-fflags", "+nobuffer",
		"-flags", "low_delay",
		"-f", "h264", "-i", "pipe:0",
		"-c:v", "copy",
		"-f", "mp4",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-frag_duration", "200000",
		"pipe:1",
	)

	outputReader, err := cmd.StdoutPipe()
	if err != nil {
		r.videoBroadcast.removeConsumer(writer)
		writer.Close()
		return nil, err
	}
	cmd.Stdin = reader

	if err := cmd.Start(); err != nil {
		r.videoBroadcast.removeConsumer(writer)
		writer.Close()
		return nil, err
	}

	go func() {
		cmd.Wait()
		writer.Close()
		r.videoBroadcast.removeConsumer(writer)
	}()

	r.videoStreamCmd = cmd

	if val, ok := config.Get().GetKey("playActiveCameraSound"); ok && val.(bool) {
		sfx.Get().PlayRecording()
	}

	log.Println("Recorder: Started video streaming")
	return outputReader, nil
}

func (r *Recorder) StopVideoStream() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.videoStreamCmd != nil && r.videoStreamCmd.Process != nil {
		r.videoStreamCmd.Process.Kill()
	}
	r.videoStreamCmd = nil

	log.Println("Recorder: Stopped video streaming")
	return nil
}

func (r *Recorder) StartRecording(outputPath string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.recording {
		return fmt.Errorf("already recording")
	}

	// Start video recording
	videoReader, videoWriter := io.Pipe()
	r.videoBroadcast.addConsumer(videoWriter)

	videoCmd := exec.Command("ffmpeg",
		"-f", "h264", "-i", "pipe:0",
		"-c:v", "copy",
		"-f", "mp4", outputPath,
	)
	videoCmd.Stdin = videoReader

	if err := videoCmd.Start(); err != nil {
		r.videoBroadcast.removeConsumer(videoWriter)
		videoWriter.Close()
		return err
	}

	go func() {
		videoCmd.Wait()
		videoWriter.Close()
		r.videoBroadcast.removeConsumer(videoWriter)
	}()

	r.recordVideoCmd = videoCmd

	// Start audio recording if available
	if r.audioBroadcast != nil {
		audioReader, audioWriter := io.Pipe()
		r.audioBroadcast.addConsumer(audioWriter)

		audioCmd := exec.Command("ffmpeg",
			"-f", "s16le", "-ar", "48000", "-ac", "1", "-i", "pipe:0",
			"-c:a", "aac",
			"-f", "mp4", outputPath[:len(outputPath)-4]+"_audio.m4a",
		)
		audioCmd.Stdin = audioReader

		if err := audioCmd.Start(); err != nil {
			log.Printf("Recorder: Failed to start audio recording: %v", err)
		} else {
			go func() {
				audioCmd.Wait()
				audioWriter.Close()
				r.audioBroadcast.removeConsumer(audioWriter)
			}()
			r.recordAudioCmd = audioCmd
		}
	}

	r.recording = true

	if val, ok := config.Get().GetKey("playActiveCameraSound"); ok && val.(bool) {
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

	if r.recordVideoCmd != nil && r.recordVideoCmd.Process != nil {
		r.recordVideoCmd.Process.Kill()
	}

	if r.recordAudioCmd != nil && r.recordAudioCmd.Process != nil {
		r.recordAudioCmd.Process.Kill()
	}

	r.recording = false
	r.recordVideoCmd = nil
	r.recordAudioCmd = nil

	log.Println("Recorder: Stopped recording")
	return nil
}

func (r *Recorder) CapturePreview() ([]byte, error) {
	r.videoBroadcast.frameMu.RLock()
	frame := r.videoBroadcast.latestFrame
	r.videoBroadcast.frameMu.RUnlock()

	if len(frame) == 0 {
		return nil, fmt.Errorf("no frame available yet")
	}

	cmd := exec.Command("ffmpeg",
		"-err_detect", "ignore_err",
		"-f", "h264", "-i", "pipe:0",
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
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := config.Get().SetKey("microphoneEnabled", enabled); err != nil {
		return err
	}

	if enabled {
		// Start microphone if not already running
		if r.audioBroadcast == nil {
			if err := r.startMicrophone(); err != nil {
				log.Printf("Recorder: Failed to start microphone: %v", err)
				return err
			}
		}
	} else {
		// Stop microphone if running
		if r.audioBroadcast != nil {
			r.stopMicrophone()
		}
	}

	return nil
}

func (r *Recorder) StartAudioStream() (io.ReadCloser, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.audioBroadcast == nil {
		return nil, fmt.Errorf("audio broadcast not available")
	}

	reader, writer := io.Pipe()
	broadcast := r.audioBroadcast
	broadcast.addConsumer(writer)

	// Clean up consumer when reader is closed
	go func() {
		io.Copy(io.Discard, reader)
		writer.Close()
		broadcast.removeConsumer(writer)
	}()

	log.Println("Recorder: Started audio streaming")
	return reader, nil
}
