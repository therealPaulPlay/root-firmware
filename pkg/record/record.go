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

// keyframeBufferSize controls H.264 keyframe capture starting from SPS (NAL type 7).
//
// Preview extraction: Must contain complete I-frame (SPS+PPS+IDR) for artifact-free decoding.
// Smaller values risk corrupted/stretched frames in CapturePreview().
//
// Stream chunking: Larger buffers → fewer, larger MP4 fragments sent to relay server.
// 500KB yields ~5-6 chunks/sec, 256KB yields ~10 chunks/sec (more relay overhead).
const keyframeBufferSize = 500 * 1024 // 500KB

// audioChunkSize controls PCM audio chunk accumulation before broadcast.
// 48kHz mono S16_LE = 48000 samples/sec * 2 bytes/sample = 96000 bytes/sec
// 48KB = 500ms of audio, yielding ~2 chunks/sec.
const audioChunkSize = 48 * 1024 // 48KB

type Recorder struct {
	mu             sync.RWMutex
	recording      bool
	recordVideoCmd *exec.Cmd
	recordAudioCmd *exec.Cmd
	videoCmd       *exec.Cmd
	videoBroadcast *VideoBroadcast
	videoStreaming bool
	videoStreamCmd *exec.Cmd
	audioCmd       *exec.Cmd
	audioBroadcast *AudioBroadcast
	audioStreaming bool
	audioStreamCmd *exec.Cmd
}

type VideoBroadcast struct {
	consumers   []*Consumer
	consumersMu sync.RWMutex
	latestFrame []byte
	frameMu     sync.RWMutex
}

type AudioBroadcast struct {
	consumers   []*Consumer
	consumersMu sync.RWMutex
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
	cmd := exec.Command("arecord", "-l")
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Recorder: No microphone available (arecord failed): %v", err)
		return ""
	}

	// Match "card N: ... device M:" pattern
	re := regexp.MustCompile(`card (\d+):.*device (\d+):`)
	matches := re.FindSubmatch(output)
	if len(matches) >= 3 {
		deviceStr := fmt.Sprintf("plughw:%s,%s", matches[1], matches[2])
		log.Printf("Recorder: Using microphone at %s", deviceStr)
		return deviceStr
	}

	log.Println("Recorder: No microphone available")
	return ""
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

	r.videoCmd = cameraCmd
	r.videoBroadcast = &VideoBroadcast{consumers: make([]*Consumer, 0)}

	go r.videoBroadcastLoop(cameraOut)
	return nil
}

func (r *Recorder) videoBroadcastLoop(cameraOut io.ReadCloser) {
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
					r.mu.RLock()
					broadcast := r.videoBroadcast
					r.mu.RUnlock()

					if broadcast != nil {
						broadcast.frameMu.Lock()
						broadcast.latestFrame = append([]byte(nil), keyframeBuffer.Bytes()...)
						broadcast.frameMu.Unlock()
					}
					capturingKeyframe = false
				}
			}

			// Broadcast to consumers
			r.mu.RLock()
			broadcast := r.videoBroadcast
			r.mu.RUnlock()

			if broadcast != nil {
				broadcast.consumersMu.RLock()
				for _, consumer := range broadcast.consumers {
					consumer.writer.Write(data)
				}
				broadcast.consumersMu.RUnlock()
			}
		}

		if err != nil {
			log.Printf("Recorder: Camera error: %v", err)

			// Safely access broadcast (may be nil if camera was stopped)
			r.mu.RLock()
			broadcast := r.videoBroadcast
			r.mu.RUnlock()

			if broadcast != nil {
				broadcast.consumersMu.Lock()
				for _, consumer := range broadcast.consumers {
					consumer.writer.Close()
				}
				broadcast.consumersMu.Unlock()
			}
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

	micCmd := exec.Command("arecord",
		"-D", "hw:0,0",
		"-f", "S16_LE",
		"-r", "48000",
		"-c", "1",
		"-t", "raw",
	)

	audioOut, err := micCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create audio pipe: %w", err)
	}

	if err := micCmd.Start(); err != nil {
		return fmt.Errorf("failed to start audio: %w", err)
	}

	r.audioCmd = micCmd
	r.audioBroadcast = &AudioBroadcast{consumers: make([]*Consumer, 0)}

	go r.audioBroadcastLoop(audioOut)
	log.Println("Recorder: Audio broadcast started")
	return nil
}

