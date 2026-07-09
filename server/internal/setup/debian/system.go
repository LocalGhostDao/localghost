package debian

import (
	"io"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/profile"
	"github.com/LocalGhostDao/localghost/server/internal/setup"
)

// System implements setup.System for bare-metal Debian 13. The PKI is done natively in Go (pki.go);
// the OS operations shell out to the standard Debian tools (sgdisk, cryptsetup, useradd, systemctl,
// nginx). Each Do has a matching exists-Check so the setup plan is idempotent, and the destructive
// disk steps run only in Apply.
//
// Storage model: ONE full-disk LUKS container on the raw disk (no partition table, no decoys, no
// equal-size juggling). The LUKS key is a random full-entropy AMK that is sealed in the TPM bound to
// the main PIN, so unlock requires the PIN (the TPM checks it and applies its DA lockout in hardware)
// and the key never exists outside a TPM unseal. The PIN is chosen at setup, so the AMK can be
// sealed and the disk formatted in one pass.
//
// This is the concrete box backend the orchestration in setup/plan.go drives. It must run as root.
type System struct {
	Disk      string // e.g. /dev/nvme1n1, the RAW disk to LUKS-format whole (DESTRUCTIVE)
	CaDir     string // e.g. /etc/ghost/ca
	Host      string // box IP/hostname for the server cert
	ExecDir   string // where the daemon binaries live, e.g. /usr/local/bin
	StateDir  string // /var/lib/ghost
	TPMDevice string // e.g. /dev/tpmrm0, where the AMK is sealed
	MainPIN   string // chosen at setup; seals the AMK and opens the container
	WipePIN   string // chosen at setup; crypto-erases everything (optional, "" to skip)
	SealMode  string // "tpm" (default) or "software"; which seal tier to provision
	SvcUser   string // service user the cohort runs as (default "ghost"); owns the volume bin/logs/run

	// Confirm asks the operator a yes/no question during setup. It is used by the TPM sole-tenant
	// check: if the box's TPM already holds objects LocalGhost did not create, setting the GLOBAL
	// dictionary-attack policy (and a later `tpm2 clear` during resetup) would affect them too, so
	// we ask before proceeding. Set by ghost-setup to a terminal y/N prompt. If nil, the check
	// fails CLOSED , an unresolved "is this a shared TPM?" aborts rather than silently reconfigures.
	Confirm func(prompt string) (bool, error)

	pki *PKI
}

