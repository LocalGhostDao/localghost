package grib2

import (
	"errors"
	"fmt"
	"math"
)

// bits reads big-endian bit fields from a byte slice.
type bits struct {
	b   []byte
	pos int // in bits
}

func (r *bits) left() int { return len(r.b)*8 - r.pos }

// read takes n bits (n ≤ 32); past the end it returns 0 and sets short.
func (r *bits) read(n int) uint32 {
	if n == 0 {
		return 0
	}
	var v uint32
	for n > 0 {
		if r.pos>>3 >= len(r.b) {
			r.pos += n
			return v << uint(n)
		}
		byt := r.b[r.pos>>3]
		off := r.pos & 7
		take := 8 - off
		if take > n {
			take = n
		}
		chunk := (uint32(byt) >> uint(8-off-take)) & (1<<uint(take) - 1)
		v = v<<uint(take) | chunk
		r.pos += take
		n -= take
	}
	return v
}

// align moves to the next octet boundary.
func (r *bits) align() {
	if r.pos&7 != 0 {
		r.pos += 8 - r.pos&7
	}
}

// unpackSimple reads n values of nbits each (template 5.0).
func unpackSimple(data []byte, n, nbits int) ([]uint32, error) {
	if nbits == 0 {
		return make([]uint32, n), nil
	}
	if nbits > 32 {
		return nil, fmt.Errorf("grib2: %d bits a value", nbits)
	}
	if len(data)*8 < n*nbits {
		return nil, fmt.Errorf("grib2: %d values of %d bits in %d bytes", n, nbits, len(data))
	}
	r := bits{b: data}
	out := make([]uint32, n)
	for i := range out {
		out[i] = r.read(nbits)
	}
	return out, nil
}

// unpackComplex reads a field packed in groups (template 5.2) or in groups of spatial
// differences (5.3): the groups' reference values, widths and lengths, then each group's
// values, then the differences undone, then the scaling. n is the grid's point count, coded
// how many of them the bitmap keeps.
func (m *Message) unpackComplex(n, coded int) ([]float32, error) {
	c := m.complex
	if c.splitting != 1 {
		return nil, fmt.Errorf("grib2: group splitting method %d (only general splitting, 1, is read)", c.splitting)
	}
	if c.missingMgmt > 1 {
		return nil, fmt.Errorf("grib2: missing value management %d (0 and 1 are read)", c.missingMgmt)
	}
	ng := c.groups
	if ng <= 0 {
		return nil, errors.New("grib2: no groups")
	}
	r := bits{b: m.data}
	// spatial differencing: the first value(s) and the overall minimum, sign and magnitude,
	// each in extraOctets
	var first [2]int64
	var minsd int64
	if m.template == 3 && c.order > 0 {
		if c.order > 2 {
			return nil, fmt.Errorf("grib2: spatial differencing of order %d", c.order)
		}
		w := c.extraOctets * 8
		if w == 0 || w > 32 {
			return nil, fmt.Errorf("grib2: %d octets for the spatial differencing descriptors", c.extraOctets)
		}
		// the first values are originals (never negative: the reference is the minimum), the
		// minimum of the differences is a sign and a magnitude
		for i := 0; i < c.order; i++ {
			first[i] = int64(r.read(w))
		}
		minsd = signMagBits(r.read(w), w)
	}
	refs := make([]uint32, ng)
	for i := range refs {
		refs[i] = r.read(m.nbits)
	}
	r.align()
	widths := make([]int, ng)
	for i := range widths {
		widths[i] = c.widthRef + int(r.read(c.widthBits))
	}
	r.align()
	lengths := make([]int, ng)
	total := 0
	for i := range lengths {
		lengths[i] = c.lengthRef + c.lengthInc*int(r.read(c.lengthBits))
		total += lengths[i]
	}
	r.align()
	if ng > 0 {
		total -= lengths[ng-1]
		lengths[ng-1] = c.lastLength
		total += c.lastLength
	}
	if total != coded {
		return nil, fmt.Errorf("grib2: the groups hold %d values for %d coded points", total, coded)
	}
	// the values, a group at a time; a missing point (management 1) is a value of all ones at
	// the group's width, or a whole group whose reference is all ones at a width of zero
	vals := make([]int64, coded)
	missing := make([]bool, coded)
	k := 0
	for g := 0; g < ng; g++ {
		w := widths[g]
		if w > 32 {
			return nil, fmt.Errorf("grib2: group width %d", w)
		}
		allOnes := uint32(1)<<uint(w) - 1
		refOnes := uint32(1)<<uint(m.nbits) - 1
		groupMissing := c.missingMgmt == 1 && w == 0 && refs[g] == refOnes
		for i := 0; i < lengths[g]; i++ {
			v := r.read(w)
			if groupMissing || (c.missingMgmt == 1 && w > 0 && v == allOnes) {
				missing[k] = true
			} else {
				vals[k] = int64(refs[g]) + int64(v)
			}
			k++
		}
	}
	if r.pos > len(m.data)*8 {
		return nil, errors.New("grib2: the groups run past the data section")
	}
	// the differences undone over the values present
	if m.template == 3 && c.order > 0 {
		idx := 0
		var prev1, prev2 int64
		for i := 0; i < coded; i++ {
			if missing[i] {
				continue
			}
			switch {
			case idx == 0:
				vals[i] = first[0]
			case c.order == 2 && idx == 1:
				vals[i] = first[1]
			case c.order == 1:
				vals[i] = vals[i] + minsd + prev1
			default:
				vals[i] = vals[i] + minsd + 2*prev1 - prev2
			}
			prev2, prev1 = prev1, vals[i]
			idx++
		}
	}
	out := make([]float32, n)
	scale := math.Pow(2, float64(m.binScale)) / math.Pow(10, float64(m.decScale))
	ref := float64(m.ref) / math.Pow(10, float64(m.decScale))
	k = 0
	for i := 0; i < n; i++ {
		if m.bitmap != nil && m.bitmap[i>>3]&(0x80>>(i&7)) == 0 {
			out[i] = Missing
			continue
		}
		if missing[k] {
			out[i] = Missing
		} else {
			out[i] = float32(ref + float64(vals[k])*scale)
		}
		k++
	}
	return out, nil
}

// signMagBits reads a sign-and-magnitude integer of w bits.
func signMagBits(v uint32, w int) int64 {
	sign := uint32(1) << uint(w-1)
	if v&sign != 0 {
		return -int64(v &^ sign)
	}
	return int64(v)
}
