package record

import (
	"sync"
	"time"

	"root-firmware/pkg/globals"
)

// Ring buffer capacities derived from max recording + lookback durations
var (
	maxBufferDuration = globals.MaxRecordDuration + globals.LookbackDuration + 5*time.Second // headroom
	gopsPerSecond     = globals.CameraFramerate / globals.CameraGOPSize
	audioChunksPerSec = (globals.AudioSampleRate*2 + globals.AudioChunkSize - 1) / globals.AudioChunkSize // S16_LE mono, rounded up
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

// flush returns entries from the last maxAge, oldest first
// They reference the ring's byte slices directly — callers must
// consume them before the ring overwrites those slots
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
