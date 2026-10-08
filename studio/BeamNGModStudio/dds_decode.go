package main

import (
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"math/bits"
)

// ─── DDS header constants ──────────────────────────────────────

const (
	ddsMagic       uint32 = 0x20534444 // "DDS "
	ddsHeaderSize         = 124
	ddspfSize             = 32
	dx10HeaderSize        = 20

	ddsdMipmapCount uint32 = 0x20000

	ddpfAlphaPixels uint32 = 0x1
	ddpfFourCC      uint32 = 0x4
	ddpfRGB         uint32 = 0x40
	ddpfLuminance   uint32 = 0x20000

	ddsMaxDim = 16384
)

// ─── Internal format codes ─────────────────────────────────────

type ddsFormat int

const (
	dfBC1 ddsFormat = iota
	dfBC2
	dfBC3
	dfBC4
	dfBC4S
	dfBC5
	dfBC5S
	dfBC7
	dfRGBA8
	dfBGRA8
	dfBGRX8
	dfUncompressed
)

// ─── Parsed DDS info ───────────────────────────────────────────

type ddsInfo struct {
	width, height int
	mipCount      int
	format        ddsFormat
	label         string
	dataOffset    int
	bpp           int    // uncompressed only
	rMask, gMask  uint32 // uncompressed only
	bMask, aMask  uint32
	luminance     bool
}

// ─── BC7 interpolation weights ─────────────────────────────────

var bc7Weights2 = [4]uint32{0, 21, 43, 64}
var bc7Weights3 = [8]uint32{0, 9, 18, 27, 37, 46, 55, 64}
var bc7Weights4 = [16]uint32{0, 4, 9, 13, 17, 21, 26, 30, 34, 38, 43, 47, 51, 55, 60, 64}

// BC7 2-subset partition table: 64 partitions × 16 texels.
// Copied verbatim from the BC7 specification / bc7enc reference.
var bc7Partition2 = [1024]byte{
	0, 0, 1, 1, 0, 0, 1, 1, 0, 0, 1, 1, 0, 0, 1, 1,
	0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1,
	0, 1, 1, 1, 0, 1, 1, 1, 0, 1, 1, 1, 0, 1, 1, 1,
	0, 0, 0, 1, 0, 0, 1, 1, 0, 0, 1, 1, 0, 1, 1, 1,
	0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 1, 1,
	0, 0, 1, 1, 0, 1, 1, 1, 0, 1, 1, 1, 1, 1, 1, 1,
	0, 0, 0, 1, 0, 0, 1, 1, 0, 1, 1, 1, 1, 1, 1, 1,
	0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 1, 1, 0, 1, 1, 1,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 1, 1,
	0, 0, 1, 1, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	0, 0, 0, 0, 0, 0, 0, 1, 0, 1, 1, 1, 1, 1, 1, 1,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 1, 1, 1,
	0, 0, 0, 1, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1,
	0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1,
	0, 0, 0, 0, 1, 0, 0, 0, 1, 1, 1, 0, 1, 1, 1, 1,
	0, 1, 1, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 1, 1, 1, 0,
	0, 1, 1, 1, 0, 0, 1, 1, 0, 0, 0, 1, 0, 0, 0, 0,
	0, 0, 1, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 1, 0, 0, 0, 1, 1, 0, 0, 1, 1, 1, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 1, 1, 0, 0,
	0, 1, 1, 1, 0, 0, 1, 1, 0, 0, 1, 1, 0, 0, 0, 1,
	0, 0, 1, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 0,
	0, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 1, 0, 0,
	0, 1, 1, 0, 0, 1, 1, 0, 0, 1, 1, 0, 0, 1, 1, 0,
	0, 0, 1, 1, 0, 1, 1, 0, 0, 1, 1, 0, 1, 1, 0, 0,
	0, 0, 0, 1, 0, 1, 1, 1, 1, 1, 1, 0, 1, 0, 0, 0,
	0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0,
	0, 1, 1, 1, 0, 0, 0, 1, 1, 0, 0, 0, 1, 1, 1, 0,
	0, 0, 1, 1, 1, 0, 0, 1, 1, 0, 0, 1, 1, 1, 0, 0,
	0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1,
	0, 0, 0, 0, 1, 1, 1, 1, 0, 0, 0, 0, 1, 1, 1, 1,
	0, 1, 0, 1, 1, 0, 1, 0, 0, 1, 0, 1, 1, 0, 1, 0,
	0, 0, 1, 1, 0, 0, 1, 1, 1, 1, 0, 0, 1, 1, 0, 0,
	0, 0, 1, 1, 1, 1, 0, 0, 0, 0, 1, 1, 1, 1, 0, 0,
	0, 1, 0, 1, 0, 1, 0, 1, 1, 0, 1, 0, 1, 0, 1, 0,
	0, 1, 1, 0, 1, 0, 0, 1, 0, 1, 1, 0, 1, 0, 0, 1,
	0, 1, 0, 1, 1, 0, 1, 0, 1, 0, 1, 0, 0, 1, 0, 1,
	0, 1, 1, 1, 0, 0, 1, 1, 1, 1, 0, 0, 1, 1, 1, 0,
	0, 0, 0, 1, 0, 0, 1, 1, 1, 1, 0, 0, 1, 0, 0, 0,
	0, 0, 1, 1, 0, 0, 1, 0, 0, 1, 0, 0, 1, 1, 0, 0,
	0, 0, 1, 1, 1, 0, 1, 1, 1, 1, 0, 1, 1, 1, 0, 0,
	0, 1, 1, 0, 1, 0, 0, 1, 1, 0, 0, 1, 0, 1, 1, 0,
	0, 0, 1, 1, 1, 1, 0, 0, 1, 1, 0, 0, 0, 0, 1, 1,
	0, 1, 1, 0, 0, 1, 1, 0, 1, 0, 0, 1, 1, 0, 0, 1,
	0, 0, 0, 0, 0, 1, 1, 0, 0, 1, 1, 0, 0, 0, 0, 0,
	0, 1, 0, 0, 1, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0,
	0, 0, 1, 0, 0, 1, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 1, 0, 0, 1, 1, 1, 0, 0, 1, 0,
	0, 0, 0, 0, 0, 1, 0, 0, 1, 1, 1, 0, 0, 1, 0, 0,
	0, 1, 1, 0, 1, 1, 0, 0, 1, 0, 0, 1, 0, 0, 1, 1,
	0, 0, 1, 1, 0, 1, 1, 0, 1, 1, 0, 0, 1, 0, 0, 1,
	0, 1, 1, 0, 0, 0, 1, 1, 1, 0, 0, 1, 1, 1, 0, 0,
	0, 0, 1, 1, 1, 0, 0, 1, 1, 1, 0, 0, 0, 1, 1, 0,
	0, 1, 1, 0, 1, 1, 0, 0, 1, 1, 0, 0, 1, 0, 0, 1,
	0, 1, 1, 0, 0, 0, 1, 1, 0, 0, 1, 1, 1, 0, 0, 1,
	0, 1, 1, 1, 1, 1, 1, 0, 1, 0, 0, 0, 0, 0, 0, 1,
	0, 0, 0, 1, 1, 0, 0, 0, 1, 1, 1, 0, 0, 1, 1, 1,
	0, 0, 0, 0, 1, 1, 1, 1, 0, 0, 1, 1, 0, 0, 1, 1,
	0, 0, 1, 1, 0, 0, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0,
	0, 0, 1, 0, 0, 0, 1, 0, 1, 1, 1, 0, 1, 1, 1, 0,
	0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 1, 1, 0, 1, 1, 1,
}

