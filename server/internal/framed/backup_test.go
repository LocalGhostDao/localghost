package framed

import (
	"archive/tar"
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func backupKey(t *testing.T, dir string) (priv []byte, cfg BackupConfig) {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg = BackupConfig{Dir: filepath.Join(dir, "backup"), PubFile: filepath.Join(dir, "backup.pub")}
	if err := os.WriteFile(cfg.PubFile, []byte(hex.EncodeToString(k.PublicKey().Bytes())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return k.Bytes(), cfg
}

func members(t *testing.T, sealed string, priv []byte) map[string]string {
	t.Helper()
	b, err := os.ReadFile(sealed)
	if err != nil {
		t.Fatal(err)
	}
	var plain bytes.Buffer
	if err := Unseal(bytes.NewReader(b), &plain, priv); err != nil {
		t.Fatalf("unseal: %v", err)
	}
	out := map[string]string{}
	tr := tar.NewReader(&plain)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		out[h.Name] = string(body)
	}
	return out
}

// The nightly backup holds the photo originals (audit run 2: it held the Timeline folders and
// nothing else), an incremental takes a photo whose own date is old, and the manifest lists
// what the box held.
func TestBackupHoldsTheOriginals(t *testing.T) {
	dir := t.TempDir()
	mount := filepath.Join(dir, "mnt")
	dirs := DefaultDirs(mount)
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	write := func(p, body string, mtime time.Time) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
		if !mtime.IsZero() {
			if err := os.Chtimes(p, mtime, mtime); err != nil {
				t.Fatal(err)
			}
		}
	}
	old := time.Date(2019, 6, 1, 12, 0, 0, 0, time.UTC)
	write(filepath.Join(dirs.Archive, "2019", "06", "01", "aa.jpg"), "photo one", old)
	write(filepath.Join(dirs.Preview, "aa.jpg"), "a preview", time.Time{})
	write(filepath.Join(dirs.Incoming, "123-abc"), "in flight", time.Time{})
	write(filepath.Join(mount, "framed", "timeline", "2019.json"), "timeline", time.Time{})
	priv, cfg := backupKey(t, dir)

	out, files, _, err := RunBackup(cfg, BackupRoots(mount), true, time.Time{}, lg)
	if err != nil || files != 2 {
		t.Fatalf("full: %d files, %v", files, err)
	}
	m := members(t, out, priv)
	if m["frames/archive/2019/06/01/aa.jpg"] != "photo one" || m["framed/timeline/2019.json"] != "timeline" {
		t.Fatalf("the originals are not in the backup: %v", m)
	}
	for name := range m {
		if strings.Contains(name, "preview") || strings.Contains(name, "incoming") {
			t.Fatalf("derived or passing data in the backup: %s", name)
		}
	}
	if !strings.Contains(m[BackupManifest], "frames/archive/2019/06/01/aa.jpg\n") {
		t.Fatalf("manifest: %q", m[BackupManifest])
	}

	// an incremental: a photo that arrives now with a date from 2018 is taken (by when it
	// landed, not by its own date); the one already backed up is not, and is still listed
	time.Sleep(30 * time.Millisecond)
	since := time.Now()
	time.Sleep(30 * time.Millisecond)
	write(filepath.Join(dirs.Archive, "2018", "01", "01", "bb.jpg"), "photo two", old.AddDate(-1, 0, 0))
	out2, files, _, err := RunBackup(cfg, BackupRoots(mount), false, since, lg)
	if err != nil || files != 1 {
		t.Fatalf("incremental: %d files, %v", files, err)
	}
	m = members(t, out2, priv)
	if m["frames/archive/2018/01/01/bb.jpg"] != "photo two" {
		t.Fatalf("the new photo with an old date is not in the incremental: %v", m)
	}
	if _, again := m["frames/archive/2019/06/01/aa.jpg"]; again {
		t.Fatal("the incremental took what had not changed")
	}
	if strings.Count(m[BackupManifest], "\n") != 3 {
		t.Fatalf("manifest: %q", m[BackupManifest])
	}
	// nothing half-written is left beside the backups
	ents, _ := os.ReadDir(cfg.Dir)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("left behind: %s", e.Name())
		}
	}
}

// A backup that was cut, changed or added to does not open, and says so.
func TestUnsealRefusesAShortOrChangedBackup(t *testing.T) {
	dir := t.TempDir()
	mount := filepath.Join(dir, "mnt")
	p := filepath.Join(DefaultDirs(mount).Archive, "a.bin")
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), 3*backupChunk+17), 0o640); err != nil {
		t.Fatal(err)
	}
	priv, cfg := backupKey(t, dir)
	out, _, _, err := RunBackup(cfg, BackupRoots(mount), true, time.Time{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	whole, _ := os.ReadFile(out)
	if err := Unseal(bytes.NewReader(whole), io.Discard, priv); err != nil {
		t.Fatalf("the whole backup: %v", err)
	}
	// cut at a chunk boundary: header (7+32) then one chunk of 4 + 1 MiB + 16
	cut := whole[:7+32+4+backupChunk+16]
	if err := Unseal(bytes.NewReader(cut), io.Discard, priv); err == nil {
		t.Fatal("a backup cut at a chunk boundary opened")
	}
	changed := append([]byte{}, whole...)
	changed[len(changed)/2] ^= 1
	if err := Unseal(bytes.NewReader(changed), io.Discard, priv); err == nil {
		t.Fatal("a changed backup opened")
	}
	if err := Unseal(bytes.NewReader(append(append([]byte{}, whole...), 'x')), io.Discard, priv); err == nil {
		t.Fatal("a backup with bytes after its end opened")
	}
	other, _ := ecdh.X25519().GenerateKey(rand.Reader)
	if err := Unseal(bytes.NewReader(whole), io.Discard, other.Bytes()); err == nil {
		t.Fatal("another key opened the backup")
	}
}
