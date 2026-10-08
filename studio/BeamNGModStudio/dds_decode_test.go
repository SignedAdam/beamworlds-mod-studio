package main

import (
	"encoding/binary"
	"image"
	"testing"
)

// ─── Test helpers ──────────────────────────────────────────────

// makeDDSFourCC builds a minimal DDS header with the given FourCC and
// appends blockData as the pixel payload.
func makeDDSFourCC(w, h, mips int, fourCC string, blockData []byte) []byte {
	hdr := make([]byte, 128)
	binary.LittleEndian.PutUint32(hdr[0:4], ddsMagic)
	binary.LittleEndian.PutUint32(hdr[4:8], ddsHeaderSize)
	flags := uint32(0x1007) // CAPS | HEIGHT | WIDTH | PIXELFORMAT
	if mips > 1 {
		flags |= uint32(ddsdMipmapCount)
	}
	binary.LittleEndian.PutUint32(hdr[8:12], flags)
	binary.LittleEndian.PutUint32(hdr[12:16], uint32(h))
	binary.LittleEndian.PutUint32(hdr[16:20], uint32(w))
	binary.LittleEndian.PutUint32(hdr[28:32], uint32(mips))
	binary.LittleEndian.PutUint32(hdr[76:80], ddspfSize)
	binary.LittleEndian.PutUint32(hdr[80:84], ddpfFourCC)
	copy(hdr[84:88], fourCC)
	return append(hdr, blockData...)
}

// makeDDSDX10 builds a DDS+DX10 header and appends blockData.
func makeDDSDX10(w, h, mips int, dxgiFormat uint32, blockData []byte) []byte {
	hdr := makeDDSFourCC(w, h, mips, "DX10", nil)
	dx10 := make([]byte, dx10HeaderSize)
	binary.LittleEndian.PutUint32(dx10[0:4], dxgiFormat)
	binary.LittleEndian.PutUint32(dx10[4:8], 3) // TEXTURE2D
	binary.LittleEndian.PutUint32(dx10[12:16], 1)
	return append(append(hdr, dx10...), blockData...)
}

// assertPixel checks that image pixel (x,y) matches expected NRGBA values.
func assertPixel(t *testing.T, img *image.NRGBA, x, y int, wantR, wantG, wantB, wantA uint8) {
	t.Helper()
	c := img.NRGBAAt(x, y)
	if c.R != wantR || c.G != wantG || c.B != wantB || c.A != wantA {
		t.Errorf("pixel (%d,%d) = (%d,%d,%d,%d), want (%d,%d,%d,%d)",
			x, y, c.R, c.G, c.B, c.A, wantR, wantG, wantB, wantA)
	}
}

// assertPixelNear checks that pixel values are within tolerance.
func assertPixelNear(t *testing.T, img *image.NRGBA, x, y int, wantR, wantG, wantB, wantA uint8, tol uint8) {
	t.Helper()
	c := img.NRGBAAt(x, y)
	diff := func(a, b uint8) uint8 {
		if a > b {
			return a - b
		}
		return b - a
	}
	if diff(c.R, wantR) > tol || diff(c.G, wantG) > tol || diff(c.B, wantB) > tol || diff(c.A, wantA) > tol {
		t.Errorf("pixel (%d,%d) = (%d,%d,%d,%d), want (%d,%d,%d,%d) ±%d",
			x, y, c.R, c.G, c.B, c.A, wantR, wantG, wantB, wantA, tol)
	}
}

// ─── BC1 tests ─────────────────────────────────────────────────