// BC7 3-subset partition table: 64 partitions × 16 texels.
var bc7Partition3 = [1024]byte{
	0, 0, 1, 1, 0, 0, 1, 1, 0, 2, 2, 1, 2, 2, 2, 2,
	0, 0, 0, 1, 0, 0, 1, 1, 2, 2, 1, 1, 2, 2, 2, 1,
	0, 0, 0, 0, 2, 0, 0, 1, 2, 2, 1, 1, 2, 2, 1, 1,
	0, 2, 2, 2, 0, 0, 2, 2, 0, 0, 1, 1, 0, 1, 1, 1,
	0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 2, 2, 1, 1, 2, 2,
	0, 0, 1, 1, 0, 0, 1, 1, 0, 0, 2, 2, 0, 0, 2, 2,
	0, 0, 2, 2, 0, 0, 2, 2, 1, 1, 1, 1, 1, 1, 1, 1,
	0, 0, 1, 1, 0, 0, 1, 1, 2, 2, 1, 1, 2, 2, 1, 1,
	0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2,
	0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2,
	0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 2,
	0, 0, 1, 2, 0, 0, 1, 2, 0, 0, 1, 2, 0, 0, 1, 2,
	0, 1, 1, 2, 0, 1, 1, 2, 0, 1, 1, 2, 0, 1, 1, 2,
	0, 1, 2, 2, 0, 1, 2, 2, 0, 1, 2, 2, 0, 1, 2, 2,
	0, 0, 1, 1, 0, 1, 1, 2, 1, 1, 2, 2, 1, 2, 2, 2,
	0, 0, 1, 1, 2, 0, 0, 1, 2, 2, 0, 0, 2, 2, 2, 0,
	0, 0, 0, 1, 0, 0, 1, 1, 0, 1, 1, 2, 1, 1, 2, 2,
	0, 1, 1, 1, 0, 0, 1, 1, 2, 0, 0, 1, 2, 2, 0, 0,
	0, 0, 0, 0, 1, 1, 2, 2, 1, 1, 2, 2, 1, 1, 2, 2,
	0, 0, 2, 2, 0, 0, 2, 2, 0, 0, 2, 2, 1, 1, 1, 1,
	0, 1, 1, 1, 0, 1, 1, 1, 0, 2, 2, 2, 0, 2, 2, 2,
	0, 0, 0, 1, 0, 0, 0, 1, 2, 2, 2, 1, 2, 2, 2, 1,
	0, 0, 0, 0, 0, 0, 1, 1, 0, 1, 2, 2, 0, 1, 2, 2,
	0, 0, 0, 0, 1, 1, 0, 0, 2, 2, 1, 0, 2, 2, 1, 0,
	0, 1, 2, 2, 0, 1, 2, 2, 0, 0, 1, 1, 0, 0, 0, 0,
	0, 0, 1, 2, 0, 0, 1, 2, 1, 1, 2, 2, 2, 2, 2, 2,
	0, 1, 1, 0, 1, 2, 2, 1, 1, 2, 2, 1, 0, 1, 1, 0,
	0, 0, 0, 0, 0, 1, 1, 0, 1, 2, 2, 1, 1, 2, 2, 1,
	0, 0, 2, 2, 1, 1, 0, 2, 1, 1, 0, 2, 0, 0, 2, 2,
	0, 1, 1, 0, 0, 1, 1, 0, 2, 0, 0, 2, 2, 2, 2, 2,
	0, 0, 1, 1, 0, 1, 2, 2, 0, 1, 2, 2, 0, 0, 1, 1,
	0, 0, 0, 0, 2, 0, 0, 0, 2, 2, 1, 1, 2, 2, 2, 1,
	0, 0, 0, 0, 0, 0, 0, 2, 1, 1, 2, 2, 1, 2, 2, 2,
	0, 2, 2, 2, 0, 0, 2, 2, 0, 0, 1, 2, 0, 0, 1, 1,
	0, 0, 1, 1, 0, 0, 1, 2, 0, 0, 2, 2, 0, 2, 2, 2,
	0, 1, 2, 0, 0, 1, 2, 0, 0, 1, 2, 0, 0, 1, 2, 0,
	0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 0, 0, 0, 0,
	0, 1, 2, 0, 1, 2, 0, 1, 2, 0, 1, 2, 0, 1, 2, 0,
	0, 1, 2, 0, 2, 0, 1, 2, 1, 2, 0, 1, 0, 1, 2, 0,
	0, 0, 1, 1, 2, 2, 0, 0, 1, 1, 2, 2, 0, 0, 1, 1,
	0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 0, 0, 0, 0, 1, 1,
	0, 1, 0, 1, 0, 1, 0, 1, 2, 2, 2, 2, 2, 2, 2, 2,
	0, 0, 0, 0, 0, 0, 0, 0, 2, 1, 2, 1, 2, 1, 2, 1,
	0, 0, 2, 2, 1, 1, 2, 2, 0, 0, 2, 2, 1, 1, 2, 2,
	0, 0, 2, 2, 0, 0, 1, 1, 0, 0, 2, 2, 0, 0, 1, 1,
	0, 2, 2, 0, 1, 2, 2, 1, 0, 2, 2, 0, 1, 2, 2, 1,
	0, 1, 0, 1, 2, 2, 2, 2, 2, 2, 2, 2, 0, 1, 0, 1,
	0, 0, 0, 0, 2, 1, 2, 1, 2, 1, 2, 1, 2, 1, 2, 1,
	0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 2, 2, 2, 2,
	0, 2, 2, 2, 0, 1, 1, 1, 0, 2, 2, 2, 0, 1, 1, 1,
	0, 0, 0, 2, 1, 1, 1, 2, 0, 0, 0, 2, 1, 1, 1, 2,
	0, 0, 0, 0, 2, 1, 1, 2, 2, 1, 1, 2, 2, 1, 1, 2,
	0, 2, 2, 2, 0, 1, 1, 1, 0, 1, 1, 1, 0, 2, 2, 2,
	0, 0, 0, 2, 1, 1, 1, 2, 1, 1, 1, 2, 0, 0, 0, 2,
	0, 1, 1, 0, 0, 1, 1, 0, 0, 1, 1, 0, 2, 2, 2, 2,
	0, 0, 0, 0, 0, 0, 0, 0, 2, 1, 1, 2, 2, 1, 1, 2,
	0, 1, 1, 0, 0, 1, 1, 0, 2, 2, 2, 2, 2, 2, 2, 2,
	0, 0, 2, 2, 0, 0, 1, 1, 0, 0, 1, 1, 0, 0, 2, 2,
	0, 0, 2, 2, 1, 1, 2, 2, 1, 1, 2, 2, 0, 0, 2, 2,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 1, 1, 2,
	0, 0, 0, 2, 0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0, 1,
	0, 2, 2, 2, 1, 2, 2, 2, 0, 2, 2, 2, 1, 2, 2, 2,
	0, 1, 0, 1, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
	0, 1, 1, 1, 2, 0, 1, 1, 2, 2, 0, 1, 2, 2, 2, 0,
}

