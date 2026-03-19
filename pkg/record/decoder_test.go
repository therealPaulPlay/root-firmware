package record

import (
	"image"
	"testing"
)

// --- splitNALs tests ---

// Verify output includes start code prefix
func TestSplitNALs_PreservesStartCode(t *testing.T) {
	data := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x80}
	nals := splitNALs(data)

	if len(nals) != 1 {
		t.Fatalf("splitNALs() = %d NALs, want 1", len(nals))
	}
	if nals[0][0] != 0x00 || nals[0][1] != 0x00 || nals[0][2] != 0x00 || nals[0][3] != 0x01 {
		t.Error("NAL should include start code")
	}
}

// Mix of 4-byte and 3-byte start codes with content verification
func TestSplitNALs_MixedStartCodes(t *testing.T) {
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
	if len(nals[0]) != 6 || nals[0][4] != 0x67 || nals[0][5] != 0x42 {
		t.Errorf("NAL 1 = %x, want 4-byte start + 67 42", nals[0])
	}
	if len(nals[1]) != 5 || nals[1][3] != 0x68 || nals[1][4] != 0xce {
		t.Errorf("NAL 2 = %x, want 3-byte start + 68 ce", nals[1])
	}
	if len(nals[2]) != 5 || nals[2][4] != 0x65 {
		t.Errorf("NAL 3 = %x, want 4-byte start + 65", nals[2])
	}
}

// Edge cases: empty, too short for start code
func TestSplitNALs_TooShort(t *testing.T) {
	for _, input := range [][]byte{{}, {0x00}, {0x00, 0x00}} {
		if nals := splitNALs(input); len(nals) != 0 {
			t.Errorf("splitNALs(%x) = %d NALs, want 0", input, len(nals))
		}
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
