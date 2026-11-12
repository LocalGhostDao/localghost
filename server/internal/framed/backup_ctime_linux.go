//go:build linux

package framed

import (
	"os"
	"syscall"
	"time"
)

// inodeChanged is a file's inode change time: when it last became what it is on this disk,
// whatever modification time it carries. Linux names the field Ctim; the other systems name it
// otherwise or not at all, so the read lives here (the tree is vetted for macOS too, for a
// contributor's editor: until 12 November 2026 that step failed on this one line).
func inodeChanged(info os.FileInfo) (time.Time, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(st.Ctim.Sec), int64(st.Ctim.Nsec)), true
}
