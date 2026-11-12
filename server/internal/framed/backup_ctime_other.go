//go:build !linux

package framed

import (
	"os"
	"time"
)

// inodeChanged has nothing to give off Linux: a box is Linux, and anywhere else (a contributor's
// Mac) a file's modification time stands alone.
func inodeChanged(os.FileInfo) (time.Time, bool) { return time.Time{}, false }
