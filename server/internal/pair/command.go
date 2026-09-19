package pair

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// Options for one pairing render. The daemon/setup fills these from config/flags.
type Options struct {
	Host     string // LAN address or .local; empty -> auto-detect
	Port     int    // mTLS port the box serves on
	CertPath string // PEM cert served on that port; its SHA-256 is the trust anchor
	BoxName  string // human label (defaults to hostname elsewhere)
	// IssueDevice mints the device's client cert + key as raw DER. Required , the QR carries the
	// identity, so there is nothing to render without it. Wired to PKI.IssueDeviceCertDER.
	IssueDevice func(name string) (certDER, keyDER []byte, err error)
	// Animate rotates multi-frame QRs on the terminal instead of printing them in a column. The app
	// assembles frames in any order, so the person just holds the phone up while the box cycles ,
	// no taps, no network, no feedback channel needed (and none is possible pre-enrolment: the phone
	// has no client cert until the scan completes, so nginx's mTLS wall rejects it by design).
	// Callers set this when stdout is an interactive tty.
	Animate bool
	// EnrolledSignal, if set, returns true once the box has seen its first authenticated device , i.e.
	// the phone assembled every frame and made real contact through nginx. The rotation loop polls it
	// and stops on completion, so the operator gets a printed confirmation instead of eyeballing the
	// app's frame counter. Nil disables the feature (rotation then only ends on Enter).
	EnrolledSignal func() bool
}

// Run mints a fresh device identity, builds the enroll link that CARRIES it, and writes the link
// text plus a scannable terminal QR to w. There is no pairing code and no return value but error:
// scanning the QR is enrolment, done locally on the phone, so the box has nothing to "arm" or track.
//
// EncodeQR is the seam: it turns a frame string into a Matrix (qrencode.go, the from-scratch
// byte-mode encoder, no third-party QR). The device identity (cert+key) is ~850 bytes as DER, too
// much for one comfortably-scannable QR, so NewStream splits it into small erasure-coded frames
// (any K of K+M rebuild it) that the app reassembles.
func Run(w io.Writer, opts Options, encodeQR func(string) (Matrix, error)) error {
	host := opts.Host
	if host == "" {
		var err error
		if host, err = LANHost(); err != nil {
			return err
		}
	}
	fp, err := CertFingerprint(opts.CertPath)
	if err != nil {
		return fmt.Errorf("reading cert fingerprint: %w", err)
	}
	if opts.IssueDevice == nil {
		return fmt.Errorf("no device issuer wired: cannot mint the identity the QR must carry")
	}
	certDER, keyDER, err := opts.IssueDevice("primary")
	if err != nil {
		return fmt.Errorf("issuing device cert: %w", err)
	}

	link := EnrollLink{
		Host:          host,
		Port:          opts.Port,
		Fingerprint:   fp,
		BoxName:       opts.BoxName,
		DeviceCertDER: certDER,
		DeviceKeyDER:  keyDER,
	}
	// Split the link into erasure-coded frames (qrstream.go): K data blocks plus M parity blocks,
	// any K of which rebuild the link. A small link still yields one frame set; the app path is
	// the same whether there are two frames or twenty.
	//
	// staticBudget is the per-frame byte budget for NON-animated output (static print, or a small
	// terminal falling back from animation): v8, the density field testing settled on.
	staticBudget := versionM[maxAnimatedVersion][0] - 3
	budget := staticBudget
	animate := opts.Animate
	cells := false
	if animate {
		if cols, rows, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
			if b, ok := frameBudget(cols, rows); ok {
				budget = b
				cells = cellsFit(cols, rows, maxAnimatedVersion)
			} else {
				// Too small to rotate usefully. Print statically at the conservative budget (already set
				// above) , scroll and scan, or re-run from a larger window for the animated flow.
				animate = false
				fmt.Fprintln(w, "note: terminal is small for animated QR , printing small frames statically instead; a larger window enables the rotating view")
			}
		}
	}
	stream, err := NewStream([]byte(link.String()), StreamBlockBudget(budget), 0.5)
	if err != nil {
		return fmt.Errorf("framing the link: %w", err)
	}
	frames := stream.Frames()
	render := RenderTerminal
	if cells {
		render = RenderTerminalCells
	}
	fmt.Fprintln(w)
	if animate {
		if err := animateFrames(w, frames, stream.K, encodeQR, render, opts.EnrolledSignal); err != nil && err != errEnrolled {
			return err
		}
	} else {
		fmt.Fprintf(w, "The device identity spans %d QR codes; the app needs ANY %d of them. Scan in any order , it\n", len(frames), stream.K)
		fmt.Fprintln(w, "shows progress and assembles the identity once it has enough.")
		for i, frame := range frames {
			matrix, err := encodeQR(frame)
			if err != nil {
				return fmt.Errorf("encoding QR frame %d: %w", i+1, err)
			}
			fmt.Fprintf(w, "\n--- QR %d of %d ---\n", i+1, len(frames))
			fmt.Fprintln(w, render(matrix))
		}
	}
	fmt.Fprintf(w, "  box     %s:%d\n", host, opts.Port)
	fmt.Fprintf(w, "  finger  %s\n", fp)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Link:", link.String())
	fmt.Fprintln(w, "Anyone who scans this QR gets a working device identity , show it to your phone only.")
	return nil
}

// maxAnimatedVersion is the densest QR the rotating view will draw, whatever the terminal size.
// The evidence is in frameBudget; the number is here so a test can pin it.
const maxAnimatedVersion = 8

