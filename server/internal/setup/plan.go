package setup

import "fmt"

// System is the set of privileged operations setup performs on the box. It is an interface so the
// orchestration is testable with a fake, while the real implementation shells out to parted/sgdisk,
// mkfs, cryptsetup, useradd, the box PKI, nginx, and systemd. Every "do" has a matching "exists"
// check so steps are idempotent, and a describe for the dry run.
type System interface {
	// Disk and container (DESTRUCTIVE).
	PartitionsReady() (bool, error)
	DescribePartitioning() (string, error) // e.g. "GPT on /dev/nvme0n1: one full-disk LUKS container; erases disk"
	CreatePartitions() error
	FormatContainers() error // the single full-disk encrypted container for the account

	// PIN registry: maps the main PIN to the account and the wipe PIN to the global erase.
	RegistryReady() (bool, error)
	WriteRegistry() error

	// Privileged user the daemons run as.
	GhostUserExists() (bool, error)
	CreateGhostUser() error

	// Box PKI: the box is its OWN CA. It issues its server cert AND the phone's device cert from one
	// CA. The phone pins the box cert (fingerprint travels in the QR) and accepts this CA only , no
	// Let's Encrypt, no public trust, no port 80, no ACME renewal.
	CAExists() (bool, error)
	CreateCA() error           // box CA
	IssueServerCert() error    // box's own https server cert, signed by the box CA
	ServerCertFingerprint() (string, error) // pinned by the phone

	// nginx edge.
	NginxInstalled() (bool, error)
	WriteNginxConfig(conf string) error
	ReloadNginx() error

	// systemd: ghost.secd and the backing daemons as services.
	ServicesInstalled() (bool, error)
	InstallServices(units []SystemdUnit) error
	EnableAndStartServices(names []string) error

	// TPM.
	TPMUsable() (bool, error)

	// Hygiene: the QR delivered a private key; clear it. Console hardened against bypass.
	ClearSetupArtifacts() error
	HardenConsole() error
}

// SystemdUnit is one service to install. ghost.secd plus each ghost.<x>d daemon.
type SystemdUnit struct {
	Name string // e.g. "ghost.secd"
	Unit string // the rendered .service file contents
}

