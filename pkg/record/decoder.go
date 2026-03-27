package record

import (
	"fmt"
	"image"
	"log"
	"sync"

	openh264 "github.com/y9o/go-openh264"
	"golang.org/x/image/draw"
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
// Required because OpenH264 needs NALs fed individually (SPS/PPS before IDR)
func splitNALs(data []byte) [][]byte {
	var nals [][]byte
	start := -1
	for i := 0; i < len(data)-2; i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			// Skip leading zero for 4-byte start code (0x00 0x00 0x00 0x01)
			nalStart := i
			if i > 0 && data[i-1] == 0 {
				nalStart = i - 1
			}
			if start >= 0 {
				nals = append(nals, data[start:nalStart])
			}
			start = nalStart
		}
	}
	if start >= 0 {
		nals = append(nals, data[start:])
	}
	return nals
}

// scalePlane scales a single image plane, always returning a newly allocated image
func scalePlane(pix []byte, stride, srcW, srcH, dstW, dstH int) *image.Gray {
	src := &image.Gray{Pix: pix, Stride: stride, Rect: image.Rect(0, 0, srcW, srcH)}
	dst := image.NewGray(image.Rect(0, 0, dstW, dstH))
	if srcW == dstW && srcH == dstH {
		copy(dst.Pix, src.Pix)
	} else {
		draw.BiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	}
	return dst
}

// decodeAndScale decodes an H.264 keyframe (SPS+PPS+IDR) and scales each YCbCr plane
// independently, avoiding color-space conversion - Applies luma LUT on the scaled Y plane
func (d *h264Decoder) decodeAndScale(nalData []byte, x, y int) (*image.YCbCr, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, nal := range splitNALs(nalData) {
		var planes [3][]byte
		var info openh264.SBufferInfo

		ret := d.dec.DecodeFrameNoDelay(nal, len(nal), &planes, &info)
		if ret&0x1000 != 0 {
			return nil, fmt.Errorf("DecodeFrameNoDelay fatal error: 0x%x", ret)
		}
		if info.IBufferStatus != 1 {
			continue
		}

		sys := info.UsrData_sSystemBuffer()
		w, h := int(sys.IWidth), int(sys.IHeight)
		chromaX, chromaY := (x+1)/2, (y+1)/2

		yDst := scalePlane(planes[0], int(sys.IStride[0]), w, h, x, y)
		cbDst := scalePlane(planes[1], int(sys.IStride[1]), w/2, h/2, chromaX, chromaY)
		crDst := scalePlane(planes[2], int(sys.IStride[1]), w/2, h/2, chromaX, chromaY)

		for i := range yDst.Pix {
			yDst.Pix[i] = lumaLUT[yDst.Pix[i]]
		}

		return &image.YCbCr{
			Y:              yDst.Pix,
			Cb:             cbDst.Pix,
			Cr:             crDst.Pix,
			YStride:        yDst.Stride,
			CStride:        cbDst.Stride,
			Rect:           image.Rect(0, 0, x, y),
			SubsampleRatio: image.YCbCrSubsampleRatio420,
		}, nil
	}

	return nil, fmt.Errorf("no frame produced from input")
}

// LUT for expanding BT.601 limited range luma to full range
// Y: [16,235] → [0,255]. Chroma is left unchanged to preserve the neutral point at 128
var lumaLUT [256]uint8

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