func (r *Recorder) audioBroadcastLoop(audioOut io.ReadCloser) {
	chunkBuffer := make([]byte, 0, audioChunkSize)
	readBuffer := make([]byte, 4096)

	for {
		n, err := audioOut.Read(readBuffer)
		if n > 0 {
			data := readBuffer[:n]

			// Accumulate into chunk buffer
			chunkBuffer = append(chunkBuffer, data...)

			// When we have a full chunk, broadcast it
			if len(chunkBuffer) >= audioChunkSize {
				r.mu.RLock()
				broadcast := r.audioBroadcast
				r.mu.RUnlock()

				if broadcast != nil {
					broadcast.consumersMu.RLock()
					for _, consumer := range broadcast.consumers {
						consumer.writer.Write(chunkBuffer)
					}
					broadcast.consumersMu.RUnlock()
				}
				chunkBuffer = chunkBuffer[:0] // Reset buffer, keep capacity
			}
		}

		if err != nil {
			log.Printf("Recorder: Audio error: %v", err)

			// Safely access broadcast (may be nil if stopMicrophone was called)
			r.mu.RLock()
			broadcast := r.audioBroadcast
			r.mu.RUnlock()

			if broadcast != nil {
				broadcast.consumersMu.Lock()
				for _, consumer := range broadcast.consumers {
					consumer.writer.Close()
				}
				broadcast.consumersMu.Unlock()
			}
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
		r.audioBroadcast.consumersMu.Lock()
		for _, consumer := range r.audioBroadcast.consumers {
			consumer.writer.Close()
		}
		r.audioBroadcast.consumers = nil
		r.audioBroadcast.consumersMu.Unlock()
		r.audioBroadcast = nil
	}

	r.audioCmd = nil
	log.Println("Recorder: Stopped audio broadcast")
}

func (r *Recorder) addVideoConsumer(writer io.WriteCloser) *Consumer {
	consumer := &Consumer{writer: writer}
	r.videoBroadcast.consumersMu.Lock()
	r.videoBroadcast.consumers = append(r.videoBroadcast.consumers, consumer)
	r.videoBroadcast.consumersMu.Unlock()
	return consumer
}

func (r *Recorder) removeVideoConsumer(consumer *Consumer) {
	r.mu.RLock()
	broadcast := r.videoBroadcast
	r.mu.RUnlock()

	if broadcast == nil {
		return
	}

	broadcast.consumersMu.Lock()
	defer broadcast.consumersMu.Unlock()

	for i, c := range broadcast.consumers {
		if c == consumer {
			broadcast.consumers = append(broadcast.consumers[:i], broadcast.consumers[i+1:]...)
			break
		}
	}
}

func (r *Recorder) addAudioConsumer(writer io.WriteCloser) *Consumer {
	consumer := &Consumer{writer: writer}
	r.audioBroadcast.consumersMu.Lock()
	r.audioBroadcast.consumers = append(r.audioBroadcast.consumers, consumer)
	r.audioBroadcast.consumersMu.Unlock()
	return consumer
}

func (r *Recorder) removeAudioConsumer(consumer *Consumer) {
	r.mu.RLock()
	broadcast := r.audioBroadcast
	r.mu.RUnlock()

	if broadcast == nil {
		return
	}

	broadcast.consumersMu.Lock()
	defer broadcast.consumersMu.Unlock()

	for i, c := range broadcast.consumers {
		if c == consumer {
			broadcast.consumers = append(broadcast.consumers[:i], broadcast.consumers[i+1:]...)
			break
		}
	}
}

// startFFmpegStream creates an ffmpeg process for streaming video
func startFFmpegStream(input io.Reader) (*exec.Cmd, io.ReadCloser, error) {
	cmd := exec.Command("ffmpeg",
		"-f", "h264", "-i", "pipe:0",
		"-c:v", "copy",
		"-f", "mp4",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-frag_duration", "200000",
		"pipe:1",
	)

	outputReader, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	cmd.Stdin = input

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	return cmd, outputReader, nil
}

// startFFmpegRecording creates an ffmpeg process for recording video to file
func startFFmpegRecording(input io.Reader, outputFile string) (*exec.Cmd, error) {
	cmd := exec.Command("ffmpeg",
		"-f", "h264", "-i", "pipe:0",
		"-c:v", "copy",
		"-f", "mp4", outputFile,
	)
	cmd.Stdin = input

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	return cmd, nil
}

// startAudioRecording saves raw PCM audio to file
func startAudioRecording(input io.Reader, outputFile string) (*exec.Cmd, error) {
	cmd := exec.Command("ffmpeg",
		"-f", "s16le", "-ar", "48000", "-ac", "1", "-i", "pipe:0",
		"-c:a", "aac",
		"-f", "mp4", outputFile,
	)
	cmd.Stdin = input

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start audio recording: %w", err)
	}

	return cmd, nil
}

