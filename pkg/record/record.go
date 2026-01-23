package record

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"sync"
	"syscall"
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
	videoStreamCh  chan []byte
	audioCmd       *exec.Cmd
	audioBroadcast *broadcast
	audioStreamCh  chan []byte
	recording      bool
	recordVideoCmd *exec.Cmd
	recordAudioCmd *exec.Cmd
	recordVideoCh  chan []byte
	recordAudioCh  chan []byte
	viewfinder     *viewfinderProcess
	viewfinderMu   sync.Mutex
}

type viewfinderProcess struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	latestFrame []byte
	frameMu     sync.RWMutex
	lastAccess  time.Time
	stopCh      chan struct{}
	readyCh     chan struct{}
	width       int
	height      int
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
		instance = &Recorder{}

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
		"--framerate", "15",
		"--width", "1920", "--height", "1080",
		"-b", "3000000",
		"-g", "5",
		"--ev", "5.0",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	r.videoCmd = cmd
	r.videoBroadcast = &broadcast{consumers: make([]chan []byte, 0)}

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
			log.Printf("Recorder: Video broadcast failed: %v", err)
			r.videoBroadcast.closeAll()
			log.Fatal("Recorder: Camera failure, exiting for restart")
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
	r.audioBroadcast = &broadcast{consumers: make([]chan []byte, 0)}

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

	// Stop viewfinder if running (hardware doesn't handle both simultaneously)
	r.viewfinderMu.Lock()
	if r.viewfinder != nil {
		r.stopViewfinder()
		r.viewfinder = nil
	}
	r.viewfinderMu.Unlock()

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

	if val, ok := config.Get().GetKey("playActiveCameraSound"); ok && val.(bool) {
		sfx.Get().PlayRecording()
	}

	log.Println("Recorder: Started video streaming")
	return outputReader, nil
}

func (r *Recorder) StopVideoStream() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.videoBroadcast != nil && r.videoStreamCh != nil {
		r.videoBroadcast.removeConsumer(r.videoStreamCh)
	}
	if r.videoStreamCh != nil {
		r.videoStreamCh = nil
	}

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
	videoCh := make(chan []byte, 50)
	r.videoBroadcast.addConsumer(videoCh)
	r.recordVideoCh = videoCh

	videoReader, videoWriter := io.Pipe()
	go func() {
		for data := range videoCh {
			videoWriter.Write(data)
		}
		videoWriter.Close()
	}()

	videoCmd := exec.Command("ffmpeg",
		"-f", "h264", "-i", "pipe:0",
		"-c:v", "copy",
		"-f", "mp4", outputPath,
	)
	videoCmd.Stdin = videoReader

	if err := videoCmd.Start(); err != nil {
		r.videoBroadcast.removeConsumer(videoCh)
		r.recordVideoCh = nil
		videoWriter.Close()
		return err
	}

	r.recordVideoCmd = videoCmd

	// Start audio recording if available
	if r.audioBroadcast != nil {
		audioCh := make(chan []byte, 100)
		r.audioBroadcast.addConsumer(audioCh)
		r.recordAudioCh = audioCh

		audioReader, audioWriter := io.Pipe()
		go func() {
			for data := range audioCh {
				audioWriter.Write(data)
			}
			audioWriter.Close()
		}()

		audioCmd := exec.Command("ffmpeg",
			"-f", "s16le", "-ar", "48000", "-ac", "1", "-i", "pipe:0",
			"-c:a", "aac",
			"-f", "mp4", outputPath[:len(outputPath)-4]+"_audio.m4a",
		)
		audioCmd.Stdin = audioReader

		if err := audioCmd.Start(); err != nil {
			log.Printf("Recorder: Failed to start audio recording: %v", err)
			r.audioBroadcast.removeConsumer(audioCh)
			r.recordAudioCh = nil
		} else {
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

	// Gracefully stop ffmpeg with SIGTERM to finalize MP4 files properly
	stopFFmpeg := func(cmd *exec.Cmd, name string) {
		if cmd != nil && cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
			go func() {
				done := make(chan error)
				go func() {
					done <- cmd.Wait()
				}()
				select {
				case err := <-done:
					if err != nil {
						log.Printf("Recorder: Recording %s ffmpeg exited: %v", name, err)
					}
				case <-time.After(5 * time.Second):
					if err := cmd.Process.Kill(); err != nil {
						log.Printf("Recorder: Failed to kill recording %s ffmpeg: %v", name, err)
					}
					<-done
				}
			}()
		}
	}

	stopFFmpeg(r.recordVideoCmd, "video")
	stopFFmpeg(r.recordAudioCmd, "audio")

	// Remove channels from broadcast
	if r.recordVideoCh != nil {
		r.videoBroadcast.removeConsumer(r.recordVideoCh)
		r.recordVideoCh = nil
	}

	if r.recordAudioCh != nil {
		r.audioBroadcast.removeConsumer(r.recordAudioCh)
		r.recordAudioCh = nil
	}

	r.recording = false
	r.recordVideoCmd = nil
	r.recordAudioCmd = nil

	log.Println("Recorder: Stopped recording")
	return nil
}