// NewSystem builds the Debian backend. Construct the PKI from CaDir + Host. tpmDevice is the TPM
// resource-manager node (/dev/tpmrm0); mainPIN seals the AMK; wipePIN (optional) registers the wipe.
func NewSystem(disk, caDir, host, execDir, stateDir, tpmDevice, mainPIN, wipePIN string) *System {
	return &System{
		Disk: disk, CaDir: caDir, Host: host, ExecDir: execDir,
		StateDir: stateDir, TPMDevice: tpmDevice, MainPIN: mainPIN, WipePIN: wipePIN,
		pki: NewPKI(caDir, host),
	}
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func have(name string) bool { _, err := exec.LookPath(name); return err == nil }

// runStdin runs a command feeding `in` on its stdin (used to pass the LUKS key to cryptsetup without
// it ever appearing in argv or on disk).
func runStdin(in []byte, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = bytes.NewReader(in)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// --- disk / container (DESTRUCTIVE) ---
//
// Single full-disk LUKS container on the raw disk. No partition table: cryptsetup luksFormat is
// applied to s.Disk directly, so the whole device is the encrypted container. The key is a random
// AMK sealed in the TPM under the main PIN (see hw.TPMSealedKey). Setup flow:
//   1. generate a random AMK,
//   2. seal it in the TPM bound to MainPIN (so unlock needs the PIN and the TPM rate-limits),
//   3. luksFormat the raw disk with that AMK as the key,
//   4. open it, mkfs.ext4 the mapper, leave it ready.
// The AMK only ever exists in memory here long enough to seal + format, then is zeroised.

const ghostSlot = 0 // the single account

func (s *System) mapperName() string { return "ghost-slot0" }
func (s *System) mapperPath() string { return "/dev/mapper/" + s.mapperName() }

// PartitionsReady reports whether the disk already holds our LUKS container (idempotency check).
func (s *System) PartitionsReady() (bool, error) {
	if s.Disk == "" {
		return false, fmt.Errorf("no disk configured")
	}
	// `cryptsetup isLuks` exits 0 if the device is already a LUKS container.
	return run("cryptsetup", "isLuks", s.Disk) == nil, nil
}

func (s *System) DescribePartitioning() (string, error) {
	return fmt.Sprintf("LUKS-format the WHOLE raw disk %s as one container, key sealed in the TPM "+
		"under the main PIN; THIS ERASES %s", s.Disk, s.Disk), nil
}

// CreatePartitions is a no-op in the raw-disk model: there is no partition table to create, the whole
// disk becomes the LUKS container in FormatContainers. Kept to satisfy the plan's step shape.
func (s *System) CreatePartitions() error {
	if s.Disk == "" {
		return fmt.Errorf("no disk configured")
	}
	return nil
}

// FormatContainers generates the AMK, seals it in the TPM under the main PIN, and LUKS-formats the
// raw disk with it. This is the destructive, one-time provisioning of the encrypted store. The
// The seal step (sealAndFormat, in seal.go) selects the tier at RUNTIME from s.SealMode: TPM-sealed
// AMK, or software (PIN-derived) for machines without a TPM. Not build-tagged.
func (s *System) FormatContainers() error {
	if !have("cryptsetup") {
		return fmt.Errorf("cryptsetup not installed")
	}
	if s.Disk == "" {
		return fmt.Errorf("no disk configured")
	}
	if s.MainPIN == "" {
		return fmt.Errorf("a main PIN is required to seal the AMK")
	}
	return s.sealAndFormat()
}

// formatLUKS does the cryptsetup work given an already-obtained key: luksFormat the raw disk, open,
// mkfs.ext4, close. Shared by the real and (hypothetical) sim paths so the disk logic lives once.
func (s *System) formatLUKS(amk []byte) error {
	// luksFormat the RAW disk with the AMK as the key, fed on stdin (never argv or a file).
	// --keyfile-size=32 makes cryptsetup read EXACTLY 32 bytes: the AMK is random binary and may
	// contain a 0x0A byte, which cryptsetup would otherwise treat as end-of-key and silently
	// truncate, keying the disk with a short key. Fixed length avoids that on both format and open.
	if err := runStdin(amk, "cryptsetup", "luksFormat", "--type", "luks2", "--key-file", "-",
		"--keyfile-size", "32", "--batch-mode", s.Disk); err != nil {
		return fmt.Errorf("luksFormat %s: %w", s.Disk, err)
	}
	if err := runStdin(amk, "cryptsetup", "open", "--key-file", "-", "--keyfile-size", "32",
		s.Disk, s.mapperName()); err != nil {
		return fmt.Errorf("open container: %w", err)
	}
	// mkfs.ext4 -F -q: -F forces creation without prompting even if the mapper presents a detectable
	// filesystem signature (possible when re-provisioning a disk), since prompting would block with no
	// tty; -q suppresses progress output. Safe because provisioning is already explicitly confirmed.
	if err := run("mkfs.ext4", "-F", "-q", s.mapperPath()); err != nil {
		_ = run("cryptsetup", "close", s.mapperName())
		return fmt.Errorf("mkfs.ext4: %w", err)
	}
	// Write services.conf into the fresh volume (ports + generated pg/redis passwords + daemon health
	// ports), the single operational config, crypto-erased with the data. Mapper is already open.
	if err := s.writeServicesConfig(); err != nil {
		_ = run("cryptsetup", "close", s.mapperName())
		return fmt.Errorf("write services.conf: %w", err)
	}
	if err := run("cryptsetup", "close", s.mapperName()); err != nil {
		return fmt.Errorf("close container after format: %w", err)
	}
	return nil
}

// writeServicesConfig mounts the freshly-formatted mapper, writes services.conf, unmounts. The single
// provision-time credential generation , DataStore later reads the same file to start the databases.
func (s *System) writeServicesConfig() error {
	tmp, err := os.MkdirTemp("", "ghost-provision-mnt")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := run("mount", s.mapperPath(), tmp); err != nil {
		return fmt.Errorf("mount for services.conf: %w", err)
	}
	cfg, cerr := hw.DefaultServicesConfig()
	if cerr != nil {
		_ = run("umount", tmp)
		return cerr
	}
	werr := hw.WriteServicesConfig(tmp, cfg)
	if werr == nil {
		werr = s.seedVolume(tmp) // bin/ (daemon binaries), logs/, run/, owned by the service user
	}
	if uerr := run("umount", tmp); uerr != nil && werr == nil {
		werr = fmt.Errorf("umount after volume seed: %w", uerr)
	}
	return werr
}

// seedVolume lays down the on-volume layout the box needs at first unlock: the ghost.*d + ghost.watchd
// binaries under <mount>/bin (watchd execs the cohort from here; a deploy replaces a file here and
// asks watchd to restart it), plus empty logs/ and run/ dirs. Everything is chowned to the service
// user so watchd (running as that user) owns its binaries, logs, and control socket. secd stays root
// on the unencrypted disk; only the volume contents belong to the service user.
//
// The binaries are copied FROM ExecDir (where `make` + install put them, e.g. /usr/local/bin) TO the
// volume. This is the bootstrap: the volume starts empty, so provision seeds the first copy. After
// this, deploys replace <mount>/bin/<name> directly (the release script) , the unencrypted ExecDir
// copies are only used to seed a fresh volume.
func (s *System) seedVolume(mount string) error {
	user := s.SvcUser
	if user == "" {
		user = "ghost"
	}
	binDir := filepath.Join(mount, "bin")
	for _, d := range []string{binDir, filepath.Join(mount, "logs"), filepath.Join(mount, "run"),
		filepath.Join(mount, "conf"), filepath.Join(mount, "ai-models")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return fmt.Errorf("seed dir %s: %w", d, err)
		}
	}
	// The binaries to copy come from the single daemon registry (hw.SeededBinaries), so this list
	// cannot drift from what watchd supervises. secd is NOT here , it lives on the unencrypted disk and
	// is run by systemd, not from the volume. ghost.watchd IS here (seeded but not supervised).
	cohort := hw.SeededBinaries()
	required := hw.RequiredBinaries()
	for _, name := range cohort {
		src := filepath.Join(s.ExecDir, name)
		if _, err := os.Stat(src); err != nil {
			// A missing binary at provision is a real problem (the box could not run that daemon), but
			// not fatal unless it is a required one (the supervisor or a critical daemon , without those
			// the box cannot function). Seed what exists and let the operator install the rest.
			if required[name] {
				return fmt.Errorf("required binary %s not found in %s: %w", name, s.ExecDir, err)
			}
			continue
		}
		dst := filepath.Join(binDir, name)
		if err := copyFile(src, dst, 0o750); err != nil {
			return fmt.Errorf("seed binary %s: %w", name, err)
		}
	}
	// Ownership: the whole volume tree we just created belongs to the service user.
	if err := run("chown", "-R", user+":"+user, binDir,
		filepath.Join(mount, "logs"), filepath.Join(mount, "run"),
		filepath.Join(mount, "conf"), filepath.Join(mount, "ai-models")); err != nil {
		return fmt.Errorf("chown volume seed to %s: %w", user, err)
	}
	return nil
}

// copyFile copies src to dst with the given mode, syncing to disk (this is going onto the encrypted
// volume that is about to be unmounted, so the write must land).
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// --- PIN registry ---

// RegistryReady reports whether the PIN registry already exists (idempotency).
func (s *System) RegistryReady() (bool, error) {
	_, err := os.Stat(filepath.Join(s.StateDir, "registry.blob"))
	return err == nil, nil
}

// WriteRegistry builds the PIN registry , main PIN opens slot 0, wipe PIN (if set) triggers the
// global crypto-erase , and persists it to the state dir where the daemon loads it at startup. The
// registry holds only salted PIN HASHES (never the AMK, never a PIN in the clear); the AMK itself is
// sealed in the TPM by FormatContainers. The two PINs are stored indistinguishably and padded with
// random filler, so the blob never reveals which PIN wipes or that a wipe PIN exists.
func (s *System) WriteRegistry() error {
	if s.MainPIN == "" {
		return fmt.Errorf("a main PIN is required")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("registry salt: %w", err)
	}
	st, err := profile.NewSetup(salt)
	if err != nil {
		return err
	}
	if err := st.SetMain(s.MainPIN); err != nil {
		return fmt.Errorf("register main PIN: %w", err)
	}
	if s.WipePIN != "" {
		if err := st.SetWipe(s.WipePIN); err != nil {
			return fmt.Errorf("register wipe PIN: %w", err)
		}
	}
	reg, err := st.Finalize()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		return err
	}
	return reg.Save(filepath.Join(s.StateDir, "registry.blob"))
}

