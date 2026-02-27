package record

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log"
	"math"
	"os/exec"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/globals"
	"root-firmware/pkg/sfx"
	"root-firmware/pkg/storage"

	"golang.org/x/image/draw"
)

const maxKeyframeBufferSize = 256 * 1024 // 256KB - must be big enough to contain full I-frame (SPS+PPS+IDR) for preview extraction

type Recorder struct {
	mu             sync.RWMutex
	videoCmd       *exec.Cmd
	videoBroadcast *broadcast
	videoStreamCh  chan []byte
	audioCmd       *exec.Cmd
	audioBroadcast *broadcast
	audioStreamCh  chan []byte
	recording      bool
	recordPath     string
	recordStart    time.Time
	recordLookback bool
	decoder        *h264Decoder
	videoRing      *lookbackBuffer
	audioRing      *lookbackBuffer
	muxQueue       chan muxJob
	videoHeartbeat atomic.Bool
}

type broadcast struct {
	consumers   []chan []byte
	mu          sync.RWMutex
	latestFrame []byte // video only
	frameMu     sync.RWMutex
}

func (b *broadcast) addConsumer(ch chan []byte) {
	b.mu.Lock()
	b.consumers = append(b.consumers, ch)
	b.mu.Unlock()
}

func (b *broadcast) removeConsumer(ch chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, c := range b.consumers {
		if c == ch {
			close(c)
			b.consumers = append(b.consumers[:i], b.consumers[i+1:]...)
			return
		}
	}
}

func (b *broadcast) write(data []byte) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, ch := range b.consumers {
		dataCopy := make([]byte, len(data))
		copy(dataCopy, data)
		select {
		case ch <- dataCopy:
		default:
			log.Printf("Recorder: Dropped chunk, consumer channel full!")
		}
	}
}

func (b *broadcast) closeAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.consumers {
		close(ch)
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
		if err := initOpenH264(); err != nil {
			initErr = fmt.Errorf("failed to load OpenH264: %w", err)
			return
		}

		dec, err := newDecoder()
		if err != nil {
			initErr = fmt.Errorf("failed to create H.264 decoder: %w", err)
			return
		}

		// Warm up ffmpeg with h264 demuxer + fmp4 muxer (same pipeline as streaming)
		exec.Command("ffmpeg", "-f", "lavfi", "-i", "testsrc=d=0.1:r=1", "-c:v", "libx264",
			"-f", "mp4", "-movflags", "frag_keyframe+empty_moov+default_base_moof", "-y", "/dev/null").Run()

		instance = &Recorder{
			decoder:        dec,
			videoBroadcast: &broadcast{consumers: make([]chan []byte, 0)},
			audioBroadcast: &broadcast{consumers: make([]chan []byte, 0)},
			videoRing:      newLookbackBuffer(videoRingCapacity),
			audioRing:      newLookbackBuffer(audioRingCapacity),
			muxQueue:       make(chan muxJob, 10),
		}
		go instance.muxWorker()

		if err := instance.startCamera(); err != nil {
			initErr = fmt.Errorf("failed to start camera: %w", err)
			return
		}

		if err := instance.startMicrophone(); err != nil {
			log.Printf("Recorder: Failed to start microphone: %v", err)
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
		log.Printf("Recorder: Error getting microphone (arecord failed): %v", err)
		return ""
	}

	matches := regexp.MustCompile(`card (\d+):.*device (\d+):`).FindSubmatch(output)
	if len(matches) >= 3 {
		return fmt.Sprintf("plughw:%s,%s", matches[1], matches[2])
	}

	return ""
}

func (r *Recorder) startCamera() error {
	exec.Command("pkill", "-9", "rpicam-vid").Run()
	time.Sleep(200 * time.Millisecond)

	cmd := exec.Command("rpicam-vid",
		"-t", "0", "-o", "-",
		"--codec", "h264", "-n", "--inline", "--listen",
		"--framerate", fmt.Sprintf("%d", globals.CameraFramerate),
		"--width", fmt.Sprintf("%d", globals.CameraWidth),
		"--height", fmt.Sprintf("%d", globals.CameraHeight),
		"-b", fmt.Sprintf("%d", globals.CameraBitrate),
		"-g", fmt.Sprintf("%d", globals.CameraGOPSize),
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	r.videoCmd = cmd
	go r.videoBroadcastLoop(stdout)
	go r.videoWatchdog()
	log.Println("Recorder: Camera (video broadcast) started")
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
						// Previous GOP is complete — save for preview and lookback
						if capturingKeyframe && keyframeBuffer.Len() > 0 {
							gopData := append([]byte(nil), keyframeBuffer.Bytes()...)
							r.videoBroadcast.frameMu.Lock()
							r.videoBroadcast.latestFrame = gopData
							r.videoBroadcast.frameMu.Unlock()
							r.videoRing.push(gopData)
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
			r.videoHeartbeat.Store(true)
		}

		if err != nil {
			log.Printf("Recorder: Video broadcast failed: %v", err)
			r.videoBroadcast.closeAll()
			sfx.Get().PlayCameraFailure()
			log.Fatal("Recorder: Camera failure, exiting for restart")
		}
	}
}

