// Package tsdb is the NP-TSDB: Northplane's embedded time-series engine
// (SPEC §7.3). Raw samples are compressed with the Gorilla scheme
// (delta-of-delta timestamps + XOR floats) into append-only chunks of
// two-hour windows; downsampling tiers provide long retention.
package tsdb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
)

// bitWriter appends bits MSB-first.
type bitWriter struct {
	b     []byte
	count uint8 // bits used in the last byte (0..8)
}

func (w *bitWriter) writeBit(bit bool) {
	if w.count == 0 || w.count == 8 {
		w.b = append(w.b, 0)
		w.count = 0
	}
	if bit {
		w.b[len(w.b)-1] |= 1 << (7 - w.count)
	}
	w.count++
}

func (w *bitWriter) writeBits(v uint64, n uint8) {
	for n > 0 {
		n--
		w.writeBit((v>>(n))&1 == 1)
	}
}

func (w *bitWriter) writeByte(b byte) { w.writeBits(uint64(b), 8) }

func (w *bitWriter) bytes() []byte { return w.b }

// bitReader consumes bits MSB-first.
type bitReader struct {
	b     []byte
	idx   int
	count uint8
}

var errEOF = errors.New("tsdb: chunk truncated")

func (r *bitReader) readBit() (bool, error) {
	if r.idx >= len(r.b) {
		return false, errEOF
	}
	bit := r.b[r.idx]&(1<<(7-r.count)) != 0
	r.count++
	if r.count == 8 {
		r.count = 0
		r.idx++
	}
	return bit, nil
}

func (r *bitReader) readBits(n uint8) (uint64, error) {
	var v uint64
	for i := uint8(0); i < n; i++ {
		bit, err := r.readBit()
		if err != nil {
			return 0, err
		}
		v <<= 1
		if bit {
			v |= 1
		}
	}
	return v, nil
}

// Sample is one (timestamp, value) point. Timestamps are Unix
// milliseconds (the system's canonical resolution).
type Sample struct {
	T int64   `json:"t"`
	V float64 `json:"v"`
}

// ChunkAppender encodes samples into a Gorilla chunk incrementally.
// Appends must be time-ordered; equal timestamps overwrite is NOT
// supported (callers drop duplicates).
type ChunkAppender struct {
	w bitWriter

	n         uint32
	firstT    int64
	lastT     int64
	lastDelta int64
	lastV     uint64
	leading   uint8
	trailing  uint8
}

// NewChunkAppender starts an empty chunk.
func NewChunkAppender() *ChunkAppender {
	return &ChunkAppender{leading: 0xff}
}

// Count returns the number of encoded samples.
func (a *ChunkAppender) Count() uint32 { return a.n }

// FirstTime / LastTime bound the chunk (valid when Count > 0).
func (a *ChunkAppender) FirstTime() int64 { return a.firstT }
func (a *ChunkAppender) LastTime() int64  { return a.lastT }