// BC7 anchor index for the second subset in 2-subset modes.
var bc7Anchor2 = [64]byte{
	15, 15, 15, 15, 15, 15, 15, 15,
	15, 15, 15, 15, 15, 15, 15, 15,
	15, 2, 8, 2, 2, 8, 8, 15,
	2, 8, 2, 2, 8, 8, 2, 2,
	15, 15, 6, 8, 2, 8, 15, 15,
	2, 8, 2, 2, 2, 15, 15, 6,
	6, 2, 6, 8, 15, 15, 2, 2,
	15, 15, 15, 15, 15, 2, 2, 15,
}

// BC7 anchor index for subset 1 in 3-subset modes.
var bc7Anchor31 = [64]byte{
	3, 3, 15, 15, 8, 3, 15, 15,
	8, 8, 6, 6, 6, 5, 3, 3,
	3, 3, 8, 15, 3, 3, 6, 10,
	5, 8, 8, 6, 8, 5, 15, 15,
	8, 15, 3, 5, 6, 10, 8, 15,
	15, 3, 15, 5, 15, 15, 15, 15,
	3, 15, 5, 5, 5, 8, 5, 10,
	5, 10, 8, 13, 15, 12, 3, 3,
}

// BC7 anchor index for subset 2 in 3-subset modes.
var bc7Anchor32 = [64]byte{
	15, 8, 8, 3, 15, 15, 3, 8,
	15, 15, 15, 15, 15, 15, 15, 8,
	15, 8, 15, 3, 15, 8, 15, 8,
	3, 15, 6, 10, 15, 15, 10, 8,
	15, 3, 15, 10, 10, 8, 9, 10,
	6, 15, 8, 15, 3, 6, 6, 8,
	15, 3, 15, 15, 15, 15, 15, 15,
	15, 15, 15, 15, 3, 15, 15, 8,
}

