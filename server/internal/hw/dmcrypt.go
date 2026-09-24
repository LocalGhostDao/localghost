package hw

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/procs"
)

// DMCryptMounter implements container.Mounter using dm-crypt (LUKS) via cryptsetup. The account's
// container is the whole raw disk (single-account model), LUKS-formatted at setup with the AMK. The
// key that opens it is the TPM-unsealed account master key (NOT the PIN directly , the PIN unseals
// the key, the key opens the volume). So mounting needs the unsealed key, which is why the unlock
// flow does TPM unseal THEN mount.
//
// Layout: the LUKS container is the raw disk (e.g. /dev/nvme1n1), mapped to /dev/mapper/ghost-slot0,
// mounted at <stateDir>/mnt/slot0. There is one account (slot 0); the slot parameter is kept for the
// container.Mounter interface and is always 0 here.
//
// NOT validated in CI (needs root + cryptsetup + a real disk). Built against the real cryptsetup CLI;
// exercise on the box.

type DMCryptMounter struct {
	stateDir string
	disk     string // the raw LUKS-formatted disk, e.g. /dev/nvme1n1
	// keyFor returns the TPM-unsealed master key for a slot. Wired to the TPM SealedKey per slot.
	keyFor func(slot int, pin string) ([]byte, error)
}

func NewDMCryptMounter(stateDir, disk string, keyFor func(slot int, pin string) ([]byte, error)) *DMCryptMounter {
	return &DMCryptMounter{stateDir: stateDir, disk: disk, keyFor: keyFor}
}

// diskPath is the LUKS container backing the slot. Single-account: it is the raw disk regardless of
// slot.
func (m *DMCryptMounter) diskPath(slot int) string   { return m.disk }
func (m *DMCryptMounter) mapperName(slot int) string { return fmt.Sprintf("ghost-slot%d", slot) }
func (m *DMCryptMounter) mapperPath(slot int) string { return "/dev/mapper/" + m.mapperName(slot) }
func (m *DMCryptMounter) mountPath(slot int) string {
	return filepath.Join(m.stateDir, "mnt", fmt.Sprintf("slot%d", slot))
}

// MountPath is the public accessor the datastore uses to find a slot's mounted volume.
func (m *DMCryptMounter) MountPath(slot int) string { return m.mountPath(slot) }

// Mount unseals the account key (caller passes the PIN), opens the LUKS volume with it, and mounts
// the filesystem. The key is passed to cryptsetup via stdin (a key file descriptor), never on the
// command line, and zeroised after.
func (m *DMCryptMounter) Mount(slot int, pin string) (string, error) {
	key, err := m.keyFor(slot, pin)
	if err != nil {
		return "", fmt.Errorf("unseal key for slot %d: %w", slot, err)
	}
	defer zero(key)
	return m.MapWithKey(slot, key)
}

// MapWithKey opens the LUKS volume with an already-unsealed key and mounts the filesystem. This lets
// the unlock flow unseal the key once (the Unseal stage) and reuse it here, rather than unsealing
// twice. The caller owns and zeroises key.
func (m *DMCryptMounter) MapWithKey(slot int, key []byte) (string, error) {
	mapper := m.mapperName(slot)
	// Already open? cryptsetup status returns 0 if active.
	if exec.Command("cryptsetup", "status", mapper).Run() == nil {
		return m.ensureMounted(slot)
	}
	disk := m.diskPath(slot)
	// THE DISK MAY HAVE MOVED. NVMe (and SATA) names are handed out in probe order, and probe order
	// is not stable across boots: after a power cut /dev/nvme1n1 can be the OS disk and the volume
	// /dev/nvme0n1. When the configured path is not a LUKS container, every LUKS container on the
	// box is tried with the key , a luksOpen with the wrong key changes nothing and fails, and the
	// AMK is random and unique, so the one that opens IS the volume. The journal then names the
	// stable /dev/disk/by-id path to put in the unit, so it never depends on luck again.
	if !isLuks(disk) {
		cands := luksDevices()
		slog.Warn("configured disk is not a LUKS container , the disk names may have moved at boot; trying every LUKS container with the key",
			"fn", "MapWithKey", "configured", disk, "candidates", strings.Join(cands, " "))
		found, err := findByKey(cands, func(dev string) error { return luksOpen(dev, mapper, key) })
		if err != nil {
			return "", fmt.Errorf("luksOpen slot %d: %s is not a LUKS container (disk names move between boots) and no LUKS container on the box opens with this key (%v); lsblk -o NAME,SIZE,FSTYPE shows the disks, and --disk in the ghost.secd unit should be a /dev/disk/by-id path", slot, disk, err)
		}
		slog.Warn("found the volume at a different name: point --disk in the ghost.secd unit at the stable name so the next boot does not have to search",
			"fn", "MapWithKey", "configured", disk, "found", found, "stable", stableName(found, "/dev/disk/by-id"))
		return m.ensureMounted(slot)
	}
	if err := luksOpen(disk, mapper, key); err != nil {
		return "", fmt.Errorf("luksOpen slot %d: %w", slot, err)
	}
	return m.ensureMounted(slot)
}

