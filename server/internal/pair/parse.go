package pair

import (
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Parse is the inverse of EnrollLink.String, mirroring the app's EnrollLink.parse. ghostctl uses
// it to connect from a link (typed, or read from the QR's payload), so you can enroll without
// standing at the box. host, code and fp are mandatory; the fingerprint is the trust anchor.
func Parse(raw string) (EnrollLink, error) {
	text := strings.TrimSpace(raw)
	const prefix = "localghost://enroll?"
	if !strings.HasPrefix(strings.ToLower(text), prefix) {
		return EnrollLink{}, fmt.Errorf("not a localghost enroll link")
	}
	q, err := url.ParseQuery(text[len(prefix):])
	if err != nil {
		return EnrollLink{}, err
	}
	host := strings.TrimSpace(q.Get("host"))
	fp := strings.TrimSpace(q.Get("fp"))
	if host == "" || fp == "" {
		return EnrollLink{}, fmt.Errorf("link missing host or fingerprint")
	}
	// cert/key are optional AT PARSE (a link can be inspected without them), but REQUIRED to actually
	// enrol , the caller checks. Decode if present.
	var certDER, keyDER []byte
	if c := strings.TrimSpace(q.Get("cert")); c != "" {
		pemBytes, derr := base64.RawURLEncoding.DecodeString(c)
		if derr != nil {
			return EnrollLink{}, fmt.Errorf("bad cert encoding: %w", derr)
		}
		if blk, _ := pem.Decode(pemBytes); blk != nil {
			certDER = blk.Bytes
		} else {
			return EnrollLink{}, fmt.Errorf("cert is not valid PEM")
		}
	}
	if k := strings.TrimSpace(q.Get("key")); k != "" {
		pemBytes, derr := base64.RawURLEncoding.DecodeString(k)
		if derr != nil {
			return EnrollLink{}, fmt.Errorf("bad key encoding: %w", derr)
		}
		if blk, _ := pem.Decode(pemBytes); blk != nil {
			keyDER = blk.Bytes
		} else {
			return EnrollLink{}, fmt.Errorf("key is not valid PEM")
		}
	}
	port := 8443
	if p := strings.TrimSpace(q.Get("port")); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return EnrollLink{}, fmt.Errorf("bad port %q", p)
		}
		port = n
	}
	return EnrollLink{
		Host:          host,
		Port:          port,
		Fingerprint:   normaliseFp(fp),
		BoxName:       strings.TrimSpace(q.Get("name")),
		DeviceCertDER: certDER,
		DeviceKeyDER:  keyDER,
	}, nil
}
