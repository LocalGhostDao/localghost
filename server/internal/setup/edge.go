package setup

// THE PHONE'S TLS GOES TO SECD UNTOUCHED (nginx stream, ssl_preread). nginx used to terminate the
// phone's TLS on 443 and pass its certificate to ghost.secd as a header on plain HTTP, which any
// process on the box could forge (secd edge.go has the story). Now nginx's stream module reads only
// the name the phone asks for (SNI) and forwards the raw TLS: the box's name to ghost.secd, which
// does the TLS and checks the device certificate itself; every other name to the http sites, which
// move from 443 to 127.0.0.1:4443 and learn their clients' addresses from a PROXY line.
//
// This file is the plan, pure where it can be: the listen lines rewritten, the stream config, the
// include in nginx.conf, the real-IP file. ApplyPassthrough does it to a tree of files (the root is
// "/" on a box, a temporary directory in tests), keeps every file it changes, checks with nginx -t,
// reloads, checks the result from outside, and puts everything back if any step fails.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Passthrough names the three ends.
type Passthrough struct {
	Domain    string // the box's name, as the phone asks for it
	SecdAddr  string // ghost.secd's listener (127.0.0.1:8443)
	SitesAddr string // where the http sites move to (127.0.0.1:4443)
}

const (
	StreamConfPath = "/etc/nginx/localghost-stream.conf"
	RealIPConfPath = "/etc/nginx/conf.d/localghost-realip.conf"
	GhostSiteLink  = "/etc/nginx/sites-enabled/ghost-secd"
	EdgeFlagPath   = "/etc/ghost/edge"
	streamMarker   = "# localghost: the phone's TLS by name, before any http server sees it"
)

var listenRE = regexp.MustCompile(`^(\s*)listen\s+([^;#]+);(.*)$`)

// ErrListenUnsupported: a 443 listener on a specific address; the move does not guess.
var ErrListenUnsupported = errors.New("a listener on a specific address")

// RewriteListens moves every TCP listener on port 443 in one config file to sitesAddr with
// proxy_protocol. A "[::]:443" line is commented out when its server block also listens on IPv4
// 443 (the stream takes IPv6 too, and two moved lines would be one listener twice); a block that
// listens only on [::]:443 has that line moved instead. It reports how many lines changed and
// whether any IPv6 listener was there.
func RewriteListens(conf, sitesAddr string) (out string, changed int, ipv6 bool, err error) {
	lines := strings.Split(conf, "\n")
	type lst struct {
		i            int
		indent, tail string
		addr         string
		params       []string
		v6           bool
	}
	blockOf := make([]int, len(lines)) // the server block each line is in (0: none)
	var stack []int                    // block ids by depth (0 for a non-server block)
	next := 0
	for i, ln := range lines {
		code := ln
		if j := strings.Index(code, "#"); j >= 0 {
			code = code[:j]
		}
		cur := 0
		for k := len(stack) - 1; k >= 0; k-- {
			if stack[k] != 0 {
				cur = stack[k]
				break
			}
		}
		blockOf[i] = cur
		for _, f := range strings.FieldsFunc(code, func(r rune) bool { return r == '{' || r == '}' || r == ';' }) {
			_ = f
		}
		opens := strings.Count(code, "{")
		closes := strings.Count(code, "}")
		for o := 0; o < opens; o++ {
			if o == 0 && regexp.MustCompile(`^\s*server\s*\{`).MatchString(code) {
				next++
				stack = append(stack, next)
			} else {
				stack = append(stack, 0)
			}
		}
		for c := 0; c < closes && len(stack) > 0; c++ {
			stack = stack[:len(stack)-1]
		}
	}
	var found []lst
	v4In := map[int]bool{}
	for i, ln := range lines {
		m := listenRE.FindStringSubmatch(ln)
		if m == nil || strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		spec := strings.Fields(m[2])
		if len(spec) == 0 {
			continue
		}
		addr, params := spec[0], spec[1:]
		host, port := splitListen(addr)
		if port != "443" || hasParam(params, "quic") {
			continue // not ours: another port, or HTTP/3 (UDP)
		}
		switch {
		case strings.HasPrefix(host, "["):
			if host != "[::]" {
				return "", 0, false, fmt.Errorf("%w: %s", ErrListenUnsupported, strings.TrimSpace(ln))
			}
			ipv6 = true
			found = append(found, lst{i, m[1], m[3], addr, params, true})
		case host == "" || host == "*" || host == "0.0.0.0":
			v4In[blockOf[i]] = true
			found = append(found, lst{i, m[1], m[3], addr, params, false})
		default:
			return "", 0, false, fmt.Errorf("%w: %s", ErrListenUnsupported, strings.TrimSpace(ln))
		}
	}
	movedIn := map[int]bool{}
	for _, l := range found {
		b := blockOf[l.i]
		if l.v6 && (v4In[b] || movedIn[b]) {
			lines[l.i] = l.indent + "# localghost moved (the stream listens on [::]:443): " + strings.TrimSpace(lines[l.i])
			changed++
			continue
		}
		var keep []string
		for _, p := range l.params {
			if p == "proxy_protocol" || strings.HasPrefix(p, "ipv6only") {
				continue
			}
			keep = append(keep, p)
		}
		keep = append(keep, "proxy_protocol")
		lines[l.i] = l.indent + "listen " + sitesAddr + " " + strings.Join(keep, " ") + ";" + l.tail +
			" # localghost moved from " + l.addr
		movedIn[b] = true
		changed++
	}
	return strings.Join(lines, "\n"), changed, ipv6, nil
}

