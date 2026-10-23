package main

// edge-passthrough moves nginx off the phone's TLS: nginx forwards the raw TLS stream to ghost.secd
// by the name the phone asks for (SNI), and ghost.secd checks the device certificate itself. The
// other sites on the box move from 443 to 127.0.0.1:4443 behind the stream. The plan and the
// file-by-file rewrite live in internal/setup (edge.go); this is the box-side runner: nginx -t, the
// reload, and the check from outside that the box's name serves the box's certificate and another
// name serves its own. Everything is backed up first and put back if any step fails.

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/setup"
)

func edgePassthrough(args []string) {
	fs := flag.NewFlagSet("edge-passthrough", flag.ExitOnError)
	domain := fs.String("domain", "", "the box's name, as the phone asks for it (e.g. vlad.localghost.ai)")
	secdAddr := fs.String("secd", "127.0.0.1:8443", "ghost.secd's listener")
	sitesAddr := fs.String("sites", "127.0.0.1:4443", "where the other http sites move to")
	root := fs.String("root", "/", "filesystem root (for a dry run against a copy)")
	caDir := fs.String("ca", "/etc/ghost/ca", "the box CA dir, for the check from outside")
	dryRun := fs.Bool("dry-run", false, "show what would change, touch nothing")
	undo := fs.Bool("undo", false, "put the nginx config back the way it was")
	backup := fs.String("backup", "", "with --undo: a specific backup dir (default: the newest)")
	_ = fs.Parse(args)

	runner := &nginxRunner{caDir: *caDir, secdAddr: *secdAddr}
	if *undo {
		b, err := setup.UndoPassthrough(*root, *backup, runner)
		if err != nil {
			fatal("undo: %v (the files are in %s)", err, b)
		}
		fmt.Printf("nginx put back from %s and reloaded\n", b)
		return
	}
	if *domain == "" {
		fatal("--domain is required (the name the phone connects to)")
	}
	p := setup.Passthrough{Domain: *domain, SecdAddr: *secdAddr, SitesAddr: *sitesAddr}
	res, err := setup.ApplyPassthrough(*root, p, runner, *dryRun)
	for _, w := range res.Warnings {
		fmt.Fprintln(os.Stderr, "note:", w)
	}
	if err != nil {
		fatal("edge-passthrough: %v", err)
	}
	if *dryRun {
		fmt.Printf("[dry run] would move %d listen line(s) to %s and write:\n", res.Moved, *sitesAddr)
		for _, f := range res.Changed {
			fmt.Println("  ", f)
		}
		if res.OtherName != "" {
			fmt.Printf("  and would check that %s still serves its own certificate\n", res.OtherName)
		}
		return
	}
	fmt.Printf("done. %d listen line(s) moved to %s; the old files are in %s\n", res.Moved, *sitesAddr, res.Backup)
	fmt.Println("the phone's TLS now reaches ghost.secd untouched, and /etc/ghost/edge says tls: plain HTTP on :8443 gets the down page")
	fmt.Println("(ghost-cli ghost.secd edge shows the mode; --undo puts the http site and the old mode back)")
}

// nginxRunner is setup.EdgeRunner on the box.
type nginxRunner struct {
	caDir    string
	secdAddr string
}

func (n *nginxRunner) TestNginx() error {
	out, err := exec.Command("nginx", "-t").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

func (n *nginxRunner) ReloadNginx() error {
	out, err := exec.Command("systemctl", "reload", "nginx").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

// Check dials 443 as the phone would: the box's name must serve a certificate the box's own CA
// signed (secd behind the stream), and another site's name must serve something else (the http
// sites are still there). It never sends a client certificate, so it only inspects what is served.
func (n *nginxRunner) Check(p setup.Passthrough, otherName string) error {
	boxCert, err := servedCert(p.Domain)
	if err != nil {
		return fmt.Errorf("dialing the box's name %s: %w", p.Domain, err)
	}
	if !signedByBoxCA(boxCert, n.caDir) {
		return fmt.Errorf("%s served a certificate the box CA did not sign, so the stream is not reaching ghost.secd", p.Domain)
	}
	if otherName != "" {
		other, err := servedCert(otherName)
		if err != nil {
			return fmt.Errorf("dialing a site's name %s: %w", otherName, err)
		}
		if signedByBoxCA(other, n.caDir) {
			return fmt.Errorf("%s is being served ghost.secd's certificate, so the other sites are not reachable , the config is put back", otherName)
		}
	}
	return nil
}

func servedCert(name string) (*x509.Certificate, error) {
	raw, err := net.DialTimeout("tcp", "127.0.0.1:443", 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(6 * time.Second))
	conn := tls.Client(raw, &tls.Config{ServerName: name, InsecureSkipVerify: true}) // we inspect the cert ourselves
	if err := conn.Handshake(); err != nil {
		return nil, err
	}
	cs := conn.ConnectionState()
	if len(cs.PeerCertificates) == 0 {
		return nil, fmt.Errorf("no certificate served")
	}
	return cs.PeerCertificates[0], nil
}

func signedByBoxCA(cert *x509.Certificate, caDir string) bool {
	caPEM, err := os.ReadFile(caDir + "/box-ca.pem")
	if err != nil {
		return false
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return false
	}
	_, err = cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err == nil
}
