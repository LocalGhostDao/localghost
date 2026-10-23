package watchd

// The log disk-guard. watchd measures its log folder and defends the volume against a runaway logger,
// but it stays DUMB about what to delete and smart about when to ASK. Three bands:
//
//   under soft cap:  normal , today+yesterday plain, older gzipped, prune beyond retention.
//   soft..hard:      ask ghost.oracled (the local model) whether we are over-logging and, if so, which
//                    service should drop to a quieter level. watchd applies the model's level advice
//                    over that service's control socket. It does NOT delete recent logs on its own.
//   over hard cap:   the dumb backstop , delete oldest ARCHIVES first, because a full volume wedges the
//                    box and that is worse than losing old compressed logs. Never touches today's open
//                    files; recent logs are protected until the very end.
//
// The model call is failure-tolerant: a short deadline, and if oracled is busy, slow, or (on a locking
// box) gone, watchd falls back to the safe default , protect recent logs, log loudly that it is over
// soft cap and could not get guidance. The model IMPROVES the decision; it is never a dependency for
// staying alive. And the model returns POLICY (lower this service's level), never a delete capability:
// the worst a bad model reply can do is make logging quieter or, at hard cap where watchd acts alone,
// drop an old archive , never eat today's logs on an LLM's say-so.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
)

// DiskGuard checks the log folder against caps and applies the model's advice. Held by watchd; run on
// the same midnight tick as the Roller, plus optionally more often if you want tighter control.
type DiskGuard struct {
	logDir    string
	runDir    string // <mount>/run , to reach service sockets and oracled
	softCapMB int
	hardCapMB int
	cohort    []string // service names, so the guard can lower a specific one's level
	log       Slogger
}

// Slogger is the minimal logger the guard needs (watchd's jlog satisfies it).
type Slogger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// NewDiskGuard builds a guard. Caps come from watchd.conf (svcconf.Base).
func NewDiskGuard(logDir, runDir string, softCapMB, hardCapMB int, cohort []string, log Slogger) *DiskGuard {
	return &DiskGuard{
		logDir: logDir, runDir: runDir, softCapMB: softCapMB, hardCapMB: hardCapMB,
		cohort: cohort, log: log,
	}
}

// Check measures the folder and acts per band. Called after each archive+prune pass.
func (g *DiskGuard) Check() {
	sizeMB := int(g.folderBytes() / (1 << 20))
	switch {
	case sizeMB >= g.hardCapMB:
		g.log.Warn("log folder over HARD cap, pruning oldest archives", "fn", "Check",
			"sizeMB", sizeMB, "hardCapMB", g.hardCapMB)
		g.enforceHardCap(sizeMB)
	case sizeMB >= g.softCapMB:
		g.log.Warn("log folder over soft cap, asking oracle for guidance", "fn", "Check",
			"sizeMB", sizeMB, "softCapMB", g.softCapMB)
		g.askOracleAndApply(sizeMB)
	default:
		// under soft cap , nothing to do
	}
}

