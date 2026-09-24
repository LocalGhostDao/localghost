package gpu

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Card is what sysfs says about one NVIDIA display device, read WITHOUT opening it: the kernel
// answers these from PCI config space, which never wakes the nvidia driver (no /dev/nvidia0 open,
// no RmInitAdapter), so the phone can ask as often as it likes.
type Card struct {
	Addr      string // 0000:2e:00.0
	Driver    string // "nvidia", or "" when nothing is bound
	Present   bool   // config space answers (a card off the bus reads 0xffff)
	Width     int    // negotiated lanes
	MaxWidth  int    // lanes the card supports
	Speed     string // "2.5 GT/s PCIe"
	MaxSpeed  string
	PowerDraw string // runtime power state, when the kernel reports it
}

// Report is the host.gpu drill-in: the cards, the driver's view, the last probe, the kernel log.
type Report struct {
	Cards       []Card
	DriverKnows []string
	Probe       string // the last nvidia-smi answer or why there was none
	Xids        int
	LastXid     string
	InitFails   int // RmInitAdapter failed lines in this boot
	Verdict     string
}

var sysPCI = "/sys/bus/pci/devices" // replaced in tests

// Cards lists the NVIDIA display devices (vendor 0x10de, class 0x03xxxx) sysfs knows.
func Cards() []Card {
	es, err := os.ReadDir(sysPCI)
	if err != nil {
		return nil
	}
	var out []Card
	for _, e := range es {
		dir := filepath.Join(sysPCI, e.Name())
		vendor := readTrim(filepath.Join(dir, "vendor"))
		class := readTrim(filepath.Join(dir, "class"))
		if vendor != "0x10de" || !strings.HasPrefix(class, "0x03") {
			continue
		}
		c := Card{Addr: e.Name(), Present: true}
		if l, err := os.Readlink(filepath.Join(dir, "driver")); err == nil {
			c.Driver = filepath.Base(l)
		}
		c.Width, _ = strconv.Atoi(readTrim(filepath.Join(dir, "current_link_width")))
		c.MaxWidth, _ = strconv.Atoi(readTrim(filepath.Join(dir, "max_link_width")))
		c.Speed = readTrim(filepath.Join(dir, "current_link_speed"))
		c.MaxSpeed = readTrim(filepath.Join(dir, "max_link_speed"))
		c.PowerDraw = readTrim(filepath.Join(dir, "power_state"))
		if b, err := os.ReadFile(filepath.Join(dir, "config")); err == nil && len(b) >= 2 && b[0] == 0xff && b[1] == 0xff {
			c.Present = false
		}
		out = append(out, c)
	}
	return out
}

func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// kernelLog is the kernel ring buffer (root only; "" otherwise). Read through syslog(2), no exec.
var kernelLog = func() string {
	n, err := syscall.Klogctl(10, nil) // SYSLOG_ACTION_SIZE_BUFFER
	if err != nil || n <= 0 {
		return ""
	}
	if n > 16<<20 {
		n = 16 << 20
	}
	buf := make([]byte, n)
	m, err := syscall.Klogctl(3, buf) // SYSLOG_ACTION_READ_ALL
	if err != nil || m <= 0 {
		return ""
	}
	return string(buf[:m])
}

// Diagnose assembles the report. Nothing here opens a GPU device or runs nvidia-smi; the probe line
// is whatever Query last learned (it runs on its own clock, with its own backoff).
func Diagnose() Report {
	r := Report{Cards: Cards(), DriverKnows: Known()}
	mu.Lock()
	switch {
	case lastErr != nil:
		r.Probe = lastErr.Error()
		if !nextTry.IsZero() {
			r.Probe += fmt.Sprintf(" (next look in %s)", time.Until(nextTry).Round(time.Second))
		}
	case !last.At.IsZero():
		r.Probe = fmt.Sprintf("%.1f/%.1f GB, %.0f%% busy, %s ago", last.UsedMiB/1024, last.TotalMiB/1024, last.Util, time.Since(last.At).Round(time.Second))
	default:
		r.Probe = "not asked yet"
	}
	mu.Unlock()
	for _, line := range strings.Split(kernelLog(), "\n") {
		switch {
		case strings.Contains(line, "NVRM: Xid"):
			r.Xids++
			r.LastXid = strings.TrimSpace(line)
		case strings.Contains(line, "RmInitAdapter failed"):
			r.InitFails++
		}
	}
	r.Verdict = verdict(r)
	return r
}

