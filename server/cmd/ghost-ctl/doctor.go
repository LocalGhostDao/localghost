package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/nsreach"
)

// doctor is the box's own check-up: what is open to the internet and by which process, whether
// the rules that are supposed to stand between the internet and the daemons are in place, what
// the box's name and its mail's name resolve to, when the certificates run out, and what the
// machine does about its own updates. Every line is PASS, WARN, FAIL or INFO with what to do.
// It reads; it changes nothing. Run as root (the sockets' owners and sshd's effective config
// need it); without root the owner column is blank and a few checks say so.
//
//	sudo ghost-ctl doctor            # everything
//	sudo ghost-ctl doctor --no-net   # no DNS or outbound connections (an offline box)
func doctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	noNet := fs.Bool("no-net", false, "skip DNS lookups and outbound probes")
	mount := fs.String("mount", "/var/lib/ghost/mnt/slot0", "the unlocked volume (reached through ghost.secd when hidden)")
	fs.Parse(args)
	d := &doc{noNet: *noNet, mount: *mount, env: readEnv("/etc/ghost/ghost.env")}
	if os.Geteuid() != 0 {
		d.info("not root: socket owners, sshd's effective config and the nftables tables are not visible; run with sudo for the whole picture")
	}
	d.services()
	d.listeners()
	d.firewall()
	d.edge()
	d.certs()
	d.ssh()
	d.updates()
	d.mail()
	d.reach()
	fails, warns := 0, 0
	for _, r := range d.out {
		fmt.Printf("%-4s %s\n", r.level, r.text)
		if r.fix != "" {
			fmt.Printf("     %s\n", r.fix)
		}
		switch r.level {
		case "FAIL":
			fails++
		case "WARN":
			warns++
		}
	}
	fmt.Printf("\n%d fail, %d warn\n", fails, warns)
	if fails > 0 {
		os.Exit(1)
	}
}

type result struct{ level, text, fix string }

type doc struct {
	noNet bool
	mount string
	env   map[string]string
	out   []result
	// filled by listeners(), used by the later checks
	public  []listener
	hasMail bool
}

func (d *doc) pass(t string)      { d.out = append(d.out, result{"PASS", t, ""}) }
func (d *doc) info(t string)      { d.out = append(d.out, result{"INFO", t, ""}) }
func (d *doc) warn(t, fix string) { d.out = append(d.out, result{"WARN", t, fix}) }
func (d *doc) fail(t, fix string) { d.out = append(d.out, result{"FAIL", t, fix}) }
func (d *doc) host() string       { return strings.Split(d.env["GHOST_HOST"], ":")[0] }
func unitExists(name string) bool {
	_, err := os.Stat("/etc/systemd/system/" + name + ".service")
	return err == nil
}

func readEnv(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return out
}

func active(unit string) bool {
	return exec.Command("systemctl", "is-active", "--quiet", unit).Run() == nil
}

func enabled(unit string) bool {
	return exec.Command("systemctl", "is-enabled", "--quiet", unit).Run() == nil
}

// --- services ---

func (d *doc) services() {
	if len(d.env) == 0 {
		d.fail("/etc/ghost/ghost.env is missing or unreadable: this is not a set-up box, or not root", "tools/server_setup_root.sh writes it")
	} else if d.host() == "" {
		d.fail("GHOST_HOST is not set in /etc/ghost/ghost.env", "the box's public name, as the phone reaches it")
	} else {
		d.pass("box name " + d.host())
	}
	for _, u := range []string{"ghost.secd", "nginx"} {
		if active(u) {
			d.pass(u + " is running")
		} else {
			d.fail(u+" is not running", "systemctl status "+u)
		}
	}
	d.hasMail = unitExists("ghost.maild")
	if d.hasMail {
		if active("ghost.maild") {
			d.pass("ghost.maild (the mail relay) is running on this machine")
		} else {
			d.fail("ghost.maild is installed and not running", "systemctl status ghost.maild")
		}
	}
	if unitExists("nftables") || exec.Command("systemctl", "cat", "nftables").Run() == nil {
		if !enabled("nftables") {
			d.warn("nftables.service is not enabled: the firewall tables are gone after a reboot", "sudo tools/firewall.sh enables it")
		}
	}
}

