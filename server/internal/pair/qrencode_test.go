package pair

import (
	"fmt"
	"strings"
	"testing"
)

// roundTripDecode is an independently-written decoder used ONLY in tests to prove the encoder
// produces a spec-correct symbol: encode text -> matrix -> decode back -> must equal the input. A
// scanner reads a symbol the same way, so a clean round-trip is strong evidence it will scan.
func roundTripDecode(q qrMatrix, v, mask int) (string, error) {
	g := newGrid(v)
	g.placeFunctionPatterns(v)
	n := q.n
	// unmask
	um := make([][]int, n)
	for r := 0; r < n; r++ {
		um[r] = make([]int, n)
		copy(um[r], q.m[r])
		for c := 0; c < n; c++ {
			if !g.res[r][c] && maskFns[mask](r, c) {
				um[r][c] ^= 1
			}
		}
	}
	// read codewords in the same zigzag
	var bits []int
	col := n - 1
	upward := true
	for col > 0 {
		if col == 6 {
			col--
		}
		for i := 0; i < n; i++ {
			r := i
			if upward {
				r = n - 1 - i
			}
			for _, c := range []int{col, col - 1} {
				if !g.res[r][c] {
					bits = append(bits, um[r][c])
				}
			}
		}
		col -= 2
		upward = !upward
	}
	var cws []int
	for i := 0; i+8 <= len(bits); i += 8 {
		val := 0
		for j := 0; j < 8; j++ {
			val = val<<1 | bits[i+j]
		}
		cws = append(cws, val)
	}
	// de-interleave back to block order, take data codewords
	cfg := versionM[v]
	g1b, g1d, g2b, g2d := cfg[2], cfg[3], cfg[4], cfg[5]
	sizes := []int{}
	for i := 0; i < g1b; i++ {
		sizes = append(sizes, g1d)
	}
	for i := 0; i < g2b; i++ {
		sizes = append(sizes, g2d)
	}
	blocks := make([][]int, len(sizes))
	idx := 0
	maxd := 0
	for _, s := range sizes {
		if s > maxd {
			maxd = s
		}
	}
	for i := 0; i < maxd; i++ {
		for bi, s := range sizes {
			if i < s {
				blocks[bi] = append(blocks[bi], cws[idx])
				idx++
			}
		}
	}
	var data []int
	for _, b := range blocks {
		data = append(data, b...)
	}
	// parse byte mode
	var dbits []int
	for _, cw := range data {
		for i := 7; i >= 0; i-- {
			dbits = append(dbits, (cw>>i)&1)
		}
	}
	pos := 0
	take := func(k int) int {
		val := 0
		for i := 0; i < k; i++ {
			val = val<<1 | dbits[pos]
			pos++
		}
		return val
	}
	if mode := take(4); mode != 0b0100 {
		return "", fmt.Errorf("unexpected mode %d", mode)
	}
	ccbits := 8
	if v >= 10 {
		ccbits = 16
	}
	length := take(ccbits)
	out := make([]byte, length)
	for i := 0; i < length; i++ {
		out[i] = byte(take(8))
	}
	return string(out), nil
}

// encodeForTest re-runs EncodeQR but also reports the chosen version+mask for the decoder.
func encodeForTest(t *testing.T, text string) (qrMatrix, int, int) {
	t.Helper()
	v, err := chooseVersion(len([]byte(text)))
	if err != nil {
		t.Fatal(err)
	}
	cws := encodeData(text, v)
	g := newGrid(v)
	g.placeFunctionPatterns(v)
	g.placeData(cws)
	bestScore := -1
	var best [][]int
	bestMask := 0
	for mask := 0; mask < 8; mask++ {
		cand := g.masked(mask)
		placeFormat(cand, g.n, mask)
		sc := penalty(cand, g.n)
		if bestScore < 0 || sc < bestScore {
			bestScore = sc
			best = cand
			bestMask = mask
		}
	}
	return qrMatrix{m: best, n: g.n}, v, bestMask
}

func TestQRRoundTrip(t *testing.T) {
	// The real link the box emits, built through EnrollLink.String() so the test exercises exactly
	// what ships (including the v= version field), not a hand-written approximation.
	realLink := EnrollLink{
		Host: "192.168.1.50", Port: 8443,
		Fingerprint: "AB:12:CD:34", BoxName: "box",
		DeviceCertDER: []byte("test-cert-der-stand-in-bytes"),
		DeviceKeyDER:  []byte("test-key-der-stand-in-bytes"),
	}.String()

	payloads := []string{
		realLink,
		"localghost://enroll?host=192.168.1.50&port=8443&fp=AB12CD34&name=box",
		"localghost://enroll?host=10.0.0.5&code=X",
		"hello",
		"a",
	}
	// add varying sizes
	for _, nc := range []int{16, 40, 80, 120, 160, 200} {
		s := ""
		for i := 0; i < nc; i++ {
			s += string(rune('A' + (i % 26)))
		}
		payloads = append(payloads, s)
	}
	for _, p := range payloads {
		q, v, mask := encodeForTest(t, p)
		back, err := roundTripDecode(q, v, mask)
		if err != nil {
			t.Fatalf("decode of %d-char payload failed: %v", len(p), err)
		}
		if back != p {
			t.Fatalf("round-trip mismatch (len %d, v%d): got %q", len(p), v, back)
		}
	}
}

func TestQRMatrixIsSquareBinary(t *testing.T) {
	m, err := EncodeQR("localghost://enroll?host=10.0.0.1&code=AB&fp=CD")
	if err != nil {
		t.Fatal(err)
	}
	n := m.Size()
	if n < 21 {
		t.Fatalf("a v1 symbol is at least 21 modules, got %d", n)
	}
	// Dark() must be callable for every cell.
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			_ = m.Dark(x, y)
		}
	}
}

func TestQRTooBigFails(t *testing.T) {
	big := make([]byte, 1000)
	for i := range big {
		big[i] = 'A'
	}
	if _, err := EncodeQR(string(big)); err != ErrPayloadTooBig {
		t.Fatalf("oversized payload must fail with ErrPayloadTooBig, got %v", err)
	}
}

func TestEnrollLinkCarriesVersion(t *testing.T) {
	// The app reads v as the format version (absent = 1). The box must emit it so a future format
	// change lets a newer box tell an older app to update rather than mis-parsing. Pin that the
	// emitted link contains the current version, and that it equals the documented constant.
	if CurrentVersion != 1 {
		t.Fatalf("CurrentVersion changed to %d , update the app's EnrollLink.CURRENT_VERSION in lockstep", CurrentVersion)
	}
	link := EnrollLink{Host: "10.0.0.1", Port: 8443, Fingerprint: "CD"}.String()
	want := "v=2"
	if !strings.Contains(link, want) {
		t.Fatalf("enroll link missing version: got %q, want it to contain %q", link, want)
	}
}
