// Package update puts a new server release on the box and takes it off again.
//
// THE FLOW. A release is built from a tag (tools/release_build.sh), published to the mirror as the
// set "server" and signed with the site key like every other set. The phone checks the mirror once
// a day; when the person taps DEPLOY it downloads the set and hands it to the box. secd checks the
// signature and every hash with the mirror's own verifier and the key the box already has
// (mirror_fetch.sh over a file:// copy), then this package:
//
//  1. unpacks the bundle into its own directory, refusing anything but binaries under bin/, tools
//     under tools/ and the notes at the top (VERSION, COMMIT, CHANGES.txt; NOTES.md and install.sh
//     are let through too, for a bundle that carries them);
//  2. keeps what runs now (secd, the tools, the cohort from the volume) in prev/;
//  3. installs the new: secd and the command-line tools over /opt/localghost/bin (a rename, so the
//     running secd keeps its old file), the cohort into the staging directory (secd ingests it at
//     the next unlock, before the daemons start), the tools into /opt/localghost/tools. The setup
//     binaries a bundle carries since 0.0.5 (ghost-setup, the tile cutters, the TPM repair tool) are
//     left in the unpacked release, and ghost-update-guard is never touched: what undoes a bad
//     release is not replaced by one;
//  4. opens a TRIAL (trial.json). secd then locks and restarts onto the new build.
//
// THE TRIAL ENDS ONE OF TWO WAYS. Confirmed: the first unlock after it completes and the daemons
// stay up for ten minutes. Rolled back (prev/ put back, secd restarted onto it):
//   - the new secd does not stay up: ghost-update-guard runs before every start of the unit and
//     counts starts; the new secd resets the count once it has served a minute, so a third quick
//     start in a row puts the old build back before systemd starts it;
//   - the first unlock fails past checking the PIN, or a critical daemon keeps dying in its first
//     ten minutes (secd does these itself);
//   - the person asks, from the phone.
//
// Everything here is on the OS disk: binaries and fingerprints, nothing of the person's.
package update

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Paths is where things live. Defaults for a box: [BoxPaths].
type Paths struct {
	State   string // /var/lib/ghost/update: incoming/, verified/, releases/, prev/, trial.json
	BinDir  string // /opt/localghost/bin: ghost.secd, ghost-cli, ghost-ctl
	Tools   string // /opt/localghost/tools: mirror_fetch.sh and the key it pins
	Staging string // /var/lib/ghost/staging/bin: the cohort, ingested at the next unlock
}

// BoxPaths are the paths on a box.
func BoxPaths() Paths {
	return Paths{
		State:   "/var/lib/ghost/update",
		BinDir:  "/opt/localghost/bin",
		Tools:   "/opt/localghost/tools",
		Staging: "/var/lib/ghost/staging/bin",
	}
}

// systemBins are the binaries that live in BinDir; every other bin/ file is the cohort, except
// setupBins, which stay in the unpacked release (they are for setting a box up, not for running one,
// and the guard is the one thing a release never replaces).
var systemBins = map[string]bool{"ghost.secd": true, "ghost-cli": true, "ghost-ctl": true, "ghost-qr": true}

var setupBins = map[string]bool{"ghost-setup": true, "ghost-update-guard": true, "ghost-landtiles": true, "ghost-roadtiles": true, "ghost-tpmreset": true}

// maxBundleFile bounds one file in a bundle (a Go binary is tens of MB).
const maxBundleFile = 256 << 20

// Release is an unpacked bundle.
type Release struct {
	Version string   `json:"version"`
	Commit  string   `json:"commit"`
	Changes []string `json:"changes"`
	Dir     string   `json:"dir"`
	Bins    []string `json:"bins"`
	Tools   []string `json:"tools"`
}