// --- what listens, and to whom ---

type listener struct {
	proto string // tcp, udp
	ip    net.IP
	port  int
	owner string // process name, when visible
	user  string
}

func (d *doc) listeners() {
	inodes := socketOwners()
	var all []listener
	for _, f := range []struct{ path, proto string }{{"/proc/net/tcp", "tcp"}, {"/proc/net/tcp6", "tcp"}, {"/proc/net/udp", "udp"}, {"/proc/net/udp6", "udp"}} {
		all = append(all, readProcNet(f.path, f.proto, inodes)...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].port != all[j].port {
			return all[i].port < all[j].port
		}
		return all[i].proto < all[j].proto
	})
	// what LocalGhost expects on a public address, and what must never be there
	expected := map[string]string{"tcp/80": "nginx", "tcp/443": "nginx", "tcp/25": "ghost.maild"}
	neverPublic := func(l listener) string {
		switch {
		case l.port == 8443:
			return "ghost.secd's plain port"
		case l.port >= 6000 && l.port <= 6199:
			return "the vault's Postgres or Redis"
		case l.port == 5432 || l.port == 6379:
			return "a database"
		case l.port == 8332 || l.port == 8545 || l.port == 8546 || l.port == 8551 || l.port == 9090:
			return "a node's RPC port"
		case strings.Contains(l.owner, "llama"):
			return "the model server"
		case strings.Contains(l.owner, "code-server"):
			return "code-server"
		case strings.HasPrefix(l.owner, "ghost."):
			return "a LocalGhost daemon"
		}
		return ""
	}
	seen := map[string]bool{}
	for _, l := range all {
		if l.ip.IsLoopback() {
			continue
		}
		key := fmt.Sprintf("%s/%d", l.proto, l.port)
		if seen[key] {
			continue
		}
		seen[key] = true
		d.public = append(d.public, l)
		who := l.owner
		if who == "" {
			who = "owner not visible"
		}
		if l.user != "" {
			who += " as " + l.user
		}
		if why := neverPublic(l); why != "" {
			d.fail(fmt.Sprintf("%s port %d is open to the network: %s (%s)", l.proto, l.port, why, who), "bind it to 127.0.0.1 or put it behind nginx with a client certificate; nothing of the box's belongs on a public address but 80, 443 and the relay's 25")
			continue
		}
		if want, ok := expected[key]; ok {
			if l.owner == "" || strings.Contains(l.owner, want) || (want == "ghost.maild" && l.owner == "ghost.maild") {
				d.pass(fmt.Sprintf("%s port %d: %s", l.proto, l.port, who))
			} else {
				d.warn(fmt.Sprintf("%s port %d is held by %s, not %s", l.proto, l.port, who, want), "the box expects "+want+" there")
			}
			continue
		}
		switch l.port {
		case 22:
			d.info(fmt.Sprintf("tcp port 22: %s. Fine on the LAN; make sure the router does not forward it", who))
		case 8333, 30303, 30304:
			d.info(fmt.Sprintf("%s port %d: %s (a node's peer port, yours to keep patched; its RPC must stay on loopback, checked above)", l.proto, l.port, who))
		default:
			d.warn(fmt.Sprintf("%s port %d is open to the network: %s, not something LocalGhost opened", l.proto, l.port, who), "if it is not yours on purpose, stop it or bind it to 127.0.0.1")
		}
	}
	if len(d.public) == 0 {
		d.warn("nothing listens on a public address (not even nginx on 443)", "the phone cannot reach the box from outside")
	}
}

