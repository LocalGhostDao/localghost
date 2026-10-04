package update

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var elf = []byte("\x7fELF fake binary")

type entry struct {
	name string
	body []byte
	typ  byte
}

func bundle(t *testing.T, entries []entry) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "r.tar.gz")
	f, _ := os.Create(p)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: typ}
		if typ == tar.TypeSymlink {
			h.Linkname, h.Size = "/etc/passwd", 0
		}
		tw.WriteHeader(h)
		if typ == tar.TypeReg {
			tw.Write(e.body)
		}
	}
	tw.Close()
	gz.Close()
	f.Close()
	return p
}

func good(version string) []entry {
	return []entry{
		{name: "VERSION", body: []byte(version + "\n")},
		{name: "COMMIT", body: []byte("abc123\n")},
		{name: "CHANGES.txt", body: []byte("fix one thing\nadd another\n")},
		{name: "bin/ghost.secd", body: append(append([]byte{}, elf...), version...)},
		{name: "bin/ghost.watchd", body: elf},
		{name: "bin/ghost.framed", body: append(append([]byte{}, elf...), version...)},
		{name: "bin/ghost-cli", body: elf},
		{name: "tools/mirror_fetch.sh", body: []byte("#!/bin/sh\n")},
	}
}

func TestUnpackTakesOnlyARelease(t *testing.T) {
	rel, err := Unpack(bundle(t, good("0.9.3")), filepath.Join(t.TempDir(), "u"))
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "0.9.3" || rel.Commit != "abc123" || len(rel.Changes) != 2 || len(rel.Bins) != 4 || len(rel.Tools) != 1 {
		t.Fatalf("release %+v", rel)
	}
	for _, bad := range [][]entry{
		append(good("1"), entry{name: "../../etc/cron.d/x", body: []byte("x")}),
		append(good("1"), entry{name: "etc/passwd", body: []byte("x")}),
		append(good("1"), entry{name: "bin/evil", typ: tar.TypeSymlink}),
		append(good("1"), entry{name: "bin/script", body: []byte("#!/bin/sh")}), // not ELF
		good("1")[1:], // no VERSION
		{{name: "VERSION", body: []byte("1")}, {name: "bin/ghost.watchd", body: elf}}, // no secd
	} {
		if _, err := Unpack(bundle(t, bad), filepath.Join(t.TempDir(), "u")); err == nil {
			t.Fatalf("took a bundle it should not: %v", bad[len(bad)-1].name)
		}
	}
	// the notes beside the three, and the installer a 0.0.5 bundle carries at its top, are let through
	full := append(good("0.0.5"), entry{name: "NOTES.md", body: []byte("# notes\n")}, entry{name: "install.sh", body: []byte("#!/bin/sh\n")})
	if _, err := Unpack(bundle(t, full), filepath.Join(t.TempDir(), "u")); err != nil {
		t.Fatalf("a bundle with NOTES.md and install.sh at the top: %v", err)
	}
}

