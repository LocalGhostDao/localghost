// Package procs looks at other processes through /proc: who holds a mount, and what still runs
// from the volume's own bin after the cohort is gone. No tool needed, no dependency; a process
// that vanishes mid-scan is simply skipped.
package procs

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// KillStrays ends every process whose executable path starts with [prefix] , "<mount>/bin/" for
// the cohort's orphans after watchd confirmed it down, "<mount>/bin/llama-server" for oracled's
// predecessor's child before it spawns its own. Nothing legitimate runs from the volume's bin at
// either moment; what does is a child that outlived its parent (the 60-day-old llama-server that
// survived every halt since the build before Pdeathsig, state R, parent 1, ignoring SIGTERM, and
// then held the volume open for the unmount and the service's cgroup open for a three-minute
// systemctl timeout). SIGTERM first, [grace] later SIGKILL, and each one named in the log with
// its age. Returns the names of what it had to kill.
func KillStrays(prefix string, grace time.Duration) []string {
	self := os.Getpid()
	type stray struct {
		pid  int
		comm string
	}
	var found []stray
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		exe, err := os.Readlink("/proc/" + e.Name() + "/exe")
		if err != nil {
			continue
		}
		exe = strings.TrimSuffix(exe, " (deleted)")
		if !strings.HasPrefix(exe, prefix) {
			continue
		}
		comm := strings.TrimPrefix(exe, prefix)
		if c, err := os.ReadFile("/proc/" + e.Name() + "/comm"); err == nil {
			comm = strings.TrimSpace(string(c))
		}
		found = append(found, stray{pid, comm})
	}
	if len(found) == 0 {
		return nil
	}
	var names []string
	for _, f := range found {
		age := ""
		if st, err := os.Stat("/proc/" + strconv.Itoa(f.pid)); err == nil {
			age = time.Since(st.ModTime()).Round(time.Minute).String()
		}
		slog.Warn("stray process from the volume after the cohort was confirmed down , killing it",
			"fn", "KillStrays", "pid", f.pid, "comm", f.comm, "age", age)
		if p, err := os.FindProcess(f.pid); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
		names = append(names, fmt.Sprintf("%s[%d]", f.comm, f.pid))
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		alive := false
		for _, f := range found {
			if stillRunning(f.pid) {
				alive = true
				break
			}
		}
		if !alive {
			return names
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, f := range found {
		if stillRunning(f.pid) {
			slog.Warn("stray ignored SIGTERM, SIGKILL", "fn", "KillStrays", "pid", f.pid, "comm", f.comm)
			if p, err := os.FindProcess(f.pid); err == nil {
				_ = p.Signal(syscall.SIGKILL)
			}
		}
	}
	return names
}

// stillRunning: the process exists and is not a zombie (dead, waiting for a parent's wait).
func stillRunning(pid int) bool {
	return procState(pid) != "" && procState(pid) != "Z"
}

// procState is the one-letter state from /proc/<pid>/stat ("" when the process is gone).
func procState(pid int) string {
	st, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	// "pid (comm) S ppid ..." , the state is the field after the parenthesised comm.
	if i := strings.LastIndexByte(string(st), ')'); i >= 0 && i+2 < len(st) {
		return string(st[i+2])
	}
	return "?"
}

// HoldersOf names the processes that keep a mount busy: anything whose executable, working
// directory, root, an open descriptor or a MEMORY MAPPING lives under it (the mapping is the one
// `fuser -m` shows and a descriptor scan misses , a model file mmap'd by a dying llama-server).
// Read from /proc, no tool needed; a process that vanishes mid-scan is simply skipped, and the
// caller itself is NOT skipped (secd holding its own volume open would be exactly the bug to
// see). Each is "comm[pid] state" , state D or Z is a corpse the kernel is still clearing, S or R
// is alive.
func HoldersOf(mnt string) string {
	prefix := strings.TrimRight(mnt, "/") + "/"
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return "?"
	}
	var out []string
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a process entry
		}
		dir := "/proc/" + e.Name()
		holds := false
		for _, link := range []string{"exe", "cwd", "root"} {
			if t, err := os.Readlink(dir + "/" + link); err == nil && (strings.HasPrefix(t, prefix) || t == mnt) {
				holds = true
				break
			}
		}
		if !holds {
			if fds, err := os.ReadDir(dir + "/fd"); err == nil {
				for _, fd := range fds {
					if t, err := os.Readlink(dir + "/fd/" + fd.Name()); err == nil && strings.HasPrefix(t, prefix) {
						holds = true
						break
					}
				}
			}
		}
		if !holds {
			if maps, err := os.ReadFile(dir + "/maps"); err == nil && strings.Contains(string(maps), " "+prefix) {
				holds = true
			}
		}
		if !holds {
			continue
		}
		comm := "?"
		if c, err := os.ReadFile(dir + "/comm"); err == nil {
			comm = strings.TrimSpace(string(c))
		}
		state := procState(pid)
		if state == "" {
			state = "?"
		}
		out = append(out, fmt.Sprintf("%s[%d] %s", comm, pid, state))
	}
	if len(out) == 0 {
		return "none found in /proc (a mount held from another namespace, or a lazy reference)"
	}
	return strings.Join(out, ", ")
}