// ─── BC7 mode descriptors ──────────────────────────────────────

type bc7ModeDesc struct {
	numSubsets uint32
	partBits   uint
	rotBits    uint
	idxSelBits uint
	colorBits  uint
	alphaBits  uint
	pbitMode   int // 0 = none, 1 = per-endpoint, 2 = shared per-subset
	indexBits  uint
	index2Bits uint
}

var bc7Modes = [8]bc7ModeDesc{
	{3, 4, 0, 0, 4, 0, 1, 3, 0}, // 0
	{2, 6, 0, 0, 6, 0, 2, 3, 0}, // 1
	{3, 6, 0, 0, 5, 0, 0, 2, 0}, // 2
	{2, 6, 0, 0, 7, 0, 1, 2, 0}, // 3
	{1, 0, 2, 1, 5, 6, 0, 2, 3}, // 4
	{1, 0, 2, 0, 7, 8, 0, 2, 2}, // 5
	{1, 0, 0, 0, 7, 7, 1, 4, 0}, // 6
	{2, 6, 0, 0, 5, 5, 1, 2, 0}, // 7
}

// ─── Bit reader ────────────────────────────────────────────────

type ddsBitReader struct {
	data []byte
	pos  uint
}

func (br *ddsBitReader) read(n uint) uint32 {
	var val uint32
	done := uint(0)
	for done < n {
		byteIdx := br.pos >> 3
		bitOff := br.pos & 7
		avail := 8 - bitOff
		want := n - done
		if want > avail {
			want = avail
		}
		val |= (uint32(br.data[byteIdx]>>bitOff) & ((1 << want) - 1)) << done
		done += want
		br.pos += want
	}
	return val
}

// ─── BC1 block decode ──────────────────────────────────────────

func ddsRGB565(c uint16) (r, g, b uint8) {
	r5 := (c >> 11) & 0x1F
	g6 := (c >> 5) & 0x3F
	b5 := c & 0x1F
	r = uint8((r5 << 3) | (r5 >> 2))
	g = uint8((g6 << 2) | (g6 >> 4))
	b = uint8((b5 << 3) | (b5 >> 2))
	return
}

// decodeBC1Block decodes an 8-byte BC1 color block into 16 NRGBA texels.
// When opaque is true (BC2/BC3 color sub-block), 4-color mode is always used.
func decodeBC1Block(block []byte, opaque bool) (pixels [64]byte) {
	c0 := binary.LittleEndian.Uint16(block[0:2])
	c1 := binary.LittleEndian.Uint16(block[2:4])
	r0, g0, b0 := ddsRGB565(c0)
	r1, g1, b1 := ddsRGB565(c1)

	var pal [4][4]uint8
	pal[0] = [4]uint8{r0, g0, b0, 255}
	pal[1] = [4]uint8{r1, g1, b1, 255}

	if c0 > c1 || opaque {
		pal[2] = [4]uint8{
			uint8((2*uint16(r0) + uint16(r1) + 1) / 3),
			uint8((2*uint16(g0) + uint16(g1) + 1) / 3),
			uint8((2*uint16(b0) + uint16(b1) + 1) / 3),
			255,
		}
		pal[3] = [4]uint8{
			uint8((uint16(r0) + 2*uint16(r1) + 1) / 3),
			uint8((uint16(g0) + 2*uint16(g1) + 1) / 3),
			uint8((uint16(b0) + 2*uint16(b1) + 1) / 3),
			255,
		}
	} else {
		pal[2] = [4]uint8{
			uint8((uint16(r0) + uint16(r1)) / 2),
			uint8((uint16(g0) + uint16(g1)) / 2),
			uint8((uint16(b0) + uint16(b1)) / 2),
			255,
		}
		pal[3] = [4]uint8{0, 0, 0, 0}
	}

	for row := range 4 {
		idxByte := block[4+row]
		for col := range 4 {
			idx := (idxByte >> (2 * uint(col))) & 3
			p := (row*4 + col) * 4
			pixels[p+0] = pal[idx][0]
			pixels[p+1] = pal[idx][1]
			pixels[p+2] = pal[idx][2]
			pixels[p+3] = pal[idx][3]
		}
	}
	return
}

// ─── Alpha block (shared by BC3/BC4/BC5) ───────────────────────

func ddsDecodeAlphaBlock(block []byte, signed bool) (out [16]byte) {
	var a0, a1 int
	if signed {
		a0 = int(int8(block[0]))
		a1 = int(int8(block[1]))
	} else {
		a0 = int(block[0])
		a1 = int(block[1])
	}

	var pal [8]int
	pal[0] = a0
	pal[1] = a1
	if a0 > a1 {
		for i := 1; i <= 6; i++ {
			pal[i+1] = ((7-i)*a0 + i*a1 + 3) / 7
		}
	} else {
		for i := 1; i <= 4; i++ {
			pal[i+1] = ((5-i)*a0 + i*a1 + 2) / 5
		}
		if signed {
			pal[6] = -128
			pal[7] = 127
		} else {
			pal[6] = 0
			pal[7] = 255
		}
	}

	idx := uint64(block[2]) | uint64(block[3])<<8 | uint64(block[4])<<16 |
		uint64(block[5])<<24 | uint64(block[6])<<32 | uint64(block[7])<<40

	for i := range 16 {
		v := pal[(idx>>(3*uint(i)))&7]
		if signed {
			v += 128
		}
		if v < 0 {
			v = 0
		} else if v > 255 {
			v = 255
		}
		out[i] = byte(v)
	}
	return
}

