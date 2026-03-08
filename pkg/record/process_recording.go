package record

import (
	"bytes"
	"fmt"
	"log"
	"os/exec"
	"sync"
	"time"

	"root-firmware/pkg/globals"
	"root-firmware/pkg/storage"
)

// Ring buffer capacities derived from max recording + lookback durations
var (
	maxBufferDuration = globals.MaxRecordDuration + globals.LookbackDuration + 5*time.Second // headroom
	gopsPerSecond     = globals.CameraFramerate / globals.CameraGOPSize
	audioChunksPerSec = (globals.AudioSampleRate * 2) / globals.AudioChunkSize // S16_LE mono = 2 bytes/sample
	videoRingCapacity = int(maxBufferDuration.Seconds())*gopsPerSecond + 10
	audioRingCapacity = int(maxBufferDuration.Seconds())*audioChunksPerSec + 10
)

// lookbackEntry holds a timestamped chunk of video (complete GOP) or audio (raw PCM)
type lookbackEntry struct {
	data      []byte
	timestamp time.Time
}

// lookbackBuffer is a fixed-capacity circular buffer that retains recent entries
type lookbackBuffer struct {
	mu       sync.Mutex
	entries  []lookbackEntry
	head     int
	count    int
	capacity int
}

func newLookbackBuffer(capacity int) *lookbackBuffer {
	return &lookbackBuffer{entries: make([]lookbackEntry, capacity), capacity: capacity}
}

func (lb *lookbackBuffer) push(data []byte) {
	lb.mu.Lock()
	copied := make([]byte, len(data))
	copy(copied, data)
	lb.entries[lb.head] = lookbackEntry{data: copied, timestamp: time.Now()}
	lb.head = (lb.head + 1) % lb.capacity
	if lb.count < lb.capacity {
		lb.count++
	}
	lb.mu.Unlock()
}

// flush returns entries from the last maxAge, oldest first.
// Returned entries reference the ring's byte slices directly — callers must
// consume them before the ring overwrites those slots (~28s of headroom).
func (lb *lookbackBuffer) flush(maxAge time.Duration) []lookbackEntry {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	result := make([]lookbackEntry, 0, lb.count)
	start := (lb.head - lb.count + lb.capacity) % lb.capacity
	for i := 0; i < lb.count; i++ {
		e := lb.entries[(start+i)%lb.capacity]
		if e.timestamp.After(cutoff) {
			result = append(result, e)
		}
	}
	return result
}

// muxJob represents a recording that needs to be muxed to MP4/M4A and saved
type muxJob struct {
	videoEntries []lookbackEntry
	audioEntries []lookbackEntry
	outputPath   string
	duration     float64
	eventID      string
	eventType    string
	preview      []byte
	detection    *storage.DetectionResult
}

// muxWorker processes mux jobs serially — one ffmpeg at a time to avoid CPU contention on the Pi
func (r *Recorder) muxWorker() {
	for job := range r.muxQueue {
		if err := r.muxVideo(job.videoEntries, job.outputPath, job.duration); err != nil {
			log.Printf("Recorder: Skipping save for %s due to mux failure", job.outputPath)
			continue
		}
		r.muxAudio(job.audioEntries, job.videoEntries[0].timestamp, job.outputPath)
		storage.Get().SaveRecording(job.eventID, job.outputPath, job.duration, job.eventType, job.preview, job.detection)
	}
}

// muxVideo writes H.264 GOPs to an MP4 file using ffmpeg copy mode
func (r *Recorder) muxVideo(entries []lookbackEntry, outputPath string, duration float64) error {
	if len(entries) == 0 {
		return fmt.Errorf("no video entries")
	}

	var buf bytes.Buffer
	for _, e := range entries {
		buf.Write(e.data)
	}

	log.Printf("Recorder: Muxing %d GOPs (%.2fs, %dKB) to %s", len(entries), duration, buf.Len()/1024, outputPath)

	cmd := exec.Command("ffmpeg", "-f", "h264", "-i", "pipe:0", "-c:v", "copy", "-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", outputPath)
	cmd.Stdin = &buf
	if err := cmd.Run(); err != nil {
		log.Printf("Recorder: Failed to mux video: %v", err)
		return err
	}
	return nil
}

// muxAudio writes PCM audio entries to an M4A file, aligned to the video start time
func (r *Recorder) muxAudio(entries []lookbackEntry, videoStart time.Time, outputPath string) {
	if len(entries) == 0 {
		return
	}

	var buf bytes.Buffer
	for _, e := range entries {
		if !e.timestamp.Before(videoStart) {
			buf.Write(e.data)
		}
	}

	// Check buffer length since all entries might be from before videoStart
	if buf.Len() == 0 {
		return
	}

	audioPath := outputPath[:len(outputPath)-4] + "_audio.m4a"
	log.Printf("Recorder: Muxing audio (%dKB) to %s", buf.Len()/1024, audioPath)
	cmd := exec.Command("ffmpeg", "-f", "s16le", "-ar", fmt.Sprintf("%d", globals.AudioSampleRate), "-ac", "1", "-i", "pipe:0", "-c:a", "aac", "-f", "mp4", audioPath)
	cmd.Stdin = &buf
	if err := cmd.Run(); err != nil {
		log.Printf("Recorder: Failed to mux audio: %v", err)
	}
}