func (s *System) GhostUserExists() (bool, error) {
	return run("id", "ghost") == nil, nil
}

func (s *System) CreateGhostUser() error {
	return run("useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "ghost")
}

// --- PKI (native) ---

func (s *System) CAExists() (bool, error)            { return s.pki.Exists(), nil }
func (s *System) CreateCA() error                    { return s.pki.CreateCA() }
func (s *System) IssueServerCert() error             { return s.pki.IssueServerCert() }
func (s *System) ServerCertExists() (bool, error)    { return s.pki.ServerCertExists(), nil }
func (s *System) ServerCertFingerprint() (string, error) { return s.pki.ServerFingerprint() }

// --- nginx ---

func (s *System) NginxInstalled() (bool, error) { return have("nginx"), nil }

func (s *System) WriteNginxConfig(conf string) error {
	path := "/etc/nginx/sites-available/ghost-secd"
	if err := os.WriteFile(path, []byte(conf), 0o644); err != nil {
		return err
	}
	link := "/etc/nginx/sites-enabled/ghost-secd"
	_ = os.Remove(link)
	return os.Symlink(path, link)
}

func (s *System) ReloadNginx() error {
	if err := run("nginx", "-t"); err != nil {
		return fmt.Errorf("nginx config test failed: %w", err)
	}
	return run("systemctl", "reload", "nginx")
}

// --- systemd ---

func (s *System) ServicesInstalled() (bool, error) {
	// Three conditions, all required: the unit file exists, the staged binary exists, AND the staged
	// binary matches the freshly built one. The third is what makes re-provisioning after a code fix
	// actually deploy the fix , without it, a stale staged binary reports "done" and the box keeps
	// running yesterday's bug.
	if _, err := os.Stat("/etc/systemd/system/ghost.secd.service"); err != nil {
		return false, nil
	}
	staged := filepath.Join(setup.SystemBinDir, "ghost.secd")
	src := filepath.Join(s.ExecDir, "ghost.secd")
	sh, err := fileSHA256(staged)
	if err != nil {
		return false, nil // not staged (or unreadable) , stage it
	}
	bh, err := fileSHA256(src)
	if err != nil {
		return true, nil // no fresh build to compare against; what is staged stands
	}
	return sh == bh, nil
}

// fileSHA256 hashes a file , used to detect a stale staged binary.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *System) InstallServices(units []setup.SystemdUnit) error {
	// Stage each unit's binary into SystemBinDir , a stable system path the hardened unit can execute
	// (ProtectHome=yes hides /home, so the build dir under the user's home is not usable as ExecStart).
	// Source is s.ExecDir, where make built the binaries as ghost-setup's siblings.
	if err := os.MkdirAll(setup.SystemBinDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", setup.SystemBinDir, err)
	}
	for _, u := range units {
		src := filepath.Join(s.ExecDir, u.Name)
		if _, err := os.Stat(src); err != nil {
			return fmt.Errorf("service binary %s not found in %s (run make box first): %w", u.Name, s.ExecDir, err)
		}
		if err := copyFile(src, filepath.Join(setup.SystemBinDir, u.Name), 0o755); err != nil {
			return fmt.Errorf("stage %s into %s: %w", u.Name, setup.SystemBinDir, err)
		}
	}
	for _, u := range units {
		path := filepath.Join("/etc/systemd/system", u.Name+".service")
		if err := os.WriteFile(path, []byte(u.Unit), 0o644); err != nil {
			return err
		}
	}
	return run("systemctl", "daemon-reload")
}

