package pair

import (
	"bytes"
	"strings"
	"testing"
	"time"
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

// The frame set is FIXED: any link that fits eight budget-sized blocks becomes exactly 8 data + 4
// parity frames , the sentence on the screen never changes , and each block is sized to the link,
// not to the budget, so a short link makes lighter symbols rather than fewer frames. A link too
// long for eight blocks falls back to more frames with half again as parity.
func TestFrameSetIsTwelveAnyEight(t *testing.T) {
	budget := versionM[maxAnimatedVersion][0] - 3
	for _, n := range []int{120, 500, 858, 880} {
		payload := []byte(strings.Repeat("y", n))
		bb := (len(payload) + streamDataFrames - 1) / streamDataFrames
		if mb := StreamBlockBudget(budget); bb > mb {
			bb = mb
		}
		s, err := NewStream(payload, bb, float64(streamParityFrames)/float64(streamDataFrames))
		if err != nil {
			t.Fatal(err)
		}
		if s.K != streamDataFrames || s.M != streamParityFrames {
			t.Fatalf("%d bytes: %d+%d frames, want %d+%d", n, s.K, s.M, streamDataFrames, streamParityFrames)
		}
		// Any eight of the twelve rebuild it , the last four data frames missing is the hard case.
		var parts []string
		for i, f := range s.Frames() {
			if i >= 4 && i < 8 {
				continue
			}
			parts = append(parts, f)
		}
		got, err := DecodeStream(parts)
		if err != nil || string(got) != string(payload) {
			t.Fatalf("%d bytes: rebuild from frames 1-4 + parity failed: %v", n, err)
		}
	}
	// Past eight budget-sized blocks the count grows, parity stays at half.
	long := []byte(strings.Repeat("z", 8*StreamBlockBudget(budget)+1))
	s, err := NewStream(long, StreamBlockBudget(budget), float64(streamParityFrames)/float64(streamDataFrames))
	if err != nil {
		t.Fatal(err)
	}
	if s.K != 9 || s.M != 5 {
		t.Fatalf("overlong link: %d+%d, want 9+5", s.K, s.M)
	}
	if defaultHold != 1000*time.Millisecond {
		t.Fatalf("default hold %s, want one second", defaultHold)
	}
}

// The rotation prints the sentence the person acts on , which frame, how many, how many are
// enough, how long each stays , and stops when the box sees the phone enrolled.
func TestAnimateFramesCaptionAndHold(t *testing.T) {
	s, err := NewStream([]byte(strings.Repeat("q", 96)), 12, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	calls := 0
	enrolled := func() bool { calls++; return calls >= 3 }
	t0 := time.Now()
	err = animateFrames(&out, s.Frames(), s.K, EncodeQR, RenderTerminal, enrolled, 20*time.Millisecond, nil)
	if err != errEnrolled {
		t.Fatalf("rotation should end on enrolment, got %v", err)
	}
	if took := time.Since(t0); took < 60*time.Millisecond || took > 2*time.Second {
		t.Fatalf("three frames at 20ms took %s", took)
	}
	text := out.String()
	if !strings.Contains(text, "QR 1 of 12 , hold the phone steady; any 8 of these complete the enrolment (0.0s each, 0s a lap).") {
		t.Fatalf("caption missing from:\n%s", text[:min(len(text), 400)])
	}
	if !strings.Contains(text, "QR 3 of 12") || !strings.Contains(text, "Enrolment complete") {
		t.Fatalf("rotation did not advance to frame 3 and finish")
	}
	// Enter on the terminal ends it too, without the enrolment banner.
	stop := make(chan struct{})
	close(stop)
	out.Reset()
	if err := animateFrames(&out, s.Frames(), s.K, EncodeQR, RenderTerminal, nil, time.Hour, stop); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if strings.Contains(out.String(), "Enrolment complete") {
		t.Fatal("Enter is not enrolment")
	}
}