// socketOwners maps socket inodes to the process name and user that hold them (root sees all).
func socketOwners() map[uint64]listener {
	out := map[uint64]listener{}
	procs, _ := os.ReadDir("/proc")
	for _, p := range procs {
		pid := p.Name()
		if _, err := strconv.Atoi(pid); err != nil {
			continue
		}
		fds, err := os.ReadDir("/proc/" + pid + "/fd")
		if err != nil {
			continue
		}
		comm, _ := os.ReadFile("/proc/" + pid + "/comm")
		name := strings.TrimSpace(string(comm))
		user := ""
		if st, err := os.ReadFile("/proc/" + pid + "/status"); err == nil {
			for _, line := range strings.Split(string(st), "\n") {
				if strings.HasPrefix(line, "Uid:") {
					f := strings.Fields(line)
					if len(f) > 1 {
						user = userName(f[1])
					}
				}
			}
		}
		for _, fd := range fds {
			target, err := os.Readlink("/proc/" + pid + "/fd/" + fd.Name())
			if err != nil || !strings.HasPrefix(target, "socket:[") {
				continue
			}
			ino, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]"), 10, 64)
			if err == nil {
				out[ino] = listener{owner: name, user: user}
			}
		}
	}
	return out
}

func userName(uid string) string {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return uid
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Split(sc.Text(), ":")
		if len(p) > 2 && p[2] == uid {
			return p[0]
		}
	}
	return uid
}

// readProcNet reads one /proc/net table: listening TCP sockets, and bound UDP sockets.
func readProcNet(path, proto string, owners map[uint64]listener) []listener {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []listener
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fl := strings.Fields(sc.Text())
		if len(fl) < 10 {
			continue
		}
		state := fl[3]
		if (proto == "tcp" && state != "0A") || (proto == "udp" && state != "07") {
			continue
		}
		addr, portHex, ok := strings.Cut(fl[1], ":")
		if !ok {
			continue
		}
		port, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil {
			continue
		}
		ip := procIP(addr)
		if ip == nil {
			continue
		}
		l := listener{proto: proto, ip: ip, port: int(port)}
		if ino, err := strconv.ParseUint(fl[9], 10, 64); err == nil {
			if o, ok := owners[ino]; ok {
				l.owner, l.user = o.owner, o.user
			}
		}
		out = append(out, l)
	}
	return out
}

// procIP decodes /proc/net's little-endian hex addresses.
func procIP(h string) net.IP {
	b, err := hex.DecodeString(h)
	if err != nil {
		return nil
	}
	switch len(b) {
	case 4:
		return net.IPv4(b[3], b[2], b[1], b[0])
	case 16:
		ip := make(net.IP, 16)
		for i := 0; i < 16; i += 4 {
			ip[i], ip[i+1], ip[i+2], ip[i+3] = b[i+3], b[i+2], b[i+1], b[i]
		}
		if v4 := ip.To4(); v4 != nil {
			return v4
		}
		return ip
	}
	return nil
}

// --- the firewall tables ---

func (d *doc) firewall() {
	if _, err := exec.LookPath("nft"); err != nil {
		d.fail("nftables is not installed", "apt install nftables, then sudo tools/firewall.sh")
		return
	}
	out, err := exec.Command("nft", "list", "table", "inet", "localghost").CombinedOutput()
	switch {
	case err != nil && os.Geteuid() != 0:
		d.info("nftables tables need root to read")
		return
	case err != nil:
		d.fail("the box's nftables table (inet localghost) is not loaded: any local user can reach ghost.secd's plain port and forge the edge header", "sudo tools/firewall.sh")
	case strings.Contains(string(out), "dport 8443") && strings.Contains(string(out), "skuid"):
		d.pass("firewall: ghost.secd's plain port is reachable by root and nginx alone (inet localghost)")
	default:
		d.fail("the box's nftables table is loaded without the loopback rule", "sudo tools/firewall.sh")
	}
	if _, err := os.Stat("/etc/nftables.d/localghost.nft"); err != nil {
		d.warn("/etc/nftables.d/localghost.nft is missing: the table is not reloaded at boot", "sudo tools/firewall.sh")
	} else if b, err := os.ReadFile("/etc/nftables.conf"); err != nil || !strings.Contains(string(b), "/etc/nftables.d/*.nft") {
		d.warn("/etc/nftables.conf does not include /etc/nftables.d/*.nft: the table is not reloaded at boot", "sudo tools/firewall.sh")
	}
	if d.hasMail {
		out, err := exec.Command("nft", "list", "table", "inet", "ghostmail").CombinedOutput()
		if err != nil || !strings.Contains(string(out), "dport 25") {
			d.warn("the relay's nftables table (inet ghostmail) is not loaded: port 25 has no per-source limits", "in the web repo: sudo deploy/mail/scripts/firewall.sh")
		} else {
			d.pass("firewall: port 25 is rate-limited per source and closed to IPv6 (inet ghostmail)")
		}
	}
}

