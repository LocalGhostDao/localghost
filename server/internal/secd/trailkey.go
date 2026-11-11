package secd

// THE PHONE'S TRAIL, SEALED. The phone keeps where it has been (the spool waiting for the box, and
// the last two days it draws itself) sealed to an X25519 key whose private half lives here, in the
// vault, and nowhere on the phone at rest. The phone seals each point with the public half as it
// records it (in the background, locked, with no key to open anything); it can read its own points
// again only after a PIN unlock, when it fetches the private half from here and holds it in memory
// until the app locks. A phone taken and searched shows a list of sealed lines.
//
// A sealed line is "s1:" and base64 of: the sender's one-off X25519 public key (32 bytes), a GCM
// nonce (12), and AES-256-GCM of the point's text ("ts lat lon [acc]", the spool's own line) with
// "s1:" as additional data. The AES key is HKDF-SHA256 of the X25519 secret, salted with both
// public keys, info "localghost trail v1". The phone's side is sync/TrailSeal.kt.
//
// The key is per device, kept at <mount>/secd/trail/<device>.json, root's and 0600 (the daemons'
// user cannot read it). secd opens the sealed points of an upload before it spools them, so ghost
// .framed reads the same points it always did.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const trailPrefix = "s1:"

const trailInfo = "localghost trail v1"

// trailKeyFile is what the vault keeps for one device.
type trailKeyFile struct {
	Public  string `json:"public"`  // base64, 32 bytes
	Private string `json:"private"` // base64, 32 bytes
}

func trailKeyPath(mount, dev string) string {
	return filepath.Join(mount, "secd", "trail", dev+".json")
}

// loadTrailKey reads the device's key; os.ErrNotExist when it has none.
func loadTrailKey(mount, dev string) (*ecdh.PrivateKey, error) {
	if dev == "" {
		return nil, os.ErrNotExist
	}
	b, err := os.ReadFile(trailKeyPath(mount, dev))
	if err != nil {
		return nil, err
	}
	var f trailKeyFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("trail key file: %w", err)
	}
	return trailKeyFrom(f.Public, f.Private)
}

// trailKeyFrom checks a public/private pair (base64 raw X25519) belongs together.
func trailKeyFrom(pub64, priv64 string) (*ecdh.PrivateKey, error) {
	pub, err1 := base64.StdEncoding.DecodeString(pub64)
	priv, err2 := base64.StdEncoding.DecodeString(priv64)
	if err1 != nil || err2 != nil || len(pub) != 32 || len(priv) != 32 {
		return nil, errors.New("a trail key is two 32-byte X25519 keys in base64")
	}
	k, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	if string(k.PublicKey().Bytes()) != string(pub) {
		return nil, errors.New("the private key does not match the public one")
	}
	return k, nil
}

func saveTrailKey(mount, dev string, k *ecdh.PrivateKey) error {
	// <mount>/secd is secd's, not the daemons', but the volume's root is theirs: the way down is
	// walked without following a link (vaultfs.go), so the key lands on the volume or nowhere
	d, err := dirUnder(mount, filepath.Join("secd", "trail"), true, 0o700, 0, 0)
	if err != nil {
		return err
	}
	defer d.Close()
	b, _ := json.Marshal(trailKeyFile{
		Public:  base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()),
		Private: base64.StdEncoding.EncodeToString(k.Bytes()),
	})
	if err := writeIn(d, dev+".json.part", b, 0o600, 0, 0); err != nil {
		return err
	}
	return d.Rename(dev+".json.part", dev+".json")
}