func (r *Recorder) StartVideoStream() (io.ReadCloser, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.videoStreaming {
		return nil, fmt.Errorf("already streaming")
	}

	reader, writer := io.Pipe()
	consumer := r.addVideoConsumer(writer)

	cmd, outputReader, err := startFFmpegStream(reader)
	if err != nil {
		r.removeVideoConsumer(consumer)
		writer.Close()
		return nil, err
	}

	go func() {
		cmd.Wait()
		writer.Close()
		r.removeVideoConsumer(consumer)
	}()

	r.videoStreamCmd = cmd
	r.videoStreaming = true

	if val, ok := config.Get().GetKey("playActiveCameraSound"); ok && val.(bool) {
		sfx.Get().PlayRecording()
	}

	log.Println("Recorder: Started video streaming")
	return outputReader, nil
}

func (r *Recorder) StopVideoStream() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.videoStreaming {
		return nil
	}

	if r.videoStreamCmd != nil && r.videoStreamCmd.Process != nil {
		r.videoStreamCmd.Process.Kill()
	}

	r.videoStreaming = false
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
	videoConsumer := r.addVideoConsumer(videoWriter)

	videoCmd, err := startFFmpegRecording(videoReader, outputPath)
	if err != nil {
		r.removeVideoConsumer(videoConsumer)
		videoWriter.Close()
		return err
	}

	go func() {
		videoCmd.Wait()
		videoWriter.Close()
		r.removeVideoConsumer(videoConsumer)
	}()

	r.recordVideoCmd = videoCmd

	// Start audio recording if microphone is enabled and available
	if r.audioBroadcast != nil {
		audioOutputPath := outputPath[:len(outputPath)-4] + "_audio.m4a"
		audioReader, audioWriter := io.Pipe()
		audioConsumer := r.addAudioConsumer(audioWriter)

		audioCmd, err := startAudioRecording(audioReader, audioOutputPath)
		if err != nil {
			log.Printf("Recorder: Failed to start audio recording: %v", err)
		} else {
			go func() {
				audioCmd.Wait()
				audioWriter.Close()
				r.removeAudioConsumer(audioConsumer)
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

	// Stop video recording
	if r.recordVideoCmd != nil && r.recordVideoCmd.Process != nil {
		r.recordVideoCmd.Process.Kill()
	}

	// Stop audio recording
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
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := config.Get().SetKey("microphoneEnabled", enabled); err != nil {
		return err
	}

	// Stop or start audio broadcast based on new setting
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

// StartAudioStream starts streaming raw PCM audio from the broadcast
func (r *Recorder) StartAudioStream() (io.ReadCloser, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.audioBroadcast == nil {
		return nil, fmt.Errorf("audio broadcast not available")
	}

	reader, writer := io.Pipe()
	consumer := r.addAudioConsumer(writer)

	// Clean up consumer when stream stops
	go func() {
		// Wait for reader to be closed
		buf := make([]byte, 1)
		for {
			_, err := reader.Read(buf)
			if err != nil {
				break
			}
		}
		writer.Close()
		r.removeAudioConsumer(consumer)
	}()

	log.Println("Recorder: Started audio streaming")
	return reader, nil
}