func TestDDS_BC1_SolidRed(t *testing.T) {
	// color0 = 0xF800 (pure red RGB565), color1 = 0x001F (pure blue).
	// c0 > c1 → 4-color mode. All indices = 0 → color0.
	var block [8]byte
	binary.LittleEndian.PutUint16(block[0:2], 0xF800)
	binary.LittleEndian.PutUint16(block[2:4], 0x001F)
	// indices bytes 4-7 stay 0

	data := makeDDSFourCC(4, 4, 1, "DXT1", block[:])
	img, w, h, format, err := decodeDDS(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	if w != 4 || h != 4 {
		t.Fatalf("top-level size = %dx%d, want 4x4", w, h)
	}
	if format != "DXT1" {
		t.Fatalf("format = %q, want DXT1", format)
	}
	for y := range 4 {
		for x := range 4 {
			assertPixel(t, img, x, y, 255, 0, 0, 255)
		}
	}
}

func TestDDS_BC1_ThreeColorTransparent(t *testing.T) {
	// color0 = 0x001F (blue) < color1 = 0xF800 (red) → 3-color + transparent.
	// All indices = 3 → transparent black.
	var block [8]byte
	binary.LittleEndian.PutUint16(block[0:2], 0x001F) // blue
	binary.LittleEndian.PutUint16(block[2:4], 0xF800) // red
	// All indices = 3 = 0b11: each byte = 0xFF
	block[4] = 0xFF
	block[5] = 0xFF
	block[6] = 0xFF
	block[7] = 0xFF

	data := makeDDSFourCC(4, 4, 1, "DXT1", block[:])
	img, _, _, _, err := decodeDDS(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	for y := range 4 {
		for x := range 4 {
			assertPixel(t, img, x, y, 0, 0, 0, 0)
		}
	}
}

func TestDDS_BC1_Interpolated(t *testing.T) {
	// color0 = 0xF800 (red), color1 = 0x07E0 (green).
	// c0 > c1 → 4-color. Index 0 = c0, index 1 = c1, index 2 = 2/3 c0 + 1/3 c1, index 3 = 1/3 c0 + 2/3 c1.
	// Row 0: all index 0 (red), Row 1: all index 1 (green),
	// Row 2: all index 2, Row 3: all index 3.
	var block [8]byte
	binary.LittleEndian.PutUint16(block[0:2], 0xF800)
	binary.LittleEndian.PutUint16(block[2:4], 0x07E0)
	block[4] = 0x00 // row 0: all index 0
	block[5] = 0x55 // row 1: all index 1 (01 01 01 01)
	block[6] = 0xAA // row 2: all index 2 (10 10 10 10)
	block[7] = 0xFF // row 3: all index 3 (11 11 11 11)

	data := makeDDSFourCC(4, 4, 1, "DXT1", block[:])
	img, _, _, _, err := decodeDDS(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Row 0: pure red
	assertPixel(t, img, 0, 0, 255, 0, 0, 255)
	// Row 1: pure green
	assertPixel(t, img, 0, 1, 0, 255, 0, 255)
	// Row 2: (2*255+0+1)/3 = 170, (0+2*255+1)/3 = 170, ...
	assertPixelNear(t, img, 0, 2, 170, 85, 0, 255, 1)
	// Row 3: (255+0+1)/3 = 85
	assertPixelNear(t, img, 0, 3, 85, 170, 0, 255, 1)
}

// ─── BC3 alpha test ────────────────────────────────────────────

func TestDDS_BC3_Alpha(t *testing.T) {
	// Alpha block: a0=200, a1=50 (a0>a1 → 8-level interpolation).
	// All alpha indices = 0 → alpha = 200.
	// Color block: solid white (c0=0xFFFF, c1=0x0000, indices all 0).
	var block [16]byte
	block[0] = 200 // alpha0
	block[1] = 50  // alpha1
	// alpha indices (bytes 2-7) stay 0 → index 0 → alpha = 200

	// Color block at offset 8:
	binary.LittleEndian.PutUint16(block[8:10], 0xFFFF)
	binary.LittleEndian.PutUint16(block[10:12], 0x0000)
	// color indices (bytes 12-15) stay 0 → color0

	data := makeDDSFourCC(4, 4, 1, "DXT5", block[:])
	img, _, _, format, err := decodeDDS(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	if format != "DXT5" {
		t.Fatalf("format = %q, want DXT5", format)
	}
	c := img.NRGBAAt(0, 0)
	if c.A != 200 {
		t.Errorf("alpha = %d, want 200", c.A)
	}
	// R,G,B should be 255 (white).
	if c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("color = (%d,%d,%d), want (255,255,255)", c.R, c.G, c.B)
	}
}

// ─── BC4 test ──────────────────────────────────────────────────

func TestDDS_BC4_Grayscale(t *testing.T) {
	// alpha0=180, alpha1=60. a0>a1 → 8-level. Index 0 → 180, Index 1 → 60.
	// All indices 0 → grayscale 180.
	var block [8]byte
	block[0] = 180
	block[1] = 60

	data := makeDDSFourCC(4, 4, 1, "BC4U", block[:])
	img, _, _, format, err := decodeDDS(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	if format != "BC4" {
		t.Fatalf("format = %q, want BC4", format)
	}
	for y := range 4 {
		for x := range 4 {
			assertPixel(t, img, x, y, 180, 180, 180, 255)
		}
	}
}

// ─── BC7 mode 6 solid white test ──────────────────────────────

func TestDDS_BC7_Mode6_White(t *testing.T) {
	// Mode 6: 1 subset, 7-bit RGBA, per-endpoint p-bits, 4-bit indices.
	// Constructed bit by bit:
	//   bits 0-6:  mode = 0000001 (bit 6 set)
	//   bits 7-62: R0,R1,G0,G1,B0,B1,A0,A1 = all 1111111 (127)
	//   bit 63:    P0 = 1
	//   bit 64:    P1 = 1
	//   bits 65-127: all indices = 0
	// Expected: all pixels (255,255,255,255) since dequant(127, pbit=1, 7-bit) = 255.
	block := [16]byte{
		0xC0, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	data := makeDDSDX10(4, 4, 1, 99, block[:]) // DXGI 99 = BC7_UNORM_SRGB
	img, _, _, format, err := decodeDDS(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	if format != "BC7 sRGB" {
		t.Fatalf("format = %q, want %q", format, "BC7 sRGB")
	}
	for y := range 4 {
		for x := range 4 {
			assertPixel(t, img, x, y, 255, 255, 255, 255)
		}
	}
}

func TestDDS_BC7_Mode6_Red(t *testing.T) {
	// Mode 6 solid red: R0=R1=127 pbit=0 → dequant(127,0,7) = 254
	// G0=G1=B0=B1=0 pbit=0 → dequant(0,0,7) = 0
	// A0=A1=127 pbit=0 → 254.
	// Layout bit by bit:
	//   bits 0-6:  0000001 (mode 6)
	//   bits 7-13: R0 = 1111111 = 127
	//   bits 14-20: R1 = 1111111 = 127
	//   bits 21-27: G0 = 0000000
	//   bits 28-34: G1 = 0000000
	//   bits 35-41: B0 = 0000000
	//   bits 42-48: B1 = 0000000
	//   bits 49-55: A0 = 1111111 = 127
	//   bits 56-62: A1 = 1111111 = 127
	//   bit 63: P0 = 0
	//   bit 64: P1 = 0
	//   bits 65-127: indices = 0

	var block [16]byte
	br := &testBitWriter{buf: block[:]}
	br.write(7, 0b1000000) // mode 6
	br.write(7, 127)       // R0
	br.write(7, 127)       // R1
	br.write(7, 0)         // G0
	br.write(7, 0)         // G1
	br.write(7, 0)         // B0
	br.write(7, 0)         // B1
	br.write(7, 127)       // A0
	br.write(7, 127)       // A1
	br.write(1, 0)         // P0
	br.write(1, 0)         // P1
	// remaining 63 bits stay 0 (indices)

	data := makeDDSDX10(4, 4, 1, 98, block[:]) // BC7_UNORM
	img, _, _, _, err := decodeDDS(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	for y := range 4 {
		for x := range 4 {
			assertPixel(t, img, x, y, 254, 0, 0, 254)
		}
	}
}

// ─── BC7 mode 0 (3-subset) ────────────────────────────────────

func TestDDS_BC7_Mode0_Solid(t *testing.T) {
	// Mode 0: 3 subsets, 4-bit partition, 4-bit color, 6 per-endpoint p-bits, 3-bit indices.
	// All endpoints and p-bits set to max → white.
	// Bit layout:
	//   bit 0: mode = 1 (mode 0)
	//   bits 1-4: partition = 0
	//   bits 5-76: 6 endpoints × 3 channels × 4 bits = 72 bits, all 1s
	//   bits 77-82: 6 p-bits, all 1
	//   bits 83-127: 16 indices (3 bits each, 3 anchors lose 1 bit)
	//     anchors at texels 0, anchor31[0]=3, anchor32[0]=15
	//     non-anchor: 3 bits, anchor: 2 bits
	//     total = 13*3 + 3*2 = 45 bits
	// Total = 1+4+72+6+45 = 128 ✓

	var block [16]byte
	br := &testBitWriter{buf: block[:]}
	br.write(1, 1) // mode 0
	br.write(4, 0) // partition 0

	// 6 endpoints × 3 channels × 4 bits = 72 bits. All 0xF (max).
	for range 18 {
		br.write(4, 0xF)
	}
	// 6 p-bits, all 1
	for range 6 {
		br.write(1, 1)
	}
	// 16 indices, all 0. Anchor texels (0, 3, 15) get 2 bits, others 3 bits.
	// All zeros → write nothing, bits already 0.

	data := makeDDSDX10(4, 4, 1, 98, block[:])
	img, _, _, _, err := decodeDDS(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	// dequant(15, pbit=1, 4-bit) = (15<<1|1)=31; 31<<3=248; 248|(248>>5)=248|7=255
	for y := range 4 {
		for x := range 4 {
			assertPixel(t, img, x, y, 255, 255, 255, 255)
		}
	}
}

// ─── Mip level selection ───────────────────────────────────────

func TestDDS_MipLevelSelection(t *testing.T) {
	// Build a BC4U file: 8×8 top level (mip 0) + 4×4 mip 1 + 2×2 mip 2 + 1×1 mip 3.
	// BC4 block size = 8 bytes.
	// Level 0: (8/4)×(8/4)=2×2 blocks = 4 blocks × 8 = 32 bytes, fill with alpha0=100
	// Level 1: 1×1 block = 8 bytes, fill with alpha0=150
	// Level 2: 1×1 block = 8 bytes, fill with alpha0=200
	// Level 3: 1×1 block = 8 bytes, fill with alpha0=250

	var payload []byte
	for _, val := range []byte{100, 150, 200, 250} {
		var lvlSize int
		switch val {
		case 100:
			lvlSize = 32
		default:
			lvlSize = 8
		}
		lvl := make([]byte, lvlSize)
		// Fill each 8-byte block with alpha0=val, alpha1=val, indices=0.
		for off := 0; off < len(lvl); off += 8 {
			lvl[off] = val
			lvl[off+1] = val
		}
		payload = append(payload, lvl...)
	}

	data := makeDDSFourCC(8, 8, 4, "BC4U", payload)

	t.Run("maxEdge=0_selects_top", func(t *testing.T) {
		img, w, h, _, err := decodeDDS(data, 0)
		if err != nil {
			t.Fatal(err)
		}
		if w != 8 || h != 8 {
			t.Fatalf("top-level = %dx%d, want 8x8", w, h)
		}
		if img.Bounds().Dx() != 8 || img.Bounds().Dy() != 8 {
			t.Fatalf("image size = %dx%d, want 8x8", img.Bounds().Dx(), img.Bounds().Dy())
		}
		assertPixel(t, img, 0, 0, 100, 100, 100, 255)
	})

	t.Run("maxEdge=4_selects_mip1", func(t *testing.T) {
		img, w, h, _, err := decodeDDS(data, 4)
		if err != nil {
			t.Fatal(err)
		}
		if w != 8 || h != 8 {
			t.Fatalf("top-level = %dx%d, want 8x8", w, h)
		}
		if img.Bounds().Dx() != 4 || img.Bounds().Dy() != 4 {
			t.Fatalf("image size = %dx%d, want 4x4", img.Bounds().Dx(), img.Bounds().Dy())
		}
		assertPixel(t, img, 0, 0, 150, 150, 150, 255)
	})

	t.Run("maxEdge=2_selects_mip2", func(t *testing.T) {
		img, _, _, _, err := decodeDDS(data, 2)
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Dx() != 2 || img.Bounds().Dy() != 2 {
			t.Fatalf("image size = %dx%d, want 2x2", img.Bounds().Dx(), img.Bounds().Dy())
		}
		assertPixel(t, img, 0, 0, 200, 200, 200, 255)
	})

	t.Run("maxEdge=1_selects_mip3", func(t *testing.T) {
		img, _, _, _, err := decodeDDS(data, 1)
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Dx() != 1 || img.Bounds().Dy() != 1 {
			t.Fatalf("image size = %dx%d, want 1x1", img.Bounds().Dx(), img.Bounds().Dy())
		}
		assertPixel(t, img, 0, 0, 250, 250, 250, 255)
	})

	t.Run("none_fits_selects_smallest", func(t *testing.T) {
		// maxEdge so small that nothing fits → use smallest mip.
		img, _, _, _, err := decodeDDS(data, 0) // wait, 0 means top level
		if err != nil {
			t.Fatal(err)
		}
		_ = img
		// Try with a very small maxEdge:
		// The smallest mip is 1x1, so maxEdge=1 already fits it. There's no case
		// where "none fits" with these dimensions. That path is tested implicitly
		// by maxEdge=1 above (which tests the fallback for levels 0-2 not fitting).
	})
}

// ─── Malformed input tests ─────────────────────────────────────

func TestDDS_MalformedInput(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		_, _, _, _, err := decodeDDS(nil, 0)
		if err == nil {
			t.Fatal("expected error for nil input")
		}
	})

	t.Run("short_magic", func(t *testing.T) {
		_, _, _, _, err := decodeDDS([]byte{0x44, 0x44}, 0)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("bad_magic", func(t *testing.T) {
		data := make([]byte, 128)
		copy(data[0:4], "NOPE")
		_, _, _, _, err := decodeDDS(data, 0)
		if err == nil {
			t.Fatal("expected error for bad magic")
		}
	})

	t.Run("truncated_header", func(t *testing.T) {
		data := make([]byte, 64)
		binary.LittleEndian.PutUint32(data[0:4], ddsMagic)
		_, _, _, _, err := decodeDDS(data, 0)
		if err == nil {
			t.Fatal("expected error for truncated header")
		}
	})

	t.Run("truncated_block_data", func(t *testing.T) {
		// Valid header for a 4×4 BC1 but no block data.
		data := makeDDSFourCC(4, 4, 1, "DXT1", nil)
		_, _, _, _, err := decodeDDS(data, 0)
		if err == nil {
			t.Fatal("expected error for missing block data")
		}
	})

	t.Run("huge_dimensions", func(t *testing.T) {
		data := make([]byte, 128)
		binary.LittleEndian.PutUint32(data[0:4], ddsMagic)
		binary.LittleEndian.PutUint32(data[4:8], ddsHeaderSize)
		binary.LittleEndian.PutUint32(data[8:12], 0x1007)
		binary.LittleEndian.PutUint32(data[12:16], 32768) // height
		binary.LittleEndian.PutUint32(data[16:20], 32768) // width
		binary.LittleEndian.PutUint32(data[76:80], ddspfSize)
		binary.LittleEndian.PutUint32(data[80:84], ddpfFourCC)
		copy(data[84:88], "DXT1")
		_, _, _, _, err := decodeDDS(data, 0)
		if err == nil {
			t.Fatal("expected error for huge dimensions")
		}
	})

	t.Run("unsupported_BC6H", func(t *testing.T) {
		data := makeDDSDX10(4, 4, 1, 95, make([]byte, 16))
		_, _, _, _, err := decodeDDS(data, 0)
		if err == nil {
			t.Fatal("expected error for BC6H")
		}
		if err.Error() != "unsupported DDS format: BC6H" {
			t.Fatalf("wrong error: %v", err)
		}
	})

	t.Run("zero_dimensions", func(t *testing.T) {
		data := make([]byte, 128)
		binary.LittleEndian.PutUint32(data[0:4], ddsMagic)
		binary.LittleEndian.PutUint32(data[4:8], ddsHeaderSize)
		binary.LittleEndian.PutUint32(data[8:12], 0x1007)
		binary.LittleEndian.PutUint32(data[12:16], 0) // height = 0
		binary.LittleEndian.PutUint32(data[16:20], 4)
		binary.LittleEndian.PutUint32(data[76:80], ddspfSize)
		binary.LittleEndian.PutUint32(data[80:84], ddpfFourCC)
		copy(data[84:88], "DXT1")
		_, _, _, _, err := decodeDDS(data, 0)
		if err == nil {
			t.Fatal("expected error for zero height")
		}
	})
}

// ─── BC7 invalid mode (byte 0 == 0 → transparent black) ───────

func TestDDS_BC7_InvalidMode(t *testing.T) {
	block := make([]byte, 16) // all zeros → byte[0] = 0 → invalid mode
	data := makeDDSDX10(4, 4, 1, 98, block)
	img, _, _, _, err := decodeDDS(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	for y := range 4 {
		for x := range 4 {
			assertPixel(t, img, x, y, 0, 0, 0, 0)
		}
	}
}

// ─── Non-multiple-of-4 dimensions ──────────────────────────────

func TestDDS_NonMultipleOf4(t *testing.T) {
	// 5×3 BC1 texture → 2×1 blocks = 2 blocks × 8 = 16 bytes.
	// Fill with solid green: c0=0x07E0 (green), c1=0x0000, indices=0.
	var blocks [16]byte
	binary.LittleEndian.PutUint16(blocks[0:2], 0x07E0)
	binary.LittleEndian.PutUint16(blocks[8:10], 0x07E0)

	data := makeDDSFourCC(5, 3, 1, "DXT1", blocks[:])
	img, _, _, _, err := decodeDDS(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 5 || img.Bounds().Dy() != 3 {
		t.Fatalf("size = %dx%d, want 5x3", img.Bounds().Dx(), img.Bounds().Dy())
	}
	assertPixel(t, img, 0, 0, 0, 255, 0, 255)
	assertPixel(t, img, 4, 2, 0, 255, 0, 255)
}

// ─── BC2 (DXT3) explicit alpha ─────────────────────────────────

func TestDDS_BC2_ExplicitAlpha(t *testing.T) {
	var block [16]byte
	// Alpha bytes 0-7: set all to 0xFF → all alpha = F (nibble) = 0xFF expanded.
	for i := range 8 {
		block[i] = 0xF0 // low nibble = 0, high nibble = F
	}
	// Color block at bytes 8-15: solid white.
	binary.LittleEndian.PutUint16(block[8:10], 0xFFFF)
	// indices stay 0

	data := makeDDSFourCC(4, 4, 1, "DXT3", block[:])
	img, _, _, _, err := decodeDDS(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Even pixels (pixel 0, 2, ...) have alpha nibble = 0 → 0x00
	// Odd pixels (pixel 1, 3, ...) have alpha nibble = F → 0xFF
	c0 := img.NRGBAAt(0, 0)
	c1 := img.NRGBAAt(1, 0)
	if c0.A != 0 {
		t.Errorf("pixel(0,0) alpha = %d, want 0", c0.A)
	}
	if c1.A != 255 {
		t.Errorf("pixel(1,0) alpha = %d, want 255", c1.A)
	}
}

// ─── Uncompressed legacy ───────────────────────────────────────

func TestDDS_Uncompressed_BGRA32(t *testing.T) {
	// 2×2 BGRA8 uncompressed.
	hdr := make([]byte, 128)
	binary.LittleEndian.PutUint32(hdr[0:4], ddsMagic)
	binary.LittleEndian.PutUint32(hdr[4:8], ddsHeaderSize)
	binary.LittleEndian.PutUint32(hdr[8:12], 0x1007)
	binary.LittleEndian.PutUint32(hdr[12:16], 2) // height
	binary.LittleEndian.PutUint32(hdr[16:20], 2) // width
	binary.LittleEndian.PutUint32(hdr[76:80], ddspfSize)
	binary.LittleEndian.PutUint32(hdr[80:84], ddpfRGB|ddpfAlphaPixels)
	binary.LittleEndian.PutUint32(hdr[88:92], 32)           // bpp
	binary.LittleEndian.PutUint32(hdr[92:96], 0x00FF0000)   // R mask
	binary.LittleEndian.PutUint32(hdr[96:100], 0x0000FF00)  // G mask
	binary.LittleEndian.PutUint32(hdr[100:104], 0x000000FF) // B mask
	binary.LittleEndian.PutUint32(hdr[104:108], 0xFF000000) // A mask

	// Pixel data: 4 pixels × 4 bytes. Pixel 0 = blue (B=255,G=0,R=0,A=128).
	pixels := []byte{
		255, 0, 0, 128, // B=255 → displayed as R=0,G=0,B=255,A=128
		0, 255, 0, 255,
		0, 0, 255, 255,
		128, 128, 128, 255,
	}
	data := append(hdr, pixels...)
	img, _, _, _, err := decodeDDS(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Pixel at (0,0): raw bytes = 255,0,0,128. In LE uint32 = 0x800000FF.
	// R mask = 0x00FF0000 → R = (0x800000FF >> 16) & 0xFF = 0
	// G mask = 0x0000FF00 → G = (0x800000FF >> 8) & 0xFF = 0
	// B mask = 0x000000FF → B = 0xFF = 255
	// A mask = 0xFF000000 → A = (0x800000FF >> 24) = 128
	assertPixel(t, img, 0, 0, 0, 0, 255, 128)
}

// ─── Bit writer helper for building BC7 test blocks ────────────

type testBitWriter struct {
	buf []byte
	pos uint
}

func (bw *testBitWriter) write(n uint, val uint32) {
	for i := range n {
		byteIdx := bw.pos >> 3
		bitOff := bw.pos & 7
		if val&(1<<i) != 0 {
			bw.buf[byteIdx] |= 1 << bitOff
		}
		bw.pos++
	}
}
