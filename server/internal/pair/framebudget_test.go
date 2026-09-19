package pair

import (
	"strings"
	"testing"
)

// A big terminal must not buy denser frames: the cap is what field testing settled on, and a
// realistic v3 identity link (host, port, fingerprint, DER P256 cert + key, ~850 bytes) becomes
// eight data frames and four parity frames of it, every one within v8.
func TestFrameBudgetCapsAtEasyVersion(t *testing.T) {
	budget, ok := frameBudget(300, 120)
	if !ok {
		t.Fatal("a huge terminal must animate")
	}
	if want := versionM[maxAnimatedVersion][0] - 3; budget != want {
		t.Fatalf("budget = %d, want the v%d budget %d", budget, maxAnimatedVersion, want)
	}
	link := "localghost://enroll?" + strings.Repeat("x", 850-len("localghost://enroll?"))
	s, err := NewStream([]byte(link), StreamBlockBudget(budget), 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if s.K != 8 || s.M != 4 {
		t.Fatalf("an 850-byte link splits into %d+%d frames, want 8+4", s.K, s.M)
	}
	frames := s.Frames()
	for i, f := range frames {
		m, err := EncodeQR(f)
		if err != nil {
			t.Fatalf("frame %d: %v", i+1, err)
		}
		if side := m.Size(); side > qrSide(maxAnimatedVersion) {
			t.Fatalf("frame %d is %d modules a side, over v%d's %d", i+1, side, maxAnimatedVersion, qrSide(maxAnimatedVersion))
		}
	}
}

// A small console still declines to animate below v8 rather than exploding into dozens of frames.
func TestFrameBudgetDeclinesTinyTerminal(t *testing.T) {
	if _, ok := frameBudget(80, 24); ok {
		t.Fatal("an 80x24 console cannot hold a v8 frame; it must print statically")
	}
	// Full-cell rendering needs 57 rows for v8 plus captions; a maximised 4K terminal has them, a
	// laptop's 50-row window does not and keeps the half-block form.
	if !cellsFit(200, 70, 8) || cellsFit(200, 50, 8) || cellsFit(100, 70, 8) {
		t.Fatal("cellsFit thresholds")
	}
}

// The full-cell renderer paints every module as two background-coloured spaces and nothing else,
// so no font glyph can leave a gap through a module.
func TestRenderTerminalCells(t *testing.T) {
	m, err := EncodeQR("LGQR2 0 1 2 5 00000000 0000 hello")
	if err != nil {
		t.Fatal(err)
	}
	out := RenderTerminalCells(m)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != m.Size()+8 {
		t.Fatalf("%d lines for a %d-module symbol with quiet zone", len(lines), m.Size())
	}
	if strings.ContainsAny(out, "\u2588\u2580\u2584") {
		t.Fatal("block glyphs in the cell rendering")
	}
	if !strings.HasPrefix(lines[0], "\x1b[47m  ") {
		t.Fatalf("quiet zone must be white cells: %q", lines[0][:12])
	}
}