// --- nginx and the edge ---

func (d *doc) edge() {
	if out, err := exec.Command("nginx", "-t").CombinedOutput(); err != nil {
		why := strings.TrimSpace(string(out))
		if why == "" {
			why = err.Error()
		}
		d.fail("nginx -t fails", why)
	} else {
		d.pass("nginx config parses")
	}
	flag, _ := os.ReadFile("/etc/ghost/edge")
	tls := strings.TrimSpace(string(flag)) == "tls"
	_, streamErr := os.Stat("/etc/nginx/localghost-stream.conf")
	_, siteErr := os.Stat("/etc/nginx/sites-enabled/ghost-secd")
	switch {
	case tls && streamErr == nil:
		d.pass("edge: passthrough (nginx hands the phone's TLS to ghost.secd by name)")
		if siteErr == nil {
			d.warn("edge: the old ghost-secd site is back in sites-enabled beside the passthrough", "a redeploy before this fix re-rendered it; remove /etc/nginx/sites-enabled/ghost-secd and reload nginx")
		}
	case !tls && streamErr != nil:
		if siteErr != nil {
			d.fail("edge: no ghost-secd site in nginx and no passthrough", "sudo tools/redeploy.sh --nginx-only renders the site")
		} else {
			d.pass("edge: nginx terminates the phone's TLS and passes the header (the ghost-secd site)")
		}
	case tls:
		d.fail("edge: /etc/ghost/edge says tls but there is no stream passthrough: ghost.secd refuses the header path and the phone sees the box as down", "rm /etc/ghost/edge, or sudo ghost-ctl edge-passthrough --domain <name>")
	default:
		d.fail("edge: the stream passthrough is in nginx but /etc/ghost/edge does not say tls: plain requests with a forged header reach ghost.secd", "sudo ghost-ctl edge-passthrough --domain <name> again (it writes the flag after its check)")
	}
}

// --- certificates ---

func (d *doc) certs() {
	caDir := d.env["GHOST_CA_DIR"]
	if caDir == "" {
		caDir = "/etc/ghost/ca"
	}
	d.certExpiry(filepath.Join(caDir, "box-server.pem"), "the box's server certificate", 30)
	d.certExpiry(filepath.Join(caDir, "box-ca.pem"), "the box CA", 90)
	lives, _ := filepath.Glob("/etc/letsencrypt/live/*/fullchain.pem")
	for _, p := range lives {
		d.certExpiry(p, "Let's Encrypt "+filepath.Base(filepath.Dir(p)), 14)
	}
}

func (d *doc) certExpiry(path, what string, warnDays int) {
	b, err := os.ReadFile(path)
	if err != nil {
		if strings.Contains(what, "box") {
			d.fail(what+" is missing at "+path, "ghost-setup makes it")
		}
		return
	}
	block, _ := pem.Decode(b)
	if block == nil {
		d.fail(what+" at "+path+" is not PEM", "")
		return
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		d.fail(what+" does not parse: "+err.Error(), "")
		return
	}
	left := time.Until(c.NotAfter)
	switch {
	case left < 0:
		d.fail(what+" expired on "+c.NotAfter.Format("2 January 2006"), "renew it; the phone refuses an expired box")
	case left < time.Duration(warnDays)*24*time.Hour:
		d.warn(fmt.Sprintf("%s runs out in %d days (%s)", what, int(left.Hours()/24), c.NotAfter.Format("2 January 2006")), "renew before then")
	default:
		d.pass(fmt.Sprintf("%s valid until %s", what, c.NotAfter.Format("2 January 2006")))
	}
}

