package framed

// BACKUPS , the second copy of the irreplaceable bytes. Design rules, in order:
//
//  1. ASYMMETRIC OR NOTHING. The nightly job holds only a RECIPIENT PUBLIC KEY
//     (/var/lib/ghost/backup.pub, 64 hex chars, placed by the operator). It can write backups it
//     can never read; the private key lives offline with the human. A stolen box plus a stolen
//     backup disk yields ciphertext twice.
//  2. OPT-IN BY KEY. No backup.pub, no backups , one log line at startup, then silence. The
//     operator who has not made a key has not decided where their second copy lives, and a backup
//     system that guesses is a backup system that leaks.
//  3. The app cannot reach the backup directory , it is outside every served mount path, and the
//     appears-down surface has no route near it.
//  4. WEEKLY FULL, DAILY INCREMENTAL. Sunday (or first ever run) tars the originals (BackupRoots);
//     other nights tar only files that changed since the last backup watermark. Four fulls are
//     kept; incrementals older than the oldest kept full go with it. Every backup ends with a
//     manifest of what the box held that night: an incremental carries additions, never
//     deletions, so a restore of a full and its incrementals brings back what was deleted in
//     between, and the manifest is how to tell.
//  5. WHAT THE SEAL IS. Secrecy and damage detection: nobody without the private key reads a
//     backup, and a changed or cut file does not open. It is not proof of origin: anyone with
//     backup.pub can write a file that opens. Keep the backup disk where only you can write.
//
// Seal format ("GHSTBK1"): magic || ephemeral X25519 pub (32) || chunks. Per file: fresh ephemeral
// key, shared = ECDH(ephemeral, recipient), key = HKDF-SHA256(shared, salt=ephPub||recipPub,
// info="ghost-backup-v1"). Chunks: uint32 BE ciphertext length || ChaCha20-Poly1305 ciphertext of
// up to 1 MiB plaintext. Nonce: bytes 0..7 = BE64 chunk index, byte 11 = 0x01 on the final chunk
// (a truncated file fails authentication instead of decrypting to a shorter archive). This is the
// same hand-rolled STREAM shape used elsewhere in the repo , stdlib + x/crypto only, no new deps.

import (
	"archive/tar"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const backupMagic = "GHSTBK1"
const backupChunk = 1 << 20 // 1 MiB plaintext per sealed chunk

// sealedWriter encrypts a stream to a recipient public key in authenticated chunks.
type sealedWriter struct {
	w    io.Writer
	aead interface {
		Seal(dst, nonce, plaintext, additionalData []byte) []byte
		NonceSize() int
	}
	buf   []byte
	n     int
	chunk uint64
}

func newSealedWriter(w io.Writer, recipientPub []byte) (*sealedWriter, error) {
	if len(recipientPub) != 32 {
		return nil, fmt.Errorf("recipient public key must be 32 bytes, got %d", len(recipientPub))
	}
	curve := ecdh.X25519()
	eph, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ephemeral key: %w", err)
	}
	rp, err := curve.NewPublicKey(recipientPub)
	if err != nil {
		return nil, fmt.Errorf("recipient key: %w", err)
	}
	shared, err := eph.ECDH(rp)
	if err != nil {
		return nil, fmt.Errorf("ecdh: %w", err)
	}
	ephPub := eph.PublicKey().Bytes()
	salt := append(append([]byte{}, ephPub...), recipientPub...)
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, salt, []byte("ghost-backup-v1")), key); err != nil {
		return nil, fmt.Errorf("hkdf: %w", err)
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte(backupMagic)); err != nil {
		return nil, err
	}
	if _, err := w.Write(ephPub); err != nil {
		return nil, err
	}
	return &sealedWriter{w: w, aead: aead, buf: make([]byte, backupChunk)}, nil
}

func (s *sealedWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		c := copy(s.buf[s.n:], p)
		s.n += c
		p = p[c:]
		total += c
		if s.n == len(s.buf) {
			if err := s.flush(false); err != nil {
				return total, err
			}
		}
	}
	return total, nil
}

func (s *sealedWriter) flush(final bool) error {
	nonce := make([]byte, s.aead.NonceSize())
	binary.BigEndian.PutUint64(nonce[:8], s.chunk)
	if final {
		nonce[11] = 1
	}
	ct := s.aead.Seal(nil, nonce, s.buf[:s.n], nil)
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(ct)))
	if _, err := s.w.Write(lenBuf[:]); err != nil {
		return err
	}
	if _, err := s.w.Write(ct); err != nil {
		return err
	}
	s.chunk++
	s.n = 0
	return nil
}

// Close seals the final (possibly empty) chunk , the 0x01 nonce byte is the end-of-stream mark.
func (s *sealedWriter) Close() error { return s.flush(true) }

