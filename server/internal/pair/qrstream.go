package pair

// Erasure-coded enrolment frames (LGQR2). The LGQR1 scheme needed every one of N specific frames,
// so a frame the phone missed cost a whole lap of the rotation before it came round again , the
// wait the person actually noticed. LGQR2 splits the link into K data blocks and adds M parity
// blocks from a Cauchy matrix over GF(256): ANY K distinct frames, data or parity, rebuild the
// link (an MDS code, the same maths as the QR symbols' own Reed-Solomon, one level up). The
// rotation shows all K+M; the phone is done after K distinct catches, whichever they were, so a
// miss costs one more frame, never a lap. The same trick air-gapped hardware wallets use for
// large payloads, in its deterministic form (no PRNG to keep in sync between Go and Kotlin).
//
// Frame wire format: an ASCII header, then the block's raw bytes (the QR carries bytes; the app
// reads byte mode as ISO-8859-1, which maps them 1:1):
//
//	LGQR2 <idx> <K> <M> <len> <crc32> <pcrc> <body>
//
//	idx    0-based frame index: 0..K-1 are data blocks, K..K+M-1 are parity blocks
//	K, M   data and parity block counts
//	len    payload length in bytes (the last data block is zero-padded to the block size)
//	crc32  CRC-32 (IEEE) of the whole payload, 8 hex , identifies the set and verifies the join
//	pcrc   CRC-32 of this frame's body, low 16 bits, 4 hex , a corrupted read of one frame is
//	       dropped instead of poisoning the set (a QR decoder leaning on erasures can produce a
//	       consistent-looking wrong body)
//	body   blockSize bytes, blockSize = ceil(len / K)
//
// Identity of a set is (K, M, len, crc32). The app collects parts by idx under that identity,
// solves once it holds K, and checks crc32 over the result.

import (
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"
)

// StreamMagic identifies an erasure-coded enrolment frame and pins the framing version.
const StreamMagic = "LGQR2"

// streamHeaderMax is the header's worst-case length, reserved out of every frame's byte budget:
// magic, three 2-3 digit counts, a 4-digit length, 8 + 4 hex, seven separators.
const streamHeaderMax = 5 + 1 + 3 + 1 + 3 + 1 + 3 + 1 + 5 + 1 + 8 + 1 + 4 + 1

// Stream is one payload's frame set.
type Stream struct {
	K, M   int
	Len    int
	CRC    uint32
	blocks [][]byte // K data blocks then M parity blocks, all blockSize long
}

// NewStream splits payload into blocks of at most blockBytes and adds parity. parityRatio is M/K
// (0.5 = one parity per two data blocks, rounded up, at least 2). K+M must stay under 256 (the
// field), which a v8 frame budget and any real link satisfy by a wide margin.
func NewStream(payload []byte, blockBytes int, parityRatio float64) (*Stream, error) {
	if blockBytes < 1 {
		return nil, fmt.Errorf("block size %d", blockBytes)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("empty payload")
	}
	k := (len(payload) + blockBytes - 1) / blockBytes
	m := int(float64(k)*parityRatio + 0.999)
	if m < 2 {
		m = 2
	}
	if k+m > 255 {
		return nil, fmt.Errorf("payload needs %d+%d frames; the field allows 255", k, m)
	}
	bs := (len(payload) + k - 1) / k
	s := &Stream{K: k, M: m, Len: len(payload), CRC: crc32.ChecksumIEEE(payload)}
	s.blocks = make([][]byte, k+m)
	for i := 0; i < k; i++ {
		b := make([]byte, bs)
		copy(b, payload[i*bs:min(len(payload), (i+1)*bs)])
		s.blocks[i] = b
	}
	for j := 0; j < m; j++ {
		p := make([]byte, bs)
		for i := 0; i < k; i++ {
			c := cauchy(k, j, i)
			for t := 0; t < bs; t++ {
				p[t] ^= byte(gfMul(c, int(s.blocks[i][t])))
			}
		}
		s.blocks[k+j] = p
	}
	return s, nil
}

// cauchy is the parity matrix entry for parity row j and data column i: 1 / (x_j + y_i) with
// x_j = k + j and y_i = i, all distinct field elements, so every square submatrix of [I | C] is
// invertible , the property that makes any K frames enough.
func cauchy(k, j, i int) int {
	return gfInv((k + j) ^ i)
}

func gfInv(a int) int {
	if a == 0 {
		panic("gf inverse of zero")
	}
	return gfExp[255-gfLog[a]]
}

// Frames renders every frame (data first, then parity) as the strings the QR encoder takes.
func (s *Stream) Frames() []string {
	out := make([]string, 0, len(s.blocks))
	for idx := range s.blocks {
		out = append(out, s.Frame(idx))
	}
	return out
}

// Frame renders one frame.
func (s *Stream) Frame(idx int) string {
	body := s.blocks[idx]
	return fmt.Sprintf("%s %d %d %d %d %08x %04x %s", StreamMagic, idx, s.K, s.M, s.Len, s.CRC,
		crc32.ChecksumIEEE(body)&0xffff, body)
}

// StreamBlockBudget is how many payload bytes one frame carries at a per-frame byte budget.
func StreamBlockBudget(frameBytes int) int {
	b := frameBytes - streamHeaderMax
	if b < 8 {
		b = 8
	}
	return b
}