// --- ssh ---

func (d *doc) ssh() {
	if !active("ssh") && !active("sshd") {
		return
	}
	out, err := exec.Command("sshd", "-T").Output()
	if err != nil {
		if os.Geteuid() == 0 {
			d.info("sshd -T did not answer; its config was not checked")
		}
		return
	}
	cfg := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, " "); ok {
			cfg[k] = v
		}
	}
	if cfg["passwordauthentication"] == "yes" {
		d.warn("ssh accepts passwords", "PasswordAuthentication no in /etc/ssh/sshd_config (keys only), then systemctl reload ssh")
	} else {
		d.pass("ssh: keys only")
	}
	if cfg["permitrootlogin"] != "no" {
		d.warn("ssh permits root login ("+cfg["permitrootlogin"]+")", "PermitRootLogin no")
	}
}

// --- updates ---

func (d *doc) updates() {
	b, err := os.ReadFile("/etc/apt/apt.conf.d/20auto-upgrades")
	if err != nil || !strings.Contains(string(b), `Unattended-Upgrade "1"`) {
		d.warn("unattended security updates are off: nginx, the kernel and OpenSSL face the internet with the patches you remember to apply", "apt install unattended-upgrades && dpkg-reconfigure -plow unattended-upgrades")
	} else {
		d.pass("unattended security updates are on")
	}
	if b, err := os.ReadFile("/var/run/reboot-required"); err == nil && len(b) > 0 {
		d.warn("a reboot is pending for an installed update (usually the kernel)", "reboot when the box is locked anyway")
	}
}

// --- mail DNS ---

type dnsRec struct {
	Name, Type, Value, Why string
}