// Unpack reads a release bundle (tar.gz) into dest and checks what is in it.
func Unpack(bundle, dest string) (Release, error) {
	var rel Release
	f, err := os.Open(bundle)
	if err != nil {
		return rel, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return rel, fmt.Errorf("bundle is not gzip: %w", err)
	}
	if err := os.RemoveAll(dest); err != nil {
		return rel, err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return rel, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return rel, fmt.Errorf("bundle: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return rel, fmt.Errorf("bundle: %s is not a plain file", h.Name)
		}
		dir, base := path.Split(name)
		ok := false
		switch dir {
		case "":
			ok = base == "VERSION" || base == "COMMIT" || base == "CHANGES.txt" || base == "NOTES.md" || base == "install.sh"
		case "bin/":
			ok = safeName(base)
		case "tools/":
			ok = safeName(base)
		}
		if !ok || strings.Contains(name, "..") {
			return rel, fmt.Errorf("bundle: %s has no place in a release", h.Name)
		}
		if h.Size > maxBundleFile {
			return rel, fmt.Errorf("bundle: %s is %d MB", h.Name, h.Size>>20)
		}
		out := filepath.Join(dest, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return rel, err
		}
		mode := os.FileMode(0o644)
		if dir == "bin/" || strings.HasSuffix(base, ".sh") {
			mode = 0o755
		}
		w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return rel, err
		}
		_, cerr := io.Copy(w, io.LimitReader(tr, maxBundleFile))
		if err := w.Close(); cerr == nil {
			cerr = err
		}
		if cerr != nil {
			return rel, cerr
		}
		switch dir {
		case "bin/":
			if !isELF(out) {
				return rel, fmt.Errorf("bundle: bin/%s is not a Linux binary", base)
			}
			rel.Bins = append(rel.Bins, base)
		case "tools/":
			rel.Tools = append(rel.Tools, base)
		}
	}
	read := func(n string) string {
		b, _ := os.ReadFile(filepath.Join(dest, n))
		return strings.TrimSpace(string(b))
	}
	rel.Version, rel.Commit, rel.Dir = read("VERSION"), read("COMMIT"), dest
	for _, l := range strings.Split(read("CHANGES.txt"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			rel.Changes = append(rel.Changes, l)
		}
	}
	sort.Strings(rel.Bins)
	sort.Strings(rel.Tools)
	if rel.Version == "" || !safeName(rel.Version) {
		return rel, errors.New("bundle: no VERSION")
	}
	if !contains(rel.Bins, "ghost.secd") || !contains(rel.Bins, "ghost.watchd") {
		return rel, errors.New("bundle: a release carries ghost.secd and ghost.watchd at least")
	}
	return rel, nil
}

