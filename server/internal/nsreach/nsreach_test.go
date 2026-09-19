package nsreach

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPIDsFindsSelf(t *testing.T) {
	b, err := os.ReadFile("/proc/self/comm")
	if err != nil {
		t.Skip("no /proc here")
	}
	me := strings.TrimSpace(string(b))
	found := false
	for _, pid := range PIDs(me) {
		if pid == os.Getpid() {
			found = true
		}
	}
	if !found {
		t.Fatalf("PIDs(%q) did not include this process (%d)", me, os.Getpid())
	}
	if len(PIDs("no-such-daemon-name")) != 0 {
		t.Fatal("phantom pid for a name nobody has")
	}
}

func TestPathKeepsExistingAndUnknown(t *testing.T) {
	dir := t.TempDir()
	if got := Path(dir); got != dir {
		t.Fatalf("existing path must come back untouched, got %s", got)
	}
	missing := filepath.Join(dir, "not-here", "run")
	if got := Path(missing); got != missing {
		// ghost.secd is not running in a test, so there is no door to take: the caller's own
		// path comes back so its error names what the person typed.
		t.Fatalf("missing path with no secd must come back untouched, got %s", got)
	}
	t.Setenv("GHOST_NS_AUTO", "0")
	if got := Path(missing); got != missing {
		t.Fatalf("GHOST_NS_AUTO=0 must disable the detour, got %s", got)
	}
}