// moveTrailKey follows a device to its new certificate (device/rekey): same key, new name.
func moveTrailKey(mount, from, to string) error {
	if from == "" || to == "" || from == to {
		return nil
	}
	d, err := dirUnder(mount, filepath.Join("secd", "trail"), false, 0, 0, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer d.Close()
	err = d.Rename(from+".json", to+".json")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func trailAEAD(shared, ephPub, recipPub []byte) (cipher.AEAD, error) {
	salt := make([]byte, 0, 64)
	salt = append(append(salt, ephPub...), recipPub...)
	key, err := hkdf.Key(sha256.New, shared, salt, trailInfo, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// openTrail opens one sealed line.
func openTrail(k *ecdh.PrivateKey, line string) (string, error) {
	if !strings.HasPrefix(line, trailPrefix) {
		return "", errors.New("not a sealed line")
	}
	raw, err := base64.StdEncoding.DecodeString(line[len(trailPrefix):])
	if err != nil || len(raw) < 32+12+16 {
		return "", errors.New("sealed line too short")
	}
	eph, err := ecdh.X25519().NewPublicKey(raw[:32])
	if err != nil {
		return "", err
	}
	shared, err := k.ECDH(eph)
	if err != nil {
		return "", err
	}
	aead, err := trailAEAD(shared, raw[:32], k.PublicKey().Bytes())
	if err != nil {
		return "", err
	}
	pt, err := aead.Open(nil, raw[32:44], raw[44:], []byte(trailPrefix))
	if err != nil {
		return "", errors.New("sealed line does not open with this key")
	}
	return string(pt), nil
}

// sealTrail seals one line to a public key (the phone's side; here for tests and symmetry).
func sealTrail(pub *ecdh.PublicKey, plain string) (string, error) {
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		return "", err
	}
	aead, err := trailAEAD(shared, eph.PublicKey().Bytes(), pub.Bytes())
	if err != nil {
		return "", err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := append(append(eph.PublicKey().Bytes(), nonce...), aead.Seal(nil, nonce, []byte(plain), []byte(trailPrefix))...)
	return trailPrefix + base64.StdEncoding.EncodeToString(out), nil
}

// openLocationBatch turns a phone's batch with sealed points ({"source", "points", "sealed"}) into
// the plain shape ghost.framed reads ({"source", "points"}). A line that does not open, or opens to
// something that is not a point, is counted and left out (it could never be read). errNoTrailKey
// when the device has no key here.
func openLocationBatch(body []byte, k *ecdh.PrivateKey) ([]byte, int, error) {
	var in struct {
		Source string            `json:"source"`
		Points []json.RawMessage `json:"points"`
		Sealed []string          `json:"sealed"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, 0, err
	}
	if len(in.Sealed) == 0 {
		return body, 0, nil
	}
	if k == nil {
		return nil, 0, errNoTrailKey
	}
	bad := 0
	for _, line := range in.Sealed {
		pt, err := openTrail(k, line)
		if err != nil {
			bad++
			continue
		}
		var ts int64
		var lat, lon float64
		if n, _ := fmt.Sscanf(pt, "%d %g %g", &ts, &lat, &lon); n != 3 || ts <= 0 || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			bad++
			continue
		}
		point := map[string]any{"ts": ts, "lat": lat, "lon": lon}
		// "ts lat lon acc via": how the phone took it goes on (the accuracy stays behind)
		if f := strings.Fields(pt); len(f) == 5 && validVia(f[4]) {
			point["via"] = f[4]
		}
		p, _ := json.Marshal(point)
		in.Points = append(in.Points, p)
	}
	out, err := json.Marshal(map[string]any{"source": in.Source, "points": in.Points})
	return out, bad, err
}

var errNoTrailKey = errors.New("no trail key for this device")

// validVia: a short lowercase word (w, p, a today), nothing a phone could smuggle a query into.
func validVia(v string) bool {
	if v == "" || len(v) > 8 {
		return false
	}
	for _, r := range v {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// handleTrailKey , GET /v1/trail/key: this device's key, for the app to read its own trail while
// unlocked ({"have":false} when the box has none). POST /v1/trail/key {"public","private"}: the
// phone hands its key to the vault (base64 raw X25519), once; it keeps only the public half.
func (s *Server) handleTrailKey(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) {
		s.appearsDown(w)
		return
	}
	mount, ok := s.voiceMount()
	dev := deviceKeyFromRequest(r)
	if !ok || dev == "" {
		s.appearsDown(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		k, err := loadTrailKey(mount, dev)
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, map[string]any{"have": false})
			return
		}
		if err != nil {
			secdLog.Warn("trail key unreadable", "fn", "handleTrailKey", "err", err)
			s.appearsDown(w)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, map[string]any{"have": true,
			"public":  base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()),
			"private": base64.StdEncoding.EncodeToString(k.Bytes())})
	case http.MethodPost:
		var req trailKeyFile
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
			s.appearsDown(w)
			return
		}
		k, err := trailKeyFrom(req.Public, req.Private)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if old, err := loadTrailKey(mount, dev); err == nil && string(old.PublicKey().Bytes()) != string(k.PublicKey().Bytes()) {
			secdLog.Info("trail key replaced (a new key on the phone)", "fn", "handleTrailKey", "device", dev[:8])
		}
		if err := saveTrailKey(mount, dev, k); err != nil {
			secdLog.Warn("trail key not kept", "fn", "handleTrailKey", "err", err)
			s.appearsDown(w)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		s.appearsDown(w)
	}
}