// verdict says in plain words which layer is at fault, first match wins: no card, card off the bus,
// no driver, a narrow link, the driver failing to bring the chip up, a fault logged, or working.
func verdict(r Report) string {
	if len(r.Cards) == 0 {
		return "no NVIDIA card on the PCI bus: it fell off before the kernel enumerated it, or it is not seated"
	}
	c := r.Cards[0]
	switch {
	case !c.Present:
		return "the card is listed but answers nothing: it has fallen off the bus (a cold power cycle, then check seating and power)"
	case c.Driver == "":
		return "the card is on the bus but no driver is bound (nvidia module not loaded, or its probe refused the card)"
	case c.MaxWidth > 0 && c.Width > 0 && c.Width < c.MaxWidth:
		s := fmt.Sprintf("the link came up at x%d of x%d: physical , reseat the card, check the riser and the slot", c.Width, c.MaxWidth)
		if r.InitFails > 0 {
			s += fmt.Sprintf("; and the driver cannot bring the chip up (RmInitAdapter failed ×%d), most likely because of that link", r.InitFails)
		}
		return s
	case r.InitFails > 0:
		return fmt.Sprintf("the driver is bound but cannot bring the chip up (RmInitAdapter failed ×%d this boot): power (the 8-pin, the PSU) or a failing card", r.InitFails)
	case len(r.DriverKnows) == 0:
		return "the driver is bound but lists no GPU , it has not finished (or failed) initialising"
	case strings.Contains(r.Probe, "GB,"):
		if r.Xids > 0 {
			return fmt.Sprintf("working now, but %d GPU fault(s) logged this boot , watch it", r.Xids)
		}
		return "working: the card answers and the driver sees it"
	}
	return "the card and driver look present; nvidia-smi says: " + r.Probe
}

// Rows is the report as the Box Status drill-in shows it, verdict first.
func (r Report) Rows() [][2]string {
	rows := [][2]string{{"verdict", r.Verdict}}
	for _, c := range r.Cards {
		link := "?"
		if c.Width > 0 {
			link = fmt.Sprintf("x%d of x%d", c.Width, c.MaxWidth)
			if c.Speed != "" {
				link += " · " + c.Speed
				if c.MaxSpeed != "" && c.MaxSpeed != c.Speed {
					link += " (max " + c.MaxSpeed + ")"
				}
			}
		}
		drv := c.Driver
		if drv == "" {
			drv = "none"
		}
		state := "answers"
		if !c.Present {
			state = "OFF THE BUS"
		}
		rows = append(rows, [2]string{"card " + c.Addr, state + " · driver " + drv}, [2]string{"link", link})
		if c.PowerDraw != "" {
			rows = append(rows, [2]string{"power state", c.PowerDraw})
		}
	}
	known := strings.Join(r.DriverKnows, " ")
	if known == "" {
		known = "none"
	}
	rows = append(rows, [2]string{"driver lists", known}, [2]string{"nvidia-smi", r.Probe})
	if r.InitFails > 0 {
		rows = append(rows, [2]string{"init failures", strconv.Itoa(r.InitFails) + " this boot (each open of the device retries)"})
	}
	if r.Xids > 0 {
		last := r.LastXid
		if len(last) > 160 {
			last = last[len(last)-160:]
		}
		rows = append(rows, [2]string{"GPU faults", strconv.Itoa(r.Xids) + " this boot · last: " + last})
	} else {
		rows = append(rows, [2]string{"GPU faults", "none logged this boot"})
	}
	rows = append(rows, [2]string{"read how", "sysfs and the kernel log only , nothing here opens the card"})
	return rows
}