// folderBytes sums the log dir (including archive/).
func (g *DiskGuard) folderBytes() int64 {
	var total int64
	_ = filepath.Walk(g.logDir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// oracleAdvice is the structured reply we ask the model for , per-service level directives. The model
// decides POLICY; watchd executes it through the control sockets.
type oracleAdvice struct {
	OverLogging bool `json:"overLogging"`
	Directives  []struct {
		Service string `json:"service"`
		Level   string `json:"level"` // debug|info|warn|error
	} `json:"directives"`
}

// askOracleAndApply samples recent logs, asks oracled to judge over-logging, and applies any level
// directives. Short deadline; any failure falls back to the safe default (do nothing destructive, the
// hard cap remains the real backstop).
func (g *DiskGuard) askOracleAndApply(sizeMB int) {
	samples := g.sampleRecent(4 << 10) // ~4KB tail per service
	prompt := buildLogTriagePrompt(sizeMB, g.softCapMB, samples)

	oc := oracle.NewClient(g.runDir, 3*time.Second) // short: background triage, drop if slow
	resp, err := oc.Infer(oracle.Request{
		Capability: "classify",
		Class:      oracle.ClassLocalSmall,
		Priority:   oracle.PriorityBackground,
		Input:      prompt,
		DeadlineMS: 2500,
	})
	if err != nil {
		g.log.Warn("oracle unavailable for log triage, holding (recent logs protected)", "fn",
			"askOracleAndApply", "err", err)
		return
	}
	var adv oracleAdvice
	if err := json.Unmarshal([]byte(resp.Output), &adv); err != nil {
		g.log.Warn("oracle reply not parseable, holding", "fn", "askOracleAndApply", "err", err)
		return
	}
	if !adv.OverLogging || len(adv.Directives) == 0 {
		g.log.Info("oracle: not over-logging or no action; holding", "fn", "askOracleAndApply")
		return
	}
	for _, d := range adv.Directives {
		if !knownService(g.cohort, d.Service) {
			continue
		}
		cli := ctlsock.NewClientTimeout(d.Service, g.runDir, 3*time.Second)
		// the model may only make a daemon QUIETER; the prompt says so, and this is where it is
		// enforced: read the level the daemon has, apply the directive only when it raises the
		// threshold, never "debug" (the audit found a level=debug reply was applied as asked)
		cur, err := cli.Call("log-level", nil)
		if err != nil {
			g.log.Warn("could not read the current level; directive dropped", "fn", "askOracleAndApply",
				"svc", d.Service, "err", err)
			continue
		}
		want := strings.ToLower(strings.TrimSpace(d.Level))
		if !quieter(cur.Text, want) {
			g.log.Warn("refused oracle level directive: it would not raise the threshold", "fn", "askOracleAndApply",
				"svc", d.Service, "have", cur.Text, "want", d.Level)
			continue
		}
		if _, err := cli.Call("log-level", map[string]any{"level": want}); err != nil {
			g.log.Warn("could not apply oracle level directive", "fn", "askOracleAndApply",
				"svc", d.Service, "level", d.Level, "err", err)
			continue
		}
		g.log.Info("applied oracle level directive", "fn", "askOracleAndApply",
			"svc", d.Service, "level", d.Level)
	}
}

// enforceHardCap deletes oldest archives until back under the hard cap or archives are exhausted. It
// never touches today's/yesterday's plain files , recent logs are protected even here; the model is
// not consulted (over hard cap is an emergency, act immediately).
func (g *DiskGuard) enforceHardCap(sizeMB int) {
	archDir := filepath.Join(g.logDir, "archive")
	entries, err := os.ReadDir(archDir)
	if err != nil {
		return
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files) // dated names sort oldest-first
	for _, f := range files {
		if int(g.folderBytes()/(1<<20)) < g.hardCapMB {
			return
		}
		_ = os.Remove(filepath.Join(archDir, f))
		g.log.Warn("hard cap: removed old archive", "fn", "enforceHardCap", "file", f)
	}
	// If we deleted every archive and are STILL over hard cap, the recent plain logs are the cause.
	// We still do not delete them , shout instead; a box logging faster than the hard cap with only
	// 2 days of plain logs has a real problem the operator must see.
	if int(g.folderBytes()/(1<<20)) >= g.hardCapMB {
		g.log.Error("STILL over hard cap after clearing archives; recent logs are the cause and are "+
			"NOT being deleted , investigate a runaway logger", "fn", "enforceHardCap")
	}
}

// sampleRecent returns the tail of each service's current-day log, keyed by service name.
func (g *DiskGuard) sampleRecent(tailBytes int64) map[string]string {
	out := map[string]string{}
	td := today()
	for _, svc := range g.cohort {
		p := filepath.Join(g.logDir, svc+"-"+td+".log")
		if tail, err := tailFile(p, tailBytes); err == nil && tail != "" {
			out[svc] = tail
		}
	}
	return out
}

func tailFile(path string, n int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	start := int64(0)
	if fi.Size() > n {
		start = fi.Size() - n
	}
	buf := make([]byte, fi.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err.Error() != "EOF" {
		return "", err
	}
	return string(buf), nil
}

func knownService(cohort []string, name string) bool {
	for _, s := range cohort {
		if s == name {
			return true
		}
	}
	return false
}

// buildLogTriagePrompt asks the model to judge over-logging and return STRICT JSON. Kept terse; the
// model just needs the samples and the ask. The response contract is oracleAdvice.
func buildLogTriagePrompt(sizeMB, softCapMB int, samples map[string]string) string {
	b, _ := json.Marshal(samples)
	return `You are the log janitor for a small always-on appliance. The log folder is ` +
		itoa(sizeMB) + `MB, over its soft cap of ` + itoa(softCapMB) + `MB. Below are recent log tails ` +
		`per service as JSON. Decide if any service is over-logging (verbose, repetitive, low-value at ` +
		`its current level). Reply with STRICT JSON only, no prose: ` +
		`{"overLogging":bool,"directives":[{"service":"<name>","level":"debug|info|warn|error"}]}. ` +
		`Only include a directive to RAISE a noisy service's threshold (e.g. debug->info) to reduce ` +
		`volume; never lower a threshold. Samples: ` + string(b)
}

// itoa is a tiny int->string to keep the prompt builder allocation-light and dependency-free.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// levelRank orders the thresholds; a higher rank is quieter, -1 is a string no daemon knows.
func levelRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return 0
	case "info":
		return 1
	case "warn":
		return 2
	case "error":
		return 3
	}
	return -1
}

// quieter reports whether moving a daemon from have to want only RAISES its threshold.
func quieter(have, want string) bool {
	h, w := levelRank(have), levelRank(want)
	return h >= 0 && w > h
}
