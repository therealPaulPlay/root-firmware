package record

import (
	"testing"
	"time"
)

func TestLookbackBuffer_Push(t *testing.T) {
	lb := newLookbackBuffer(10)

	lb.push([]byte{1, 2, 3})
	lb.push([]byte{4, 5, 6})

	if lb.count != 2 {
		t.Errorf("count = %d, want 2", lb.count)
	}
}

func TestLookbackBuffer_PushCopiesData(t *testing.T) {
	lb := newLookbackBuffer(10)

	data := []byte{1, 2, 3}
	lb.push(data)

	// Modify original
	data[0] = 99

	// Buffer should have original value
	entries := lb.flush(time.Hour)
	if entries[0].data[0] != 1 {
		t.Error("push should copy data, not reference it")
	}
}

func TestLookbackBuffer_FlushEmpty(t *testing.T) {
	lb := newLookbackBuffer(10)

	entries := lb.flush(time.Hour)
	if len(entries) != 0 {
		t.Errorf("flush() on empty buffer = %d entries, want 0", len(entries))
	}
}

func TestLookbackBuffer_FlushReturnsOldestFirst(t *testing.T) {
	lb := newLookbackBuffer(10)

	lb.push([]byte{1})
	lb.push([]byte{2})
	lb.push([]byte{3})

	entries := lb.flush(time.Hour)

	if len(entries) != 3 {
		t.Fatalf("flush() = %d entries, want 3", len(entries))
	}
	if entries[0].data[0] != 1 || entries[1].data[0] != 2 || entries[2].data[0] != 3 {
		t.Error("entries should be oldest first")
	}
}

func TestLookbackBuffer_FlushRespectsMaxAge(t *testing.T) {
	lb := newLookbackBuffer(10)

	lb.push([]byte{1})
	time.Sleep(50 * time.Millisecond)
	lb.push([]byte{2})

	// Only get entries from last 30ms
	entries := lb.flush(30 * time.Millisecond)

	if len(entries) != 1 {
		t.Fatalf("flush(30ms) = %d entries, want 1", len(entries))
	}
	if entries[0].data[0] != 2 {
		t.Error("should only return recent entry")
	}
}

func TestLookbackBuffer_Circular(t *testing.T) {
	lb := newLookbackBuffer(3)

	lb.push([]byte{1})
	lb.push([]byte{2})
	lb.push([]byte{3})
	lb.push([]byte{4}) // Overwrites first

	if lb.count != 3 {
		t.Errorf("count = %d, want 3 (capacity)", lb.count)
	}

	entries := lb.flush(time.Hour)
	if len(entries) != 3 {
		t.Fatalf("flush() = %d entries, want 3", len(entries))
	}

	// Should have 2, 3, 4 (oldest first)
	if entries[0].data[0] != 2 || entries[1].data[0] != 3 || entries[2].data[0] != 4 {
		t.Errorf("entries = %v, want [2 3 4]", []byte{entries[0].data[0], entries[1].data[0], entries[2].data[0]})
	}
}
