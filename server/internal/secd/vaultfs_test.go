package secd

// What root writes into the vault goes onto the volume or nowhere. The daemons' user owns the
// volume and every name on it; these tests stand in for a daemon that has swapped one of those
// names for a link to somewhere else on the machine (audit run 2, 10 Nov 2026).

import (
	"crypto/ecdh"
	"crypto/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const nobodyUID = 65534

// outside is a directory that stands for /etc: one file in it, and nothing may change.
func outside(t *testing.T, base string) (dir string, unchanged func()) {
	t.Helper()
	dir = filepath.Join(base, "etc")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "passwd"), []byte("root:x:0:0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	owner := func(p string) uint32 {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Sys().(*syscall.Stat_t).Uid
	}
	was, wasFile := owner(dir), owner(filepath.Join(dir, "passwd"))
	return dir, func() {
		t.Helper()
		ents, _ := os.ReadDir(dir)
		if len(ents) != 1 || ents[0].Name() != "passwd" {
			names := []string{}
			for _, e := range ents {
				names = append(names, e.Name())
			}
			t.Fatalf("the outside directory changed: %v", names)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "passwd")); string(b) != "root:x:0:0\n" {
			t.Fatalf("the outside file changed: %q", b)
		}
		if owner(dir) != was || owner(filepath.Join(dir, "passwd")) != wasFile {
			t.Fatal("the outside directory or its file changed owner")
		}
	}
}

func vaultServer(t *testing.T) (s *Server, base, mount string) {
	t.Helper()
	base = t.TempDir()
	mount = filepath.Join(base, "mnt", "slot0")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	s = &Server{}
	s.cfg.StateDir = base
	if os.Getuid() == 0 {
		s.cfg.RunUser = "nobody"
	}
	return s, base, mount
}

