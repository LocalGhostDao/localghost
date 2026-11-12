package setup

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ROOT'S HANDS IN THE VOLUME (second audit, 12 Nov 2026). update.sh and the tools it runs are
// root, and the unlocked volume is the daemons' user's from its root down: a daemon that had
// been taken over can put a link where any name in there stands. On main, `chown` of a name
// re-owned what a link pointed at, `cp -f old old.prev` wrote the daemon's own bytes wherever
// old.prev pointed, and `mv -f x name` moved x INTO a directory a link at "name" pointed at;
// run against planted links in a scratch layout, the first two gave the daemons' user a
// root-owned file each. The tools now follow no link there, and this holds them to it: in the
// lines that touch the volume, no cp, no chown or chmod of a name, and every mv and install
// with -T. (The scripts are shell, the box is where they run; this reads them.)
func TestRootToolsFollowNoLinkInTheVolume(t *testing.T) {
	tools := filepath.Join("..", "..", "tools")
	// the paths that are on the volume, as each script names them
	vol := map[string]*regexp.Regexp{
		"update.sh":        regexp.MustCompile(`\$AI\b|\$ON\b|\$WK\b|\$DOOR\$GEO\b`),
		"models_check.sh":  regexp.MustCompile(`\$DIR/`),
		"setup_whisper.sh": regexp.MustCompile(`\$TO/|\$DEST\b`),
		"mirror_fetch.sh":  regexp.MustCompile(`"\$DIR/\$name"`),
	}
	word := func(line, w string) bool {
		return regexp.MustCompile(`(^|[\s;&|(])` + regexp.QuoteMeta(w) + `\s`).MatchString(line)
	}
	for name, onVolume := range vol {
		b, err := os.ReadFile(filepath.Join(tools, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			code := strings.TrimSpace(line)
			if code == "" || strings.HasPrefix(code, "#") || !onVolume.MatchString(code) {
				continue
			}
			// staging under /var/lib/ghost (root's own) is not the volume
			if strings.Contains(code, "/var/lib/ghost/staging") {
				continue
			}
			at := name + ":" + itoa(i+1) + ": " + code
			if word(code, "cp") {
				t.Errorf("cp into the volume (it writes through a link at the destination): %s", at)
			}
			if word(code, "chown") && !strings.Contains(code, "chown -h") && !strings.Contains(code, "chown -R -h") {
				t.Errorf("chown of a name on the volume without -h: %s", at)
			}
			if word(code, "chmod") && !strings.Contains(code, "vol_mode()") {
				t.Errorf("chmod of a name on the volume (use vol_mode): %s", at)
			}
			for _, m := range regexp.MustCompile(`\bmv (-\w+)`).FindAllStringSubmatch(code, -1) {
				if !strings.Contains(m[1], "T") {
					t.Errorf("mv without -T on the volume: %s", at)
				}
			}
			if word(code, "install") && !strings.Contains(code, "install -T") {
				t.Errorf("install without -T on the volume: %s", at)
			}
			if word(code, "ln") && !strings.Contains(code, "ln -fT") {
				t.Errorf("ln without -T on the volume: %s", at)
			}
		}
		if name != "mirror_fetch.sh" && (!strings.Contains(string(b), "vol_own()") || !strings.Contains(string(b), "vol_mode()")) {
			t.Errorf("%s: vol_own and vol_mode are gone", name)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}
