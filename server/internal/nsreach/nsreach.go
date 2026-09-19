// Package nsreach finds the unlocked volume from OUTSIDE ghost.secd's mount namespace.
//
// The volume is mounted inside secd's private mount namespace on purpose: the host mount table
// never shows the decrypted drive, and nothing on the host stumbles into it. The cost was paid by
// the operator's tools , from a root shell, /var/lib/ghost/mnt/slot0/run does not exist, so ghost-cli
// could reach the cohort only after an nsenter, and nsenter cannot see /home (ProtectHome), so the
// binary first had to be copied somewhere else. A copy per command, forever.
//
// The kernel already has a door: /proc/<pid>/root is a magic link into that process's root AND its
// mount namespace, for a caller allowed to ptrace it (root). Regular files open through it, and a
// unix socket connects through it like any other path. So when the plain path is missing and
// ghost.secd is running, every tool here simply looks at /proc/<secd>/root/<path> instead. No
// nsenter, no staging, the repo's own binary from wherever it was built. GHOST_NS_AUTO=0 disables.
package nsreach

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Path returns p if it exists here, else /proc/<ghost.secd pid>/root/<p> if THAT exists, else p
// unchanged (so the caller's error names the path the person typed).
func Path(p string) string {
	if _, err := os.Stat(p); err == nil || os.Getenv("GHOST_NS_AUTO") == "0" {
		return p
	}
	for _, pid := range PIDs("ghost.secd") {
		cand := filepath.Join("/proc", strconv.Itoa(pid), "root", p)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return p
}

// Reached reports whether p is a /proc/<pid>/root detour , for tools that want to say so once.
func Reached(p string) bool { return strings.HasPrefix(p, "/proc/") }

// PIDs lists processes whose comm is exactly name (comm is the executable's basename, 15 chars max ,
// "ghost.secd" fits). Scans /proc directly: no pidof, no exec, no PATH dependency.
func PIDs(name string) []int {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range ents {
		pid, perr := strconv.Atoi(e.Name())
		if perr != nil || !e.IsDir() {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		if rerr != nil {
			continue
		}
		if strings.TrimSpace(string(b)) == name {
			out = append(out, pid)
		}
	}
	return out
}