func (d *doc) mail() {
	path := nsreach.Path(filepath.Join(d.mount, "noted", "mail", "dns.json"))
	b, err := os.ReadFile(path)
	if err != nil {
		d.info("mail: not set up on this box (no dns.json), or the box is locked")
		return
	}
	var dj struct {
		Domain  string   `json:"domain"`
		Address string   `json:"address"`
		Shared  bool     `json:"shared"`
		Records []dnsRec `json:"records"`
		Warning string   `json:"warning"`
	}
	if err := json.Unmarshal(b, &dj); err != nil || dj.Address == "" {
		d.info("mail: no address yet")
		return
	}
	d.pass("mail address " + dj.Address)
	if dj.Warning != "" {
		d.warn("mail: "+dj.Warning, "")
	}
	if d.noNet {
		d.info("mail DNS not checked (--no-net)")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := net.DefaultResolver
	for _, rec := range dj.Records {
		name := strings.TrimSuffix(rec.Name, ".")
		switch rec.Type {
		case "MX":
			mxs, err := r.LookupMX(ctx, name)
			want := strings.TrimSuffix(strings.TrimPrefix(rec.Value, "10 "), ".")
			found := false
			for _, m := range mxs {
				if strings.EqualFold(strings.TrimSuffix(m.Host, "."), want) {
					found = true
				}
			}
			if err != nil || !found {
				d.fail("mail DNS: "+name+" MX does not point at "+want, "add the record: "+rec.Name+" MX "+rec.Value)
			} else {
				d.pass("mail DNS: " + name + " MX " + want)
			}
		case "TXT":
			txts, err := r.LookupTXT(ctx, name)
			want := strings.Trim(rec.Value, `"`)
			found := false
			for _, t := range txts {
				if strings.ReplaceAll(t, " ", "") == strings.ReplaceAll(want, " ", "") {
					found = true
				}
			}
			switch {
			case found:
				d.pass("mail DNS: " + name + " TXT present (" + rec.Why + ")")
			case err == nil && len(txts) > 0:
				d.fail("mail DNS: "+name+" TXT differs from the box's value", "replace it with the value from `ghost-cli ghost.noted mail-dns`")
			default:
				d.fail("mail DNS: "+name+" TXT is missing ("+rec.Why+")", "add it: `ghost-cli ghost.noted mail-dns` prints it")
			}
		}
	}
	// the shared domain's own records and the relay's name, which the box relies on either way
	domain := dj.Domain
	if domain == "" {
		if _, after, ok := strings.Cut(dj.Address, "@"); ok {
			domain = after
		}
	}
	mxs, err := r.LookupMX(ctx, domain)
	if err != nil || len(mxs) == 0 {
		d.fail("mail DNS: "+domain+" has no MX: nothing can be delivered to "+dj.Address, "the domain's operator adds it")
		return
	}
	relay := strings.TrimSuffix(mxs[0].Host, ".")
	d.pass("mail DNS: " + domain + " takes mail at " + relay)
	if txts, _ := r.LookupTXT(ctx, domain); !hasPrefix(txts, "v=spf1") {
		d.warn("mail DNS: "+domain+" has no SPF record: receivers cannot tell the relay is allowed to send for it", "")
	}
	if txts, _ := r.LookupTXT(ctx, "_dmarc."+domain); !hasPrefix(txts, "v=DMARC1") {
		d.warn("mail DNS: "+domain+" has no DMARC record", "")
	}
	ips, err := r.LookupIP(ctx, "ip4", relay)
	if err != nil || len(ips) == 0 {
		d.fail("mail DNS: the relay "+relay+" has no IPv4 address", "")
		return
	}
	names, _ := r.LookupAddr(ctx, ips[0].String())
	ptrOK := false
	for _, n := range names {
		if strings.EqualFold(strings.TrimSuffix(n, "."), relay) {
			ptrOK = true
		}
	}
	if ptrOK {
		d.pass("mail DNS: " + ips[0].String() + " reverses to " + relay + " (PTR)")
	} else {
		d.warn("mail DNS: "+ips[0].String()+" has no reverse name of "+relay+": Gmail and Outlook file the relay's mail as spam or refuse it", "ask the address's provider for a PTR record pointing at "+relay)
	}
	if conn, err := net.DialTimeout("tcp", net.JoinHostPort(relay, "25"), 8*time.Second); err == nil {
		conn.SetReadDeadline(time.Now().Add(8 * time.Second))
		banner := make([]byte, 128)
		n, _ := conn.Read(banner)
		conn.Close()
		if strings.HasPrefix(string(banner[:n]), "220") {
			d.pass("mail: " + relay + ":25 answers (" + strings.TrimSpace(strings.SplitN(string(banner[:n]), "\n", 2)[0]) + ")")
		} else {
			d.warn("mail: "+relay+":25 connected but did not greet", "")
		}
	} else {
		d.info("mail: " + relay + ":25 not reachable from here (" + err.Error() + "); from inside the same network that can be the router not hairpinning. From mobile data: openssl s_client -starttls smtp -connect " + relay + ":25")
	}
}

func hasPrefix(xs []string, p string) bool {
	for _, x := range xs {
		if strings.HasPrefix(x, p) {
			return true
		}
	}
	return false
}

// --- the box's own name from outside ---

func (d *doc) reach() {
	host := d.host()
	if host == "" || d.noNet {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		if net.ParseIP(host) == nil {
			d.fail("the box name "+host+" does not resolve", "the A record at the domain's DNS")
			return
		}
	} else {
		d.pass("the box name " + host + " resolves to " + ips[0].String())
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 8 * time.Second}, "tcp", net.JoinHostPort(host, "443"), &tls.Config{InsecureSkipVerify: true, ServerName: host})
	if err != nil {
		d.info("443 at " + host + " not reachable from here (" + err.Error() + "): from inside the same network that is usually the router not hairpinning; the phone on mobile data is the real test")
		return
	}
	defer conn.Close()
	cs := conn.ConnectionState()
	if len(cs.PeerCertificates) > 0 {
		c := cs.PeerCertificates[0]
		d.pass("443 at " + host + " answers with a certificate for " + c.Subject.CommonName + " from " + c.Issuer.CommonName)
	}
}