// streamPart is one parsed frame.
type streamPart struct {
	idx, k, m, n int
	crc          uint32
	body         []byte
}

// ParseStreamFrame checks a frame's shape and its own CRC. It is the box-side reference for the
// app's parser (the app mirrors this line for line).
func ParseStreamFrame(frame string) (streamPart, error) {
	var p streamPart
	fields := strings.SplitN(frame, " ", 8)
	if len(fields) != 8 || fields[0] != StreamMagic {
		return p, fmt.Errorf("not a %s frame", StreamMagic)
	}
	var err error
	if p.idx, err = strconv.Atoi(fields[1]); err != nil || p.idx < 0 {
		return p, fmt.Errorf("bad idx %q", fields[1])
	}
	if p.k, err = strconv.Atoi(fields[2]); err != nil || p.k < 1 {
		return p, fmt.Errorf("bad K %q", fields[2])
	}
	if p.m, err = strconv.Atoi(fields[3]); err != nil || p.m < 0 {
		return p, fmt.Errorf("bad M %q", fields[3])
	}
	if p.n, err = strconv.Atoi(fields[4]); err != nil || p.n < 1 {
		return p, fmt.Errorf("bad len %q", fields[4])
	}
	c, err := strconv.ParseUint(fields[5], 16, 32)
	if err != nil {
		return p, fmt.Errorf("bad crc %q", fields[5])
	}
	p.crc = uint32(c)
	pc, err := strconv.ParseUint(fields[6], 16, 16)
	if err != nil {
		return p, fmt.Errorf("bad part crc %q", fields[6])
	}
	if p.idx >= p.k+p.m {
		return p, fmt.Errorf("idx %d outside %d+%d", p.idx, p.k, p.m)
	}
	p.body = []byte(fields[7])
	bs := (p.n + p.k - 1) / p.k
	if len(p.body) != bs {
		return p, fmt.Errorf("body is %d bytes, block size is %d", len(p.body), bs)
	}
	if crc32.ChecksumIEEE(p.body)&0xffff != uint32(pc) {
		return p, fmt.Errorf("part crc mismatch")
	}
	return p, nil
}

// DecodeStream rebuilds the payload from any frames of one set , at least K distinct ones,
// duplicates and order irrelevant, a frame from another set rejected. The box-side reference the
// app mirrors, and what the tests use to prove the code is MDS.
func DecodeStream(frames []string) ([]byte, error) {
	if len(frames) == 0 {
		return nil, fmt.Errorf("no frames")
	}
	var first *streamPart
	parts := map[int][]byte{}
	for _, f := range frames {
		p, err := ParseStreamFrame(f)
		if err != nil {
			return nil, err
		}
		if first == nil {
			first = &p
		} else if p.k != first.k || p.m != first.m || p.n != first.n || p.crc != first.crc {
			return nil, fmt.Errorf("frame from a different enrolment set")
		}
		if _, dup := parts[p.idx]; !dup {
			parts[p.idx] = p.body
		}
	}
	k := first.k
	if len(parts) < k {
		return nil, fmt.Errorf("have %d distinct frames, need %d", len(parts), k)
	}
	bs := len(first.body)
	// Rows: one per held frame, data rows are unit vectors, parity rows are Cauchy rows. Solve
	// A * D = R by Gauss-Jordan over GF(256), R carried alongside as byte rows.
	rows := make([][]int, 0, k)
	rhs := make([][]byte, 0, k)
	for idx := 0; idx < k+first.m && len(rows) < k; idx++ {
		body, ok := parts[idx]
		if !ok {
			continue
		}
		row := make([]int, k)
		if idx < k {
			row[idx] = 1
		} else {
			for i := 0; i < k; i++ {
				row[i] = cauchy(k, idx-k, i)
			}
		}
		rows = append(rows, row)
		r := make([]byte, bs)
		copy(r, body)
		rhs = append(rhs, r)
	}
	for col := 0; col < k; col++ {
		piv := -1
		for r := col; r < k; r++ {
			if rows[r][col] != 0 {
				piv = r
				break
			}
		}
		if piv < 0 {
			return nil, fmt.Errorf("singular system") // cannot happen for a Cauchy set; defensive
		}
		rows[col], rows[piv] = rows[piv], rows[col]
		rhs[col], rhs[piv] = rhs[piv], rhs[col]
		inv := gfInv(rows[col][col])
		for c := 0; c < k; c++ {
			rows[col][c] = gfMul(rows[col][c], inv)
		}
		for t := 0; t < bs; t++ {
			rhs[col][t] = byte(gfMul(int(rhs[col][t]), inv))
		}
		for r := 0; r < k; r++ {
			if r == col || rows[r][col] == 0 {
				continue
			}
			f := rows[r][col]
			for c := 0; c < k; c++ {
				rows[r][c] ^= gfMul(f, rows[col][c])
			}
			for t := 0; t < bs; t++ {
				rhs[r][t] ^= byte(gfMul(f, int(rhs[col][t])))
			}
		}
	}
	out := make([]byte, 0, k*bs)
	for i := 0; i < k; i++ {
		out = append(out, rhs[i]...)
	}
	out = out[:first.n]
	if crc32.ChecksumIEEE(out) != first.crc {
		return nil, fmt.Errorf("checksum mismatch , frames do not rebuild the payload")
	}
	return out, nil
}
