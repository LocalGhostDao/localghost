package setup

import (
	"strings"
	"testing"
)

// secd's unit forbids core dumps (inherited by everything it starts) and keeps its hardening.
func TestSecdUnitHasNoCoreDumps(t *testing.T) {
	u := SystemdUnits("/opt/localghost/bin", DaemonConfig{StateDir: "/var/lib/ghost", Disk: "/dev/disk/by-id/x", Port: 8443, RunUser: "coder"})
	if len(u) != 1 {
		t.Fatalf("%d units", len(u))
	}
	for _, must := range []string{"LimitCORE=0", "ProtectHome=yes", "User=root", "--addr 127.0.0.1:8443"} {
		if !strings.Contains(u[0].Unit, must) {
			t.Fatalf("unit lacks %q:\n%s", must, u[0].Unit)
		}
	}
}

// A volume that is a file waits for its drive; a disk needs no such line.
func TestSecdUnitWaitsForTheDriveOfAFileVolume(t *testing.T) {
	u := SystemdUnits("/opt/localghost/bin", DaemonConfig{StateDir: "/var/lib/ghost", Disk: "/mnt/data/localghost.img", Port: 8443})
	if !strings.Contains(u[0].Unit, "RequiresMountsFor=/mnt/data\n") || !strings.Contains(u[0].Unit, "--disk /mnt/data/localghost.img") {
		t.Fatalf("unit:\n%s", u[0].Unit)
	}
	u = SystemdUnits("/opt/localghost/bin", DaemonConfig{StateDir: "/var/lib/ghost", Disk: "/dev/disk/by-id/x", Port: 8443})
	if strings.Contains(u[0].Unit, "RequiresMountsFor") {
		t.Fatalf("a disk has no mount to wait for:\n%s", u[0].Unit)
	}
}
