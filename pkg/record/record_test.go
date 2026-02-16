package record

import (
	"image"
	"testing"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/testutil"
)

func setupTestRecord(t *testing.T) func() {
	t.Helper()

	config.ResetForTesting()

	cleanupGlobals := testutil.SetupTempGlobals(t)

	if err := config.Init(); err != nil {
		t.Fatalf("config.Init() error = %v", err)
	}

	return func() {
		cleanupGlobals()
		config.ResetForTesting()
	}
}

// --- splitNALs tests ---

func TestSplitNALs_Empty(t *testing.T) {
	nals := splitNALs([]byte{})
	if len(nals) != 0 {
		t.Errorf("splitNALs([]) = %d NALs, want 0", len(nals))
	}
}

func TestSplitNALs_SingleNAL(t *testing.T) {
	// Single NAL with 4-byte start code, payload without 0x00 bytes
	data := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x80, 0x1e}
	nals := splitNALs(data)

	if len(nals) != 1 {
		t.Fatalf("splitNALs() = %d NALs, want 1", len(nals))
	}
}

func TestSplitNALs_MultipleNALs(t *testing.T) {
	// SPS + PPS + IDR with clean payloads (no embedded 0x00 0x00 patterns)
	data := []byte{
		// NAL 1: SPS
		0x00, 0x00, 0x00, 0x01, 0x67, 0x42,
		// NAL 2: PPS
		0x00, 0x00, 0x00, 0x01, 0x68, 0xce,
		// NAL 3: IDR
		0x00, 0x00, 0x00, 0x01, 0x65, 0x88,
	}
	nals := splitNALs(data)

	if len(nals) != 3 {
		t.Errorf("splitNALs() = %d NALs, want 3", len(nals))
	}
}

func TestSplitNALs_ThreeByteStartCode(t *testing.T) {
	// 3-byte start code (0x00 0x00 0x01), payload without 0x00 bytes
	data := []byte{0x00, 0x00, 0x01, 0x67, 0x42, 0x80}
	nals := splitNALs(data)

	if len(nals) != 1 {
		t.Errorf("splitNALs() = %d NALs, want 1", len(nals))
	}
}

func TestSplitNALs_PreservesStartCode(t *testing.T) {
	// Verify NALs include their start codes
	data := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x80}
	nals := splitNALs(data)

	if len(nals) != 1 {
		t.Fatalf("splitNALs() = %d NALs, want 1", len(nals))
	}

	// First bytes should be start code
	if nals[0][0] != 0x00 || nals[0][1] != 0x00 || nals[0][2] != 0x00 || nals[0][3] != 0x01 {
		t.Error("NAL should include start code")
	}
}

func TestSplitNALs_MixedStartCodes(t *testing.T) {
	// Mix of 4-byte and 3-byte start codes
	data := []byte{
		// NAL 1: 4-byte start code
		0x00, 0x00, 0x00, 0x01, 0x67, 0x42,
		// NAL 2: 3-byte start code
		0x00, 0x00, 0x01, 0x68, 0xce,
		// NAL 3: 4-byte start code
		0x00, 0x00, 0x00, 0x01, 0x65,
	}
	nals := splitNALs(data)

	if len(nals) != 3 {
		t.Fatalf("splitNALs() = %d NALs, want 3", len(nals))
	}

	// Verify NAL 1: 4-byte start + payload 0x67 0x42
	if len(nals[0]) != 6 || nals[0][4] != 0x67 || nals[0][5] != 0x42 {
		t.Errorf("NAL 1 = %x, want 4-byte start + 67 42", nals[0])
	}

	// Verify NAL 2: 3-byte start + payload 0x68 0xce
	if len(nals[1]) != 5 || nals[1][3] != 0x68 || nals[1][4] != 0xce {
		t.Errorf("NAL 2 = %x, want 3-byte start + 68 ce", nals[1])
	}

	// Verify NAL 3: 4-byte start + payload 0x65
	if len(nals[2]) != 5 || nals[2][4] != 0x65 {
		t.Errorf("NAL 3 = %x, want 4-byte start + 65", nals[2])
	}
}

func TestSplitNALs_TooShort(t *testing.T) {
	// Input shorter than minimum start code (3 bytes)
	if nals := splitNALs([]byte{0x00}); len(nals) != 0 {
		t.Errorf("splitNALs([00]) = %d NALs, want 0", len(nals))
	}
	if nals := splitNALs([]byte{0x00, 0x00}); len(nals) != 0 {
		t.Errorf("splitNALs([00 00]) = %d NALs, want 0", len(nals))
	}
}

// --- lookbackBuffer tests ---

func TestLookbackBuffer_New(t *testing.T) {
	lb := newLookbackBuffer(10)

	if lb.capacity != 10 {
		t.Errorf("capacity = %d, want 10", lb.capacity)
	}
	if lb.count != 0 {
		t.Errorf("count = %d, want 0", lb.count)
	}
}

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

// --- broadcast tests ---

