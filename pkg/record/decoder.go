package record

import (
	"fmt"
	"image"
	"log"
	"sync"

	openh264 "github.com/y9o/go-openh264"
)

const openh264Library = "libopenh264.so"

type h264Decoder struct {
	dec *openh264.ISVCDecoder
	mu  sync.Mutex
}

var decoderOnce sync.Once

// initOpenH264 loads the OpenH264 shared library - must be called before using the decoder
func initOpenH264() error {
	var err error
	decoderOnce.Do(func() {
		err = openh264.Open(openh264Library)
		if err != nil {
			err = fmt.Errorf("failed to load OpenH264 library: %w", err)
		}
	})
	return err
}

func newDecoder() (*h264Decoder, error) {
	var dec *openh264.ISVCDecoder
	if ret := openh264.WelsCreateDecoder(&dec); ret != 0 {
		return nil, fmt.Errorf("WelsCreateDecoder failed: %d", ret)
	}

	param := openh264.SDecodingParam{
		EEcActiveIdc: openh264.ERROR_CON_SLICE_MV_COPY_CROSS_IDR_FREEZE_RES_CHANGE,
	}
	if ret := dec.Initialize(&param); ret != 0 {
		openh264.WelsDestroyDecoder(dec)
		return nil, fmt.Errorf("decoder Initialize failed: %d", ret)
	}

	// Suppress verbose logging
	traceLevel := 1 // WELS_LOG_ERROR only
	dec.SetOption(openh264.DECODER_OPTION_TRACE_LEVEL, &traceLevel)

	log.Println("Recorder: OpenH264 decoder initialized")
	return &h264Decoder{dec: dec}, nil
}

// splitNALs splits Annex B H.264 data into individual NAL units (each prefixed with its start code)
func splitNALs(data []byte) [][]byte {
	var nals [][]byte
	start := -1
	for i := 0; i < len(data)-3; i++ {
		if data[i] == 0 && data[i+1] == 0 && ((data[i+2] == 1) || (i+3 < len(data) && data[i+2] == 0 && data[i+3] == 1)) {
			if start >= 0 {
				nals = append(nals, data[start:i])
			}
			start = i
		}
	}
	if start >= 0 {
		nals = append(nals, data[start:])
	}
	return nals
}

// decode takes raw H.264 Annex B data (SPS+PPS+IDR) and returns a decoded YCbCr image
// NAL units are fed individually so the decoder processes parameter sets before the IDR slice
func (d *h264Decoder) decode(nalData []byte) (*image.YCbCr, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	nals := splitNALs(nalData)
	if len(nals) == 0 {
		return nil, fmt.Errorf("no NAL units found in input")
	}

	for _, nal := range nals {
		var dst [3][]byte
		var info openh264.SBufferInfo

		ret := d.dec.DecodeFrameNoDelay(nal, len(nal), &dst, &info)
		if ret&0x1000 != 0 {
			return nil, fmt.Errorf("DecodeFrameNoDelay fatal error: 0x%x", ret)
		}

		if info.IBufferStatus == 1 {
			sys := info.UsrData_sSystemBuffer()
			return &image.YCbCr{
				Y:              dst[0],
				Cb:             dst[1],
				Cr:             dst[2],
				YStride:        int(sys.IStride[0]),
				CStride:        int(sys.IStride[1]),
				Rect:           image.Rect(0, 0, int(sys.IWidth), int(sys.IHeight)),
				SubsampleRatio: image.YCbCrSubsampleRatio420,
			}, nil
		}
	}

	return nil, fmt.Errorf("no frame produced after decoding %d NAL units", len(nals))
}

// LUT for expanding BT.601 limited range luma to full range in-place
// Y: [16,235] → [0,255]. Chroma is left unchanged to preserve the neutral point at 128
var lumaLUT [256]uint8

// init() functions run before main() in Go
func init() {
	for i := range 256 {
		v := (i - 16) * 255 / 219
		if v < 0 {
			v = 0
		} else if v > 255 {
			v = 255
		}
		lumaLUT[i] = uint8(v)
	}
}

// expandLimitedRange expands luma from BT.601 limited range to full range in-place
func expandLimitedRange(img *image.YCbCr) {
	for i := range img.Y {
		img.Y[i] = lumaLUT[img.Y[i]]
	}
}
