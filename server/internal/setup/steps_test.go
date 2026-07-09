package setup

import (
	"strings"
	"testing"
)

func mkStep(name string, destructive, already, problem, fail bool, touched *[]string) Step {
	return Step{
		Name:        name,
		Destructive: destructive,
		Check:       func() (bool, error) { if problem { return false, ErrStepFailed }; return already, nil },
		Describe:    func() (string, error) { return "would " + name, nil },
		Do:          func() error { *touched = append(*touched, name); if fail { return ErrStepFailed }; return nil },
	}
}

func TestDryRunTouchesNothing(t *testing.T) {
	var touched []string
	p := NewPlan(
		mkStep("partition", true, false, false, false, &touched),
		mkStep("ghost user", false, false, false, false, &touched),
	)
	planned, err := p.DryRun()
	if err != nil {
		t.Fatalf("clean dry run: %v", err)
	}
	if len(touched) != 0 {
		t.Fatal("dry run must not call any Do")
	}
	if len(planned) != 2 || planned[0].Action == "" {
		t.Fatal("dry run must describe each step")
	}
}

func TestApplyAfterCleanDryRun(t *testing.T) {
	var touched []string
	p := NewPlan(
		mkStep("partition", true, false, false, false, &touched),
		mkStep("ghost user", false, false, false, false, &touched),
	)
	dry, _ := p.DryRun()
	if _, err := p.Apply(dry); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(touched) != 2 {
		t.Fatalf("apply must run every step: %v", touched)
	}
}

func TestDirtyDryRunBlocksApply(t *testing.T) {
	var touched []string
	p := NewPlan(mkStep("partition", true, false, true, false, &touched)) // Check errors => problem
	dry, err := p.DryRun()
	if err != ErrDryRunDirty {
		t.Fatalf("a problem must make the dry run dirty, got %v", err)
	}
	if _, aerr := p.Apply(dry); aerr == nil {
		t.Fatal("apply must refuse a dirty dry run")
	}
	if len(touched) != 0 {
		t.Fatal("nothing must be applied after a dirty dry run")
	}
}

func TestApplyStopsAtFirstFailure(t *testing.T) {
	var touched []string
	p := NewPlan(
		mkStep("box CA", false, false, false, true, &touched), // Do fails
		mkStep("nginx", false, false, false, false, &touched),
	)
	dry, _ := p.DryRun()
	res, err := p.Apply(dry)
	if err == nil {
		t.Fatal("a failing step must stop apply")
	}
	if len(touched) != 1 {
		t.Fatal("steps after a failure must not run")
	}
	if res[len(res)-1].Status != Failed {
		t.Fatal("the failing step must be Failed")
	}
}

func TestSystemdUnitsHardenedAndOrdered(t *testing.T) {
	units := SystemdUnits("/usr/local/bin", DaemonConfig{Host: "192.168.1.50", CaDir: "/etc/ghost/ca", StateDir: "/var/lib/ghost", Port: 8443})
	// Exactly ONE unit now: ghost.secd. ghost.secd starts ghost.watchd on the encrypted volume, and
	// ghost.watchd supervises the ghost.*d daemons , not systemd , so they get no boot-time units (they
	// cannot start before the volume is mounted at unlock).
	if len(units) != 1 {
		t.Fatalf("expected exactly one systemd unit (ghost.secd), got %d", len(units))
	}
	secd := units[0]
	if secd.Name != "ghost.secd" {
		t.Fatalf("the one unit must be ghost.secd, got %s", secd.Name)
	}
	// It runs as ghost and is hardened.
	if !strings.Contains(secd.Unit, "User=root") {
		t.Fatal("ghost.secd must run as root , it mounts dm-crypt and needs CAP_SYS_ADMIN")
	}
	// ExecStart must live under SystemBinDir, NOT the build/exec dir: the unit sets ProtectHome=yes,
	// so a /home path would be invisible to the service (exit 203). This is the regression guard for
	// exactly that bug.
	if !strings.Contains(secd.Unit, "ExecStart="+SystemBinDir+"/ghost.secd") {
		t.Fatalf("ExecStart must run from %s (ProtectHome=yes hides /home), unit:\n%s", SystemBinDir, secd.Unit)
	}
	if !strings.Contains(secd.Unit, "ProtectHome=yes") {
		t.Fatal("ghost.secd unit must keep ProtectHome=yes")
	}
	// ghost.secd does not depend on itself.
	if strings.Contains(secd.Unit, "Requires=ghost.secd.service") {
		t.Fatal("ghost.secd must not require itself")
	}
	// ghost.secd gets TPM access (it does the unseal) and must NOT have a private /dev (it needs the
	// real disk + dm-crypt to mount).
	// ghost.secd reaches the TPM (it does the unseal) and the real disk + dm-crypt to mount. Both come
	// from a real /dev, i.e. NOT a private/sandboxed device namespace , the unit deliberately avoids a
	// DeviceAllow whitelist (which would deny /dev/mapper) and instead sets PrivateDevices=no. So the
	// correct assertion is the absence of device sandboxing, not the presence of a specific device line.
	if !strings.Contains(secd.Unit, "PrivateDevices=no") {
		t.Fatal("ghost.secd must have a real /dev (PrivateDevices=no) for the TPM, raw disk, and dm-crypt")
	}
	if strings.Contains(secd.Unit, "PrivateDevices=yes") {
		t.Fatal("ghost.secd must NOT have PrivateDevices=yes , it needs the real disk and dm-crypt")
	}
}