func (s *System) EnableAndStartServices(names []string) error {
	for _, n := range names {
		svc := n + ".service"
		if err := run("systemctl", "enable", svc); err != nil {
			return err
		}
		if err := run("systemctl", "start", svc); err != nil {
			return err
		}
	}
	return nil
}

// --- TPM ---

func (s *System) TPMUsable() (bool, error) {
	// A usable TPM 2.0 exposes a resource-manager device. Presence is the cheap check; the real
	// seal/unseal is the wipe/auth TPM seam.
	_, err := os.Stat("/dev/tpmrm0")
	return err == nil, nil
}

// --- hygiene ---

func (s *System) ClearSetupArtifacts() error {
	// The QR carried a device key; clear shell history and any rendered QR / temp files.
	_ = os.Remove(filepath.Join(os.Getenv("HOME"), ".bash_history"))
	_ = run("history", "-c") // best-effort; shell builtin may not exist as a binary
	matches, _ := filepath.Glob(filepath.Join(s.StateDir, "setup-qr-*"))
	for _, m := range matches {
		_ = os.Remove(m)
	}
	return nil
}

func (s *System) HardenConsole() error {
	// Make the local console unusable as a bypass: disable autologin getty overrides if present.
	// Best-effort; the operator's console policy is theirs, we just remove an obvious autologin.
	_ = os.Remove("/etc/systemd/system/getty@tty1.service.d/autologin.conf")
	return nil
}
