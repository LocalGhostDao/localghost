package update

// THE SHELF. The mirror publishes one server release, the newest, and keeps no earlier one the
// phone could offer. So the box keeps its own: every signed set the phone handed over goes, once it
// verified and went on, to releases/<version>/set (the manifest, its signature, the build folder as
// uploaded), beside the unpacked bundle. An earlier release goes back on from there the way a new
// one goes on from the phone: the set is verified again against the pinned site key (the manifest
// is older than the newest the box has used, which the verifier is told to allow for this one
// run), unpacked again, put on, and tried. Nothing on the shelf is trusted for having been there.
//
// The shelf holds the last few releases taken (keepReleases) and never drops the one running.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// keepReleases is how many releases the shelf holds, the running one among them.
const keepReleases = 4

// Kept is one release on the shelf.
type Kept struct {
	Version string `json:"version"`
	Name    string `json:"name,omitempty"`   // the release's name (RELEASE.txt), "" for none
	Commit  string `json:"commit,omitempty"` // the commit it was built from
	Date    string `json:"date,omitempty"`   // when it was built, RFC 3339 UTC (the commit's time: a release is reproducible)
	Go      string `json:"go,omitempty"`     // the Go that built it
	Changes int    `json:"changes"`          // lines of CHANGES.txt
	At      int64  `json:"at"`               // when the box took it, unix seconds
	Set     bool   `json:"set"`              // the signed set is kept: it can go back on
}

// SetDir is where a kept release's signed set lives.
func SetDir(p Paths, version string) string {
	return filepath.Join(p.State, "releases", version, "set")
}

// Shelf lists the releases the box holds, newest taken first.
func Shelf(p Paths) []Kept {
	ents, _ := os.ReadDir(filepath.Join(p.State, "releases"))
	var out []Kept
	for _, e := range ents {
		v := e.Name()
		if !e.IsDir() || v == "unpacking" || !safeName(v) {
			continue
		}
		dir := filepath.Join(p.State, "releases", v)
		vb, err := os.ReadFile(filepath.Join(dir, "VERSION"))
		if err != nil || strings.TrimSpace(string(vb)) != v {
			continue
		}
		k := Kept{Version: v}
		if st, err := os.Stat(filepath.Join(dir, "VERSION")); err == nil {
			k.At = st.ModTime().Unix()
		}
		if cb, err := os.ReadFile(filepath.Join(dir, "COMMIT")); err == nil {
			k.Commit = strings.TrimSpace(string(cb))
		}
		if ch, err := os.ReadFile(filepath.Join(dir, "CHANGES.txt")); err == nil {
			for _, l := range strings.Split(string(ch), "\n") {
				if strings.TrimSpace(l) != "" {
					k.Changes++
				}
			}
		}
		if _, err := os.Stat(filepath.Join(SetDir(p, v), "MANIFEST.txt.asc")); err == nil {
			k.Set = true
			if notes, _ := filepath.Glob(filepath.Join(SetDir(p, v), "*", "server", "RELEASE.txt")); len(notes) == 1 {
				if b, err := os.ReadFile(notes[0]); err == nil {
					kv := ReleaseText(string(b))
					if kv["version"] == v {
						k.Name, k.Date, k.Go = kv["name"], kv["date"], kv["go"]
						if kv["commit"] != "" {
							k.Commit = kv["commit"]
						}
					}
				}
			}
		}
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].At != out[j].At {
			return out[i].At > out[j].At
		}
		return out[i].Version > out[j].Version
	})
	return out
}

// ReleaseText reads RELEASE.txt's "key=value" lines (version, name, commit, date, go, bundle,
// since); the "changes:" block is left to its readers.
func ReleaseText(s string) map[string]string {
	kv := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, " ") || strings.TrimSpace(l) == "changes:" {
			continue
		}
		if i := strings.IndexByte(l, '='); i > 0 {
			kv[strings.TrimSpace(l[:i])] = strings.TrimSpace(l[i+1:])
		}
	}
	return kv
}

// Keep moves the set at from (the upload as the phone made it: MANIFEST.txt, MANIFEST.txt.asc,
// <build>/server/...) onto the shelf as version's. A set already there for the version is replaced.
func Keep(p Paths, version, from string) error {
	dst := SetDir(p, version)
	if from == dst {
		return nil
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Rename(from, dst)
}

// Prune drops the oldest releases past keepReleases, never running's. The names dropped come back.
func Prune(p Paths, running string) []string {
	var dropped []string
	kept := 0
	for _, k := range Shelf(p) {
		if k.Version == running {
			continue
		}
		kept++
		if kept < keepReleases {
			continue
		}
		if os.RemoveAll(filepath.Join(p.State, "releases", k.Version)) == nil {
			dropped = append(dropped, k.Version)
		}
	}
	return dropped
}
