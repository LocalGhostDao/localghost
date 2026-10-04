package setup

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// THE VOLUME AS A FILE. A box's volume is one LUKS container, and cryptsetup is as happy to format
// and open a regular file as a whole disk (it attaches a loop device itself). So the volume can be a
// file on any drive that is mounted at boot (a second disk with a filesystem already on it, a NAS
// share is not: the key never goes over a network), allocated in full so it never runs the drive
// out of room under the box's feet. The seal, the PINs and the wipe are the same: the file is
// ciphertext, and without the key it stays that way. What differs is small and listed here.

// IsImage reports whether a --disk value names a file rather than a block device: anything not
// under /dev/. A block device given by a path outside /dev (a bind, a symlink elsewhere) is not
// supported; the unit wants the real name.
func IsImage(disk string) bool {
	return disk != "" && !strings.HasPrefix(filepath.Clean(disk), "/dev/")
}

// ParseSize reads a size like "500G", "1.5T" or "750000M" (powers of 1024; a bare number is bytes)
// into bytes. The smallest volume accepted is 20 GiB: the databases, the engine and the models
// alone are above ten, and a volume that fills on its first day is a support call.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, fmt.Errorf("no size given (--size 500G)")
	}
	// 500G, 500GB, 500GiB: the unit letter, with or without a B or an iB after it
	num := strings.TrimSuffix(s, "B")
	num = strings.TrimSuffix(num, "I")
	mult := int64(1)
	if num != "" {
		switch num[len(num)-1] {
		case 'K':
			mult = 1 << 10
		case 'M':
			mult = 1 << 20
		case 'G':
			mult = 1 << 30
		case 'T':
			mult = 1 << 40
		}
	}
	if mult != 1 {
		num = num[:len(num)-1]
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("%q is not a size (500G, 1.5T)", s)
	}
	n := int64(f * float64(mult))
	if n < 20<<30 {
		return 0, fmt.Errorf("%s is too small for a volume: 20G at least (the databases, the engine and the models take ten)", s)
	}
	return n, nil
}

// SizeText writes bytes the way people read them ("500 GB", "1.5 TB"), in powers of 1024.
func SizeText(n int64) string {
	switch {
	case n >= 1<<40:
		return trimFloat(float64(n)/(1<<40)) + " TB"
	case n >= 1<<30:
		return trimFloat(float64(n)/(1<<30)) + " GB"
	default:
		return trimFloat(float64(n)/(1<<20)) + " MB"
	}
}

func trimFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}