func (r *Recorder) CapturePreview(x int, y int) ([]byte, error) {
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
		"-vf", fmt.Sprintf("scale=%d:%d", x, y),
		"-f", "image2",
		"-c:v", "mjpeg",
		"-q:v", "10",
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

func (r *Recorder) CaptureViewfinderFrame(x, y int) ([]byte, error) {
	// Check if stream is running (performance consideration)
	r.mu.RLock()
	streamRunning := r.videoStreamCh != nil
	r.mu.RUnlock()

	if streamRunning {
		return nil, fmt.Errorf("cannot use viewfinder while stream is active")
	}

	r.viewfinderMu.Lock()

	// If no active process, or active viewfinder process has incorrect resolution, (re)start
	if r.viewfinder == nil || r.viewfinder.width != x || r.viewfinder.height != y {
		if r.viewfinder != nil {
			r.stopViewfinder()
		}
		r.startViewfinder(x, y)
	}
	vf := r.viewfinder
	r.viewfinderMu.Unlock()

	<-vf.readyCh
	vf.frameMu.Lock()
	vf.lastAccess = time.Now()
	frame := vf.latestFrame
	vf.frameMu.Unlock()
	return frame, nil
}

func (r *Recorder) startViewfinder(x, y int) {
	log.Printf("Recorder: Starting viewfinder (%dx%d)", x, y)
	vf := &viewfinderProcess{
		width: x, height: y, stopCh: make(chan struct{}), readyCh: make(chan struct{}),
		lastAccess: time.Now(),
	}
	r.viewfinder = vf

	go func() {
		cmd := exec.Command("ffmpeg",
			"-probesize", "32",
			"-f", "h264", "-i", "pipe:0",
			"-vf", fmt.Sprintf("scale=%d:%d", x, y),
			"-f", "rawvideo", "-pix_fmt", "gray",
			"pipe:1")
		stdin, _ := cmd.StdinPipe()
		stdout, _ := cmd.StdoutPipe()
		cmd.Start()
		vf.cmd = cmd
		vf.stdin = stdin

		// Feed keyframes periodically
		go func() {
			lastKeyframe := []byte(nil)
			for {
				select {
				case <-time.After(250 * time.Millisecond):
					r.videoBroadcast.frameMu.RLock()
					keyframe := r.videoBroadcast.latestFrame
					r.videoBroadcast.frameMu.RUnlock()
					if len(keyframe) > 0 && !bytes.Equal(keyframe, lastKeyframe) {
						if _, err := stdin.Write(keyframe); err != nil {
							return // Pipe closed, exit feeder
						}
						lastKeyframe = append([]byte(nil), keyframe...)
					}
				case <-vf.stopCh:
					return
				}
			}
		}()

		// Read decoded frames
		go func() {
			frameSize, firstFrame := x*y, true
			for {
				buf := make([]byte, frameSize)
				if _, err := io.ReadFull(stdout, buf); err != nil {
					return
				}
				vf.frameMu.Lock()
				vf.latestFrame = buf
				vf.frameMu.Unlock()
				if firstFrame {
					close(vf.readyCh)
					firstFrame = false
				}
			}
		}()

		// Auto-stop after idle
		for {
			select {
			case <-time.After(time.Second):
				vf.frameMu.Lock()
				idle := time.Since(vf.lastAccess)
				vf.frameMu.Unlock()
				if idle > 5*time.Second {
					r.viewfinderMu.Lock()
					r.stopViewfinder()
					r.viewfinder = nil
					r.viewfinderMu.Unlock()
					return
				}
			case <-vf.stopCh:
				return
			}
		}
	}()
}

func (r *Recorder) stopViewfinder() {
	if r.viewfinder == nil {
		return
	}
	vf := r.viewfinder
	close(vf.stopCh)

	// Close stdin first to unblock any pending writes and signal EOF to ffmpeg
	if vf.stdin != nil {
		vf.stdin.Close()
	}

	if vf.cmd != nil && vf.cmd.Process != nil {
		vf.cmd.Process.Signal(syscall.SIGTERM)
		go func() {
			done := make(chan error, 1)
			go func() { done <- vf.cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					log.Printf("Recorder: Viewfinder ffmpeg exited: %v", err)
				} else {
					log.Println("Recorder: Viewfinder stopped")
				}
			case <-time.After(3 * time.Second):
				if err := vf.cmd.Process.Kill(); err != nil {
					log.Printf("Recorder: Failed to kill viewfinder ffmpeg: %v", err)
				}
				<-done
			}
		}()
	}
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

func (r *Recorder) StartAudioStream() (chan []byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.audioBroadcast == nil {
		return nil, fmt.Errorf("audio broadcast not available")
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

	if r.audioBroadcast != nil && r.audioStreamCh != nil {
		r.audioBroadcast.removeConsumer(r.audioStreamCh)
	}
	if r.audioStreamCh != nil {
		r.audioStreamCh = nil
	}

	log.Println("Recorder: Stopped audio streaming")
	return nil
}
