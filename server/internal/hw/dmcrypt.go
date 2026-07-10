package hw

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
func (m *DMCryptMounter) diskPath(slot int) string  { return m.disk }
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
	// luksOpen reading the key from stdin. --keyfile-size=32 reads EXACTLY 32 bytes: the AMK is
	// random binary and may contain a 0x0A byte that cryptsetup would otherwise treat as end-of-key.
	// This MUST match the keyfile-size used at luksFormat (setup), or the key would differ.
	open := exec.Command("cryptsetup", "luksOpen", "--key-file", "-", "--keyfile-size", "32", m.diskPath(slot), mapper)
	open.Stdin = strings.NewReader(string(key))
	if out, err := open.CombinedOutput(); err != nil {
		return "", fmt.Errorf("luksOpen slot %d: %v: %s", slot, err, strings.TrimSpace(string(out)))
	}
	return m.ensureMounted(slot)
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
	_ = os.Chmod(parent, 0o711) // ensure traversable even if it pre-existed as 0700
	if err := os.MkdirAll(mnt, 0o700); err != nil {
		return "", err
	}
	// Mounted already?
	if isMountpoint(mnt) {
		return mnt, nil
	}
	mount := exec.Command("mount", m.mapperPath(slot), mnt)
	if out, err := mount.CombinedOutput(); err != nil {
		return "", fmt.Errorf("mount slot %d: %v: %s", slot, err, strings.TrimSpace(string(out)))
	}
	return mnt, nil
}

// Unmount unmounts the filesystem and closes the LUKS mapping, so the key is no longer resident.
func (m *DMCryptMounter) Unmount(slot int) error {
	mnt := m.mountPath(slot)
	if isMountpoint(mnt) {
		if out, err := exec.Command("umount", mnt).CombinedOutput(); err != nil {
			return fmt.Errorf("umount slot %d: %v: %s", slot, err, strings.TrimSpace(string(out)))
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

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}