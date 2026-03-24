package record

import (
	"bytes"
	"encoding/binary"
	"io"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/mp4"

	"root-firmware/pkg/globals"
)

const fmp4Timescale = uint32(90000)
const sampleDur = fmp4Timescale / uint32(globals.CameraFramerate) // 6000 ticks per frame

// fmp4Muxer converts raw H.264 Annex B into fragmented MP4, flushing per GOP
type fmp4Muxer struct {
	w          io.Writer
	seqNum     uint32
	decodeTime uint64
	initDone   bool
	gopBuf     []byte
}

func newFMP4Muxer(w io.Writer) *fmp4Muxer {
	return &fmp4Muxer{w: w}
}

// SeedKeyframe emits the init segment (ftyp+moov) from a keyframe's SPS/PPS
// Frame data is not emitted — stale frames would cause decoder timestamp errors
func (m *fmp4Muxer) SeedKeyframe(data []byte) error {
	return m.writeInit(avc.GetParameterSetsFromByteStream(data))
}

// writeInit creates and writes the init segment (ftyp+moov) from SPS/PPS
func (m *fmp4Muxer) writeInit(spss, ppss [][]byte) error {
	if len(spss) == 0 || len(ppss) == 0 {
		return nil
	}
	init := mp4.CreateEmptyInit()
	trak := init.AddEmptyTrack(fmp4Timescale, "video", "und")
	if err := trak.SetAVCDescriptor("avc1", spss, ppss, true); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		return err
	}
	m.initDone = true
	_, err := m.w.Write(buf.Bytes())
	return err
}

// Write accepts raw H.264 Annex B data, flushing a fragment at each GOP boundary (SPS)
func (m *fmp4Muxer) Write(data []byte) (int, error) {
	n := len(data)
	for len(data) > 0 {
		idx := findStartCode(data, avc.NALU_SPS)
		if idx < 0 {
			m.gopBuf = append(m.gopBuf, data...)
			break
		}
		m.gopBuf = append(m.gopBuf, data[:idx]...)
		if err := m.Flush(); err != nil {
			return 0, err
		}
		data = data[idx:]
		sc := startCodeLen(data)
		m.gopBuf = append(m.gopBuf, data[:sc]...)
		data = data[sc:]
	}
	return n, nil
}

// Flush writes any remaining buffered data as a final fragment
func (m *fmp4Muxer) Flush() error {
	if len(m.gopBuf) == 0 {
		return nil
	}
	gopData := m.gopBuf
	m.gopBuf = m.gopBuf[:0]

	// Emit init segment on first GOP with SPS/PPS
	if !m.initDone {
		if err := m.writeInit(avc.GetParameterSetsFromByteStream(gopData)); err != nil {
			return err
		}
		if !m.initDone {
			return nil // No SPS/PPS found
		}
	}

	var buf bytes.Buffer

	nalus := avc.ExtractNalusFromByteStream(gopData)
	frag, err := mp4.CreateFragment(m.seqNum+1, 1)
	if err != nil {
		return err
	}

	// Build AVCC samples, checking for IDR presence
	type sample struct {
		data  []byte
		flags uint32
	}
	var samples []sample
	hasIDR := false
	for _, nalu := range nalus {
		if len(nalu) == 0 || !avc.IsVideoNaluType(avc.GetNaluType(nalu[0])) {
			continue
		}
		avccData := lengthPrefix(nalu)
		flags := mp4.NonSyncSampleFlags
		if avc.IsIDRSample(avccData) {
			flags = mp4.SyncSampleFlags
			hasIDR = true
		}
		samples = append(samples, sample{avccData, flags})
	}

	// Drop fragments without an IDR — orphan P-frames would cause decode errors
	if len(samples) == 0 || !hasIDR {
		return nil
	}

	for i, s := range samples {
		frag.AddFullSample(mp4.FullSample{
			Sample:     mp4.NewSample(s.flags, sampleDur, uint32(len(s.data)), 0),
			DecodeTime: m.decodeTime + uint64(i)*uint64(sampleDur),
			Data:       s.data,
		})
	}
	m.seqNum++
	m.decodeTime += uint64(len(samples)) * uint64(sampleDur)
	if err := frag.Encode(&buf); err != nil {
		return err
	}
	_, err = m.w.Write(buf.Bytes())
	return err
}

// lengthPrefix wraps a raw NALU with a 4-byte big-endian length (AVCC format)
func lengthPrefix(nalu []byte) []byte {
	out := make([]byte, 4+len(nalu))
	binary.BigEndian.PutUint32(out, uint32(len(nalu)))
	copy(out[4:], nalu)
	return out
}

// findStartCode returns the offset of the first start code with matching NALU type, or -1
func findStartCode(data []byte, naluType avc.NaluType) int {
	end := len(data) - 4
	for i := range end {
		if data[i] != 0 || data[i+1] != 0 {
			continue
		}
		var nalByte int
		if data[i+2] == 1 {
			nalByte = i + 3
		} else if data[i+2] == 0 && i+3 < len(data) && data[i+3] == 1 {
			nalByte = i + 4
		} else {
			continue
		}
		if nalByte < len(data) && avc.GetNaluType(data[nalByte]) == naluType {
			return i
		}
	}
	return -1
}

// startCodeLen returns 3 or 4 depending on the Annex B start code prefix
func startCodeLen(data []byte) int {
	if len(data) >= 4 && data[0] == 0 && data[1] == 0 && data[2] == 0 && data[3] == 1 {
		return 4
	}
	return 3
}