// A bundle since 0.0.5 carries what sets a box up as well: those binaries stay in the unpacked
// release, the guard is never replaced, ghost-qr joins the system binaries, the cohort is staged.
func TestApplyLeavesTheSetupBinariesAlone(t *testing.T) {
	p, cohort := boxAt(t)
	os.WriteFile(filepath.Join(p.BinDir, "ghost-update-guard"), []byte("the guard"), 0o755)
	ents := append(good("0.0.5"),
		entry{name: "bin/ghost-setup", body: elf}, entry{name: "bin/ghost-update-guard", body: elf},
		entry{name: "bin/ghost-landtiles", body: elf}, entry{name: "bin/ghost-qr", body: elf},
		entry{name: "tools/setup.sh", body: []byte("#!/bin/sh\n")})
	rel, err := Unpack(bundle(t, ents), filepath.Join(p.State, "releases", "0.0.5"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(p, rel, cohort, "0.0.4"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"ghost-setup", "ghost-update-guard", "ghost-landtiles"} {
		if _, err := os.Stat(filepath.Join(p.Staging, n)); err == nil {
			t.Fatalf("%s staged with the cohort", n)
		}
		if _, err := os.Stat(filepath.Join(p.BinDir, n)); err == nil && n != "ghost-update-guard" {
			t.Fatalf("%s installed as a system binary", n)
		}
	}
	if read(filepath.Join(p.BinDir, "ghost-update-guard")) != "the guard" {
		t.Fatal("the guard was replaced by the release")
	}
	if _, err := os.Stat(filepath.Join(p.BinDir, "ghost-qr")); err != nil {
		t.Fatal("ghost-qr is a system binary and was not installed")
	}
	if _, err := os.Stat(filepath.Join(p.Tools, "setup.sh")); err != nil {
		t.Fatal("setup.sh not installed with the tools")
	}
	if _, err := os.Stat(filepath.Join(rel.Dir, "bin", "ghost-setup")); err != nil {
		t.Fatal("ghost-setup is not in the unpacked release")
	}
}

func boxAt(t *testing.T) (Paths, string) {
	root := t.TempDir()
	p := Paths{State: filepath.Join(root, "update"), BinDir: filepath.Join(root, "opt/bin"),
		Tools: filepath.Join(root, "opt/tools"), Staging: filepath.Join(root, "staging")}
	cohort := filepath.Join(root, "mnt/bin")
	os.MkdirAll(p.BinDir, 0o755)
	os.MkdirAll(cohort, 0o755)
	os.WriteFile(filepath.Join(p.BinDir, "ghost.secd"), []byte("old secd"), 0o755)
	os.WriteFile(filepath.Join(cohort, "ghost.framed"), []byte("old framed"), 0o755)
	os.WriteFile(filepath.Join(cohort, "ghost.watchd"), []byte("old watchd"), 0o755)
	return p, cohort
}

func read(p string) string { b, _ := os.ReadFile(p); return string(b) }

func TestApplyThenRollBack(t *testing.T) {
	p, cohort := boxAt(t)
	rel, err := Unpack(bundle(t, good("0.9.3")), filepath.Join(p.State, "releases", "0.9.3"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(p, rel, cohort, "0.9.2"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(read(filepath.Join(p.BinDir, "ghost.secd")), "0.9.3") {
		t.Fatal("secd not replaced")
	}
	if !strings.HasSuffix(read(filepath.Join(p.Staging, "ghost.framed")), "0.9.3") {
		t.Fatal("the cohort is not staged for the next unlock")
	}
	if _, err := os.Stat(filepath.Join(p.Staging, "ghost.secd")); err == nil {
		t.Fatal("secd staged with the cohort")
	}
	tr := LoadTrial(p)
	if tr.State != "trial" || tr.Version != "0.9.3" || tr.Prev != "0.9.2" {
		t.Fatalf("trial %+v", tr)
	}
	if err := Rollback(p, "asked"); err != nil {
		t.Fatal(err)
	}
	if read(filepath.Join(p.BinDir, "ghost.secd")) != "old secd" || read(filepath.Join(p.Staging, "ghost.framed")) != "old framed" {
		t.Fatal("the earlier build is not back")
	}
	if tr := LoadTrial(p); tr.State != "rolled_back" || tr.Reason != "asked" {
		t.Fatalf("trial after rollback %+v", tr)
	}
}

func TestGuardRollsBackAQuickCrashLoop(t *testing.T) {
	p, cohort := boxAt(t)
	rel, _ := Unpack(bundle(t, good("0.9.3")), filepath.Join(p.State, "releases", "0.9.3"))
	if err := Apply(p, rel, cohort, "0.9.2"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if rb, err := Guard(p); rb || err != nil {
			t.Fatalf("start %d rolled back: %v", i+1, err)
		}
	}
	// a minute up: the count starts again
	ServedAMinute(p)
	for i := 0; i < 3; i++ {
		if rb, _ := Guard(p); rb {
			t.Fatal("rolled back a build that had stayed up")
		}
	}
	rb, err := Guard(p)
	if !rb || err != nil {
		t.Fatalf("the fourth quick start did not roll back: %v", err)
	}
	if read(filepath.Join(p.BinDir, "ghost.secd")) != "old secd" {
		t.Fatal("secd not put back")
	}
	// nothing on trial: the guard does nothing
	if rb, _ := Guard(p); rb {
		t.Fatal("the guard acted with no trial")
	}
}

func TestNoRollbackWithoutAnEarlierBuild(t *testing.T) {
	p, _ := boxAt(t)
	if err := Rollback(p, "x"); err == nil {
		t.Fatal("rolled back to nothing")
	}
}