func splitListen(a string) (host, port string) {
	if strings.HasPrefix(a, "unix:") {
		return a, ""
	}
	if !strings.Contains(a, ":") || (strings.HasPrefix(a, "[") && !strings.Contains(a, "]:")) {
		if isDigits(a) {
			return "", a
		}
		return a, "80"
	}
	i := strings.LastIndex(a, ":")
	return a[:i], a[i+1:]
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func hasParam(ps []string, want string) bool {
	for _, p := range ps {
		if p == want {
			return true
		}
	}
	return false
}

// StreamConf is the stream config: the box's name to secd, every other name to the sites (or to
// secd too, on a box that serves nothing else, so an unknown name meets the same 503).
func (p Passthrough) StreamConf(ipv6, haveSites bool) string {
	def := p.SecdAddr
	if haveSites {
		def = p.SitesAddr
	}
	v6 := ""
	if ipv6 {
		v6 = "    listen [::]:443;\n"
	}
	return fmt.Sprintf(`# generated by ghost-ctl edge-passthrough , do not edit by hand
# The phone's TLS for %[1]s goes to ghost.secd untouched: nginx reads the name the phone asks for
# (SNI) and nothing else. secd does the TLS and checks the device certificate itself. Every other
# name goes to the http sites on %[3]s, with a PROXY line so they still see their clients.
map $ssl_preread_server_name $localghost_upstream {
    hostnames;
    %[1]s  %[2]s;
    default  %[4]s;
}
server {
    listen 443;
%[5]s    ssl_preread on;
    proxy_protocol on;
    proxy_pass $localghost_upstream;
    proxy_connect_timeout 5s;
    proxy_timeout 1h;
}
`, p.Domain, p.SecdAddr, p.SitesAddr, def, v6)
}

// RealIPConf lets the sites behind the stream log their clients, not 127.0.0.1.
const RealIPConf = `# generated by ghost-ctl edge-passthrough: the http sites now sit behind the stream on 127.0.0.1,
# which sends a PROXY line with the client's address first
set_real_ip_from 127.0.0.1;
real_ip_header proxy_protocol;
`

var streamBlockRE = regexp.MustCompile(`(?m)^\s*stream\s*\{`)

// AddStreamInclude puts the stream config's include into nginx.conf: inside an existing stream
// block, or in a new one at the end. Idempotent.
func AddStreamInclude(nginxConf, streamPath string) string {
	inc := "include " + streamPath + ";"
	if strings.Contains(nginxConf, inc) {
		return nginxConf
	}
	if loc := streamBlockRE.FindStringIndex(nginxConf); loc != nil {
		return nginxConf[:loc[1]] + "\n    " + inc + " " + streamMarker + nginxConf[loc[1]:]
	}
	if !strings.HasSuffix(nginxConf, "\n") {
		nginxConf += "\n"
	}
	return nginxConf + "\n" + streamMarker + "\nstream {\n    " + inc + "\n}\n"
}

// --- applying it ---

// EdgeRunner is what ApplyPassthrough needs from the box: nginx -t, a reload, and the check from
// outside (the box's name answers with the box's certificate, another name with its own).
type EdgeRunner interface {
	TestNginx() error
	ReloadNginx() error
	Check(p Passthrough, otherName string) error
}

// EdgeResult says what was done, for the operator.
type EdgeResult struct {
	Changed   []string // files written (backed up first)
	Moved     int      // listen lines moved
	OtherName string   // a site's name the check used ("" when the box serves nothing else)
	Backup    string   // where the old files are
	Warnings  []string
}

// ApplyPassthrough does the move under root. dryRun reports without writing. On any failure after
// the first write, every file is put back and nginx reloaded onto the old config.
func ApplyPassthrough(root string, p Passthrough, run EdgeRunner, dryRun bool) (EdgeResult, error) {
	var res EdgeResult
	if p.Domain == "" || p.SecdAddr == "" || p.SitesAddr == "" {
		return res, errors.New("domain, secd and sites addresses are all needed")
	}
	at := func(pth string) string { return filepath.Join(root, pth) }

	nginxConfPath := "/etc/nginx/nginx.conf"
	nc, err := os.ReadFile(at(nginxConfPath))
	if err != nil {
		return res, fmt.Errorf("nginx.conf: %w", err)
	}
	if strings.Contains(string(nc), "include "+StreamConfPath+";") {
		return res, errors.New("the stream passthrough is already in nginx.conf (ghost-ctl edge-passthrough --undo takes it out)")
	}

	// every config the http block reads: nginx.conf itself, conf.d, sites-enabled (through links)
	files := []string{nginxConfPath}
	for _, g := range []string{"/etc/nginx/conf.d/*.conf", "/etc/nginx/sites-enabled/*"} {
		m, _ := filepath.Glob(at(g))
		sort.Strings(m)
		for _, f := range m {
			rel := "/" + strings.TrimPrefix(f, filepath.Clean(root)+"/")
			if rel == GhostSiteLink {
				continue // the old front door: disabled below, not moved
			}
			files = append(files, rel)
		}
	}
	type change struct{ path, text string }
	var changes []change
	ipv6, haveSites := false, false
	for _, f := range files {
		real := f
		if l, err := filepath.EvalSymlinks(at(f)); err == nil {
			real = "/" + strings.TrimPrefix(l, filepath.Clean(root)+"/")
		}
		b, err := os.ReadFile(at(real))
		if err != nil {
			continue
		}
		out, n, v6, err := RewriteListens(string(b), p.SitesAddr)
		if err != nil {
			return res, fmt.Errorf("%s: %w (move it by hand, then run this again)", f, err)
		}
		if v6 {
			ipv6 = true
		}
		if strings.Contains(string(b), "real_ip_header") {
			res.Warnings = append(res.Warnings, f+" sets its own real_ip_header: add \"set_real_ip_from 127.0.0.1;\" there or it will see 127.0.0.1")
		}
		if n > 0 {
			haveSites = true
			res.Moved += n
			if res.OtherName == "" {
				res.OtherName = firstServerName(string(b), p.Domain)
			}
			if f == nginxConfPath {
				nc = []byte(out)
				continue
			}
			changes = append(changes, change{real, out})
		}
	}
	if !strings.Contains(string(nc), "/etc/nginx/conf.d/") && haveSites {
		res.Warnings = append(res.Warnings, "nginx.conf does not include conf.d: the sites will log 127.0.0.1 until "+RealIPConfPath+"'s two lines are in the http block")
	}
	changes = append(changes,
		change{nginxConfPath, AddStreamInclude(string(nc), StreamConfPath)},
		change{StreamConfPath, p.StreamConf(ipv6, haveSites)},
	)
	if haveSites {
		changes = append(changes, change{RealIPConfPath, RealIPConf})
	}
	for _, c := range changes {
		res.Changed = append(res.Changed, c.path)
	}
	if _, err := os.Lstat(at(GhostSiteLink)); err == nil {
		res.Changed = append(res.Changed, GhostSiteLink+" (disabled)")
	}
	if dryRun {
		return res, nil
	}

	// keep everything first, then write
	res.Backup = "/etc/nginx/localghost-before-passthrough-" + time.Now().UTC().Format("20060102T150405Z")
	var written []string
	restore := func(cause error) (EdgeResult, error) {
		if rerr := restoreBackup(root, res.Backup, written); rerr != nil {
			return res, fmt.Errorf("%v; and putting the old files back failed: %v (they are in %s)", cause, rerr, res.Backup)
		}
		if rerr := run.ReloadNginx(); rerr != nil {
			return res, fmt.Errorf("%v; the old files are back but nginx did not reload: %v", cause, rerr)
		}
		return res, fmt.Errorf("%v; the old config is back and nginx reloaded onto it", cause)
	}
	backup := func(pth string) error {
		dst := at(filepath.Join(res.Backup, pth))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		fi, err := os.Lstat(at(pth))
		if errors.Is(err, os.ErrNotExist) {
			return os.WriteFile(dst+".absent", nil, 0o644)
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(at(pth))
			if err != nil {
				return err
			}
			return os.WriteFile(dst+".symlink", []byte(target), 0o644)
		}
		b, err := os.ReadFile(at(pth))
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, fi.Mode().Perm())
	}
	for _, c := range changes {
		if err := backup(c.path); err != nil {
			return res, fmt.Errorf("backup of %s: %w (nothing changed)", c.path, err)
		}
	}
	if _, err := os.Lstat(at(GhostSiteLink)); err == nil {
		if err := backup(GhostSiteLink); err != nil {
			return res, fmt.Errorf("backup of %s: %w (nothing changed)", GhostSiteLink, err)
		}
	}
	for _, c := range changes {
		mode := os.FileMode(0o644)
		if fi, err := os.Stat(at(c.path)); err == nil {
			mode = fi.Mode().Perm()
		}
		written = append(written, c.path)
		if err := writeAtomic(at(c.path), []byte(c.text), mode); err != nil {
			return restore(fmt.Errorf("writing %s: %w", c.path, err))
		}
	}
	if _, err := os.Lstat(at(GhostSiteLink)); err == nil {
		written = append(written, GhostSiteLink)
		if err := os.Remove(at(GhostSiteLink)); err != nil {
			return restore(fmt.Errorf("disabling %s: %w", GhostSiteLink, err))
		}
	}
	if err := run.TestNginx(); err != nil {
		return restore(fmt.Errorf("nginx -t refused the new config: %w", err))
	}
	if err := run.ReloadNginx(); err != nil {
		return restore(fmt.Errorf("nginx did not reload: %w", err))
	}
	if err := run.Check(p, res.OtherName); err != nil {
		return restore(fmt.Errorf("after the reload: %w", err))
	}
	// the switch is one step: once the stream carries the phone's TLS, secd stops trusting plain
	// HTTP (the flag file edgeStrict reads). Left to a printed hint, the box sat fail-open until
	// someone typed it (the audit's edge lead).
	if err := writeAtomic(at(EdgeFlagPath), []byte("tls\n"), 0o644); err != nil {
		res.Warnings = append(res.Warnings, "could not write "+EdgeFlagPath+" ("+err.Error()+"); run: echo tls | sudo tee "+EdgeFlagPath)
	} else {
		res.Changed = append(res.Changed, EdgeFlagPath)
	}
	return res, nil
}