// ─── BC4 block ─────────────────────────────────────────────────

func decodeBC4Block(block []byte, signed bool) (pixels [64]byte) {
	ch := ddsDecodeAlphaBlock(block, signed)
	for i := range 16 {
		p := i * 4
		pixels[p+0] = ch[i]
		pixels[p+1] = ch[i]
		pixels[p+2] = ch[i]
		pixels[p+3] = 255
	}
	return
}

// ─── BC5 block ─────────────────────────────────────────────────

func decodeBC5Block(block []byte, signed bool) (pixels [64]byte) {
	rch := ddsDecodeAlphaBlock(block[0:8], signed)
	gch := ddsDecodeAlphaBlock(block[8:16], signed)

	for i := range 16 {
		r := rch[i]
		g := gch[i]

		// Normal-map Z reconstruction: map [0,255] → [-1,1], compute z.
		nx := float64(r)/127.5 - 1.0
		ny := float64(g)/127.5 - 1.0
		zsq := 1.0 - nx*nx - ny*ny
		if zsq < 0 {
			zsq = 0
		}
		nz := math.Sqrt(zsq)
		bv := nz*127.5 + 127.5
		var b byte
		if bv >= 255 {
			b = 255
		} else {
			b = byte(bv + 0.5)
		}

		p := i * 4
		pixels[p+0] = r
		pixels[p+1] = g
		pixels[p+2] = b
		pixels[p+3] = 255
	}
	return
}

// ─── BC7 helpers ───────────────────────────────────────────────

func bc7Dequant(val, pbit, valBits uint32) uint32 {
	total := valBits + 1
	val = (val << 1) | pbit
	val <<= (8 - total)
	val |= val >> total
	return val
}

func bc7DequantNoPbit(val, valBits uint32) uint32 {
	val <<= (8 - valBits)
	val |= val >> valBits
	return val
}

func bc7Interp(lo, hi, w uint32) uint32 {
	return (lo*(64-w) + hi*w + 32) >> 6
}

func bc7WeightsForBits(n uint) []uint32 {
	switch n {
	case 2:
		return bc7Weights2[:]
	case 3:
		return bc7Weights3[:]
	case 4:
		return bc7Weights4[:]
	}
	return nil
}

// ─── BC7 block decode ──────────────────────────────────────────

func decodeBC7Block(block []byte) (pixels [64]byte) {
	if block[0] == 0 {
		return // all zeros = transparent black
	}

	mode := uint(bits.TrailingZeros8(block[0]))
	if mode > 7 {
		return
	}
	mi := &bc7Modes[mode]
	br := &ddsBitReader{data: block[:16], pos: mode + 1}

	partition := br.read(mi.partBits)
	rotation := br.read(mi.rotBits)
	idxSel := br.read(mi.idxSelBits)

	numEPs := mi.numSubsets * 2
	var ep [6][4]uint32

	for c := range uint32(3) {
		for e := range numEPs {
			ep[e][c] = br.read(mi.colorBits)
		}
	}
	if mi.alphaBits > 0 {
		for e := range numEPs {
			ep[e][3] = br.read(mi.alphaBits)
		}
	}

	// P-bits.
	var pb [6]uint32
	var numPB uint32
	switch mi.pbitMode {
	case 1:
		numPB = numEPs
	case 2:
		numPB = mi.numSubsets
	}
	for i := range numPB {
		pb[i] = br.read(1)
	}

	// Dequantize endpoints.
	for e := range numEPs {
		for c := range uint32(3) {
			switch mi.pbitMode {
			case 0:
				ep[e][c] = bc7DequantNoPbit(ep[e][c], uint32(mi.colorBits))
			case 1:
				ep[e][c] = bc7Dequant(ep[e][c], pb[e], uint32(mi.colorBits))
			case 2:
				ep[e][c] = bc7Dequant(ep[e][c], pb[e/2], uint32(mi.colorBits))
			}
		}
		if mi.alphaBits > 0 {
			switch mi.pbitMode {
			case 0:
				ep[e][3] = bc7DequantNoPbit(ep[e][3], uint32(mi.alphaBits))
			case 1:
				ep[e][3] = bc7Dequant(ep[e][3], pb[e], uint32(mi.alphaBits))
			case 2:
				ep[e][3] = bc7Dequant(ep[e][3], pb[e/2], uint32(mi.alphaBits))
			}
		} else {
			ep[e][3] = 255
		}
	}

	// Read primary indices.
	var idx [16]uint32
	for i := range 16 {
		anchor := false
		switch mi.numSubsets {
		case 1:
			anchor = i == 0
		case 2:
			anchor = i == 0 || i == int(bc7Anchor2[partition])
		case 3:
			anchor = i == 0 || i == int(bc7Anchor31[partition]) || i == int(bc7Anchor32[partition])
		}
		if anchor {
			idx[i] = br.read(mi.indexBits - 1)
		} else {
			idx[i] = br.read(mi.indexBits)
		}
	}

	// Read secondary indices (modes 4, 5).
	var idx2 [16]uint32
	if mi.index2Bits > 0 {
		for i := range 16 {
			if i == 0 {
				idx2[i] = br.read(mi.index2Bits - 1)
			} else {
				idx2[i] = br.read(mi.index2Bits)
			}
		}
	}

	wt := bc7WeightsForBits(mi.indexBits)
	var wt2 []uint32
	if mi.index2Bits > 0 {
		wt2 = bc7WeightsForBits(mi.index2Bits)
	}

	for i := range 16 {
		var subset uint32
		switch mi.numSubsets {
		case 2:
			subset = uint32(bc7Partition2[partition*16+uint32(i)])
		case 3:
			subset = uint32(bc7Partition3[partition*16+uint32(i)])
		}
		e0 := subset * 2
		e1 := e0 + 1

		var r, g, b, a uint32
		if mi.index2Bits > 0 {
			var cw, aw uint32
			if idxSel == 0 {
				cw = wt[idx[i]]
				aw = wt2[idx2[i]]
			} else {
				cw = wt2[idx2[i]]
				aw = wt[idx[i]]
			}
			r = bc7Interp(ep[e0][0], ep[e1][0], cw)
			g = bc7Interp(ep[e0][1], ep[e1][1], cw)
			b = bc7Interp(ep[e0][2], ep[e1][2], cw)
			a = bc7Interp(ep[e0][3], ep[e1][3], aw)
		} else {
			w := wt[idx[i]]
			r = bc7Interp(ep[e0][0], ep[e1][0], w)
			g = bc7Interp(ep[e0][1], ep[e1][1], w)
			b = bc7Interp(ep[e0][2], ep[e1][2], w)
			a = bc7Interp(ep[e0][3], ep[e1][3], w)
		}

		switch rotation {
		case 1:
			a, r = r, a
		case 2:
			a, g = g, a
		case 3:
			a, b = b, a
		}

		p := i * 4
		pixels[p+0] = byte(r)
		pixels[p+1] = byte(g)
		pixels[p+2] = byte(b)
		pixels[p+3] = byte(a)
	}
	return
}

