package chart

import (
	"encoding/binary"
	"hash"
	"hash/fnv"
	"image/color"
	"math"
)

// keyHash builds the raster key of a gauge or a sparkline: an FNV-1a hash
// of what decides its pixels.
type keyHash struct {
	h   hash.Hash64
	buf [8]byte
}

func newKeyHash() *keyHash { return &keyHash{h: fnv.New64a()} }

func (k *keyHash) bits(v uint64) {
	binary.LittleEndian.PutUint64(k.buf[:], v)
	_, _ = k.h.Write(k.buf[:])
}

func (k *keyHash) float(f float64) { k.bits(math.Float64bits(f)) }

// int hashes v as a float, as Chart.RasterKey does: a time in nanoseconds
// keeps a resolution far below a pixel.
func (k *keyHash) int(v int64) { k.float(float64(v)) }

func (k *keyHash) ints(vs ...int) {
	for _, v := range vs {
		k.int(int64(v))
	}
}

func (k *keyHash) flag(b bool) {
	if b {
		k.bits(1)
		return
	}
	k.bits(0)
}

func (k *keyHash) color(c color.RGBA) {
	k.bits(uint64(c.R)<<24 | uint64(c.G)<<16 | uint64(c.B)<<8 | uint64(c.A))
}

func (k *keyHash) sum() uint64 { return k.h.Sum64() }
