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
	// Exactly ONE unit now: ghost.secd. The ghost.*d daemons are supervised by ghost.secd on the
	// encrypted volume, not by systemd, so they get no boot-time units (they cannot start before the
	// volume is mounted at unlock).
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
	// ghost.secd does not depend on itself.
	if strings.Contains(secd.Unit, "Requires=ghost.secd.service") {
		t.Fatal("ghost.secd must not require itself")
	}
	// ghost.secd gets TPM access (it does the unseal) and must NOT have a private /dev (it needs the
	// real disk + dm-crypt to mount).
	if !strings.Contains(secd.Unit, "/dev/tpmrm0") {
		t.Fatal("ghost.secd must have TPM device access")
	}
	if strings.Contains(secd.Unit, "PrivateDevices=yes") {
		t.Fatal("ghost.secd must NOT have PrivateDevices=yes , it needs the real disk and dm-crypt")
	}
}
