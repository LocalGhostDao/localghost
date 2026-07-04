// ghostctl is a command-line LocalGhost client. Its first job is enrolment: given an enroll link
// (the same string the box's QR carries), it saves the device identity the link itself delivers.
// The link IS the credential , the box generated the device cert + key and put them in the link
// (raw DER, base64url), so enrolment is local: parse, save, done. No network call, no pairing-code
// exchange; a session token is issued later, at first PIN unlock, exactly as on the phone. It is
// the test client for ghost.secd before the phone is wired.
//
//	ghostctl enroll "localghost://enroll?v=1&host=...&port=...&fp=...&name=...&cert=...&key=..."
package main

import (
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/term"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/pair"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "enroll":
		if len(os.Args) < 3 {
			usage()
		}
		link, err := pair.Parse(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, "bad link:", err)
			os.Exit(1)
		}
		fmt.Printf("box      %s:%d\n", link.Host, link.Port)
		fmt.Printf("identity %s\n", link.Fingerprint)
		if err := enroll(link); err != nil {
			fmt.Fprintln(os.Stderr, "enrol failed:", err)
			os.Exit(1)
		}
	case "migrate-to-tpm":
		migrate(os.Args[2:], hw.SealModeTPM)
	case "migrate-to-software":
		migrate(os.Args[2:], hw.SealModeSoftware)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: ghostctl <command>")
	fmt.Fprintln(os.Stderr, "  enroll <enroll-link>          save the device identity carried in the QR link")
	fmt.Fprintln(os.Stderr, "  migrate-to-tpm [flags]        re-wrap the disk key from the software tier into the TPM")
	fmt.Fprintln(os.Stderr, "  migrate-to-software [flags]   re-wrap the disk key from the TPM into the software tier")
	fmt.Fprintln(os.Stderr, "migration never touches the disk , the LUKS key is re-wrapped, not changed")
	os.Exit(2)
}

// migrate re-wraps the AMK into the target tier, following the crash-safe order documented on
// hw.ReWrap: re-wrap + verify (both wrappings valid), flip the mode (commit), destroy the old
// wrapping. Run ON the box, as a user that can read/write seal.env (and reach /dev/tpmrm0 for the
// tpm direction). The box should be LOCKED while migrating , the daemon re-reads the mode per unseal,
// but changing tiers under a live mount is asking for a confused operator story.
func migrate(args []string, target string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	stateDir := fs.String("state", "/var/lib/ghost", "state dir holding seal.env")
	tpmDevice := fs.String("tpm-device", "/dev/tpmrm0", "TPM resource-manager device")
	_ = fs.Parse(args)

	store := hw.NewEnvSealStore(filepath.Join(*stateDir, "seal.env"))
	mode, err := store.Mode()
	if err != nil {
		fatal("read seal.env: %v", err)
	}
	if mode == target {
		fatal("box is already on the %s tier , nothing to migrate", target)
	}
	if mode == "" {
		fatal("box is not provisioned (no seal mode in seal.env); run ghost-setup first")
	}

	cur, err := hw.SelectSealer(mode, *tpmDevice, store, ghostSlot)
	if err != nil {
		fatal("current tier: %v", err)
	}
	next, err := hw.SelectSealer(target, *tpmDevice, store, ghostSlot)
	if err != nil {
		fatal("target tier: %v", err)
	}

	if target == hw.SealModeTPM {
		fmt.Println("Migrating software -> TPM. A wrong PIN here charges the TPM's lockout budget.")
	} else {
		fmt.Println("Migrating TPM -> software. The wrapped key will live in seal.env; its strength")
		fmt.Println("becomes Argon2id cost x PIN entropy, with no hardware lockout. Use a strong PIN.")
	}
	pin, err := promptPIN("Enter the MAIN PIN: ")
	if err != nil {
		fatal("read pin: %v", err)
	}

	if err := hw.ReWrap(cur, next, pin); err != nil {
		fatal("migrate: %v", err)
	}
	if err := store.SetMode(target); err != nil {
		fatal("flip seal mode (the new wrapping IS in place; re-run to retry the flip): %v", err)
	}
	if err := cur.Destroy(); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: migrated, but destroying the old %s wrapping failed: %v\n", mode, err)
		fmt.Fprintln(os.Stderr, "the old wrapping still exists and can recover the disk key; clean it up manually")
	}
	if target == hw.SealModeTPM {
		_ = store.DeleteSalt()
		if err := hw.SetupLockout(*tpmDevice, pin); err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: migrated, but setting the TPM lockout failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "the key is TPM-sealed but without the dictionary-attack policy; re-run ghost-tpmreset guidance")
		}
	}
	fmt.Printf("migrated to the %s tier , same disk key, re-wrapped\n", target)
}

const ghostSlot = 0 // single-account model, matches setup + the daemon

func promptPIN(label string) (string, error) {
	fmt.Print(label)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

// enroll saves the device identity carried inside the link. Cert/key are optional at parse time but
// REQUIRED to enrol, so a code-only link fails here with instructions rather than half-enrolling.
// Files are PEM (openssl-inspectable) even though the link carries DER.
func enroll(link pair.EnrollLink) error {
	if len(link.DeviceCertDER) == 0 || len(link.DeviceKeyDER) == 0 {
		return fmt.Errorf("this link carries no device certificate , regenerate the enrolment QR on the box")
	}
	dir := "./ghostctl-identity"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: link.DeviceCertDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: link.DeviceKeyDER})
	if err := os.WriteFile(dir+"/device.pem", certPEM, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(dir+"/device-key.pem", keyPEM, 0o600); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("enrolled. device identity saved to", dir)
	fmt.Println("a session token is issued at first PIN unlock, not at enrolment")
	return nil
}
