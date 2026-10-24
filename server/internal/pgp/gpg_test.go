package pgp

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Interoperability with GnuPG, both ways, when gpg is on the machine (it is in CI and on the
// box); skipped otherwise. GnuPG is the reference the other clients are measured against.
func TestGnuPGInterop(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("no gpg")
	}
	home, err := os.MkdirTemp("/tmp", "lgpg")
	if err != nil {
		t.Skip("no temp dir")
	}
	defer os.RemoveAll(home)
	os.Chmod(home, 0o700)
	gpg := func(stdin []byte, args ...string) ([]byte, error) {
		cmd := exec.Command("gpg", append([]string{"--homedir", home, "--batch", "--yes", "--pinentry-mode", "loopback", "--passphrase", "", "--trust-model", "always", "--no-tty", "-q"}, args...)...)
		cmd.Stdin = bytes.NewReader(stdin)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		if err != nil {
			return out.Bytes(), &gpgErr{err, errb.String()}
		}
		return out.Bytes(), nil
	}
	defer exec.Command("gpgconf", "--homedir", home, "--kill", "gpg-agent").Run()
	now := time.Now()

	// GnuPG's own keys: a modern one and an RSA one
	for _, g := range [][]string{{"G <g@example>", "ed25519", "cv25519"}, {"R <r@example>", "rsa3072", "rsa3072"}} {
		if _, err := gpg(nil, "--quick-gen-key", g[0], g[1], "cert,sign", "never"); err != nil {
			t.Fatal(err)
		}
		fpr := gpgFingerprint(t, gpg, g[0])
		if _, err := gpg(nil, "--quick-add-key", fpr, g[2], "encr", "never"); err != nil {
			t.Fatal(err)
		}
	}
	gExport, err := gpg(nil, "--export", "--armor", "g@example")
	if err != nil {
		t.Fatal(err)
	}
	rExport, err := gpg(nil, "--export", "--armor", "r@example")
	if err != nil {
		t.Fatal(err)
	}
	gKey, err := ParsePublicKey(gExport, now)
	if err != nil {
		t.Fatalf("parse gpg ed25519 key: %v", err)
	}
	rKey, err := ParsePublicKey(rExport, now)
	if err != nil {
		t.Fatalf("parse gpg rsa key: %v", err)
	}
	if !gKey.CanEncrypt() || !rKey.CanEncrypt() || gKey.UserIDs[0] != "G <g@example>" || len(gKey.signers) == 0 || len(rKey.signers) == 0 {
		t.Fatalf("gpg keys: %+v / %+v", gKey, rKey)
	}

	// the box's key, imported into GnuPG
	box, err := Generate("Box <box@example>", now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gpg([]byte(box.Armor()), "--import"); err != nil {
		t.Fatalf("gpg refused the box's public key: %v", err)
	}
	if out, err := gpg(nil, "--list-keys", "--with-colons", "box@example"); err != nil || !strings.Contains(string(out), "box@example") {
		t.Fatalf("box key not listed: %v\n%s", err, out)
	}

	plain := []byte("Plaintext for the relay to carry and never read.\nSecond line, ünïcödé.\n")
	ioDir := t.TempDir()
	plainPath := filepath.Join(ioDir, "in.txt")
	os.WriteFile(plainPath, plain, 0o600)

	// GnuPG encrypts and signs to the box, with each compression it might choose
	for _, comp := range []string{"none", "zlib", "zip", "bzip2"} {
		for _, signer := range []string{"g@example", "r@example"} {
			out, err := gpg(nil, "--armor", "--compress-algo", comp, "-r", "box@example", "-u", signer, "--sign", "--encrypt", "-o", "-", plainPath)
			if err != nil {
				t.Fatalf("gpg encrypt (%s, %s): %v", comp, signer, err)
			}
			m, err := Open(out, box, now, []*PublicKey{gKey, rKey})
			if err != nil {
				t.Fatalf("open gpg's message (%s, %s): %v", comp, signer, err)
			}
			if !m.Encrypted || !m.Signed || !m.Verified || !bytes.Equal(m.Data, plain) {
				t.Fatalf("gpg's message (%s, %s): %+v", comp, signer, m)
			}
			want := gKey.Fingerprint
			if signer == "r@example" {
				want = rKey.Fingerprint
			}
			if m.SignedBy != want {
				t.Fatalf("signed by %x, want %x", m.SignedBy, want)
			}
		}
	}
	// GnuPG encrypts without signing, binary
	out, err := gpg(nil, "-r", "box@example", "--encrypt", "-o", "-", plainPath)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Open(out, box, now, nil)
	if err != nil || m.Signed || !bytes.Equal(m.Data, plain) {
		t.Fatalf("%+v %v", m, err)
	}
	// GnuPG signs only (binary, --sign) and detached
	out, err = gpg(nil, "-u", "g@example", "--sign", "-o", "-", plainPath)
	if err != nil {
		t.Fatal(err)
	}
	m, err = Open(out, nil, now, []*PublicKey{gKey})
	if err != nil || m.Encrypted || !m.Verified || !bytes.Equal(m.Data, plain) {
		t.Fatalf("%+v %v", m, err)
	}
	out, err = gpg(nil, "-u", "r@example", "--armor", "--detach-sign", "-o", "-", plainPath)
	if err != nil {
		t.Fatal(err)
	}
	m, err = VerifyDetached(plain, out, now, []*PublicKey{rKey})
	if err != nil || !m.Verified || m.SignedBy != rKey.Fingerprint {
		t.Fatalf("%+v %v", m, err)
	}

	// a signing subkey (the back-signature path) and a NIST key
	gFpr := gpgFingerprint(t, gpg, "g@example")
	if _, err := gpg(nil, "--quick-add-key", gFpr, "ed25519", "sign", "never"); err != nil {
		t.Fatal(err)
	}
	gExport, _ = gpg(nil, "--export", "--armor", "g@example")
	gKey, err = ParsePublicKey(gExport, now)
	if err != nil || len(gKey.signers) != 2 {
		t.Fatalf("signing subkey not accepted: %v, %d signers", err, len(gKey.signers))
	}
	subFpr := ""
	listed, err := gpg(nil, "--list-keys", "--with-colons", "g@example")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(listed), "\n") {
		if strings.HasPrefix(line, "fpr:") {
			subFpr = strings.Split(line, ":")[9] // the last fpr line is the newest subkey's
		}
	}
	out, err = gpg(nil, "-u", subFpr+"!", "--sign", "-o", "-", plainPath)
	if err != nil {
		t.Fatal(err)
	}
	m, err = Open(out, nil, now, []*PublicKey{gKey})
	if err != nil || !m.Verified || m.SignedBy != gKey.Fingerprint {
		t.Fatalf("signing subkey: %+v %v", m, err)
	}
	if _, err := gpg(nil, "--quick-gen-key", "N <n@example>", "nistp256", "cert,sign", "never"); err != nil {
		t.Fatal(err)
	}
	nFpr := gpgFingerprint(t, gpg, "n@example")
	if _, err := gpg(nil, "--quick-add-key", nFpr, "nistp256", "encr", "never"); err != nil {
		t.Fatal(err)
	}
	nExport, err := gpg(nil, "--export", "--armor", "n@example")
	if err != nil {
		t.Fatal(err)
	}
	nKey, err := ParsePublicKey(nExport, now)
	if err != nil || !nKey.CanEncrypt() {
		t.Fatalf("nistp256: %v", err)
	}
	out, err = gpg(nil, "-r", "box@example", "-u", "n@example", "--sign", "--encrypt", "-o", "-", plainPath)
	if err != nil {
		t.Fatal(err)
	}
	m, err = Open(out, box, now, []*PublicKey{nKey})
	if err != nil || !m.Verified || !bytes.Equal(m.Data, plain) {
		t.Fatalf("nistp256 signed: %+v %v", m, err)
	}
	msgN, _ := Encrypt(plain, "", now, []*PublicKey{nKey}, nil)
	nPath := filepath.Join(ioDir, "n.pgp")
	os.WriteFile(nPath, msgN, 0o600)
	if out, err := gpg(nil, "--decrypt", nPath); err != nil || !bytes.Equal(out, plain) {
		t.Fatalf("nistp256 decrypt: %v %q", err, out)
	}
	// an expired key is refused
	if _, err := gpg(nil, "--quick-gen-key", "E <e@example>", "ed25519", "cert,sign", "seconds=1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	eExport, err := gpg(nil, "--export", "--armor", "e@example")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePublicKey(eExport, time.Now()); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired key accepted: %v", err)
	}

	// the box encrypts and signs to GnuPG's keys; GnuPG decrypts and checks the signature
	for _, to := range []struct {
		key *PublicKey
		uid string
	}{{gKey, "g@example"}, {rKey, "r@example"}} {
		msg, err := Encrypt(plain, "in.txt", now, []*PublicKey{to.key}, box)
		if err != nil {
			t.Fatal(err)
		}
		msgPath := filepath.Join(ioDir, to.uid+".asc")
		os.WriteFile(msgPath, []byte(Armor(BlockMessage, msg)), 0o600)
		out, err := gpg(nil, "--status-fd", "2", "--decrypt", msgPath)
		if err != nil {
			t.Fatalf("gpg decrypt for %s: %v", to.uid, err)
		}
		if !bytes.Equal(out, plain) {
			t.Fatalf("gpg decrypted to %q", out)
		}
		// the status lines went to stderr, which gpgErr keeps only on failure; ask again for them
		status := gpgStatus(t, home, msgPath)
		if !strings.Contains(status, "DECRYPTION_OKAY") || !strings.Contains(status, "GOODSIG") || !strings.Contains(status, "VALIDSIG") {
			t.Fatalf("gpg status for %s:\n%s", to.uid, status)
		}
	}
	// the box to both at once, unsigned
	msg, _ := Encrypt(plain, "", now, []*PublicKey{gKey, rKey}, nil)
	both := filepath.Join(ioDir, "both.pgp")
	os.WriteFile(both, msg, 0o600)
	out, err = gpg(nil, "--decrypt", both)
	if err != nil || !bytes.Equal(out, plain) {
		t.Fatalf("both: %v %q", err, out)
	}
	// the box's detached signature, checked by GnuPG
	sig, _ := DetachedSignature(plain, now, box)
	sigPath := filepath.Join(ioDir, "in.txt.asc")
	os.WriteFile(sigPath, []byte(Armor(BlockSignature, sig)), 0o600)
	if _, err := gpg(nil, "--verify", sigPath, plainPath); err != nil {
		t.Fatalf("gpg --verify: %v", err)
	}
	// the box's signed-only message, read by GnuPG
	signed, _ := Sign(plain, "in.txt", now, box)
	signedPath := filepath.Join(ioDir, "signed.pgp")
	os.WriteFile(signedPath, signed, 0o600)
	out, err = gpg(nil, "--decrypt", signedPath)
	if err != nil || !bytes.Equal(out, plain) {
		t.Fatalf("signed-only: %v %q", err, out)
	}
	status := gpgStatus(t, home, signedPath)
	if !strings.Contains(status, "GOODSIG") {
		t.Fatalf("signed-only status:\n%s", status)
	}
}

type gpgErr struct {
	err    error
	stderr string
}

func (e *gpgErr) Error() string { return e.err.Error() + ": " + strings.TrimSpace(e.stderr) }

func gpgFingerprint(t *testing.T, gpg func([]byte, ...string) ([]byte, error), uid string) string {
	out, err := gpg(nil, "--list-keys", "--with-colons", uid)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "fpr:") {
			return strings.Split(line, ":")[9]
		}
	}
	t.Fatalf("no fingerprint for %s", uid)
	return ""
}

func gpgStatus(t *testing.T, home, path string) string {
	cmd := exec.Command("gpg", "--homedir", home, "--batch", "--yes", "--pinentry-mode", "loopback", "--passphrase", "", "--trust-model", "always", "--no-tty", "--status-fd", "1", "--decrypt", "-o", os.DevNull, path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gpg status: %v\n%s", err, out)
	}
	return string(out)
}
