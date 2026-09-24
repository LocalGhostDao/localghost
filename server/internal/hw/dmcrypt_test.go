package hw

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// After a power cut the disk names can swap; the key picks the volume out of the LUKS containers.
func TestFindByKeyPicksTheOneThatOpens(t *testing.T) {
	tried := []string{}
	open := func(dev string) error {
		tried = append(tried, dev)
		if dev == "/dev/nvme0n1" {
			return nil
		}
		return errors.New("No key available with this passphrase.\nsecond line")
	}
	got, err := findByKey([]string{"/dev/sda2", "/dev/nvme0n1", "/dev/sdb"}, open)
	if err != nil || got != "/dev/nvme0n1" {
		t.Fatalf("got %q %v", got, err)
	}
	if strings.Join(tried, ",") != "/dev/sda2,/dev/nvme0n1" {
		t.Fatalf("tried %v: must stop at the first that opens", tried)
	}
	_, err = findByKey([]string{"/dev/sda2"}, open)
	if err == nil || !strings.Contains(err.Error(), "/dev/sda2: No key available with this passphrase.") || strings.Contains(err.Error(), "second line") {
		t.Fatalf("err = %v", err)
	}
	if _, err := findByKey(nil, open); err == nil || !strings.Contains(err.Error(), "no LUKS container") {
		t.Fatalf("no candidates: %v", err)
	}
}

func TestStableNamePrefersWholeDiskWWN(t *testing.T) {
	dir := t.TempDir()
	dev := filepath.Join(dir, "nvme0n1")
	other := filepath.Join(dir, "nvme1n1")
	os.WriteFile(dev, nil, 0o600)
	os.WriteFile(other, nil, 0o600)
	ids := filepath.Join(dir, "by-id")
	os.Mkdir(ids, 0o755)
	os.Symlink(dev, filepath.Join(ids, "nvme-Samsung_SSD_990_PRO_2TB_S7XXX"))
	os.Symlink(dev, filepath.Join(ids, "nvme-eui.0025385b31234567"))
	os.Symlink(dev, filepath.Join(ids, "nvme-eui.0025385b31234567-part1"))
	os.Symlink(other, filepath.Join(ids, "nvme-WD_BLACK_SN850X"))
	if got := stableName(dev, ids); got != filepath.Join(ids, "nvme-eui.0025385b31234567") {
		t.Fatalf("stable = %s", got)
	}
	if got := stableName(filepath.Join(dir, "nothing"), ids); got != filepath.Join(dir, "nothing") {
		t.Fatalf("no link: %s", got)
	}
}

func TestPreenVerdict(t *testing.T) {
	if err := preenVerdict("/dev/mapper/ghost-slot0", 0, "clean", 0); err != nil {
		t.Fatal(err)
	}
	for _, c := range []int{1, 2, 3} {
		if err := preenVerdict("/dev/mapper/ghost-slot0", c, "Clearing orphaned inode\nFILE SYSTEM WAS MODIFIED", 0); err != nil {
			t.Fatalf("code %d is a repaired filesystem, not a failure: %v", c, err)
		}
	}
	err := preenVerdict("/dev/mapper/ghost-slot0", 4, "a\nb\nUNEXPECTED INCONSISTENCY; RUN fsck MANUALLY.\n\t(i.e., without -a or -p options)", 0)
	if err == nil || !strings.Contains(err.Error(), "sudo e2fsck -f /dev/mapper/ghost-slot0") || !strings.Contains(err.Error(), "NOT mounted") || !strings.Contains(err.Error(), "RUN fsck MANUALLY") {
		t.Fatalf("code 4 = %v", err)
	}
	if err := preenVerdict("/dev/x", 8, "", 0); err == nil {
		t.Fatal("an operational error is a failure")
	}
}