// Append encodes one sample. Returns false for out-of-order timestamps.
func (a *ChunkAppender) Append(t int64, v float64) bool {
	switch a.n {
	case 0:
		a.firstT = t
		// First timestamp: svarint; first value: raw 64 bits.
		var buf [binary.MaxVarintLen64]byte
		n := binary.PutVarint(buf[:], t)
		for _, b := range buf[:n] {
			a.w.writeByte(b)
		}
		a.w.writeBits(math.Float64bits(v), 64)
	case 1:
		if t <= a.lastT {
			return false
		}
		delta := t - a.lastT
		var buf [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(buf[:], uint64(delta))
		for _, b := range buf[:n] {
			a.w.writeByte(b)
		}
		a.lastDelta = delta
		a.appendValue(v)
	default:
		if t <= a.lastT {
			return false
		}
		delta := t - a.lastT
		dod := delta - a.lastDelta
		switch {
		case dod == 0:
			a.w.writeBit(false)
		case dod >= -63 && dod <= 64:
			a.w.writeBits(0b10, 2)
			a.w.writeBits(uint64(dod+63)&0x7f, 7)
		case dod >= -255 && dod <= 256:
			a.w.writeBits(0b110, 3)
			a.w.writeBits(uint64(dod+255)&0x1ff, 9)
		case dod >= -2047 && dod <= 2048:
			a.w.writeBits(0b1110, 4)
			a.w.writeBits(uint64(dod+2047)&0xfff, 12)
		default:
			a.w.writeBits(0b1111, 4)
			a.w.writeBits(uint64(dod), 64)
		}
		a.lastDelta = delta
		a.appendValue(v)
	}
	a.lastT = t
	if a.n == 0 {
		a.lastV = math.Float64bits(v)
	}
	a.n++
	return true
}

func (a *ChunkAppender) appendValue(v float64) {
	cur := math.Float64bits(v)
	xor := cur ^ a.lastV
	a.lastV = cur
	if xor == 0 {
		a.w.writeBit(false)
		return
	}
	a.w.writeBit(true)
	leading := uint8(bits.LeadingZeros64(xor))
	trailing := uint8(bits.TrailingZeros64(xor))
	if leading > 31 {
		leading = 31 // 5-bit field
	}
	if a.leading != 0xff && leading >= a.leading && trailing >= a.trailing {
		// reuse previous window
		a.w.writeBit(false)
		a.w.writeBits(xor>>a.trailing, 64-a.leading-a.trailing)
		return
	}
	a.leading, a.trailing = leading, trailing
	a.w.writeBit(true)
	a.w.writeBits(uint64(leading), 5)
	sig := 64 - leading - trailing
	// 6 bits hold 1..64 (64 wraps to 0)
	a.w.writeBits(uint64(sig&0x3f), 6)
	a.w.writeBits(xor>>trailing, sig)
}

// Bytes returns the encoded chunk payload (header excluded).
func (a *ChunkAppender) Bytes() []byte { return a.w.bytes() }

// DecodeChunk expands an encoded chunk of n samples.
func DecodeChunk(data []byte, n uint32) ([]Sample, error) {
	if n == 0 {
		return nil, nil
	}
	out := make([]Sample, 0, n)
	r := &bitReader{b: data}

	readVarint := func(signed bool) (int64, error) {
		var uv uint64
		var shift uint
		for {
			b, err := r.readBits(8)
			if err != nil {
				return 0, err
			}
			uv |= (b & 0x7f) << shift
			if b&0x80 == 0 {
				break
			}
			shift += 7
			if shift > 63 {
				return 0, fmt.Errorf("tsdb: varint overflow")
			}
		}
		if signed {
			// zig-zag
			return int64(uv>>1) ^ -int64(uv&1), nil
		}
		return int64(uv), nil
	}

	t0, err := readVarint(true)
	if err != nil {
		return nil, err
	}
	v0bits, err := r.readBits(64)
	if err != nil {
		return nil, err
	}
	out = append(out, Sample{T: t0, V: math.Float64frombits(v0bits)})
	if n == 1 {
		return out, nil
	}

	var leading, trailing uint8
	lastV := v0bits
	readValue := func() (float64, error) {
		bit, err := r.readBit()
		if err != nil {
			return 0, err
		}
		if !bit {
			return math.Float64frombits(lastV), nil
		}
		ctrl, err := r.readBit()
		if err != nil {
			return 0, err
		}
		if ctrl {
			l, err := r.readBits(5)
			if err != nil {
				return 0, err
			}
			s, err := r.readBits(6)
			if err != nil {
				return 0, err
			}
			leading = uint8(l)
			sig := uint8(s)
			if sig == 0 {
				sig = 64
			}
			trailing = 64 - leading - sig
		}
		sig := 64 - leading - trailing
		v, err := r.readBits(sig)
		if err != nil {
			return 0, err
		}
		lastV ^= v << trailing
		return math.Float64frombits(lastV), nil
	}

	delta, err := readVarint(false)
	if err != nil {
		return nil, err
	}
	t := t0 + delta
	v1, err := readValue()
	if err != nil {
		return nil, err
	}
	out = append(out, Sample{T: t, V: v1})

	for uint32(len(out)) < n {
		var dod int64
		b0, err := r.readBit()
		if err != nil {
			return nil, err
		}
		if b0 {
			b1, err := r.readBit()
			if err != nil {
				return nil, err
			}
			if !b1 { // '10'
				u, err := r.readBits(7)
				if err != nil {
					return nil, err
				}
				dod = int64(u) - 63
			} else {
				b2, err := r.readBit()
				if err != nil {
					return nil, err
				}
				if !b2 { // '110'
					u, err := r.readBits(9)
					if err != nil {
						return nil, err
					}
					dod = int64(u) - 255
				} else {
					b3, err := r.readBit()
					if err != nil {
						return nil, err
					}
					if !b3 { // '1110'
						u, err := r.readBits(12)
						if err != nil {
							return nil, err
						}
						dod = int64(u) - 2047
					} else { // '1111'
						u, err := r.readBits(64)
						if err != nil {
							return nil, err
						}
						dod = int64(u)
					}
				}
			}
		}
		delta += dod
		t += delta
		v, err := readValue()
		if err != nil {
			return nil, err
		}
		out = append(out, Sample{T: t, V: v})
	}
	return out, nil
}
