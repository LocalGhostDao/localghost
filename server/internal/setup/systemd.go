package setup

import (
	"fmt"
	"strings"
)

// The daemons that run on a box. ghost.secd is the front door; the rest bind loopback only and sit
// behind it. Setup renders a hardened systemd unit for each.
// GhostDaemons is the full roster of LocalGhost processes. Only ghost.secd gets a systemd unit
// (see SystemdUnits); ghost.secd starts ghost.watchd on the encrypted volume and ghost.watchd
// supervises the rest. Kept here as
// the canonical list and referenced by the installer to know which daemon binaries to place.
var GhostDaemons = []string{
	"ghost.secd",    // front door / trust boundary (this service)
	"ghost.noted",   // notes
	"ghost.framed",  // image -> journal
	"ghost.voiced",  // voice
	"ghost.tallyd",  // tallies
	"ghost.synthd",  // synthesis
	"ghost.cued",    // cues
	"ghost.mistd",   // ...
	"ghost.shadowd", // ...
	"ghost.watchd",  // watch
}

// DaemonConfig is the runtime configuration the ghost.secd unit needs in its ExecStart. The backing
// daemons take no flags; only ghost.secd needs the box identity to issue device certs.
type DaemonConfig struct {
	Host     string // box IP/hostname for device cert issuance
	CaDir    string // /etc/ghost/ca
	StateDir string // /var/lib/ghost
	Disk     string // the raw LUKS data disk ghost.secd mounts on unlock, e.g. /dev/nvme1n1
	Port     int    // mTLS port behind nginx
	RunUser  string // service user watchd runs the cohort as (passed to secd via --user)
}

// SystemdUnits renders a unit per daemon. ghost.secd is the only one that binds a public-facing
// socket (behind nginx); every other daemon is loopback-only and depends on ghost.secd. All run as
// the unprivileged ghost user with filesystem and capability hardening, so a compromised daemon has
// a small blast radius.
//
// DaemonConfig supplies ghost.secd's flags.
// SystemBinDir is where provisioning stages the systemd-launched binary (ghost.secd). It must be
// outside /home: the unit runs with ProtectHome=yes, so a binary under a user's home is invisible to
// the service (exit 203, EXEC). The volume-seeded cohort still comes from the build dir; only the one
// bootstrap binary systemd itself launches needs a hardening-compatible home.
const SystemBinDir = "/opt/localghost/bin"

func SystemdUnits(execDir string, cfg DaemonConfig) []SystemdUnit {
	// Exactly ONE unit: ghost.secd. The ghost.*d daemons live on the encrypted volume and are
	// supervised by ghost.watchd after unlock (internal/watchd), NOT by systemd , a boot
	// unit would try to start them against an unmounted volume and fail.
	return []SystemdUnit{
		{Name: "ghost.secd", Unit: renderUnit("ghost.secd", execDir, cfg)},
	}
}

func renderUnit(name, execDir string, cfg DaemonConfig) string {
	// Only ghost.secd is ever rendered as a unit (the ghost.*d daemons are supervised by secd, not
	// systemd). It waits for the network so nginx can proxy to it once it is up.
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\n")
	fmt.Fprintf(&b, "Description=LocalGhost %s\n", name)
	fmt.Fprintf(&b, "After=network-online.target\n")
	fmt.Fprintf(&b, "Wants=network-online.target\n")

	fmt.Fprintf(&b, "\n[Service]\n")
	fmt.Fprintf(&b, "Type=notify\n")
	// ghost.secd's flags: box identity + state + the raw disk it mounts on unlock. No enrolment env
	// , the QR carries the device cert directly, so there is no pairing code or enroll.env. (This is
	// the only unit now; ghost.secd starts ghost.watchd, which supervises the ghost.*d daemons, not systemd.)
	// secd's flags: state dir + the raw disk it mounts on unlock + listen address. No --host/--ca ,
	// device-cert issuance moved to QR render time (ghost-qr / ghost-setup), so the daemon needs
	// neither the host nor the CA path.
	// secd itself runs as root (it mounts); --user tells it which user watchd should run the COHORT
	// as. Passed through so a box provisioned with --user coder runs the daemons as coder.
	userArg := ""
	if cfg.RunUser != "" {
		userArg = " --user " + cfg.RunUser
	}
	// Log level for secd AND everything it spawns (watchd inherits secd's env, the cohort inherits
	// watchd's). Default info; set GHOST_LOG_LEVEL=debug here (or via a systemd drop-in) to make the
	// whole box verbose without a rebuild. slog drops sub-level lines cheaply, so debug is safe to
	// leave off in production and flip on when a box misbehaves.
	fmt.Fprintf(&b, "Environment=GHOST_LOG_LEVEL=info\n")
	// ExecStart runs from SystemBinDir (not execDir): see the const , ProtectHome=yes below would
	// make a /home path unexecutable. InstallServices stages the binary there.
	fmt.Fprintf(&b, "ExecStart=%s/%s --state %s --disk %s --addr 127.0.0.1:%d%s\n",
		SystemBinDir, name, cfg.StateDir, cfg.Disk, cfg.Port, userArg)
	// ghost.secd runs as ROOT. This is deliberate and unavoidable: it opens dm-crypt (cryptsetup),
	// mounts the encrypted volume (mount needs CAP_SYS_ADMIN), starts Postgres/Redis, and performs
	// crypto-erase. A non-privileged user with NoNewPrivileges literally cannot mount. So the security
	// boundary is NOT "secd is sandboxed" , it is "secd is the single small audited root component
	// behind the appears-down edge, and everything it supervises runs with less". The daemons it
	// spawns drop privileges themselves; secd stays root because its whole job is privileged.
	fmt.Fprintf(&b, "User=root\n")
	fmt.Fprintf(&b, "Restart=on-failure\nRestartSec=2\n")
	// Hardening that does NOT conflict with mounting + supervising: no new privileges beyond root's,
	// no home access, a real /dev (needed for /dev/mapper, loop, the raw disk, and the TPM when
	// present). We deliberately do NOT set ProtectSystem=strict or a DeviceAllow whitelist , the first
	// blocks the mount syscall and /run writes, the second denies /dev/mapper which cryptsetup needs.
	fmt.Fprintf(&b, "ProtectHome=yes\n")
	fmt.Fprintf(&b, "PrivateDevices=no\n")
	fmt.Fprintf(&b, "ProtectKernelTunables=yes\n")
	fmt.Fprintf(&b, "RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX\n")
	fmt.Fprintf(&b, "LockPersonality=yes\n")
	// State dir under /var/lib/ghost. Root-owned; the mount lives at <state>/mnt/slot<N>.
	fmt.Fprintf(&b, "StateDirectory=ghost\n")

	fmt.Fprintf(&b, "\n[Install]\n")
	fmt.Fprintf(&b, "WantedBy=multi-user.target\n")
	return b.String()
}
