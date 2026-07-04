package pair

import (
	"fmt"
	"io"
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
}

// Run mints a fresh device identity, builds the enroll link that CARRIES it, and writes the link
// text plus a scannable terminal QR to w. There is no pairing code and no return value but error:
// scanning the QR is enrolment, done locally on the phone, so the box has nothing to "arm" or track.
//
// EncodeQR is the seam: it turns the link string into a Matrix (qrencode.go, the from-scratch
// byte-mode encoder, no third-party QR). The payload is larger now (it holds a cert+key), which is
// why that encoder handles up to v20.
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
	matrix, err := encodeQR(link.String())
	if err != nil {
		return fmt.Errorf("encoding QR: %w", err)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, RenderTerminal(matrix))
	fmt.Fprintln(w, "Scan this with the LocalGhost app. The QR carries the device identity , scanning it enrols the phone.")
	fmt.Fprintf(w, "  box     %s:%d\n", host, opts.Port)
	fmt.Fprintf(w, "  finger  %s\n", fp)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Link:", link.String())
	fmt.Fprintln(w, "Anyone who scans this QR gets a working device identity , show it to your phone only.")
	return nil
}
