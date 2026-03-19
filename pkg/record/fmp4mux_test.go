package record

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/Eyevinn/mp4ff/avc"
)

// makeTestGOP builds a realistic H.264 Annex B GOP: SPS + PPS + IDR + (n-1) P-slices
// SPS is Constrained Baseline, Level 4.0, 1920x1080
func makeTestGOP(numFrames int) []byte {
	sc := []byte{0x00, 0x00, 0x00, 0x01}
	var buf bytes.Buffer
	// SPS (NAL type 7): Constrained Baseline, Level 4.0, 1920x1080
	buf.Write(sc)
	buf.Write([]byte{
		0x67, 0x42, 0xC0, 0x28, 0xD9, 0x00, 0x78, 0x02,
		0x27, 0xE5, 0xC0, 0x5B, 0x80, 0x80, 0x80, 0xA0,
		0x00, 0x00, 0x03, 0x00, 0x20, 0x00, 0x00, 0x06,
		0x51, 0xE3, 0x06, 0x49, 0x80,
	})
	// PPS (NAL type 8)
	buf.Write(sc)
	buf.Write([]byte{0x68, 0xCE, 0x38, 0x80})
	// IDR slice (NAL type 5)
	buf.Write(sc)
	buf.Write([]byte{0x65, 0x88, 0x80, 0x40, 0x00, 0xFF, 0xAB})
	// P-slices (NAL type 1)
	for i := 1; i < numFrames; i++ {
		buf.Write(sc)
		buf.Write([]byte{0x41, 0x9A, 0x24, 0x6C, byte(i)})
	}
	return buf.Bytes()
}

func readBox(data []byte) (boxType string, boxSize uint32, rest []byte) {
	if len(data) < 8 {
		return "", 0, nil
	}
	boxSize = binary.BigEndian.Uint32(data[:4])
	boxType = string(data[4:8])
	return boxType, boxSize, data[boxSize:]
}

func TestInitSegment(t *testing.T) {
	var buf bytes.Buffer
	m := newFMP4Muxer(&buf)
	m.Write(makeTestGOP(1))
	// Need a second GOP to flush the first
	m.Write(makeTestGOP(1))

	typ, _, rest := readBox(buf.Bytes())
	if typ != "ftyp" {
		t.Fatalf("expected ftyp, got %s", typ)
	}
	typ, _, _ = readBox(rest)
	if typ != "moov" {
		t.Fatalf("expected moov, got %s", typ)
	}
}

func TestFragmentPerGOP(t *testing.T) {
	var buf bytes.Buffer
	m := newFMP4Muxer(&buf)

	// Write 3 GOPs; first two flush on GOP boundary, third on Flush()
	for range 3 {
		m.Write(makeTestGOP(5))
	}
	m.Flush()

	data := buf.Bytes()
	var boxes []string
	for len(data) >= 8 {
		var typ string
		typ, _, data = readBox(data)
		boxes = append(boxes, typ)
	}

	// Expect: ftyp moov (init) + 3x (moof mdat)
	expected := []string{"ftyp", "moov", "moof", "mdat", "moof", "mdat", "moof", "mdat"}
	if len(boxes) != len(expected) {
		t.Fatalf("expected %d boxes, got %d: %v", len(expected), len(boxes), boxes)
	}
	for i, e := range expected {
		if boxes[i] != e {
			t.Fatalf("box[%d]: expected %s, got %s", i, e, boxes[i])
		}
	}
}

func TestEmptyFlush(t *testing.T) {
	var buf bytes.Buffer
	m := newFMP4Muxer(&buf)
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %d bytes", buf.Len())
	}
}

// Simulates: seed with keyframe, then orphan P-frames arrive (live mid-GOP),
// then a full GOP. Orphan P-frames must be dropped to avoid decode errors
func TestSeedDropsOrphanFrames(t *testing.T) {
	sc := []byte{0x00, 0x00, 0x00, 0x01}

	// Orphan P-frames (no SPS/IDR — as if we joined a live stream mid-GOP)
	var orphans bytes.Buffer
	for i := range 3 {
		orphans.Write(sc)
		orphans.Write([]byte{0x41, 0x9A, 0x24, byte(i)})
	}

	var buf bytes.Buffer
	m := newFMP4Muxer(&buf)
	m.SeedKeyframe(makeTestGOP(5))   // emits init only (ftyp+moov), no fragment
	m.Write(orphans.Bytes())         // orphan P-frames, no IDR
	m.Write(makeTestGOP(5))          // flushes the orphans (dropped — no IDR)
	m.Write(makeTestGOP(5))          // flushes the proper GOP
	m.Flush()                        // flushes the last GOP

	// Seed emits only init (no moof), orphans are dropped, so 2 moof boxes total
	data := buf.Bytes()
	moofCount := 0
	for len(data) >= 8 {
		size := binary.BigEndian.Uint32(data[:4])
		if string(data[4:8]) == "moof" {
			moofCount++
		}
		data = data[size:]
	}
	if moofCount != 2 {
		t.Fatalf("expected 2 moof boxes (orphans dropped), got %d", moofCount)
	}
}

func TestSequenceNumbers(t *testing.T) {
	var buf bytes.Buffer
	m := newFMP4Muxer(&buf)
	for range 4 {
		m.Write(makeTestGOP(5))
	}
	m.Flush()

	// Verify sequence numbers increment: skip ftyp+moov, then check moof headers
	data := buf.Bytes()
	seqNums := []uint32{}
	for len(data) >= 8 {
		size := binary.BigEndian.Uint32(data[:4])
		typ := string(data[4:8])
		if typ == "moof" && size > 24 {
			// mfhd box is at offset 8 inside moof; seqnum at offset 12 within mfhd
			mfhdType := string(data[8+4 : 8+8])
			if mfhdType == "mfhd" {
				seqNums = append(seqNums, binary.BigEndian.Uint32(data[8+12:8+16]))
			}
		}
		data = data[size:]
	}

	for i, sn := range seqNums {
		if sn != uint32(i+1) {
			t.Fatalf("seqNum[%d]: expected %d, got %d", i, i+1, sn)
		}
	}
}

func TestFindStartCode(t *testing.T) {
	sc4 := []byte{0x00, 0x00, 0x00, 0x01}
	sc3 := []byte{0x00, 0x00, 0x01}

	tests := []struct {
		name     string
		data     []byte
		naluType avc.NaluType
		want     int
	}{
		{"SPS with 4-byte start code", append(sc4, 0x67, 0x42), avc.NALU_SPS, 0},
		{"SPS with 3-byte start code", append(sc3, 0x67, 0x42), avc.NALU_SPS, 0},
		{"IDR after SPS+PPS", makeTestGOP(1), avc.NALU_IDR, 41}, // SPS start code(4) + SPS(29) + PPS start code(4) + PPS(4) = 41
		{"no match returns -1", append(sc4, 0x41, 0x9A), avc.NALU_SPS, -1},
		{"empty data", []byte{}, avc.NALU_SPS, -1},
		{"too short", []byte{0x00, 0x00, 0x01}, avc.NALU_SPS, -1},
		{"P-slice not found as IDR", append(sc4, 0x41, 0x9A, 0x24), avc.NALU_IDR, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findStartCode(tt.data, tt.naluType)
			if got != tt.want {
				t.Errorf("findStartCode(%s) = %d, want %d", tt.name, got, tt.want)
			}
		})
	}
}