// DefaultPlan is the canonical ghost.secd setup sequence, in order. Destructive disk steps come
// first (and are guarded to Apply only), then identity, the self-CA PKI, nginx, services, TPM, and
// cleanup last. dnsStep is included only with a domain; self-signed certs mean DNS is for
// reachability only, not for cert issuance, so there is no port-80/ACME dependency.
func DefaultPlan(sys System, withDomain bool, dnsCheck func() error, nginxConf string, units []SystemdUnit) *Plan {
	yes := func() (bool, error) { return false, nil } // "always run / verify on setup"
	noDesc := func() (string, error) { return "", nil }

	steps := []Step{
		{
			Name:        "partition disk",
			Destructive: true,
			Check:       sys.PartitionsReady,
			Describe:    sys.DescribePartitioning,
			Do:          sys.CreatePartitions,
		},
		{
			Name:        "format container",
			Destructive: true,
			Check:       sys.PartitionsReady, // formatting is part of the same readiness check
			Describe:    func() (string, error) { return "format one full-disk LUKS container", nil },
			Do:          sys.FormatContainers,
		},
		{
			Name:     "write PIN registry",
			Check:    sys.RegistryReady,
			Describe: func() (string, error) { return "write the PIN registry (main PIN + optional wipe PIN)", nil },
			Do:       sys.WriteRegistry,
		},
		{
			Name:     "ghost user",
			Check:    sys.GhostUserExists,
			Describe: func() (string, error) { return "create unprivileged system user 'ghost'", nil },
			Do:       sys.CreateGhostUser,
		},
		{
			Name:     "box CA (self-signed, no Let's Encrypt)",
			Check:    sys.CAExists,
			Describe: func() (string, error) { return "create the box's own certificate authority", nil },
			Do:       sys.CreateCA,
		},
		{
			Name:     "box server cert",
			Check:    func() (bool, error) { d, err := sys.CAExists(); return d, err },
			Describe: func() (string, error) { return "issue the box https server cert from the box CA", nil },
			Do:       sys.IssueServerCert,
		},
		{
			Name:     "device cert capability",
			Check:    func() (bool, error) { return sys.CAExists() },
			Describe: func() (string, error) {
				return "confirm the box CA can issue device certs; the actual phone cert is minted at " +
					"QR render time (pair.Run -> IssueDeviceCertDER) and carried in the QR, its key " +
					"never written to disk. Nothing to do here beyond the CA existing", nil
			},
			// No-op: the device cert is issued when the QR is rendered, not during the plan. Issuing
			// (and disk-persisting) one here would be a dead cert whose key defeats the DER model.
			Do: func() error { return nil },
		},
		{
			Name:     "nginx installed",
			Check:    sys.NginxInstalled,
			Describe: func() (string, error) { return "verify nginx is installed (not installed for you)", nil },
			Do:       func() error { return ErrNginxMissing },
		},
	}

	if withDomain {
		steps = append(steps, Step{
			Name:     "dns points at box",
			Check:    yes,
			Describe: func() (string, error) { return "verify the domain's A record resolves to this box", nil },
			Do:       dnsCheck,
		})
	}

	steps = append(steps,
		Step{
			Name:     "nginx config (reject unverified clients)",
			Check:    yes,
			Describe: func() (string, error) { return "write nginx config: mTLS, reject any client without a box-issued cert", nil },
			Do:       func() error { return sys.WriteNginxConfig(nginxConf) },
		},
		Step{
			Name:     "nginx reload",
			Check:    yes,
			Describe: func() (string, error) { return "reload nginx", nil },
			Do:       sys.ReloadNginx,
		},
		Step{
			Name:     "install systemd services",
			Check:    sys.ServicesInstalled,
			Describe: func() (string, error) {
				return fmt.Sprintf("install %d systemd unit (ghost.secd only; the ghost.*d daemons are "+
					"supervised by ghost.secd on the encrypted volume, not by systemd)", len(units)), nil
			},
			Do:       func() error { return sys.InstallServices(units) },
		},
		Step{
			Name:     "enable + start services",
			Check:    yes,
			Describe: func() (string, error) { return "enable and start ghost.secd (it supervises the daemons itself)", nil },
			Do:       func() error { return sys.EnableAndStartServices(unitNames(units)) },
		},
		Step{
			// Informational, not a gate. A usable TPM enables the hardware seal tier; its absence is
			// fine , the box provisions with the software tier (--seal software). The seal step is
			// where "tpm mode with no TPM" is actually refused, so this step never hard-fails the run.
			Name:     "tpm check (informational)",
			Check:    sys.TPMUsable, // already-usable => Check true => step is a no-op
			Describe: func() (string, error) {
				return "check for a usable TPM 2.0 (Intel PTT). If absent, provision with --seal " +
					"software , the disk is still encrypted, without the hardware lockout", nil
			},
			Do: func() error { return nil }, // never blocks; tier choice is enforced at the seal step
		},
		Step{
			Name:     "clear setup artifacts (the QR carried a key)",
			Check:    yes,
			Describe: func() (string, error) { return "wipe shell history, temp files, and the rendered QR", nil },
			Do:       sys.ClearSetupArtifacts,
		},
		Step{
			Name:     "harden console",
			Check:    yes,
			Describe: func() (string, error) { return "make the local console unusable as a bypass", nil },
			Do:       sys.HardenConsole,
		},
	)
	_ = noDesc
	return NewPlan(steps...)
}

func unitNames(units []SystemdUnit) []string {
	names := make([]string, len(units))
	for i, u := range units {
		names[i] = u.Name
	}
	return names
}