// ─── Uncompressed pixel decode ─────────────────────────────────

type ddsChannelInfo struct {
	shift, width uint32
}

func ddsAnalyzeMask(mask uint32) ddsChannelInfo {
	if mask == 0 {
		return ddsChannelInfo{}
	}
	return ddsChannelInfo{
		shift: uint32(bits.TrailingZeros32(mask)),
		width: uint32(bits.OnesCount32(mask)),
	}
}

func ddsExtractChannel(pixel uint32, ci ddsChannelInfo) byte {
	if ci.width == 0 {
		return 0
	}
	val := (pixel >> ci.shift) & ((1 << ci.width) - 1)
	if ci.width >= 8 {
		return byte(val >> (ci.width - 8))
	}
	val <<= (8 - ci.width)
	val |= val >> ci.width
	return byte(val)
}

// ─── DDS header parsing ───────────────────────────────────────

func parseDDSHeader(data []byte) (info ddsInfo, err error) {
	if len(data) < 4 {
		return info, fmt.Errorf("truncated DDS data")
	}
	if binary.LittleEndian.Uint32(data[0:4]) != ddsMagic {
		return info, fmt.Errorf("not a DDS file")
	}
	if len(data) < 128 {
		return info, fmt.Errorf("truncated DDS header")
	}
	if binary.LittleEndian.Uint32(data[4:8]) != ddsHeaderSize {
		return info, fmt.Errorf("invalid DDS header size")
	}

	flags := binary.LittleEndian.Uint32(data[8:12])
	info.height = int(binary.LittleEndian.Uint32(data[12:16]))
	info.width = int(binary.LittleEndian.Uint32(data[16:20]))
	info.mipCount = 1
	if flags&ddsdMipmapCount != 0 {
		mc := int(binary.LittleEndian.Uint32(data[28:32]))
		if mc > 1 {
			info.mipCount = mc
		}
	}

	if info.width <= 0 || info.height <= 0 {
		return info, fmt.Errorf("invalid DDS dimensions: %dx%d", info.width, info.height)
	}
	if info.width > ddsMaxDim || info.height > ddsMaxDim {
		return info, fmt.Errorf("DDS dimensions too large: %dx%d (max %d)", info.width, info.height, ddsMaxDim)
	}

	pfFlags := binary.LittleEndian.Uint32(data[80:84])
	fourCC := string(data[84:88])

	info.dataOffset = 128

	if pfFlags&ddpfFourCC != 0 {
		switch fourCC {
		case "DXT1":
			info.format = dfBC1
			info.label = "DXT1"
		case "DXT2":
			info.format = dfBC2
			info.label = "DXT2"
		case "DXT3":
			info.format = dfBC2
			info.label = "DXT3"
		case "DXT4":
			info.format = dfBC3
			info.label = "DXT4"
		case "DXT5":
			info.format = dfBC3
			info.label = "DXT5"
		case "ATI1":
			info.format = dfBC4
			info.label = "BC4 (ATI1)"
		case "BC4U":
			info.format = dfBC4
			info.label = "BC4"
		case "BC4S":
			info.format = dfBC4S
			info.label = "BC4 Signed"
		case "ATI2":
			info.format = dfBC5
			info.label = "BC5 (ATI2)"
		case "BC5U":
			info.format = dfBC5
			info.label = "BC5"
		case "BC5S":
			info.format = dfBC5S
			info.label = "BC5 Signed"
		case "DX10":
			if len(data) < 148 {
				return info, fmt.Errorf("truncated DX10 header")
			}
			info.dataOffset = 148
			dxgi := binary.LittleEndian.Uint32(data[128:132])
			switch dxgi {
			case 71:
				info.format = dfBC1
				info.label = "BC1"
			case 72:
				info.format = dfBC1
				info.label = "BC1 sRGB"
			case 74:
				info.format = dfBC2
				info.label = "BC2"
			case 75:
				info.format = dfBC2
				info.label = "BC2 sRGB"
			case 77:
				info.format = dfBC3
				info.label = "BC3"
			case 78:
				info.format = dfBC3
				info.label = "BC3 sRGB"
			case 80:
				info.format = dfBC4
				info.label = "BC4"
			case 81:
				info.format = dfBC4S
				info.label = "BC4 Signed"
			case 83:
				info.format = dfBC5
				info.label = "BC5"
			case 84:
				info.format = dfBC5S
				info.label = "BC5 Signed"
			case 95, 96:
				return info, fmt.Errorf("unsupported DDS format: BC6H")
			case 98:
				info.format = dfBC7
				info.label = "BC7"
			case 99:
				info.format = dfBC7
				info.label = "BC7 sRGB"
			case 28:
				info.format = dfRGBA8
				info.label = "R8G8B8A8"
			case 29:
				info.format = dfRGBA8
				info.label = "R8G8B8A8 sRGB"
			case 87:
				info.format = dfBGRA8
				info.label = "B8G8R8A8"
			case 91:
				info.format = dfBGRA8
				info.label = "B8G8R8A8 sRGB"
			case 88:
				info.format = dfBGRX8
				info.label = "B8G8R8X8"
			case 93:
				info.format = dfBGRX8
				info.label = "B8G8R8X8 sRGB"
			default:
				return info, fmt.Errorf("unsupported DDS format: DXGI %d", dxgi)
			}
		default:
			return info, fmt.Errorf("unsupported DDS format: %s", fourCC)
		}
	} else if pfFlags&(ddpfRGB|ddpfLuminance) != 0 {
		info.format = dfUncompressed
		info.bpp = int(binary.LittleEndian.Uint32(data[88:92]))
		info.rMask = binary.LittleEndian.Uint32(data[92:96])
		info.gMask = binary.LittleEndian.Uint32(data[96:100])
		info.bMask = binary.LittleEndian.Uint32(data[100:104])
		if pfFlags&ddpfAlphaPixels != 0 {
			info.aMask = binary.LittleEndian.Uint32(data[104:108])
		}
		info.luminance = pfFlags&ddpfLuminance != 0
		if info.bpp != 8 && info.bpp != 16 && info.bpp != 24 && info.bpp != 32 {
			return info, fmt.Errorf("unsupported DDS bit depth: %d bpp", info.bpp)
		}
		info.label = fmt.Sprintf("%d-bit", info.bpp)
		if info.luminance {
			info.label += " Luminance"
		}
	} else {
		return info, fmt.Errorf("unsupported DDS pixel format flags: 0x%X", pfFlags)
	}

	return info, nil
}