func swapForLink(t *testing.T, name, target string) {
	t.Helper()
	if err := os.Rename(name, name+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

func TestSpoolRefusesALinkedInbox(t *testing.T) {
	s, base, mount := vaultServer(t)
	etc, unchanged := outside(t, base)
	dir := filepath.Join(mount, "synthd", "news", "inbox")

	// an honest batch: the directories are made, and are the daemons' when secd is root
	if err := s.spool(dir, "news", []byte(`{"n":1}`)); err != nil {
		t.Fatalf("honest spool: %v", err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || strings.HasSuffix(ents[0].Name(), ".part") {
		t.Fatalf("spooled: %v", ents)
	}
	if os.Getuid() == 0 {
		for _, p := range []string{filepath.Join(mount, "synthd"), filepath.Dir(dir), dir, filepath.Join(dir, ents[0].Name())} {
			fi, _ := os.Stat(p)
			if fi.Sys().(*syscall.Stat_t).Uid != nobodyUID {
				t.Fatalf("%s is not the daemons' user's", p)
			}
		}
	}

	// the inbox swapped for a link out of the volume
	swapForLink(t, dir, etc)
	if err := s.spool(dir, "news", []byte(`{"n":2}`)); err == nil {
		t.Fatal("spooled through a link")
	}
	unchanged()

	// the same one level up, and with a link that stays inside the volume
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir+".old", dir); err != nil {
		t.Fatal(err)
	}
	swapForLink(t, filepath.Dir(dir), etc)
	if err := s.spool(dir, "news", []byte(`{"n":3}`)); err == nil {
		t.Fatal("spooled through a linked parent")
	}
	unchanged()
	private := filepath.Join(mount, "secd")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(dir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, filepath.Dir(dir)); err != nil {
		t.Fatal(err)
	}
	if err := s.spool(dir, "news", []byte(`{"n":4}`)); err == nil {
		t.Fatal("spooled through a link inside the volume")
	}
	if ents, _ := os.ReadDir(private); len(ents) != 0 {
		t.Fatalf("secd's own directory was written into: %v", ents)
	}
	if fi, _ := os.Stat(private); os.Getuid() == 0 && fi.Sys().(*syscall.Stat_t).Uid != 0 {
		t.Fatal("secd's own directory was handed over")
	}
}

func TestSpoolNeverWritesThroughALinkedName(t *testing.T) {
	_, base, mount := vaultServer(t)
	etc, unchanged := outside(t, base)
	d, err := dirUnder(mount, "noted/outbox", true, 0o750, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	box := filepath.Join(mount, "noted", "outbox")
	if err := os.Symlink(filepath.Join(etc, "passwd"), filepath.Join(box, "1.json.part")); err != nil {
		t.Fatal(err)
	}
	if err := writeIn(d, "1.json.part", []byte("mail"), 0o640, 0, 0); err != nil {
		t.Fatalf("a planted link is replaced, not an error: %v", err)
	}
	unchanged()
	fi, err := os.Lstat(filepath.Join(box, "1.json.part"))
	if err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("the part is not a plain file: %v %v", fi, err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", "../x"} {
		if _, err := createIn(d, bad, 0o640); err == nil {
			t.Fatalf("createIn took %q", bad)
		}
	}
	if _, err := dirUnder(mount, "../../etc", true, 0o750, 0, 0); err == nil {
		t.Fatal("walked above the volume")
	}
}

func TestFrameSpoolRefusesALinkedIncoming(t *testing.T) {
	s, base, mount := vaultServer(t)
	etc, unchanged := outside(t, base)
	dir := filepath.Join(mount, "frames", "incoming")
	uid, gid := s.spoolCred()
	r := httptest.NewRequest("POST", "/v1/frames", strings.NewReader("jpeg"))
	r.Header.Set("X-Ghost-Taken", "1700000000000")
	if n, err := spoolBody(s.cfg.StateDir, dir, r, 1<<20, uid, gid, &s.closing); err != nil || n != 4 {
		t.Fatalf("honest upload: %d %v", n, err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || !strings.Contains(ents[0].Name(), "-t1700000000000") {
		t.Fatalf("spooled: %v", ents)
	}
	swapForLink(t, dir, etc)
	r = httptest.NewRequest("POST", "/v1/frames", strings.NewReader("jpeg"))
	if _, err := spoolBody(s.cfg.StateDir, dir, r, 1<<20, uid, gid, &s.closing); err == nil {
		t.Fatal("a photo was spooled through a link")
	}
	unchanged()
}

func TestIngestAndHealStayOnTheVolume(t *testing.T) {
	_, base, mount := vaultServer(t)
	etc, unchanged := outside(t, base)
	staging := filepath.Join(base, "staging", "bin")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "ghost.framed"), []byte("new build"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(mount, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	// both names the ingest touches are links to a file outside
	for _, n := range []string{"ghost.framed.ingest", "ghost.framed"} {
		if err := os.Symlink(filepath.Join(etc, "passwd"), filepath.Join(bin, n)); err != nil {
			t.Fatal(err)
		}
	}
	b := &backend{}
	if os.Getuid() == 0 {
		b.runUser = "nobody"
	}
	b.ingestStagedBinaries(mount)
	unchanged()
	fi, err := os.Lstat(filepath.Join(bin, "ghost.framed"))
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o755 {
		t.Fatalf("the binary is not in place as a plain 0755 file: %v %v", fi, err)
	}
	if got, _ := os.ReadFile(filepath.Join(bin, "ghost.framed")); string(got) != "new build" {
		t.Fatalf("binary: %q", got)
	}
	if _, err := os.Stat(filepath.Join(staging, "ghost.framed")); err == nil {
		t.Fatal("the staged copy stayed")
	}

	// bin/ itself a link: nothing is ingested, nothing outside is touched
	if err := os.WriteFile(filepath.Join(staging, "ghost.noted"), []byte("new build"), 0o755); err != nil {
		t.Fatal(err)
	}
	swapForLink(t, bin, etc)
	b.ingestStagedBinaries(mount)
	unchanged()

	// the heal: a top directory and services.conf that are links are left alone, and so is a
	// link among the spool files; an old root-owned spool file is handed over
	if err := os.Symlink(filepath.Join(etc, "passwd"), filepath.Join(mount, "services.conf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(etc, filepath.Join(mount, "logs")); err != nil {
		t.Fatal(err)
	}
	spool := filepath.Join(mount, "frames", "incoming")
	if err := os.MkdirAll(spool, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, "old"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(etc, "passwd"), filepath.Join(spool, "link")); err != nil {
		t.Fatal(err)
	}
	healVolume(mount, nobodyUID, nobodyUID)
	unchanged()
	if os.Getuid() == 0 {
		if fi, _ := os.Stat(filepath.Join(spool, "old")); fi.Sys().(*syscall.Stat_t).Uid != nobodyUID {
			t.Fatal("the old spool file was not handed over")
		}
		if fi, _ := os.Stat(filepath.Join(mount, "run")); fi == nil || fi.Sys().(*syscall.Stat_t).Uid != nobodyUID {
			t.Fatal("run/ was not made for the daemons' user")
		}
	}
}

func TestTrailKeyStaysOnTheVolume(t *testing.T) {
	_, base, mount := vaultServer(t)
	etc, unchanged := outside(t, base)
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveTrailKey(mount, "aa", k); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got, err := loadTrailKey(mount, "aa"); err != nil || string(got.Bytes()) != string(k.Bytes()) {
		t.Fatalf("load: %v", err)
	}
	if err := moveTrailKey(mount, "aa", "bb"); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := loadTrailKey(mount, "bb"); err != nil {
		t.Fatalf("after the move: %v", err)
	}
	if err := moveTrailKey(mount, "gone", "cc"); err != nil {
		t.Fatalf("moving a key that is not there: %v", err)
	}
	swapForLink(t, filepath.Join(mount, "secd"), etc)
	if err := saveTrailKey(mount, "dd", k); err == nil {
		t.Fatal("a key was written through a link")
	}
	if err := moveTrailKey(mount, "bb", "ee"); err == nil {
		t.Fatal("a key was moved through a link")
	}
	unchanged()
}
