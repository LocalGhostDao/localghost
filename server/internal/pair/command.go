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
// byte-mode encoder, no third-party QR). The device identity (cert+key) is ~1.4 KB, too much for one
// comfortably-scannable QR, so ChunkLink splits it into a few small frames the app reassembles.
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
	// Chunk the link into scannable frames. A real device identity (~1.4 KB) will not fit one
	// comfortable QR, so we render a few small frames the app scans in sequence. A small link yields a
	// single frame, so this is one code path, not a special case.
	payload := chunkPayloadBytes
	animate := opts.Animate
	if animate {
		if cols, rows, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
			if p, ok := frameBudget(cols, rows); ok {
				payload = p
			} else {
				// Too small to rotate usefully (frames would shrink until a real link needs dozens).
				// Print statically instead , scroll and scan, or re-run from a larger window.
				animate = false
				fmt.Fprintln(w, "note: this terminal is small for animated QR , printing frames statically; a larger window makes this nicer")
			}
		}
	}
	frames := ChunkLinkSized(link.String(), payload)
	fmt.Fprintln(w)
	if len(frames) == 1 {
		matrix, err := encodeQR(frames[0])
		if err != nil {
			return fmt.Errorf("encoding QR: %w", err)
		}
		fmt.Fprintln(w, RenderTerminal(matrix))
		fmt.Fprintln(w, "Scan this with the LocalGhost app. The QR carries the device identity , scanning it enrols the phone.")
	} else if animate {
		if err := animateFrames(w, frames, encodeQR, opts.EnrolledSignal); err != nil && err != errEnrolled {
			return err
		}
	} else {
		fmt.Fprintf(w, "The device identity spans %d QR codes. In the app, scan them in any order , it\n", len(frames))
		fmt.Fprintln(w, "shows progress and assembles the identity once all are captured.")
		for i, frame := range frames {
			matrix, err := encodeQR(frame)
			if err != nil {
				return fmt.Errorf("encoding QR frame %d: %w", i+1, err)
			}
			fmt.Fprintf(w, "\n--- QR %d of %d ---\n", i+1, len(frames))
			fmt.Fprintln(w, RenderTerminal(matrix))
		}
	}
	fmt.Fprintf(w, "  box     %s:%d\n", host, opts.Port)
	fmt.Fprintf(w, "  finger  %s\n", fp)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Link:", link.String())
	fmt.Fprintln(w, "Anyone who scans this QR gets a working device identity , show it to your phone only.")
	return nil
}

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
	// Cap at v10 even on huge terminals. Phone cameras pointed at MONITORS fight moire and per-module
	// blur, and field testing showed dense frames (v12+) only scanning from far away , more, smaller
	// frames beat fewer, denser ones (the assembler does not care; the rotation just runs a bit
	// longer). v10 keeps a real identity link to ~8 frames.
	if v > 10 {
		v = 10
	}
	// data codewords minus byte-mode overhead (~3) and the frame header (~20, with margin).
	return versionM[v][0] - 27, true
}

// animateFrames rotates the enrolment QR frames on an interactive terminal: each frame shows for a
// couple of seconds, then the screen clears and the next appears, looping until the operator presses
// Enter. The app collects frames opportunistically in any order (FrameAssembler is order-independent
// and duplicate-safe), so the person just holds the phone steady; its "scanned N of M" counter says
// when the set is complete. No feedback channel exists , or can: pre-enrolment the phone has no
// client cert, so the box's mTLS edge rejects it, which is the appears-down design doing its job.
// The rotation is pure display; security posture is unchanged.
func animateFrames(w io.Writer, frames []string, encodeQR func(string) (Matrix, error), enrolled func() bool) error {
	// Pre-encode every frame so the loop never fails mid-rotation.
	rendered := make([]string, len(frames))
	for i, f := range frames {
		m, err := encodeQR(f)
		if err != nil {
			return fmt.Errorf("encoding QR frame %d: %w", i+1, err)
		}
		rendered[i] = RenderTerminal(m)
	}
	done := make(chan struct{})
	go func() {
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		close(done)
	}()
	// Field-tuned: 3.2s per frame. Long enough that a phone reliably locks, decodes, and registers
	// each frame (with its success pulse) before the next appears, without the rotation dragging.
	const hold = 3200 * time.Millisecond
	// One full clear up front, cursor hidden for the duration (a blinking cursor inside the symbol
	// helps nobody). Each frame then redraws from HOME with erase-to-end-of-line per line and
	// erase-below at the end , no full clears in the loop, so there is no flicker, and frames of
	// different sizes (the last chunk is shorter, so its QR can be smaller) leave no residue.
	fmt.Fprint(w, "\x1b[2J\x1b[?25l")
	defer fmt.Fprint(w, "\x1b[?25h\x1b[0m\x1b[2J\x1b[H")
	i := 0
	for {
		fmt.Fprint(w, "\x1b[H")
		fmt.Fprintf(w, "QR %d of %d , hold the phone steady; the app collects them in any order.\x1b[K\n", i%len(frames)+1, len(frames))
		fmt.Fprint(w, "Press Enter here once the app shows all frames captured.\x1b[K\n\x1b[K\n")
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