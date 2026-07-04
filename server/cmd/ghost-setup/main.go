// ghost-setup provisions the box: it runs the setup plan (partition, CA, certs, nginx, systemd),
// then renders the enrolment QR and prints the one-time pairing code to start ghost.secd with.
//
// Flow:
//	ghost-setup --disk /dev/nvme0n1 --host 192.168.1.50 --plan      # dry run, shows what it will do
//	ghost-setup --disk /dev/nvme0n1 --host 192.168.1.50 --apply     # provisions, then prints QR+code
//
// After --apply it prints the exact command to launch the daemon with enrolment armed.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"

	"github.com/LocalGhostDao/localghost/server/internal/pair"
	"github.com/LocalGhostDao/localghost/server/internal/setup"
	"github.com/LocalGhostDao/localghost/server/internal/setup/debian"
)

// promptPIN reads a PIN from the terminal WITHOUT echoing it, so it never appears on screen, in
// scrollback, or (being read from the tty, not argv) in process listings or shell history. Arbitrary
// length is allowed: the PIN is hashed into the TPM authValue, so length does not matter and is not
// enforced.
func promptPIN(label string) (string, error) {
	fmt.Print(label)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// prompt asks a question and returns the trimmed line. If the user just hits enter, def is returned.
func prompt(question, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", question, def)
	} else {
		fmt.Printf("%s: ", question)
	}
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// confirm requires the user to type the exact word "yes" (anything else aborts). Used before the
// destructive step so a stray keypress never wipes a disk.
func confirm(question string) bool {
	fmt.Printf("%s Type 'yes' to proceed: ", question)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line) == "yes"
}

// disk is one candidate disk shown in the picker.
type diskInfo struct {
	path  string // /dev/nvme1n1
	size  string // human size, e.g. 7.3T
	model string // model string or fstype hint
	inUse string // non-empty = mounted/partitioned warning to show
}

// listDisks discovers whole disks via lsblk and flags which are mounted or hold partitions, so the
// picker can steer you away from a disk that is in use. It deliberately lists only whole disks (TYPE
// disk), not partitions. showAll includes in-use disks; by default they are still listed but marked,
// so you can SEE everything but the dangerous ones are obvious.
func listDisks() ([]diskInfo, error) {
	// -d: no partitions, -n: no header, -o: columns, -b not needed (human size is fine for display)
	out, err := exec.Command("lsblk", "-dno", "NAME,SIZE,TYPE,MODEL").Output()
	if err != nil {
		return nil, err
	}
	var disks []diskInfo
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(ln)
		if len(f) < 3 || f[2] != "disk" {
			continue
		}
		name := f[0]
		path := "/dev/" + name
		model := ""
		if len(f) >= 4 {
			model = strings.Join(f[3:], " ")
		}
		d := diskInfo{path: path, size: f[1], model: model}
		// is anything from this disk mounted, or does it hold partitions with filesystems?
		if mp, _ := exec.Command("lsblk", "-no", "MOUNTPOINTS", path).Output(); len(strings.TrimSpace(string(mp))) > 0 {
			d.inUse = "MOUNTED / IN USE"
		} else if pt, _ := exec.Command("lsblk", "-no", "FSTYPE", path).Output(); len(strings.TrimSpace(string(pt))) > 0 {
			d.inUse = "has a partition table / filesystem"
		}
		disks = append(disks, d)
	}
	return disks, nil
}

// pickDisk shows the disks and returns the chosen path. It refuses to pre-select an in-use disk: if
// the user picks one that is in use, it requires an extra explicit confirmation, because picking the
// wrong disk on this box (blockchain nodes, live DBs) is the one truly catastrophic mistake.
func pickDisk() (string, error) {
	disks, err := listDisks()
	if err != nil {
		return "", fmt.Errorf("could not list disks: %w", err)
	}
	if len(disks) == 0 {
		return "", fmt.Errorf("no disks found")
	}
	fmt.Println("\nAvailable disks:")
	for i, d := range disks {
		warn := ""
		if d.inUse != "" {
			warn = "   <-- " + d.inUse + " (do NOT pick unless you are SURE)"
		}
		fmt.Printf("  %d) %-16s %-7s %s%s\n", i+1, d.path, d.size, d.model, warn)
	}
	for {
		choice := prompt("\nPick the disk to provision by number", "")
		var idx int
		if _, err := fmt.Sscanf(choice, "%d", &idx); err != nil || idx < 1 || idx > len(disks) {
			fmt.Println("  please enter a valid number from the list")
			continue
		}
		d := disks[idx-1]
		if d.inUse != "" {
			fmt.Printf("\n  WARNING: %s is %s.\n", d.path, d.inUse)
			fmt.Println("  Provisioning ERASES IT COMPLETELY. On this box that could destroy a node or database.")
			if !confirm(fmt.Sprintf("  Are you absolutely sure you want to ERASE %s?", d.path)) {
				fmt.Println("  ok, pick another.")
				continue
			}
		}
		return d.path, nil
	}
}

