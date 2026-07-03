//go:build tpm

// ghost-tpmreset resets the TPM lockout hierarchy auth back to empty using the known pinAuth(PIN),
// so a stalled provisioning run can proceed. Needed when SetupLockout was run more than once with
// differing PINs and drove the lockout hierarchy into DA lockout, and the platform tpm2_clear build
// cannot pass a lockout auth on the CLI. Not part of normal operation , a repair tool.
//
//	ghost-tpmreset --tpm /dev/tpmrm0        # prompts for the PIN that owns the lockout auth
//
// After it succeeds, re-run ghost-setup --apply; SetupLockout starts from an empty lockout auth.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"golang.org/x/term"
)

func main() {
	device := flag.String("tpm", "/dev/tpmrm0", "TPM resource-manager device")
	flag.Parse()

	fmt.Print("PIN that owns the lockout auth (the one from your first successful provisioning run): ")
	var pin string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			fmt.Fprintln(os.Stderr, "read pin:", err)
			os.Exit(1)
		}
		pin = string(b)
	} else {
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		pin = strings.TrimRight(line, "\r\n")
	}
	if pin == "" {
		fmt.Fprintln(os.Stderr, "empty PIN , aborting")
		os.Exit(2)
	}

	if err := hw.ResetLockoutAuth(*device, pin); err != nil {
		fmt.Fprintln(os.Stderr, "reset failed:", err)
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "If the error mentions lockout/0x921 , the TPM is in DA lockout. What clears it")
		fmt.Fprintln(os.Stderr, "depends on the TPM:")
		fmt.Fprintln(os.Stderr, "  - Intel PTT / firmware TPM (AMI boards, this box): a timed wait is UNRELIABLE.")
		fmt.Fprintln(os.Stderr, "    PTT hides its DA state (getcap may show inLockout=0 while auth still fails)")
		fmt.Fprintln(os.Stderr, "    and usually clears only on a COLD POWER CYCLE (full shutdown, not reboot) or a")
		fmt.Fprintln(os.Stderr, "    firmware TPM-clear: BIOS -> Trusted Computing -> Pending Operation -> TPM Clear.")
		fmt.Fprintln(os.Stderr, "    Do NOT keep retrying , each attempt can re-arm it. Cold-boot or clear, then retry.")
		fmt.Fprintln(os.Stderr, "  - Discrete TPM: the recovery window is honoured; wait it out untouched, retry once.")
		fmt.Fprintln(os.Stderr, "If it mentions auth failure (NOT lockout): the window is clear but this PIN is not")
		fmt.Fprintln(os.Stderr, "the one that set the lockout auth. Try the other PIN, or bare `tpm2_clear -c l`.")
		os.Exit(1)
	}
	fmt.Println("lockout auth reset to empty. Re-run: ghost-setup --apply")
}