// ─── Mip level arithmetic ──────────────────────────────────────

func ddsBlockSize(f ddsFormat) int {
	switch f {
	case dfBC1, dfBC4, dfBC4S:
		return 8
	case dfBC2, dfBC3, dfBC5, dfBC5S, dfBC7:
		return 16
	}
	return 0
}

func ddsIsCompressed(f ddsFormat) bool {
	return ddsBlockSize(f) > 0
}

func ddsMipSize(info *ddsInfo, level int) (w, h, byteSize int) {
	w = info.width >> level
	if w < 1 {
		w = 1
	}
	h = info.height >> level
	if h < 1 {
		h = 1
	}
	if ddsIsCompressed(info.format) {
		bx := (w + 3) / 4
		by := (h + 3) / 4
		byteSize = bx * by * ddsBlockSize(info.format)
	} else {
		byteSize = w * h * (info.bpp / 8)
	}
	return
}

// ─── Public API ────────────────────────────────────────────────

// decodeDDS decodes the largest mip level whose longest edge is <= maxEdge
// (or the smallest available level if none fits; maxEdge <= 0 means top level).
// Returns the decoded level as *image.NRGBA plus the TOP-level width/height and a short
// human format label (e.g. "BC7 sRGB", "BC5 (ATI2)", "DXT1", "B8G8R8A8").
func decodeDDS(data []byte, maxEdge int) (img *image.NRGBA, width, height int, format string, err error) {
	info, err := parseDDSHeader(data)
	if err != nil {
		return nil, 0, 0, "", err
	}

	width = info.width
	height = info.height
	format = info.label

	// Choose mip level.
	level := 0
	if maxEdge > 0 {
		chosen := -1
		for l := range info.mipCount {
			w, h, _ := ddsMipSize(&info, l)
			longest := w
			if h > longest {
				longest = h
			}
			if longest <= maxEdge {
				chosen = l
				break
			}
		}
		if chosen < 0 {
			level = info.mipCount - 1
		} else {
			level = chosen
		}
	}

	// Compute byte offset to the chosen level.
	offset := info.dataOffset
	for l := range level {
		_, _, sz := ddsMipSize(&info, l)
		offset += sz
	}

	mw, mh, mipBytes := ddsMipSize(&info, level)
	if offset+mipBytes > len(data) {
		return nil, 0, 0, "", fmt.Errorf("truncated DDS data: need %d bytes at mip level %d, have %d",
			offset+mipBytes, level, len(data))
	}
	mipData := data[offset : offset+mipBytes]

	img = image.NewNRGBA(image.Rect(0, 0, mw, mh))

	if ddsIsCompressed(info.format) {
		err = ddsDecodeCompressed(img, mipData, mw, mh, info.format)
	} else {
		err = ddsDecodeUncompressed(img, mipData, mw, mh, &info)
	}
	if err != nil {
		return nil, 0, 0, "", err
	}
	return img, width, height, format, nil
}