// UndoPassthrough puts back the files of the newest backup (or the one named).
func UndoPassthrough(root, backup string, run EdgeRunner) (string, error) {
	if backup == "" {
		m, _ := filepath.Glob(filepath.Join(root, "/etc/nginx/localghost-before-passthrough-*"))
		if len(m) == 0 {
			return "", errors.New("no backup in /etc/nginx/localghost-before-passthrough-*")
		}
		sort.Strings(m)
		backup = "/" + strings.TrimPrefix(m[len(m)-1], filepath.Clean(root)+"/")
	}
	var paths []string
	base := filepath.Join(root, backup)
	err := filepath.Walk(base, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel := "/" + strings.TrimPrefix(p, base+"/")
		rel = strings.TrimSuffix(strings.TrimSuffix(rel, ".absent"), ".symlink")
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return backup, err
	}
	if err := restoreBackup(root, backup, paths); err != nil {
		return backup, err
	}
	// back on the http site, plain HTTP with nginx's header is the path again
	_ = os.Remove(filepath.Join(root, EdgeFlagPath))
	if err := run.TestNginx(); err != nil {
		return backup, fmt.Errorf("the old files are back but nginx -t refuses them: %w", err)
	}
	return backup, run.ReloadNginx()
}

func restoreBackup(root, backup string, paths []string) error {
	var errs []string
	for _, pth := range paths {
		src := filepath.Join(root, backup, pth)
		dst := filepath.Join(root, pth)
		switch {
		case exists(src + ".absent"):
			if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err.Error())
			}
		case exists(src + ".symlink"):
			t, err := os.ReadFile(src + ".symlink")
			if err == nil {
				_ = os.Remove(dst)
				err = os.Symlink(string(t), dst)
			}
			if err != nil {
				errs = append(errs, err.Error())
			}
		default:
			b, err := os.ReadFile(src)
			if err == nil {
				fi, _ := os.Stat(src)
				err = writeAtomic(dst, b, fi.Mode().Perm())
			}
			if err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func writeAtomic(p string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".localghost-new"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

var serverNameRE = regexp.MustCompile(`(?m)^\s*server_name\s+([^;]+);`)

// firstServerName is a name one of the moved sites answers to, for the check from outside.
func firstServerName(conf, not string) string {
	for _, m := range serverNameRE.FindAllStringSubmatch(conf, -1) {
		for _, n := range strings.Fields(m[1]) {
			if n != "_" && n != not && !strings.ContainsAny(n, "*~$") && strings.Contains(n, ".") {
				return n
			}
		}
	}
	return ""
}
