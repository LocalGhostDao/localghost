package pair

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"strings"
)

// CertFingerprint reads a PEM cert file and returns the SHA-256 of its DER, as uppercase
// colon-separated hex. This is the value the phone pins. It must be computed over the exact cert
// the box serves on its mTLS port.
func CertFingerprint(pemPath string) (string, error) {
	raw, err := os.ReadFile(pemPath)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return "", fmt.Errorf("no PEM block in %s", pemPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cert.Raw)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":"), nil
}

// LANHost returns the box's best-guess LAN address for the QR. Prefers a private IPv4 on an up,
// non-loopback interface. The operator can always override with a flag (e.g. a .local name).
func LANHost() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || !ip.IsPrivate() {
				continue
			}
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("no private IPv4 found; pass --host explicitly")
}