func (r *Recorder) videoWatchdog() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if !r.videoHeartbeat.Swap(false) {
			log.Println("Recorder: No video data received in last 10s")
			r.videoBroadcast.closeAll()
			sfx.Get().PlayCameraFailure()
			log.Fatal("Recorder: Camera failure (no data), exiting for restart")
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
	exec.Command("amixer", "-D", micDevice, "sset", "Mic", "100%").Run()

	cmd := exec.Command("arecord",
		"-D", micDevice,
		"-f", "S16_LE",
		"-r", fmt.Sprintf("%d", globals.AudioSampleRate),
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
	go r.audioBroadcastLoop(stdout)
	log.Println("Recorder: Microphone (audio broadcast) started")
	return nil
}

func (r *Recorder) audioBroadcastLoop(stdout io.ReadCloser) {
	defer stdout.Close()

	chunkBuffer := make([]byte, 0, globals.AudioChunkSize)
	readBuffer := make([]byte, 4096)

	for {
		n, err := stdout.Read(readBuffer)
		if n > 0 {
			chunkBuffer = append(chunkBuffer, readBuffer[:n]...)
			if len(chunkBuffer) >= globals.AudioChunkSize {
				r.audioRing.push(chunkBuffer)
				r.audioBroadcast.write(chunkBuffer)
				chunkBuffer = chunkBuffer[:0]
			}
		}

		if err != nil {
			if err != io.EOF {
				log.Printf("Recorder: Audio broadcast error: %v", err)
			}
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

	r.audioBroadcast.closeAll()
	r.audioCmd = nil
	log.Println("Recorder: Stopped audio broadcast")
}

func (r *Recorder) StartVideoStream() (io.ReadCloser, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.videoStreamCh != nil {
		return nil, fmt.Errorf("video stream already active")
	}

	// Create channel consumer for raw H.264
	videoCh := make(chan []byte, 50)
	r.videoBroadcast.addConsumer(videoCh)
	r.videoStreamCh = videoCh

	// Create pipe for ffmpeg
	reader, writer := io.Pipe()

	// Goroutine to drain channel into pipe for ffmpeg
	go func() {
		for data := range videoCh {
			writer.Write(data)
		}
		writer.Close()
	}()

	// Convert H.264 to fragmented MP4
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
	cmd.Stdin = reader

	outputReader, err := cmd.StdoutPipe()
	if err != nil {
		r.videoBroadcast.removeConsumer(videoCh)
		writer.Close()
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		r.videoBroadcast.removeConsumer(videoCh)
		writer.Close()
		return nil, err
	}

	// Cleanup when ffmpeg exits
	go func() {
		cmd.Wait()
		r.videoBroadcast.removeConsumer(videoCh)
	}()

	log.Println("Recorder: Started video streaming")
	return outputReader, nil
}

func (r *Recorder) StopVideoStream() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.videoStreamCh != nil {
		r.videoBroadcast.removeConsumer(r.videoStreamCh)
	}
	r.videoStreamCh = nil

	log.Println("Recorder: Stopped video streaming")
	return nil
}

// StartRecording marks the start of a recording
// The ring buffer continuously captures GOPs – On StopRecording, the buffered
// data is muxed to MP4 to ensure a gapless recording (ffmpeg fails if any data is missing or duplicate)
func (r *Recorder) StartRecording(outputPath string, withLookback bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.recording = true
	r.recordPath = outputPath
	r.recordStart = time.Now()
	r.recordLookback = withLookback
	log.Printf("Recorder: Started recording to %s", outputPath)
}

// StopRecording flushes the ring buffer and enqueues an MP4 mux job
func (r *Recorder) StopRecording(eventType string, preview []byte, detection *storage.DetectionResult) (time.Duration, error) {
	r.mu.Lock()
	if !r.recording {
		r.mu.Unlock()
		return 0, nil
	}
	r.recording = false

	// Calculate how far back to flush: recording duration + lookback
	age := time.Since(r.recordStart)
	if r.recordLookback {
		age += globals.LookbackDuration
	}

	videoEntries := r.videoRing.flush(age)
	audioEntries := r.audioRing.flush(age)
	outputPath := r.recordPath
	r.mu.Unlock()

	if len(videoEntries) == 0 {
		log.Printf("Recorder: No video data to save")
		return 0, fmt.Errorf("no video data")
	}

	gopInterval := time.Second * time.Duration(globals.CameraGOPSize) / time.Duration(globals.CameraFramerate)
	duration := gopInterval * time.Duration(len(videoEntries))
	durationSec := math.Round(duration.Seconds()*100) / 100
	log.Printf("Recorder: Enqueuing mux job (%.2fs) to %s", durationSec, outputPath)

	job := muxJob{
		videoEntries: videoEntries,
		audioEntries: audioEntries,
		outputPath:   outputPath,
		duration:     durationSec,
		eventType:    eventType,
		preview:      preview,
		detection:    detection,
	}

	select {
	case r.muxQueue <- job:
	default:
		log.Printf("Recorder: Mux queue full, dropping recording %s", outputPath)
		return 0, fmt.Errorf("mux queue full")
	}

	return duration, nil
}

// decodeAndScale decodes an H.264 keyframe and scales to the target resolution, returning an image.NRGBA
func (r *Recorder) decodeAndScale(frame []byte, x, y int) (*image.NRGBA, error) {
	decoded, err := r.decoder.decode(frame)
	if err != nil {
		return nil, err
	}

	expandLimitedRange(decoded)
	dst := image.NewNRGBA(image.Rect(0, 0, x, y))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), decoded, decoded.Bounds(), draw.Src, nil)
	return dst, nil
}