// BackupConfig , where the second copy lives and who can read it.
type BackupConfig struct {
	Dir     string // e.g. /var/lib/ghost/backup (operator mounts the HDD here)
	PubFile string // e.g. /var/lib/ghost/backup.pub (64 hex chars, X25519 public key)
}

// BackupRoot is one tree that goes into the backup, and the name it has inside the tar.
type BackupRoot struct {
	Dir string // on disk
	As  string // in the tar, relative to the mount: "frames/archive"
}

// BackupRoots is what the nightly backup holds, for a mount: the photo and video originals
// (frames/archive) and the Timeline import folders (framed). Everything else under frames/ is
// derived from the originals (previews, thumbs, day paths) or passing through (incoming), and
// reprocess rebuilds it. Until the second audit (10 Nov 2026) the job sealed <mount>/framed
// alone: the daemon's name, not the archive's folder, and no original was ever in a backup.
func BackupRoots(mount string) []BackupRoot {
	return []BackupRoot{
		{Dir: DefaultDirs(mount).Archive, As: "frames/archive"},
		{Dir: filepath.Join(mount, "framed"), As: "framed"},
	}
}

// BackupManifest is the tar member that lists every file under the roots at the time of the
// backup, full or incremental, one path a line. An incremental carries what was added, never
// what was deleted; the newest manifest is the record of what the box held that night.
const BackupManifest = "MANIFEST.txt"

// changedAt is when a file last became what it is: the later of its modification time and its
// inode change time. The archive keeps a photo's own, older, modification time, so by mtime
// alone a photo that arrived yesterday was in no incremental until Sunday.
func changedAt(info os.FileInfo) time.Time {
	t := info.ModTime()
	if ct, ok := inodeChanged(info); ok && ct.After(t) {
		t = ct
	}
	return t
}

// RunBackup writes one sealed tar of the roots. Full backups take everything; incrementals take
// files that changed after `since`. Returns the sealed file path, file count, and byte count of
// plaintext archived.
func RunBackup(cfg BackupConfig, roots []BackupRoot, full bool, since time.Time, lg *slog.Logger) (string, int, int64, error) {
	pubHex, err := os.ReadFile(cfg.PubFile)
	if err != nil {
		return "", 0, 0, fmt.Errorf("no recipient key (%s): %w", cfg.PubFile, err)
	}
	pub, err := hex.DecodeString(strings.TrimSpace(string(pubHex)))
	if err != nil || len(pub) != 32 {
		return "", 0, 0, fmt.Errorf("backup.pub must be 64 hex chars of X25519 public key")
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return "", 0, 0, err
	}
	kind := "incr"
	if full {
		kind = "full"
	}
	name := fmt.Sprintf("%s-%s.tar.seal", kind, time.Now().Format("2006-01-02-1504"))
	tmp := filepath.Join(cfg.Dir, "."+name+".tmp")
	out := filepath.Join(cfg.Dir, name)
	// a fresh file, never one that was waiting there and never through a link; and no
	// half-written file left behind when the run fails
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", 0, 0, err
	}
	done := false
	defer func() {
		f.Close()
		if !done {
			_ = os.Remove(tmp)
		}
	}()
	sw, err := newSealedWriter(f, pub)
	if err != nil {
		return "", 0, 0, err
	}
	tw := tar.NewWriter(sw)
	files, bytes := 0, int64(0)
	var manifest strings.Builder
	for _, root := range roots {
		if _, serr := os.Stat(root.Dir); serr != nil {
			continue // a tree this box does not have (no Timeline import yet)
		}
		walkErr := filepath.Walk(root.Dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil // a link or a device is not an original
			}
			rel, rerr := filepath.Rel(root.Dir, p)
			if rerr != nil {
				return rerr
			}
			member := filepath.ToSlash(filepath.Join(root.As, rel))
			manifest.WriteString(member)
			manifest.WriteByte('\n')
			if !full && !changedAt(info).After(since) {
				return nil
			}
			src, oerr := os.Open(p)
			if oerr != nil {
				// A file that vanished mid-walk (rare, live system) is logged and skipped , the
				// backup should carry everything it can, not die on the first race.
				lg.Warn("backup skip", "fn", "RunBackup", "path", p, "err", oerr)
				return nil
			}
			defer src.Close()
			// the size the header promises is the size read: a file still growing is cut there
			hdr := &tar.Header{Name: member, Mode: 0o640, Size: info.Size(), ModTime: info.ModTime(), Typeflag: tar.TypeReg}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			n, cerr := io.CopyN(tw, src, info.Size())
			if cerr != nil {
				return fmt.Errorf("%s: %w", member, cerr)
			}
			files++
			bytes += n
			return nil
		})
		if walkErr != nil {
			return "", files, bytes, walkErr
		}
	}
	m := manifest.String()
	if err := tw.WriteHeader(&tar.Header{Name: BackupManifest, Mode: 0o640, Size: int64(len(m)), ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
		return "", files, bytes, err
	}
	if _, err := io.WriteString(tw, m); err != nil {
		return "", files, bytes, err
	}
	if err := tw.Close(); err != nil {
		return "", files, bytes, err
	}
	if err := sw.Close(); err != nil {
		return "", files, bytes, err
	}
	if err := f.Sync(); err != nil {
		return "", files, bytes, err
	}
	if err := f.Close(); err != nil {
		return "", files, bytes, err
	}
	if err := os.Rename(tmp, out); err != nil {
		return "", files, bytes, err
	}
	done = true
	if full {
		pruneBackups(cfg.Dir, lg)
	}
	return out, files, bytes, nil
}

