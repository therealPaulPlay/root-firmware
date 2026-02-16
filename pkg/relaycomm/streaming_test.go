package relaycomm

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func TestReadMP4Box_ValidBox(t *testing.T) {
	// Create a valid MP4 box: 4 bytes size (big endian) + 4 bytes type + payload
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint32(12)) // size = 12 (8 header + 4 payload)
	buf.WriteString("ftyp")                         // box type
	buf.WriteString("test")                         // payload

	box, err := readMP4Box(buf)
	if err != nil {
		t.Fatalf("readMP4Box() error = %v", err)
	}

	if len(box) != 12 {
		t.Errorf("box length = %d, want 12", len(box))
	}
	if string(box[4:8]) != "ftyp" {
		t.Errorf("box type = %s, want ftyp", string(box[4:8]))
	}
}

func TestReadMP4Box_MinimalBox(t *testing.T) {
	// Box with just header (size=8), no payload
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint32(8))
	buf.WriteString("moov")

	box, err := readMP4Box(buf)
	if err != nil {
		t.Fatalf("readMP4Box() error = %v", err)
	}

	if len(box) != 8 {
		t.Errorf("box length = %d, want 8", len(box))
	}
}

func TestReadMP4Box_SizeTooSmall(t *testing.T) {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint32(4)) // size < 8 is invalid
	buf.WriteString("test")

	_, err := readMP4Box(buf)
	if err != io.ErrUnexpectedEOF {
		t.Errorf("readMP4Box() error = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestReadMP4Box_ExceedsSafetyLimit(t *testing.T) {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint32(11*1024*1024)) // 11MB > 10MB limit
	buf.WriteString("mdat")

	_, err := readMP4Box(buf)
	if err == nil {
		t.Error("readMP4Box() should error when size exceeds safety limit")
	}
}

func TestReadMP4Box_IncompleteHeader(t *testing.T) {
	buf := bytes.NewBuffer([]byte{0x00, 0x00, 0x00}) // Only 3 bytes

	_, err := readMP4Box(buf)
	if err != io.ErrUnexpectedEOF {
		t.Errorf("readMP4Box() error = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestReadMP4Box_IncompletePayload(t *testing.T) {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint32(100)) // Claims 100 bytes
	buf.WriteString("mdat")
	buf.WriteString("short") // But only 5 bytes of payload

	_, err := readMP4Box(buf)
	if err != io.ErrUnexpectedEOF {
		t.Errorf("readMP4Box() error = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestReadMP4Box_MultipleBoxes(t *testing.T) {
	buf := new(bytes.Buffer)

	// First box
	binary.Write(buf, binary.BigEndian, uint32(12))
	buf.WriteString("ftyp")
	buf.WriteString("mp41")

	// Second box
	binary.Write(buf, binary.BigEndian, uint32(16))
	buf.WriteString("moov")
	buf.WriteString("testdata")

	// Read first box
	box1, err := readMP4Box(buf)
	if err != nil {
		t.Fatalf("first readMP4Box() error = %v", err)
	}
	if string(box1[4:8]) != "ftyp" {
		t.Errorf("first box type = %s, want ftyp", string(box1[4:8]))
	}

	// Read second box
	box2, err := readMP4Box(buf)
	if err != nil {
		t.Fatalf("second readMP4Box() error = %v", err)
	}
	if string(box2[4:8]) != "moov" {
		t.Errorf("second box type = %s, want moov", string(box2[4:8]))
	}
}