// frameBudget converts terminal geometry into a per-frame payload budget. Height is the binding
// constraint on most consoles: half-block rendering draws two module rows per text line, captions
// take ~6 lines, the quiet zone 8 modules. Below version 8 the per-frame payload collapses and a
// real identity link explodes into dozens of frames (an 80x25 console would need ~80 v3 frames , a
// three-minute rotation), so under v8 we decline and the caller prints statically instead.
func frameBudget(cols, rows int) (int, bool) {
	avail := cols
	if h := (rows - 6) * 2; h < avail {
		avail = h
	}
	avail -= 8 // quiet zone
	v := (avail - 17) / 4
	if v < 8 {
		return 0, false
	}
	// Cap at v8 even on huge terminals. Phone cameras pointed at MONITORS fight moire and per-module
	// blur, and field testing showed dense frames only scanning from far away , more, smaller
	// frames beat fewer, denser ones (the assembler does not care; the rotation just runs a bit
	// longer). The cap was v10, which split a real identity link into 7 frames; the LAST of those
	// (the short remainder, a v7-8 symbol) was the one that always scanned first try while the six
	// v10 frames each sometimes needed another lap of the rotation. v8 makes every frame that size,
	// 49 modules a side; with the DER link and erasure coding that is 8 data + 4 parity frames.
	if v > maxAnimatedVersion {
		v = maxAnimatedVersion
	}
	// The frame's byte budget: data codewords minus byte-mode overhead (mode + count). The stream
	// takes its header out of this (StreamBlockBudget).
	return versionM[v][0] - 3, true
}

// cellsFit says whether the terminal is tall enough to draw a frame of version v with one full
// character cell per module (RenderTerminalCells): (17+4v+8) rows plus captions. On a terminal
// that tall the bigger modules beat the half-block rendering's per-line hairlines; below it the
// half-block rendering is the only one that fits.
func cellsFit(cols, rows, v int) bool {
	side := qrSide(v) + 8
	return rows-6 >= side && cols >= 2*side
}

// animateFrames rotates the enrolment QR frames on an interactive terminal: each frame shows for a
// couple of seconds, then the screen clears and the next appears, looping until the operator presses
// Enter. The app collects frames opportunistically in any order (FrameAssembler is order-independent
// and duplicate-safe), so the person just holds the phone steady; its "scanned N of M" counter says
// when the set is complete. No feedback channel exists , or can: pre-enrolment the phone has no
// client cert, so the box's mTLS edge rejects it, which is the appears-down design doing its job.
// The rotation is pure display; security posture is unchanged.
func animateFrames(w io.Writer, frames []string, k int, encodeQR func(string) (Matrix, error), render func(Matrix) string, enrolled func() bool) error {
	// Pre-encode every frame so the loop never fails mid-rotation.
	rendered := make([]string, len(frames))
	for i, f := range frames {
		m, err := encodeQR(f)
		if err != nil {
			return fmt.Errorf("encoding QR frame %d: %w", i+1, err)
		}
		rendered[i] = render(m)
	}
	done := make(chan struct{})
	go func() {
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		close(done)
	}()
	// 2s per frame. The 3.2s the LGQR1 rotation used was insurance against missing a frame, because
	// a miss cost a whole lap; with erasure coding a miss costs one more frame, so the hold only
	// has to cover the phone's lock-and-decode (~0.3-0.8s, and it samples every 100ms while
	// assembling). Twenty attempts per frame is plenty; a lap of 17 frames is 34s and the phone
	// is normally done after K+1 or K+2 of them.
	const hold = 2000 * time.Millisecond
	// One full clear up front, cursor hidden for the duration (a blinking cursor inside the symbol
	// helps nobody). Each frame then redraws from HOME with erase-to-end-of-line per line and
	// erase-below at the end , no full clears in the loop, so there is no flicker, and frames of
	// different sizes (the last chunk is shorter, so its QR can be smaller) leave no residue.
	fmt.Fprint(w, "\x1b[2J\x1b[?25l")
	defer fmt.Fprint(w, "\x1b[?25h\x1b[0m\x1b[2J\x1b[H")
	i := 0
	for {
		fmt.Fprint(w, "\x1b[H")
		fmt.Fprintf(w, "QR %d of %d , hold the phone steady; any %d of these complete the enrolment.\x1b[K\n", i%len(frames)+1, len(frames), k)
		fmt.Fprint(w, "Press Enter here once the app shows the identity assembled.\x1b[K\n\x1b[K\n")
		for _, line := range strings.Split(rendered[i%len(frames)], "\n") {
			fmt.Fprint(w, line, "\x1b[K\n")
		}
		fmt.Fprint(w, "\x1b[J")
		select {
		case <-done:
			return nil
		case <-time.After(hold):
			i++
		}
		// After each frame, check whether the phone has completed enrolment (first authenticated
		// request reached the box). If so, stop rotating and report success , the operator no longer
		// has to read the app's counter to know it worked.
		if enrolled != nil && enrolled() {
			fmt.Fprint(w, "\x1b[2J\x1b[H")
			fmt.Fprintln(w, "Enrolment complete , the phone assembled its identity and reached the box.")
			return errEnrolled
		}
	}
}

// errEnrolled is a sentinel: the rotation ended because enrolment succeeded, not because of an error.
// Run() treats it as success.
var errEnrolled = fmt.Errorf("enrolled")
