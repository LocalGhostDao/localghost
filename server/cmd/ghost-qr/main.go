// ghost-qr renders an enrolment QR without running the full provisioning plan: it creates the box
// CA and server cert if absent (any writable dir, no root needed), mints the device identity, and
// prints the QR. For the dev/coder-user path where ghost-setup's disk partitioning is not wanted.
// With --nginx-out it also writes the appears-down nginx config for the given host, since ghost.secd
// speaks plain loopback HTTP and the TLS/mTLS edge is nginx's job.
//
//	ghost-qr --ca ~/ghost/ca --host box.example.com --port 8443 --nginx-out ~/ghost/nginx.conf
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/LocalGhostDao/localghost/server/internal/pair"
	"github.com/LocalGhostDao/localghost/server/internal/setup"
	"github.com/LocalGhostDao/localghost/server/internal/setup/debian"
)

func main() {
	caDir := flag.String("ca", "./ghost-ca", "CA + cert directory (created if absent)")
	host := flag.String("host", "", "host/IP or domain the phone connects to (required)")
	port := flag.Int("port", 8443, "public mTLS port in the enrol link")
	secd := flag.String("secd", "127.0.0.1:8443", "ghost.secd loopback address nginx proxies to")
	nginxOut := flag.String("nginx-out", "", "optional: write the appears-down nginx config here")
	flag.Parse()
	if *host == "" {
		fmt.Fprintln(os.Stderr, "--host is required")
		os.Exit(2)
	}

	pki := debian.NewPKI(*caDir, *host)
	if !pki.Exists() {
		if err := pki.CreateCA(); err != nil {
			fmt.Fprintln(os.Stderr, "create CA:", err)
			os.Exit(1)
		}
		if err := pki.IssueServerCert(); err != nil {
			fmt.Fprintln(os.Stderr, "issue server cert:", err)
			os.Exit(1)
		}
	}

	if *nginxOut != "" {
		conf := setup.DomainConfig{Domain: *host}.NginxConfig(*secd)
		if err := os.WriteFile(*nginxOut, []byte(conf), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write nginx config:", err)
			os.Exit(1)
		}
		fmt.Println("nginx config written to", *nginxOut)
	}

	if err := pair.Run(os.Stdout, pair.Options{
		Host:        *host,
		Port:        *port,
		CertPath:    *caDir + "/box-server.pem",
		BoxName:     *host,
		IssueDevice: pki.IssueDeviceCertDER,
	}, pair.EncodeQR); err != nil {
		fmt.Fprintln(os.Stderr, "render QR:", err)
		os.Exit(1)
	}
}
