package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A set kept for every release taken, RELEASE.txt read for what the phone shows, the oldest off
// the shelf past its size, the running one never.
func TestShelf(t *testing.T) {
	p, _ := boxAt(t)
	take := func(version, build string, when time.Time) {
		rel, err := Unpack(bundle(t, good(version)), filepath.Join(p.State, "releases", "unpacking"))
		if err != nil {
			t.Fatal(err)
		}
		final := filepath.Join(p.State, "releases", version)
		if err := os.Rename(rel.Dir, final); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(filepath.Join(final, "VERSION"), when, when)
		in := filepath.Join(p.State, "incoming")
		os.MkdirAll(filepath.Join(in, build, "server"), 0o700)
		os.WriteFile(filepath.Join(in, "MANIFEST.txt"), []byte("# LocalGhost Mirror Manifest\n# Build: "+build+"\n"), 0o600)
		os.WriteFile(filepath.Join(in, "MANIFEST.txt.asc"), []byte("sig"), 0o600)
		os.WriteFile(filepath.Join(in, build, "server", "RELEASE.txt"),
			[]byte("version="+version+"\nname=wisp\ncommit=deadbeef\ndate=2026-10-04T17:12:00Z\ngo=1.27.1\nbundle=x.tar.gz\nchanges:\n  fix one thing\n  add another\n"), 0o600)
		if err := Keep(p, version, in); err != nil {
			t.Fatal(err)
		}
	}
	day := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i, v := range []string{"0.9.1", "0.9.2", "0.9.3", "0.9.4", "0.9.5"} {
		take(v, "2026100"+string(rune('1'+i))+"T100000Z", day.Add(time.Duration(i)*24*time.Hour))
	}
	// an unpacked release with no VERSION of its own, and a stray directory, are not releases
	os.MkdirAll(filepath.Join(p.State, "releases", "unpacking"), 0o755)
	os.MkdirAll(filepath.Join(p.State, "releases", "junk"), 0o755)
	sh := Shelf(p)
	if len(sh) != 5 || sh[0].Version != "0.9.5" || sh[4].Version != "0.9.1" {
		t.Fatalf("shelf %+v", sh)
	}
	k := sh[0]
	if k.Name != "wisp" || k.Commit != "deadbeef" || k.Date != "2026-10-04T17:12:00Z" || k.Go != "1.27.1" || k.Changes != 2 || !k.Set || k.At != day.Add(4*24*time.Hour).Unix() {
		t.Fatalf("kept %+v", k)
	}
	if _, err := os.Stat(filepath.Join(p.State, "incoming")); err == nil {
		t.Fatal("the upload directory should have moved")
	}
	// the running release is the oldest: it stays, the oldest of the others goes
	if dropped := Prune(p, "0.9.1"); len(dropped) != 1 || dropped[0] != "0.9.2" {
		t.Fatalf("dropped %v", dropped)
	}
	sh = Shelf(p)
	if len(sh) != 4 || sh[3].Version != "0.9.1" {
		t.Fatalf("after the prune %+v", sh)
	}
	if dropped := Prune(p, "0.9.5"); len(dropped) != 0 {
		t.Fatalf("a shelf at its size drops nothing: %v", dropped)
	}
	// a set without RELEASE.txt still counts, with the bundle's facts
	os.Remove(filepath.Join(SetDir(p, "0.9.4"), "20261004T100000Z", "server", "RELEASE.txt"))
	for _, k := range Shelf(p) {
		if k.Version == "0.9.4" && (k.Name != "" || k.Commit != "abc123" || !k.Set) {
			t.Fatalf("without RELEASE.txt %+v", k)
		}
	}
	kv := ReleaseText("version=0.0.4\nname=wisp\nchanges:\n  a=b\n")
	if kv["version"] != "0.0.4" || kv["name"] != "wisp" || kv["a"] != "" {
		t.Fatalf("%v", kv)
	}
}