// Unseal reads a sealed backup from r and writes its tar to w, with the recipient's private
// key. Plaintext is written chunk by chunk as each authenticates, so a caller that must not
// act on a short archive writes to a temporary file and keeps it only when the error is nil
// (ghost.restore does): nil is returned only after the end-of-stream chunk authenticated and
// nothing follows it.
func Unseal(r io.Reader, w io.Writer, privKey []byte) error {
	priv, err := ecdh.X25519().NewPrivateKey(privKey)
	if err != nil {
		return fmt.Errorf("private key: %w", err)
	}
	head := make([]byte, len(backupMagic)+32)
	if _, err := io.ReadFull(r, head); err != nil {
		return fmt.Errorf("header: %w", err)
	}
	if string(head[:len(backupMagic)]) != backupMagic {
		return fmt.Errorf("not a ghost backup (bad magic)")
	}
	ephPub, err := ecdh.X25519().NewPublicKey(head[len(backupMagic):])
	if err != nil {
		return fmt.Errorf("ephemeral key: %w", err)
	}
	shared, err := priv.ECDH(ephPub)
	if err != nil {
		return fmt.Errorf("ecdh: %w", err)
	}
	salt := append(append([]byte{}, ephPub.Bytes()...), priv.PublicKey().Bytes()...)
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, salt, []byte("ghost-backup-v1")), key); err != nil {
		return fmt.Errorf("hkdf: %w", err)
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return err
	}
	var chunk uint64
	var lenBuf [4]byte
	for {
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return fmt.Errorf("truncated backup: the end-of-stream chunk never arrived (%w)", err)
		}
		n := binary.BigEndian.Uint32(lenBuf[:])
		if n > backupChunk+64 {
			return fmt.Errorf("chunk %d too large: corrupt or not a backup", chunk)
		}
		ct := make([]byte, n)
		if _, err := io.ReadFull(r, ct); err != nil {
			return fmt.Errorf("chunk %d cut short: %w", chunk, err)
		}
		// as a middle chunk first, then as the final one (the 0x01 marker)
		nonce := make([]byte, aead.NonceSize())
		binary.BigEndian.PutUint64(nonce[:8], chunk)
		pt, derr := aead.Open(nil, nonce, ct, nil)
		final := false
		if derr != nil {
			nonce[11] = 1
			pt, derr = aead.Open(nil, nonce, ct, nil)
			final = true
		}
		if derr != nil {
			return fmt.Errorf("chunk %d failed authentication: wrong key or corrupt file", chunk)
		}
		if _, err := w.Write(pt); err != nil {
			return fmt.Errorf("write: %w", err)
		}
		chunk++
		if final {
			var one [1]byte
			if n, _ := r.Read(one[:]); n > 0 {
				return fmt.Errorf("bytes after the end of the backup: the file is not as it was written")
			}
			return nil
		}
	}
}

// pruneBackups keeps the newest 4 fulls; incrementals older than the oldest kept full go with it.
func pruneBackups(dir string, lg *slog.Logger) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var fulls []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "full-") && strings.HasSuffix(e.Name(), ".tar.seal") {
			fulls = append(fulls, e.Name())
		}
	}
	sort.Strings(fulls) // names embed the date, so lexical order is chronological
	if len(fulls) <= 4 {
		return
	}
	cutoff := fulls[len(fulls)-4]
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".tar.seal") {
			continue
		}
		// Anything (full or incr) lexically older than the oldest kept full is out of its
		// restore chain and can go.
		if n < cutoff {
			if err := os.Remove(filepath.Join(dir, n)); err != nil {
				lg.Warn("prune failed", "fn", "pruneBackups", "file", n, "err", err)
			} else {
				lg.Info("pruned old backup", "fn", "pruneBackups", "file", n)
			}
		}
	}
}
