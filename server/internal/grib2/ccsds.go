package grib2

import (
	"errors"
	"fmt"
)

// The CCSDS lossless coder (CCSDS 121.0-B, the Rice coder libaec implements), as GRIB2's data
// template 5.42 uses it: the field's packed integers, n bits each, in blocks of J samples,
// each block coded by the option its ID names (a zero block, a second extension, a split of
// the sample into a unary high part and k low bits, or uncompressed), the samples preprocessed
// into mapped differences from the sample before, with a reference sample starting every
// reference-sample interval. The flags are libaec's: SIGNED 1, 3BYTE 2, MSB 4, PREPROCESS 8,
// RESTRICTED 16, PAD_RSI 32. ECMWF's open data is written with 3BYTE|MSB|PREPROCESS, J = 32,
// an interval of 128 blocks.

const (
	aecSigned     = 1
	aecPreprocess = 8
	aecRestricted = 16
	aecPadRSI     = 32
)

// unpackCCSDS decodes n samples of nbits each.
func unpackCCSDS(data []byte, n, nbits int, p ccsdsParams) ([]uint32, error) {
	if nbits == 0 {
		return make([]uint32, n), nil
	}
	if nbits > 32 {
		return nil, fmt.Errorf("grib2: ccsds %d bits a sample", nbits)
	}
	if p.flags&aecSigned != 0 {
		return nil, errors.New("grib2: ccsds signed samples")
	}
	J := p.blockSize
	if J != 8 && J != 16 && J != 32 && J != 64 {
		return nil, fmt.Errorf("grib2: ccsds block size %d", J)
	}
	rsi := p.rsi
	if rsi <= 0 {
		return nil, errors.New("grib2: ccsds reference sample interval 0")
	}
	idLen := 3
	switch {
	case nbits > 16:
		idLen = 5
	case nbits > 8:
		idLen = 4
	case p.flags&aecRestricted != 0 && nbits <= 4:
		if nbits <= 2 {
			idLen = 1
		} else {
			idLen = 2
		}
	}
	pp := p.flags&aecPreprocess != 0
	d := &aec{bits: bits{b: data}, n: nbits, J: J, idLen: idLen, pp: pp, xmax: uint32(1)<<uint(nbits) - 1, out: make([]uint32, 0, n)}
	if nbits == 32 {
		d.xmax = 0xffffffff
	}
	for len(d.out) < n {
		if d.left() <= 0 {
			return nil, fmt.Errorf("grib2: ccsds stream ends after %d of %d samples", len(d.out), n)
		}
		// one reference sample interval: rsi blocks, fewer at the end
		remaining := n - len(d.out)
		blocks := (remaining + J - 1) / J
		if blocks > rsi {
			blocks = rsi
		}
		d.ref = pp
		for b := 0; b < blocks && len(d.out) < n; {
			used, err := d.block(b, blocks)
			if err != nil {
				return nil, err
			}
			b += used
		}
		if p.flags&aecPadRSI != 0 {
			d.align()
		}
	}
	return d.out[:n], nil
}

type aec struct {
	bits
	n, J, idLen int
	pp          bool
	xmax        uint32
	ref         bool   // the next block starts with a reference sample
	last        uint32 // the sample before, for the unmapping
	out         []uint32
}

// block decodes the block at index b of blocks in the interval and returns how many blocks it
// covered (a run of zero blocks is several).
func (d *aec) block(b, blocks int) (int, error) {
	id := int(d.read(d.idLen))
	uncompressed := 1<<uint(d.idLen) - 1
	// the first block of an interval begins with the reference sample (after the ID and, for
	// a low-entropy block, its selector) and has one sample fewer to decode
	hadRef := d.ref
	switch {
	case id == 0:
		sel := d.read(1)
		if hadRef {
			d.emitRef(d.read(d.n))
		}
		if sel == 0 {
			// a run of zero blocks: a unary count; 5 means the rest of the 64-block segment,
			// 6 and more are one more than the run
			zb := int(d.unary()) + 1
			switch {
			case zb == 5:
				zb = 64 - b%64
			case zb > 5:
				zb--
			}
			if zb > blocks-b {
				zb = blocks - b
			}
			count := zb * d.J
			if hadRef {
				count-- // the reference stood for the first sample
			}
			for i := 0; i < count; i++ {
				d.emit(0)
			}
			return zb, nil
		}
		// the second extension: pairs of samples from one unary code each
		i := 0
		if hadRef {
			i = 1
		}
		for i < d.J {
			m := d.unary()
			beta := uint32(0)
			for (beta+1)*(beta+2)/2 <= m {
				beta++
			}
			second := m - beta*(beta+1)/2
			first := beta - second
			if i&1 == 0 {
				d.emit(first)
				i++
			}
			d.emit(second)
			i++
		}
		return 1, nil
	case id == uncompressed:
		if hadRef {
			d.emitRef(d.read(d.n))
			for i := 1; i < d.J; i++ {
				d.emit(d.read(d.n))
			}
			return 1, nil
		}
		for i := 0; i < d.J; i++ {
			d.emit(d.read(d.n))
		}
		return 1, nil
	default:
		// a split: the unary parts of every sample, then k low bits each
		k := id - 1
		count := d.J
		if hadRef {
			d.emitRef(d.read(d.n))
			count--
		}
		high := make([]uint32, count)
		for i := range high {
			high[i] = d.unary()
		}
		for i := range high {
			d.emit(high[i]<<uint(k) | d.read(k))
		}
		return 1, nil
	}
}

// unary counts the zeros before the next one.
func (d *aec) unary() uint32 {
	var c uint32
	for d.read(1) == 0 {
		c++
		if d.left() <= 0 {
			return c
		}
	}
	return c
}

// emitRef takes a reference sample: the sample itself, the start of the unmapping.
func (d *aec) emitRef(x uint32) {
	d.out = append(d.out, x)
	d.last = x
	d.ref = false
}

// emit takes a coded sample: the mapped difference from the sample before when preprocessing
// is on, the sample itself otherwise.
func (d *aec) emit(v uint32) {
	if !d.pp {
		d.out = append(d.out, v)
		return
	}
	x := d.unmap(d.last, v)
	d.out = append(d.out, x)
	d.last = x
}

// unmap undoes the preprocessor's mapping: a difference of 0 ≤ Δ ≤ θ was sent as 2Δ, of
// -θ ≤ Δ < 0 as 2|Δ|-1, and one beyond θ as θ+|Δ| (its sign is the side the sample before
// had room on), θ the room between the sample before and the nearer end of the range.
func (d *aec) unmap(prev, m uint32) uint32 {
	up := d.xmax - prev
	down := prev
	theta := up
	if down < up {
		theta = down
	}
	if m <= 2*theta {
		if m&1 == 0 {
			return prev + m/2
		}
		return prev - (m+1)/2
	}
	if down < up {
		return prev + (m - theta)
	}
	return prev - (m - theta)
}
