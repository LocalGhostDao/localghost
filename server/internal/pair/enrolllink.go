package pair

import (
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/url"
	"strings"
)

// EnrollLink is the box-side counterpart to the app's EnrollLink. The string it produces is what
// the QR carries and what EnrollLink.parse on the phone consumes, so the format is a contract:
//
//	localghost://enroll?v=1&host=...&port=...&code=...&fp=...&name=...
//
// host, code and fp are mandatory. Without the fingerprint the phone refuses the link, since the
// fingerprint is the trust anchor (no server vouches for the box).
//
// v is the format version. It MUST match the app's EnrollLink.CURRENT_VERSION. The app treats an
// absent v as 1 (older boxes shipped without it), so emitting it is backward-compatible, and it lets
// a newer box tell an older app to update rather than mis-parsing. Bump CurrentVersion here in lockstep
// with the app whenever the link format changes.
type EnrollLink struct {
	Host        string
	Port        int
	Fingerprint string
	BoxName     string
	// DeviceCertDER / DeviceKeyDER carry the device identity itself. The box issues the phone's
	// client cert + key at QR time and puts them here (raw DER, base64url in the query), so scanning
	// the QR IS enrolment , no network exchange, no pairing code. The key travels once, in the QR,
	// and is never stored on the box.
	DeviceCertDER []byte
	DeviceKeyDER  []byte
}

// CurrentVersion is the enrol-link format version this box emits. Keep equal to the app's
// EnrollLink.CURRENT_VERSION.
const CurrentVersion = 2

func (e EnrollLink) String() string {
	q := url.Values{}
	q.Set("v", fmt.Sprintf("%d", CurrentVersion))
	q.Set("host", e.Host)
	q.Set("port", fmt.Sprintf("%d", e.Port))
	// Fingerprint without separators keeps the QR payload short; the app re-inserts colons via
	// its normaliseFp. url.Values will percent-encode anything unusual, so plain hex is best.
	q.Set("fp", stripSeparators(e.Fingerprint))
	// The device identity. The app expects base64url( PEM ), so we PEM-wrap the DER here and encode
	// that. (The struct holds DER because the PKI issues DER and never writes the key to disk; PEM is
	// only the wire shape the app parser wants.) This is the bulk of the payload, which is why the QR
	// encoder goes up to v20 , a P256 cert+key is a few hundred bytes.
	if len(e.DeviceCertDER) > 0 {
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: e.DeviceCertDER})
		q.Set("cert", base64.RawURLEncoding.EncodeToString(certPEM))
	}
	if len(e.DeviceKeyDER) > 0 {
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: e.DeviceKeyDER})
		q.Set("key", base64.RawURLEncoding.EncodeToString(keyPEM))
	}
	if e.BoxName != "" {
		q.Set("name", e.BoxName)
	}
	// url.Values.Encode sorts keys; the app parser is order-independent, so that is fine.
	return "localghost://enroll?" + q.Encode()
}

// stripSeparators reduces a fingerprint to bare uppercase hex (no colons) for a compact QR.
func stripSeparators(fp string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(fp) {
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'F') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normaliseFp matches the app: uppercase, colon-separated hex pairs.
func normaliseFp(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(raw) {
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'F') {
			b.WriteRune(r)
		}
	}
	hexStr := b.String()
	var pairs []string
	for i := 0; i+1 < len(hexStr); i += 2 {
		pairs = append(pairs, hexStr[i:i+2])
	}
	return strings.Join(pairs, ":")
}