func main() {
	disk := flag.String("disk", "", "disk to provision, e.g. /dev/nvme0n1 (DESTRUCTIVE, whole disk)")
	host := flag.String("host", "", "box LAN IP/hostname the phone connects to")
	domain := flag.String("domain", "", "optional public domain (omit for the zero-server QR default)")
	caDir := flag.String("ca", "/etc/ghost/ca", "box CA + cert directory")
	execDir := flag.String("exec", "/usr/local/bin", "where the daemon binaries are installed")
	stateDir := flag.String("state", "/var/lib/ghost", "unencrypted state dir")
	tpmDevice := flag.String("tpm", "/dev/tpmrm0", "TPM resource-manager device (seals the disk key)")
	sealMode := flag.String("seal", "tpm", "seal tier: 'tpm' (hardware-sealed key, default) or 'software' (PIN-derived key, no hardware lockout , for machines without a TPM)")
	port := flag.Int("port", 8443, "mTLS port ghost.secd serves behind nginx")
	apply := flag.Bool("apply", false, "actually provision (default is a dry run)")
	flag.Parse()

	// Resolve disk and host. If either is missing, drop into the interactive wizard (pick the disk
	// from a list, type the host), so you never have to know or type a device path. Flags still work
	// for scripted/non-interactive use.
	diskVal, hostVal, domainVal := *disk, *host, *domain
	interactive := diskVal == "" || hostVal == ""
	if interactive {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Fprintln(os.Stderr, "ghost-setup: --disk and --host are required when not run interactively")
			os.Exit(2)
		}
		fmt.Println("LocalGhost box setup. Answer a few questions; nothing is changed until you confirm.")
		if diskVal == "" {
			d, err := pickDisk()
			if err != nil {
				fmt.Fprintln(os.Stderr, "disk selection failed:", err)
				os.Exit(1)
			}
			diskVal = d
		}
		if hostVal == "" {
			def, _ := os.Hostname()
			hostVal = prompt("Hostname or IP the phone connects to", def)
		}
		if domainVal == "" {
			domainVal = prompt("Public domain (optional, blank for the QR-only default)", "")
		}
	}
	if hostVal == "" {
		fmt.Fprintln(os.Stderr, "ghost-setup: a host is required")
		os.Exit(2)
	}

	// PINs are prompted, never flags: a PIN on the command line would leak via argv, ps, and shell
	// history. We only need them when actually provisioning (--apply); the dry run shows the plan
	// without touching the disk or the TPM, so it needs no PIN.
	var mainPIN, wipePIN string
	if *apply {
		var err error
		mainPIN, err = promptPIN("Choose the MAIN PIN (unlocks the box). Use a strong passphrase: ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "could not read PIN:", err)
			os.Exit(1)
		}
		again, err := promptPIN("Re-enter the MAIN PIN: ")
		if err != nil || again != mainPIN {
			fmt.Fprintln(os.Stderr, "main PINs did not match; nothing was changed.")
			os.Exit(1)
		}
		wipePIN, err = promptPIN("Choose the WIPE PIN (erases everything). Make it NOTHING like the main PIN. Leave blank to skip: ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "could not read wipe PIN:", err)
			os.Exit(1)
		}
		if wipePIN != "" {
			if wipePIN == mainPIN {
				fmt.Fprintln(os.Stderr, "the wipe PIN must differ from the main PIN; nothing was changed.")
				os.Exit(1)
			}
			againW, err := promptPIN("Re-enter the WIPE PIN: ")
			if err != nil || againW != wipePIN {
				fmt.Fprintln(os.Stderr, "wipe PINs did not match; nothing was changed.")
				os.Exit(1)
			}
		}
	}

	sys := debian.NewSystem(diskVal, *caDir, hostVal, *execDir, *stateDir, *tpmDevice, mainPIN, wipePIN)
	sys.SealMode = *sealMode

	// nginx config + systemd units the plan installs.
	ghostSecdAddr := fmt.Sprintf("127.0.0.1:%d", *port)
	var nginxConf string
	withDomain := domainVal != ""
	if withDomain {
		nginxConf = setup.DomainConfig{Domain: domainVal}.NginxConfig(ghostSecdAddr)
	}
	units := setup.SystemdUnits(*execDir, setup.DaemonConfig{
		Host: hostVal, CaDir: *caDir, StateDir: *stateDir, Disk: diskVal, Port: *port,
	})

	plan := setup.DefaultPlan(sys, withDomain, nil, nginxConf, units)

	planned, err := plan.DryRun()
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup precondition failed:", err)
		os.Exit(1)
	}
	fmt.Println("Setup plan:")
	for _, p := range planned {
		mark := " "
		if p.Destructive {
			mark = "!"
		}
		line := p.Action
		if p.Skip {
			line = "already satisfied , will skip"
		}
		if p.Problem != nil {
			line = "PRECONDITION: " + p.Problem.Error()
		}
		fmt.Printf("  [%s] %s , %s\n", mark, p.Name, line)
	}
	fmt.Println()

	if !*apply {
		fmt.Println("This was a dry run. Re-run with --apply to provision. Steps marked [!] are destructive.")
		return
	}

	// Guard against re-provisioning. If the disk is ALREADY a LUKS container, the format + seal steps
	// would skip (idempotency), leaving the TPM AMK and registry bound to the PIN from the FIRST
	// provisioning. A user re-running with a new PIN would be told "success" but the new PIN would not
	// work , only the original would, a silent lockout. So refuse clearly: re-provisioning means
	// wiping the disk first (destroys all data); changing the PIN is a separate operation.
	if *apply {
		if already, _ := sys.PartitionsReady(); already {
			fmt.Fprintf(os.Stderr, "\n%s is already a LUKS container , this disk looks provisioned.\n", diskVal)
			fmt.Fprintln(os.Stderr, "ghost-setup will not re-provision it (that would silently keep the original PIN).")
			fmt.Fprintln(os.Stderr, "To start over (DESTROYS ALL DATA): wipe the disk first, e.g.")
			fmt.Fprintf(os.Stderr, "    cryptsetup erase %s && wipefs -a %s\n", diskVal, diskVal)
			fmt.Fprintln(os.Stderr, "then run ghost-setup again. To change the PIN, use the resetup console command.")
			os.Exit(1)
		}
	}

	// Final gate before anything destructive runs. The user has seen the plan (with [!] marks) and the
	// disk they chose; require an explicit "yes" so a stray keypress never erases a disk.
	fmt.Printf("\nThis will ERASE %s and provision LocalGhost on it.\n", diskVal)
	if !confirm("This cannot be undone.") {
		fmt.Println("Aborted. Nothing was changed.")
		return
	}

	results, err := plan.Apply(planned)
	for _, r := range results {
		status := "ok"
		switch r.Status {
		case setup.Failed:
			status = "FAILED"
			if r.Err != nil {
				status += ": " + r.Err.Error()
			}
		case setup.AlreadyDone:
			status = "skipped (already done)"
		}
		fmt.Printf("  %s , %s\n", r.Name, status)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "\nsetup stopped at the first failure above.")
		os.Exit(1)
	}

	// Provisioned. Render the enrolment QR , it CARRIES the device identity, so scanning it enrols
	// the phone with no code and no network exchange. The box mints a device cert+key via the PKI,
	// embeds them in the QR, and keeps no copy of the key.
	fmt.Println("\nBox provisioned. Enrol your phone by scanning the QR below:")
	pki := debian.NewPKI(*caDir, hostVal)
	if err := pair.Run(os.Stdout, pair.Options{
		Host:        hostVal,
		Port:        *port,
		CertPath:    *caDir + "/box-server.pem",
		BoxName:     hostVal,
		IssueDevice: pki.IssueDeviceCertDER,
	}, pair.EncodeQR); err != nil {
		fmt.Fprintln(os.Stderr, "could not render enrolment QR:", err)
		os.Exit(1)
	}
	fmt.Println("\nThe QR carries a one-time device identity. Scan it with the LocalGhost app now.")
	fmt.Println("To enrol another device, re-run: ghost-qr --ca", *caDir, "--host", hostVal)
}