func (r *Recorder) CapturePreview(x int, y int) ([]byte, error) {
	r.videoBroadcast.frameMu.RLock()
	frame := r.videoBroadcast.latestFrame
	r.videoBroadcast.frameMu.RUnlock()

	if len(frame) == 0 {
		return nil, fmt.Errorf("no frame available yet")
	}

	img, err := r.decodeAndScale(frame, x, y)
	if err != nil {
		return nil, fmt.Errorf("failed to decode preview frame: %w", err)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 50}); err != nil {
		return nil, fmt.Errorf("failed to encode preview: %w", err)
	}
	return buf.Bytes(), nil
}

func (r *Recorder) CaptureViewfinderFrame(x, y int) ([]byte, error) {
	r.videoBroadcast.frameMu.RLock()
	frame := r.videoBroadcast.latestFrame
	r.videoBroadcast.frameMu.RUnlock()

	if len(frame) == 0 {
		return nil, fmt.Errorf("no frame available yet")
	}

	decoded, err := r.decoder.decode(frame)
	if err != nil {
		return nil, fmt.Errorf("failed to decode viewfinder frame: %w", err)
	}

	// Scale Y plane directly (luma = grayscale), avoiding full color conversion
	src := &image.Gray{Pix: decoded.Y, Stride: decoded.YStride, Rect: decoded.Rect}
	dst := image.NewGray(image.Rect(0, 0, x, y))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	return dst.Pix, nil
}

func (r *Recorder) SetMicrophoneEnabled(enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := config.Get().SetKey("microphoneEnabled", enabled); err != nil {
		return err
	}

	if enabled {
		// Start microphone if not already running
		if r.audioCmd == nil {
			if err := r.startMicrophone(); err != nil {
				log.Printf("Recorder: Failed to start microphone: %v", err)
				return err
			}
		}
	} else {
		// Stop microphone if running
		if r.audioCmd != nil {
			r.stopMicrophone()
		}
	}

	return nil
}

func (r *Recorder) StartAudioStream() (chan []byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.audioCmd == nil {
		return nil, fmt.Errorf("microphone not available")
	}
	if r.audioStreamCh != nil {
		return nil, fmt.Errorf("audio stream already active")
	}

	ch := make(chan []byte, 100) // Buffer 100 chunks (~400KB)
	r.audioBroadcast.addConsumer(ch)
	r.audioStreamCh = ch

	log.Println("Recorder: Started audio streaming")
	return ch, nil
}

func (r *Recorder) StopAudioStream() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.audioStreamCh != nil {
		r.audioBroadcast.removeConsumer(r.audioStreamCh)
	}
	r.audioStreamCh = nil

	log.Println("Recorder: Stopped audio streaming")
	return nil
}
