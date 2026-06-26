package secd

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/LocalGhostDao/localghost/server/internal/auth"
)

// DeviceIssuer mints a device client cert during enrolment. The daemon wires this to the box PKI
// (setup/debian.System.DeviceIdentity). Kept as an interface so the server package does not depend
// on the OS-specific setup package.
type DeviceIssuer interface {
	DeviceIdentity(name string) (certPEM, keyPEM string, err error)
}

// enrollService handles device enrollment. The QR carries the trust anchor (the box cert
// fingerprint) and the one-time pairing code, but NOT the device identity: the box issues this
// device its client cert + key during the authenticated enrol exchange and returns them here. The
// pairing code is checked through the rate-limited auth.Gate so a guessed code is throttled and
// locked out.
type enrollService struct {
	stateDir string
	gate     *auth.Gate
	issuer   DeviceIssuer
	mu       sync.Mutex
	// pairingCode is the one-time code shown in the QR at setup; cleared after a successful enroll.
	pairingCode string
}

func newEnrollService(stateDir string) *enrollService {
	return &enrollService{
		stateDir: stateDir,
		gate:     auth.NewGate(auth.DefaultPolicy(), auth.NewMemoryStore()),
	}
}

// SetIssuer wires the box PKI so enrol can mint device certs. Called by the daemon at startup.
func (e *enrollService) SetIssuer(i DeviceIssuer) {
	e.mu.Lock()
	e.issuer = i
	e.mu.Unlock()
}

// SetPairingCode is called by setup to arm enrollment with the current one-time code.
func (e *enrollService) SetPairingCode(code string) {
	e.mu.Lock()
	e.pairingCode = code
	e.mu.Unlock()
}

type enrollRequest struct {
	PairingCode string `json:"pairingCode"`
	DeviceName  string `json:"deviceName"`
}

type enrollResponse struct {
	OK            bool   `json:"ok"`
	DeviceToken   string `json:"deviceToken,omitempty"`
	DeviceCertPem string `json:"deviceCertPem,omitempty"`
	DeviceKeyPem  string `json:"deviceKeyPem,omitempty"`
	Error         string `json:"error,omitempty"`
}

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req enrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	// Rate-limit by source so a brute-forced pairing code is throttled and locks out.
	id := clientID(r)
	if err := s.enroll.gate.CheckAllowed(id); err != nil {
		writeJSON(w, enrollResponse{OK: false, Error: "too many attempts, slow down"})
		return
	}
	s.enroll.mu.Lock()
	expected := s.enroll.pairingCode
	s.enroll.mu.Unlock()

	if expected == "" || req.PairingCode != expected {
		s.enroll.gate.RecordFailure(id)
		writeJSON(w, enrollResponse{OK: false, Error: "invalid pairing code"})
		return
	}
	s.enroll.gate.RecordSuccess(id)

	// Code is good. Issue this device its client cert + key from the box PKI and return them; the
	// phone stores them and presents the cert for mTLS on every later call. The one-time code is
	// now spent: clear it in memory AND remove the enroll.env file so a daemon restart does not
	// re-arm enrolment with the same code.
	s.enroll.mu.Lock()
	issuer := s.enroll.issuer
	s.enroll.pairingCode = ""
	stateDir := s.enroll.stateDir
	s.enroll.mu.Unlock()
	if stateDir != "" {
		_ = os.Remove(filepath.Join(stateDir, "enroll.env"))
	}

	name := req.DeviceName
	if name == "" {
		name = "device"
	}
	var certPEM, keyPEM string
	var err error
	if issuer != nil {
		certPEM, keyPEM, err = issuer.DeviceIdentity(name)
		if err != nil {
			writeJSON(w, enrollResponse{OK: false, Error: "could not issue device certificate"})
			return
		}
	}

	token := newDeviceToken()
	writeJSON(w, enrollResponse{
		OK:            true,
		DeviceToken:   token,
		DeviceCertPem: certPEM,
		DeviceKeyPem:  keyPEM,
	})
}