// luksOpen maps dev as mapper with the key on stdin. --keyfile-size=32 reads EXACTLY 32 bytes: the
// AMK is random binary and may contain a 0x0A byte that cryptsetup would otherwise treat as the end
// of the key. This MUST match the keyfile-size used at luksFormat (setup), or the key would differ.
func luksOpen(dev, mapper string, key []byte) error {
	open := exec.Command("cryptsetup", "luksOpen", "--key-file", "-", "--keyfile-size", "32", dev, mapper)
	open.Stdin = strings.NewReader(string(key))
	if out, err := open.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func isLuks(dev string) bool { return exec.Command("cryptsetup", "isLuks", dev).Run() == nil }

// luksDevices is every block device blkid reports as a LUKS container.
func luksDevices() []string {
	out, err := exec.Command("blkid", "-t", "TYPE=crypto_LUKS", "-o", "device").Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

// findByKey tries open on each candidate in turn and returns the first that takes, or the errors.
func findByKey(cands []string, open func(dev string) error) (string, error) {
	if len(cands) == 0 {
		return "", fmt.Errorf("blkid lists no LUKS container at all")
	}
	var errs []string
	for _, dev := range cands {
		if err := open(dev); err != nil {
			errs = append(errs, dev+": "+firstLine(err.Error()))
			continue
		}
		return dev, nil
	}
	return "", fmt.Errorf("%s", strings.Join(errs, "; "))
}

// StableDiskName is the /dev/disk/by-id name for a disk path like /dev/nvme0n1 (unchanged when it
// already is one, or when no stable link exists). Setup writes this into the ghost.secd unit so the
// volume never depends on the order the kernel probed the disks in.
func StableDiskName(dev string) string {
	if strings.HasPrefix(dev, "/dev/disk/") {
		return dev
	}
	return stableName(dev, "/dev/disk/by-id")
}

// stableName is the /dev/disk/by-id link that resolves to dev (whole disks only, not -partN; a wwn-
// or eui. name preferred, as those follow the device across ports and controllers), or dev itself.
func stableName(dev, dir string) string {
	real, err := filepath.EvalSymlinks(dev)
	if err != nil {
		real = dev
	}
	es, err := os.ReadDir(dir)
	if err != nil {
		return dev
	}
	best := ""
	for _, e := range es {
		name := e.Name()
		if strings.Contains(name, "-part") {
			continue
		}
		target, err := filepath.EvalSymlinks(filepath.Join(dir, name))
		if err != nil || target != real {
			continue
		}
		if best == "" || (strings.HasPrefix(name, "wwn-") || strings.Contains(name, "eui.")) && !(strings.HasPrefix(best, "wwn-") || strings.Contains(best, "eui.")) {
			best = name
		}
	}
	if best == "" {
		return dev
	}
	return filepath.Join(dir, best)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// preen checks the filesystem on the open mapping before it is mounted, the way the boot does for
// the OS disk and nothing did for this one. After an unclean shutdown ext4 either replays its
// journal (a second) or has an error flagged that a mount tolerates and resize2fs refuses; e2fsck -p
// fixes what is safe to fix without asking and says when a person must look. Only ext2/3/4.
func preen(dev string) error {
	fsType, _ := exec.Command("blkid", "-o", "value", "-s", "TYPE", dev).Output()
	switch strings.TrimSpace(string(fsType)) {
	case "ext2", "ext3", "ext4":
	default:
		return nil
	}
	t0 := time.Now()
	slog.Info("checking the filesystem before mount (seconds when clean; minutes after an unclean shutdown that left errors)", "fn", "preen", "dev", dev)
	out, err := exec.Command("e2fsck", "-p", dev).CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		return fmt.Errorf("e2fsck did not run: %w", err)
	}
	return preenVerdict(dev, code, strings.TrimSpace(string(out)), time.Since(t0))
}

// preenVerdict reads e2fsck's exit bits: 0 clean, 1 errors corrected, 2 corrected and a reboot
// advised (meaningful for the root filesystem only), 4 and up a person must run it.
func preenVerdict(dev string, code int, out string, took time.Duration) error {
	switch {
	case code == 0:
		slog.Info("filesystem clean", "fn", "preen", "dev", dev, "took", took.Round(time.Millisecond).String())
		return nil
	case code&^3 == 0:
		slog.Warn("filesystem repaired after an unclean shutdown", "fn", "preen", "dev", dev, "code", code, "took", took.Round(time.Millisecond).String(), "e2fsck", lastLines(out, 4))
		return nil
	}
	return fmt.Errorf("the filesystem needs a check e2fsck will not make on its own (exit %d): the volume is unlocked but NOT mounted, so from the host run  sudo e2fsck -f %s  , answer its questions, then unlock again. e2fsck said: %s", code, dev, lastLines(out, 3))
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, " | ")
}

// IsMounted reports whether the slot's filesystem is currently mounted (a warm account).
func (m *DMCryptMounter) IsMounted(slot int) bool { return isMountpoint(m.mountPath(slot)) }

func (m *DMCryptMounter) ensureMounted(slot int) (string, error) {
	mnt := m.mountPath(slot)
	// The intermediate mnt/ dir must be TRAVERSABLE by the unprivileged service user , Postgres/Redis
	// run dropped to that user and have to path through mnt/ to reach their data on the volume. 0711
	// (traverse, no list) lets them pass without exposing what slots exist. The slotN dir and the
	// volume root inside it are chowned to the run user by the DB layer after mount.
	parent := filepath.Dir(mnt)
	if err := os.MkdirAll(parent, 0o711); err != nil {
		return "", err
	}
	if err := os.Chmod(parent, 0o711); err != nil { // traversable even if it pre-existed as 0700
		return "", fmt.Errorf("chmod %s traversable: %w", parent, err)
	}
	if err := os.MkdirAll(mnt, 0o700); err != nil {
		return "", err
	}
	// Mounted already?
	if isMountpoint(mnt) {
		return mnt, nil
	}
	if err := preen(m.mapperPath(slot)); err != nil {
		return "", fmt.Errorf("mount slot %d: %w", slot, err)
	}
	mount := exec.Command("mount", m.mapperPath(slot), mnt)
	if out, err := mount.CombinedOutput(); err != nil {
		return "", fmt.Errorf("mount slot %d: %v: %s", slot, err, strings.TrimSpace(string(out)))
	}
	return mnt, nil
}

// Unmount unmounts the filesystem and closes the LUKS mapping, so the key is no longer resident.
//
// A busy mount is waited for, up to [unmountPatience], naming what holds it. The cohort is
// confirmed dead before this runs, but a daemon's CHILD is not the cohort: llama-server, killed
// with oracled, can spend tens of seconds in the kernel releasing VRAM and the pages the CUDA
// driver pinned, and until it is fully gone its mmap of the model file holds the volume. The old
// single umount returned "target is busy" at once, the halt errored, the LUKS mapping stayed open
// with the key resident, and the next unlock found the mounted-but-dead state it now knows how
// to repair. Repairable is not good: wait for the corpse, say who it is, then unmount for real.
func (m *DMCryptMounter) Unmount(slot int) error {
	mnt := m.mountPath(slot)
	if isMountpoint(mnt) {
		t0 := time.Now()
		var lastLog time.Time
		for {
			out, err := exec.Command("umount", mnt).CombinedOutput()
			if err == nil {
				if !lastLog.IsZero() {
					slog.Info("umount succeeded after wait", "fn", "Unmount", "slot", slot, "ms", time.Since(t0).Milliseconds())
				}
				break
			}
			msg := strings.TrimSpace(string(out))
			if !strings.Contains(strings.ToLower(msg), "busy") || time.Since(t0) > unmountPatience {
				return fmt.Errorf("umount slot %d: %v: %s (after %s; holders: %s)", slot, err, msg,
					time.Since(t0).Round(time.Second), procs.HoldersOf(mnt))
			}
			// A holder that has SIGKILL pending and is still running is stuck in the kernel; no
			// amount of waiting frees the mount. Say it now, name it, and stop , the lock stays
			// partial until the box reboots, and the log says exactly that.
			if pid, who := procs.UnkillableHolder(mnt); pid > 0 {
				return fmt.Errorf("umount slot %d: target held by %s, which survives SIGKILL (stuck inside the kernel, GPU driver?) , the volume cannot be fully locked until the driver gives it back (tools/unwedge.sh) or the box reboots", slot, who)
			}
			if time.Since(lastLog) >= 5*time.Second {
				slog.Warn("umount busy, waiting", "fn", "Unmount", "slot", slot, "waitedMs", time.Since(t0).Milliseconds(), "holders", procs.HoldersOf(mnt))
				lastLog = time.Now()
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	mapper := m.mapperName(slot)
	if exec.Command("cryptsetup", "status", mapper).Run() == nil {
		if out, err := exec.Command("cryptsetup", "luksClose", mapper).CombinedOutput(); err != nil {
			return fmt.Errorf("luksClose slot %d: %v: %s", slot, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// ResizeToFill grows the account's filesystem to fill its container, with the account's own key
// already applied (the volume is open). Per-account and key-independent.
func (m *DMCryptMounter) ResizeToFill(slot int) error {
	// resize2fs on the open mapper device extends ext4 to the device size, so the filesystem uses
	// the full container (e.g. after the backing device was enlarged).
	if out, err := exec.Command("resize2fs", m.mapperPath(slot)).CombinedOutput(); err != nil {
		return fmt.Errorf("resize slot %d: %v: %s", slot, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func isMountpoint(path string) bool {
	return exec.Command("mountpoint", "-q", path).Run() == nil
}

// unmountPatience is how long a busy unmount is waited for. A SIGKILLed llama-server has been
// seen to take 44s to leave the process table; a minute covers that with room, and past it the
// error names the holder so nobody has to guess.
const unmountPatience = 75 * time.Second

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