func TestBroadcast_AddRemoveConsumer(t *testing.T) {
	b := &broadcast{consumers: make([]chan []byte, 0)}

	ch := make(chan []byte, 10)
	b.addConsumer(ch)

	if len(b.consumers) != 1 {
		t.Errorf("consumers = %d, want 1", len(b.consumers))
	}

	b.removeConsumer(ch)

	if len(b.consumers) != 0 {
		t.Errorf("consumers after remove = %d, want 0", len(b.consumers))
	}
}

func TestBroadcast_RemoveClosesChannel(t *testing.T) {
	b := &broadcast{consumers: make([]chan []byte, 0)}

	ch := make(chan []byte, 10)
	b.addConsumer(ch)
	b.removeConsumer(ch)

	// Channel should be closed
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel should be closed")
		}
	default:
		t.Error("channel should be closed, not blocking")
	}
}

func TestBroadcast_Write(t *testing.T) {
	b := &broadcast{consumers: make([]chan []byte, 0)}

	ch1 := make(chan []byte, 10)
	ch2 := make(chan []byte, 10)
	b.addConsumer(ch1)
	b.addConsumer(ch2)

	b.write([]byte{1, 2, 3})

	// Both consumers should receive
	data1 := <-ch1
	data2 := <-ch2

	if len(data1) != 3 || len(data2) != 3 {
		t.Error("both consumers should receive data")
	}
}

func TestBroadcast_WriteCopiesData(t *testing.T) {
	b := &broadcast{consumers: make([]chan []byte, 0)}

	ch := make(chan []byte, 10)
	b.addConsumer(ch)

	original := []byte{1, 2, 3}
	b.write(original)

	received := <-ch
	original[0] = 99

	if received[0] != 1 {
		t.Error("write should copy data, not reference it")
	}
}

func TestBroadcast_WriteDropsWhenFull(t *testing.T) {
	b := &broadcast{consumers: make([]chan []byte, 0)}

	ch := make(chan []byte, 1) // Small buffer
	b.addConsumer(ch)

	b.write([]byte{1})
	b.write([]byte{2}) // Should be dropped

	if len(ch) != 1 {
		t.Error("second write should be dropped when channel is full")
	}
}

func TestBroadcast_CloseAll(t *testing.T) {
	b := &broadcast{consumers: make([]chan []byte, 0)}

	ch1 := make(chan []byte, 10)
	ch2 := make(chan []byte, 10)
	b.addConsumer(ch1)
	b.addConsumer(ch2)

	b.closeAll()

	if len(b.consumers) != 0 {
		t.Error("closeAll should clear consumers")
	}

	// Both channels should be closed
	_, ok1 := <-ch1
	_, ok2 := <-ch2
	if ok1 || ok2 {
		t.Error("closeAll should close all channels")
	}
}

// --- lumaLUT / expandLimitedRange tests ---

func TestLumaLUT_Range(t *testing.T) {
	// BT.601 limited range: 16 -> 0, 235 -> 255
	if lumaLUT[16] != 0 {
		t.Errorf("lumaLUT[16] = %d, want 0", lumaLUT[16])
	}
	if lumaLUT[235] != 255 {
		t.Errorf("lumaLUT[235] = %d, want 255", lumaLUT[235])
	}

	// Mid-range: 128 should map to ~130 ((128-16)*255/219 ≈ 130)
	if lumaLUT[128] < 128 || lumaLUT[128] > 132 {
		t.Errorf("lumaLUT[128] = %d, want ~130", lumaLUT[128])
	}

	// Below 16 should clamp to 0
	if lumaLUT[0] != 0 {
		t.Errorf("lumaLUT[0] = %d, want 0", lumaLUT[0])
	}

	// Above 235 should clamp to 255
	if lumaLUT[255] != 255 {
		t.Errorf("lumaLUT[255] = %d, want 255", lumaLUT[255])
	}
}

func TestExpandLimitedRange(t *testing.T) {
	img := &image.YCbCr{
		Y:              []byte{16, 128, 235},
		Cb:             []byte{128, 128, 128},
		Cr:             []byte{128, 128, 128},
		YStride:        3,
		CStride:        3,
		Rect:           image.Rect(0, 0, 3, 1),
		SubsampleRatio: image.YCbCrSubsampleRatio444,
	}

	expandLimitedRange(img)

	if img.Y[0] != 0 {
		t.Errorf("Y[0] = %d, want 0 (from 16)", img.Y[0])
	}
	if img.Y[2] != 255 {
		t.Errorf("Y[2] = %d, want 255 (from 235)", img.Y[2])
	}

	// Chroma should be unchanged
	if img.Cb[0] != 128 || img.Cr[0] != 128 {
		t.Error("chroma should be unchanged")
	}
}

// --- MicEnabled tests ---

func TestMicEnabled_DefaultTrue(t *testing.T) {
	cleanup := setupTestRecord(t)
	defer cleanup()

	if !MicEnabled() {
		t.Error("MicEnabled() should default to true")
	}
}

func TestMicEnabled_ExplicitlyDisabled(t *testing.T) {
	cleanup := setupTestRecord(t)
	defer cleanup()

	config.Get().SetKey("microphoneEnabled", false)

	if MicEnabled() {
		t.Error("MicEnabled() should return false when disabled")
	}
}