// ─── Block-compressed decode loop ──────────────────────────────

func ddsDecodeCompressed(img *image.NRGBA, data []byte, w, h int, f ddsFormat) error {
	bx := (w + 3) / 4
	by := (h + 3) / 4
	bs := ddsBlockSize(f)
	stride := img.Stride

	for bRow := range by {
		for bCol := range bx {
			off := (bRow*bx + bCol) * bs
			if off+bs > len(data) {
				return fmt.Errorf("truncated DDS block data at block (%d,%d)", bCol, bRow)
			}
			block := data[off : off+bs]

			var px [64]byte
			switch f {
			case dfBC1:
				px = decodeBC1Block(block, false)
			case dfBC2:
				px = decodeBC2Block(block)
			case dfBC3:
				px = decodeBC3Block(block)
			case dfBC4:
				px = decodeBC4Block(block, false)
			case dfBC4S:
				px = decodeBC4Block(block, true)
			case dfBC5:
				px = decodeBC5Block(block, false)
			case dfBC5S:
				px = decodeBC5Block(block, true)
			case dfBC7:
				px = decodeBC7Block(block)
			}

			// Write texels into the image, clipping at image boundaries.
			for ty := range 4 {
				py := bRow*4 + ty
				if py >= h {
					break
				}
				rowStart := py * stride
				for tx := range 4 {
					px2 := bCol*4 + tx
					if px2 >= w {
						break
					}
					src := (ty*4 + tx) * 4
					dst := rowStart + px2*4
					img.Pix[dst+0] = px[src+0]
					img.Pix[dst+1] = px[src+1]
					img.Pix[dst+2] = px[src+2]
					img.Pix[dst+3] = px[src+3]
				}
			}
		}
	}
	return nil
}

// ─── BC2 (DXT3) block ──────────────────────────────────────────

func decodeBC2Block(block []byte) (pixels [64]byte) {
	// First 8 bytes: explicit 4-bit alpha, two per byte.
	pixels = decodeBC1Block(block[8:16], true)
	for i := range 16 {
		byteIdx := i / 2
		var a4 byte
		if i%2 == 0 {
			a4 = block[byteIdx] & 0x0F
		} else {
			a4 = block[byteIdx] >> 4
		}
		pixels[i*4+3] = a4<<4 | a4
	}
	return
}

// ─── BC3 (DXT5) block ──────────────────────────────────────────

func decodeBC3Block(block []byte) (pixels [64]byte) {
	ach := ddsDecodeAlphaBlock(block[0:8], false)
	pixels = decodeBC1Block(block[8:16], true)
	for i := range 16 {
		pixels[i*4+3] = ach[i]
	}
	return
}

// ─── Uncompressed decode loop ──────────────────────────────────

func ddsDecodeUncompressed(img *image.NRGBA, data []byte, w, h int, info *ddsInfo) error {
	bpp := info.bpp / 8
	stride := img.Stride

	switch info.format {
	case dfRGBA8:
		for y := range h {
			for x := range w {
				src := (y*w + x) * 4
				dst := y*stride + x*4
				img.Pix[dst+0] = data[src+0]
				img.Pix[dst+1] = data[src+1]
				img.Pix[dst+2] = data[src+2]
				img.Pix[dst+3] = data[src+3]
			}
		}
		return nil
	case dfBGRA8:
		for y := range h {
			for x := range w {
				src := (y*w + x) * 4
				dst := y*stride + x*4
				img.Pix[dst+0] = data[src+2]
				img.Pix[dst+1] = data[src+1]
				img.Pix[dst+2] = data[src+0]
				img.Pix[dst+3] = data[src+3]
			}
		}
		return nil
	case dfBGRX8:
		for y := range h {
			for x := range w {
				src := (y*w + x) * 4
				dst := y*stride + x*4
				img.Pix[dst+0] = data[src+2]
				img.Pix[dst+1] = data[src+1]
				img.Pix[dst+2] = data[src+0]
				img.Pix[dst+3] = 255
			}
		}
		return nil
	}

	// Generic uncompressed with arbitrary masks.
	rci := ddsAnalyzeMask(info.rMask)
	gci := ddsAnalyzeMask(info.gMask)
	bci := ddsAnalyzeMask(info.bMask)
	aci := ddsAnalyzeMask(info.aMask)

	for y := range h {
		for x := range w {
			src := (y*w + x) * bpp
			if src+bpp > len(data) {
				return fmt.Errorf("truncated uncompressed pixel data at (%d,%d)", x, y)
			}
			var pixel uint32
			for i := range bpp {
				pixel |= uint32(data[src+i]) << (8 * uint(i))
			}

			dst := y*stride + x*4
			if info.luminance {
				l := ddsExtractChannel(pixel, rci)
				img.Pix[dst+0] = l
				img.Pix[dst+1] = l
				img.Pix[dst+2] = l
			} else {
				img.Pix[dst+0] = ddsExtractChannel(pixel, rci)
				img.Pix[dst+1] = ddsExtractChannel(pixel, gci)
				img.Pix[dst+2] = ddsExtractChannel(pixel, bci)
			}
			if aci.width > 0 {
				img.Pix[dst+3] = ddsExtractChannel(pixel, aci)
			} else {
				img.Pix[dst+3] = 255
			}
		}
	}
	return nil
}