func safeName(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 100 {
		return false
	}
	for _, c := range s {
		if !(c == '.' || c == '-' || c == '_' || c == '+' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func isELF(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	var h [4]byte
	_, err = io.ReadFull(f, h[:])
	return err == nil && string(h[:]) == "\x7fELF"
}

// Trial is the state of the release last put on: on trial, confirmed, or rolled back.
type Trial struct {
	Version    string `json:"version"`
	Prev       string `json:"prev"`
	State      string `json:"state"` // trial | confirmed | rolled_back
	At         int64  `json:"at"`
	Starts     int    `json:"starts"`     // unit starts since the new build went on, reset once it has served a minute
	UnlockedAt int64  `json:"unlockedAt"` // the first unlock onto it
	Reason     string `json:"reason,omitempty"`
}

func trialPath(p Paths) string { return filepath.Join(p.State, "trial.json") }

// LoadTrial reads trial.json; a zero Trial when there is none.
func LoadTrial(p Paths) Trial {
	var t Trial
	if b, err := os.ReadFile(trialPath(p)); err == nil {
		_ = json.Unmarshal(b, &t)
	}
	return t
}

// SaveTrial writes trial.json (0600, replaced whole).
func SaveTrial(p Paths, t Trial) error {
	if err := os.MkdirAll(p.State, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(t, "", "  ")
	tmp := trialPath(p) + ".part"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, trialPath(p))
}

// Apply puts rel on: what runs now into prev/, the new files in place, a trial opened. cohortDir
// is the volume's bin/ (the cohort that runs now), "" when the box is locked (then prev keeps the
// last cohort it saved, or none). current names the version running now.
func Apply(p Paths, rel Release, cohortDir, current string) error {
	prev := filepath.Join(p.State, "prev")
	if err := os.RemoveAll(prev); err != nil {
		return err
	}
	for _, d := range []string{filepath.Join(prev, "bin"), filepath.Join(prev, "cohort"), filepath.Join(prev, "tools"), p.BinDir, p.Tools, p.Staging} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	// 1. what runs now
	for b := range systemBins {
		if err := copyIfExists(filepath.Join(p.BinDir, b), filepath.Join(prev, "bin", b), 0o755); err != nil {
			return fmt.Errorf("keep %s: %w", b, err)
		}
	}
	if cohortDir != "" {
		ents, _ := os.ReadDir(cohortDir)
		for _, e := range ents {
			n := e.Name()
			if e.Type().IsRegular() && safeName(n) && !strings.HasSuffix(n, ".ingest") && !strings.HasSuffix(n, ".prev") {
				if err := copyIfExists(filepath.Join(cohortDir, n), filepath.Join(prev, "cohort", n), 0o755); err != nil {
					return fmt.Errorf("keep %s: %w", n, err)
				}
			}
		}
	}
	for _, t := range rel.Tools {
		if err := copyIfExists(filepath.Join(p.Tools, t), filepath.Join(prev, "tools", t), 0o644); err != nil {
			return fmt.Errorf("keep tools/%s: %w", t, err)
		}
	}
	_ = os.WriteFile(filepath.Join(prev, "VERSION"), []byte(current+"\n"), 0o644)
	// 2. the new
	for _, b := range rel.Bins {
		if setupBins[b] {
			continue
		}
		src := filepath.Join(rel.Dir, "bin", b)
		dst := filepath.Join(p.Staging, b)
		if systemBins[b] {
			dst = filepath.Join(p.BinDir, b)
		}
		if err := installFile(src, dst, 0o755); err != nil {
			return fmt.Errorf("install %s: %w", b, err)
		}
	}
	for _, t := range rel.Tools {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(t, ".sh") {
			mode = 0o755
		}
		if err := installFile(filepath.Join(rel.Dir, "tools", t), filepath.Join(p.Tools, t), mode); err != nil {
			return fmt.Errorf("install tools/%s: %w", t, err)
		}
	}
	return SaveTrial(p, Trial{Version: rel.Version, Prev: current, State: "trial", At: time.Now().Unix()})
}

// Rollback puts prev/ back: secd and the tools in place, the cohort staged for the next unlock.
func Rollback(p Paths, reason string) error {
	prev := filepath.Join(p.State, "prev")
	if _, err := os.Stat(filepath.Join(prev, "bin", "ghost.secd")); err != nil {
		return errors.New("there is no earlier build kept to go back to")
	}
	if err := os.MkdirAll(p.Staging, 0o755); err != nil {
		return err
	}
	for _, sub := range []struct {
		dir, dst string
		mode     os.FileMode
	}{{"bin", p.BinDir, 0o755}, {"cohort", p.Staging, 0o755}, {"tools", p.Tools, 0o644}} {
		ents, _ := os.ReadDir(filepath.Join(prev, sub.dir))
		for _, e := range ents {
			mode := sub.mode
			if strings.HasSuffix(e.Name(), ".sh") {
				mode = 0o755
			}
			if err := installFile(filepath.Join(prev, sub.dir, e.Name()), filepath.Join(sub.dst, e.Name()), mode); err != nil {
				return fmt.Errorf("put back %s/%s: %w", sub.dir, e.Name(), err)
			}
		}
	}
	t := LoadTrial(p)
	t.State, t.Reason = "rolled_back", reason
	return SaveTrial(p, t)
}

// Guard is ghost-update-guard's work, before every start of ghost.secd: on trial, one more start;
// the third quick one in a row puts the earlier build back. True when it rolled back.
func Guard(p Paths) (bool, error) {
	t := LoadTrial(p)
	if t.State != "trial" {
		return false, nil
	}
	t.Starts++
	if t.Starts <= maxQuickStarts {
		return false, SaveTrial(p, t)
	}
	return true, Rollback(p, fmt.Sprintf("the new ghost.secd did not stay up (%d starts in a row)", t.Starts))
}

// maxQuickStarts: starts of the new build without it serving a minute, before the guard rolls back.
const maxQuickStarts = 3

// ServedAMinute is secd's word that the new build is up: the guard's count starts again.
func ServedAMinute(p Paths) {
	t := LoadTrial(p)
	if t.State == "trial" && t.Starts != 0 {
		t.Starts = 0
		_ = SaveTrial(p, t)
	}
}

func copyIfExists(src, dst string, mode os.FileMode) error {
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return installFile(src, dst, mode)
}

// installFile copies src over dst by a rename, so a running binary keeps its old file.
func installFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Chmod(tmp, mode)
	return os.Rename(tmp, dst)
}
