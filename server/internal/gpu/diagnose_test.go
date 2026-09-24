package gpu

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeCard writes a sysfs device directory the way the kernel lays one out.
func fakeCard(t *testing.T, root, addr string, width, maxWidth int, driver bool, offBus bool) {
	t.Helper()
	d := filepath.Join(root, addr)
	os.MkdirAll(d, 0o755)
	w := func(name, v string) { os.WriteFile(filepath.Join(d, name), []byte(v+"\n"), 0o644) }
	w("vendor", "0x10de")
	w("class", "0x030000")
	w("current_link_width", itoa(width))
	w("max_link_width", itoa(maxWidth))
	w("current_link_speed", "2.5 GT/s PCIe")
	w("max_link_speed", "16.0 GT/s PCIe")
	cfg := []byte{0xde, 0x10, 0x86, 0x27}
	if offBus {
		cfg = []byte{0xff, 0xff, 0xff, 0xff}
	}
	os.WriteFile(filepath.Join(d, "config"), cfg, 0o644)
	if driver {
		drv := filepath.Join(root, "..", "drivers", "nvidia")
		os.MkdirAll(drv, 0o755)
		os.Symlink(drv, filepath.Join(d, "driver"))
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func withFakes(t *testing.T, klog string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "devices")
	os.MkdirAll(root, 0o755)
	oldPCI, oldLog, oldDrv := sysPCI, kernelLog, driverDir
	sysPCI = root
	kernelLog = func() string { return klog }
	driverDir = filepath.Join(root, "..", "gpus")
	t.Cleanup(func() { sysPCI, kernelLog, driverDir = oldPCI, oldLog, oldDrv; Reset() })
	Reset()
	// an Intel iGPU and a network card: neither is ours
	for _, x := range []struct{ addr, vendor, class string }{{"0000:00:02.0", "0x8086", "0x030000"}, {"0000:05:00.0", "0x10de", "0x020000"}} {
		d := filepath.Join(root, x.addr)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "vendor"), []byte(x.vendor), 0o644)
		os.WriteFile(filepath.Join(d, "class"), []byte(x.class), 0o644)
	}
	return root
}

func TestDiagnoseNarrowLinkAndInitFailures(t *testing.T) {
	root := withFakes(t, "[1.0] NVRM: GPU 0000:2e:00.0: RmInitAdapter failed! (0x23:0x65:1552)\n[11.0] NVRM: GPU 0000:2e:00.0: RmInitAdapter failed! (0x23:0x65:1552)\n")
	fakeCard(t, root, "0000:2e:00.0", 2, 16, true, false)
	os.MkdirAll(filepath.Join(driverDir, "0000:2e:00.0"), 0o755)
	lastErr, nextTry = errors.New("No devices were found"), time.Now().Add(4*time.Minute)
	r := Diagnose()
	if len(r.Cards) != 1 || r.Cards[0].Width != 2 || r.Cards[0].MaxWidth != 16 || r.Cards[0].Driver != "nvidia" || !r.Cards[0].Present {
		t.Fatalf("cards = %+v", r.Cards)
	}
	if r.InitFails != 2 || !strings.HasPrefix(r.Verdict, "the link came up at x2 of x16: physical") || !strings.Contains(r.Verdict, "RmInitAdapter failed ×2") {
		t.Fatalf("verdict = %q (init %d)", r.Verdict, r.InitFails)
	}
	if !strings.Contains(r.Probe, "No devices were found (next look in") {
		t.Fatalf("probe = %q", r.Probe)
	}
	rows := r.Rows()
	joined := ""
	for _, kv := range rows {
		joined += kv[0] + "=" + kv[1] + "\n"
	}
	for _, want := range []string{"card 0000:2e:00.0=answers · driver nvidia", "link=x2 of x16 · 2.5 GT/s PCIe (max 16.0 GT/s PCIe)", "driver lists=0000:2e:00.0", "init failures=2 this boot", "GPU faults=none logged this boot", "nothing here opens the card"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rows lack %q:\n%s", want, joined)
		}
	}
}

func TestDiagnoseOtherStates(t *testing.T) {
	// no card at all
	withFakes(t, "")
	if r := Diagnose(); !strings.HasPrefix(r.Verdict, "no NVIDIA card on the PCI bus") {
		t.Fatalf("empty: %q", r.Verdict)
	}
	// off the bus
	root := withFakes(t, "[4274592.8] NVRM: Xid (PCI:0000:2e:00): 79, pid='<unknown>', name=<unknown>, GPU has fallen off the bus.\n")
	fakeCard(t, root, "0000:2e:00.0", 16, 16, true, true)
	r := Diagnose()
	if !strings.Contains(r.Verdict, "fallen off the bus") || r.Xids != 1 || !strings.Contains(r.LastXid, "79") {
		t.Fatalf("off bus: %+v", r)
	}
	// unbound
	root = withFakes(t, "")
	fakeCard(t, root, "0000:2e:00.0", 16, 16, false, false)
	if r := Diagnose(); !strings.Contains(r.Verdict, "no driver is bound") {
		t.Fatalf("unbound: %q", r.Verdict)
	}
	// working, full width
	root = withFakes(t, "")
	fakeCard(t, root, "0000:2e:00.0", 16, 16, true, false)
	os.MkdirAll(filepath.Join(driverDir, "0000:2e:00.0"), 0o755)
	last = Stats{UsedMiB: 3100, TotalMiB: 12282, Util: 42, At: time.Now()}
	if r := Diagnose(); r.Verdict != "working: the card answers and the driver sees it" || !strings.Contains(r.Probe, "3.0/12.0 GB, 42% busy") {
		t.Fatalf("working: %q / %q", r.Verdict, r.Probe)
	}
}
