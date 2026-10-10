package secd

// THE SOURCES. What the box draws on beyond the person's own archive, each with its state and,
// when it is not there yet, the way to bring it: the box's Wikipedia, the news feeds, the market
// numbers, the weather, the maps and the heights, the speech engine. One page on the phone
// (SOURCES), one view here.
//
//   GET  /v1/sources                 , every source's state, and the fetch job running if any
//   POST /v1/sources/fetch           , {"step": "wiki"|"maps"|"speech"|"engine"|"weights",
//                                      "region": "34:72,-25:45"|"all"|"" for maps}: tools/update.sh
//                                      <step> started on the box (from the mirror, the one
//                                      source a box takes files from, every byte checked against
//                                      the signed manifest), one at a time; GET says how it goes
//   GET  /v1/news/feeds              , the feeds with how each is doing
//   POST /v1/news/feeds              , {"add": {"name", "url"}} | {"remove": id} | {"enable": id,
//                                      "on": bool}: the list the box fetches from, changed
//
// A fetch is the same update.sh the operator runs by hand (tools/README.md), run by secd as root
// with its output kept under /var/lib/ghost/update/jobs on the OS disk (what came from the mirror
// and whether it verified: nothing of the person's). The box has no other door to the internet
// after setup, and this one opens only on a tap from an unlocked phone.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/update"
	"github.com/LocalGhostDao/localghost/server/internal/voiced"
)

// sourceDoc is one source as the phone shows it.
type sourceDoc struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	State  string `json:"state"`            // ready | partial | importing | missing | off | unknown
	Line   string `json:"line"`             // one line: what the box has
	Detail string `json:"detail,omitempty"` // a second line: how it is kept current, what is missing
	Action string `json:"action,omitempty"` // the update.sh step that brings or refreshes it, "" for none
	Label  string `json:"label,omitempty"`  // the action's words ("fetch from the mirror")
	Open   string `json:"open,omitempty"`   // the page on the phone it opens: wikipedia | news | crypto | map | ""
	Size   int64  `json:"bytes,omitempty"`
	// From is where the integration draws from, one row a place: the feeds, the exchanges, the
	// service, the data sets, the mirror. The phone's INTEGRATIONS cards list them.
	From []sourceFrom `json:"from,omitempty"`
}

// sourceFrom is one place an integration draws from.
type sourceFrom struct {
	Name  string `json:"name"`
	Role  string `json:"role"`            // what comes from it, and how often
	State string `json:"state,omitempty"` // ok | late | flaky | off | "" when not watched
}

// fetchJob is an update.sh run started from the phone.
type fetchJob struct {
	Step      string `json:"step"`
	Region    string `json:"region,omitempty"`
	StartedAt int64  `json:"startedAt"`
	EndedAt   int64  `json:"endedAt,omitempty"`
	Running   bool   `json:"running"`
	Exit      int    `json:"exit"`
	Last      string `json:"last"` // the last line of its output
	Log       string `json:"log,omitempty"`
}

type fetchState struct {
	mu  sync.Mutex
	job *fetchJob
	cmd *exec.Cmd
	// run replaces the real update.sh in tests
	run func(step, region, logPath string) (*exec.Cmd, error)
}

var fetchStepRE = regexp.MustCompile(`^(wiki|maps|speech|engine|weights|phone|embedder)$`)
var fetchRegionRE = regexp.MustCompile(`^(all|-?\d{1,2}:-?\d{1,2},-?\d{1,3}:-?\d{1,3})?$`)

// updateScript is where update.sh lives on a box: the release's tools, which redeploy.sh and
// install.sh both lay under /opt/localghost/tools.
func updateScript() string { return filepath.Join(update.BoxPaths().Tools, "update.sh") }

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	mount := fmt.Sprintf("%s/mnt/slot%d", s.cfg.StateDir, mounted)
	runDir := filepath.Join(mount, "run")
	out := map[string]any{"sources": s.sources(mount, runDir), "job": s.fetch.snapshot()}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

// ctlJSON asks a daemon over its control socket and reads the answer's data as a map.
func ctlJSON(daemon, runDir, cmd string, args map[string]any, timeout time.Duration) (map[string]any, bool) {
	resp, err := ctlsock.NewClientTimeout(daemon, runDir, timeout).Call(cmd, args)
	if err != nil || !resp.OK {
		return nil, false
	}
	var m map[string]any
	if json.Unmarshal(resp.Data, &m) != nil {
		return nil, false
	}
	return m, true
}

func num(m map[string]any, k string) float64 {
	if m == nil {
		return 0
	}
	f, _ := m[k].(float64)
	return f
}

func str(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	v, _ := m[k].(string)
	return v
}

func sub(m map[string]any, k string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[k].(map[string]any)
	return v
}

// sources reads every source's state. Each read is short; a daemon that does not answer leaves its
// source "unknown" rather than the page empty.
func (s *Server) sources(mount, runDir string) []sourceDoc {
	var out []sourceDoc

	// WIKIPEDIA, from synthd
	wk, ok := ctlJSON("ghost.synthd", runDir, "wiki", nil, 5*time.Second)
	d := sourceDoc{ID: "wikipedia", Name: "Wikipedia", Open: "wikipedia", Action: "wiki", Label: "fetch from the mirror (about 50 GB)"}
	switch {
	case !ok:
		d.State, d.Line = "unknown", "the box did not say (synthd not up?)"
	default:
		d.Size = int64(num(wk, "bytes"))
		switch str(wk, "state") {
		case "ready":
			d.State = "ready"
			d.Line = fmt.Sprintf("%s · %s articles, %s redirects in the database", str(wk, "edition"), humanCount(int64(num(wk, "articles"))), humanCount(int64(num(wk, "redirects"))))
			d.Detail = "answers \"what is X\" in the chat and the WIKIPEDIA page; a newer edition on the mirror replaces it"
			d.Label = "fetch a newer edition"
		case "importing":
			d.State = "importing"
			pct := 0.0
			if t := num(wk, "entries"); t > 0 {
				pct = 100 * num(wk, "imported") / t
			}
			d.Line = fmt.Sprintf("importing: %.0f%% of the file read, %s articles in so far", pct, humanCount(int64(num(wk, "articles"))))
			if left := int64(num(wk, "leftMinutes")); left > 0 {
				d.Line += ", about " + leftWords(left*60) + " to go"
			}
			d.Action = ""
		case "downloading":
			d.State, d.Line, d.Action = "importing", "downloading the file from the mirror; imported once it is here", ""
		case "failed":
			d.State, d.Line = "partial", "the import stopped: "+str(wk, "error")
		case "locked":
			d.State, d.Line, d.Action = "unknown", "the box is locked", ""
		default:
			d.State, d.Line = "missing", "not on the box"
			d.Detail = "the English edition without pictures, one file from the mirror, imported into the box's database; the file goes once it is in"
		}
	}
	d.From = []sourceFrom{
		{Name: "LocalGhost mirror", Role: "the one file (about 50 GB), signed, every byte checked"},
		{Name: "Wikipedia, through Kiwix", Role: "the English edition without pictures" + func() string {
			if e := str(wk, "edition"); e != "" {
				return ": " + e
			}
			return ""
		}()},
	}
	out = append(out, d)

	// NEWS, from synthd
	nw, ok := ctlJSON("ghost.synthd", runDir, "news", nil, 5*time.Second)
	d = sourceDoc{ID: "news", Name: "News", Open: "news"}
	if !ok {
		d.State, d.Line = "unknown", "the box did not say"
	} else {
		st := sub(nw, "news")
		feeds, _ := st["feeds"].([]any)
		on := 0
		for _, f := range feeds {
			if fm, _ := f.(map[string]any); fm != nil {
				if en, _ := fm["enabled"].(bool); en {
					on++
				}
			}
		}
		d.State = "ready"
		if on == 0 {
			d.State = "off"
		}
		// the feeds, one row each: the publication, what it answered, on or off
		for _, f := range feeds {
			fm, _ := f.(map[string]any)
			if fm == nil {
				continue
			}
			en, _ := fm["enabled"].(bool)
			row := sourceFrom{Name: str(fm, "name"), State: "ok"}
			switch {
			case !en:
				row.State, row.Role = "off", "switched off"
			case int64(num(fm, "lastOk")) == 0:
				row.State, row.Role = "late", "not answered yet"
			default:
				row.Role = fmt.Sprintf("%d entries, %s ago", int(num(fm, "items")), agoWords(time.Now().Unix()-int64(num(fm, "lastOk"))))
				if num(fm, "failures") > 0 {
					row.State = "flaky"
					row.Role += fmt.Sprintf(", %d failures", int(num(fm, "failures")))
				}
			}
			d.From = append(d.From, row)
		}
		d.Line = fmt.Sprintf("%d feeds on, %d answered in the last 3 h · %s stories today", on, int(num(st, "feedsOkLast3h")), humanCount(int64(num(st, "stories24h"))))
		if at := int64(num(st, "lastFetchAt")); at > 0 {
			d.Detail = "fetched " + agoWords(time.Now().Unix()-at) + " ago, by the phone on Wi-Fi or by the box; the feeds are yours to add and remove"
		} else {
			d.Detail = "not fetched yet; the phone fetches on Wi-Fi, the box when the phone is away"
		}
	}
	out = append(out, d)

	// THE MARKET NUMBERS, from tallyd's monitor
	fm, ok := ctlJSON("ghost.tallyd", runDir, "feeds", nil, 5*time.Second)
	d = sourceDoc{ID: "crypto", Name: "Crypto", Open: "crypto"}
	if !ok {
		d.State, d.Line = "unknown", "the box did not say"
	} else {
		secs, _ := fm["sections"].([]any)
		var lines []string
		state := "ready"
		for _, x := range secs {
			sm, _ := x.(map[string]any)
			if sm == nil {
				continue
			}
			id := str(sm, "id")
			if id == "prices" || id == "market" || id == "venues" || id == "ecb" {
				lines = append(lines, str(sm, "title")+": "+str(sm, "line"))
				if st := str(sm, "state"); st != "ok" && state == "ready" {
					state = "partial"
				}
			}
			// the exchanges, one row each, as the monitor has them
			if id == "venues" {
				rows, _ := sm["rows"].([]any)
				for _, r := range rows {
					rm, _ := r.(map[string]any)
					if rm == nil {
						continue
					}
					d.From = append(d.From, sourceFrom{Name: venueName(str(rm, "k")), Role: str(rm, "v"), State: str(rm, "state")})
				}
			}
		}
		d.From = append(d.From,
			sourceFrom{Name: "Coinbase listing", Role: "which coins exist and their ranks, hourly"},
			sourceFrom{Name: "ECB", Role: "the pound, euro and other rates, once a working day"},
		)
		d.State = state
		d.Line = strings.Join(lines, " · ")
		if d.Line == "" {
			d.Line = str(fm, "state")
		}
		d.Detail = "the box asks the exchanges itself every minute (BTC, ETH and SOL every few seconds) and the ECB daily; nothing from a price service"
	}
	out = append(out, d)

	// THE WEATHER, from tallyd
	ws, ok := ctlJSON("ghost.tallyd", runDir, "weather", nil, 5*time.Second)
	d = sourceDoc{ID: "weather", Name: "Weather", From: []sourceFrom{
		{Name: "Open-Meteo", Role: "the forecasts, a hundred places every two minutes, each again after a day"},
		{Name: "GeoNames", Role: "the place list, from the box's own geo set"},
	}}
	if !ok {
		d.State, d.Line = "unknown", "the box did not say"
	} else {
		table := sub(ws, "table")
		pull := sub(ws, "pull")
		places := int64(num(table, "places"))
		fetched := int64(num(table, "fetchedAt"))
		noGeo, _ := pull["noGeo"].(bool)
		failed := int64(num(pull, "failedSinceStart"))
		batches := int64(num(pull, "batchesSinceStart"))
		readErr := str(pull, "error")
		lastErr := str(sub(pull, "last"), "lastErr")
		switch {
		case places > 0:
			d.State = "ready"
			d.Line = fmt.Sprintf("the forecast of %s places, the newest pulled %s ago", humanCount(places), agoWords(time.Now().Unix()-fetched))
			if failed > 0 && lastErr != "" {
				d.Line += fmt.Sprintf(" · %d of %d batches failed since the start, the last: %s", failed, batches, lastErr)
			}
			d.Detail = "Open-Meteo, the same list every day whoever and wherever you are (the largest town of every 55 km cell of the world, six thousand cells), a hundred every two minutes; where you are is looked up on the box"
		case readErr != "":
			// the place list's query fails on this box: say the error, so the log need not be read
			d.State, d.Line = "missing", readErr
		case noGeo:
			d.State, d.Line = "missing", "nothing to pull: the box's place list has no populations (the geo set is missing, or from before the populations)"
			d.Detail = "a set from before the populations is imported again by ghost.framed at its next start (a redeploy does it); fetching the maps again does the same; the weather pulls within minutes after"
			d.Action, d.Label = "maps", "fetch the maps from the mirror"
		case failed > 0:
			d.State, d.Line = "missing", fmt.Sprintf("the pull fails: %s (%d of %d batches since the start)", lastErr, failed, batches)
			d.Detail = "the box asks api.open-meteo.com itself; the fetch log under BOX STATUS › FEEDS has every try"
		default:
			d.State, d.Line = "missing", "not pulled yet (the first hundred places come a minute and a half after ghost.tallyd starts, then a hundred every two minutes)"
		}
	}
	out = append(out, d)

	out = append(out, mapsDocCached(mount))

	// SPEECH, from voiced's state file
	d = sourceDoc{ID: "speech", Name: "Speech", Action: "speech", Label: "fetch the speech engine and model", From: []sourceFrom{
		{Name: "whisper.cpp", Role: "the engine, built for the box, on the CPU"},
		{Name: "LocalGhost mirror", Role: "the engine and the speech model, signed"},
	}}
	if st, ok := voiced.ReadState(mount); ok && st.Engine != "" {
		d.State, d.Line = "ready", "whisper.cpp with "+st.Engine+" · voice notes and questions asked aloud are heard on the box"
		d.From[1].Role = "the engine and the speech model (" + st.Engine + "), signed"
		d.Label = "refresh from the mirror"
	} else if ok && st.Why != "" {
		d.State, d.Line = "missing", st.Why
	} else {
		d.State, d.Line = "unknown", "ghost.voiced has not said"
	}
	out = append(out, d)
	return out
}

// venueName is an exchange as the phone names it ("binance" → "Binance").
func venueName(k string) string {
	switch k {
	case "okx":
		return "OKX"
	case "":
		return ""
	}
	return strings.ToUpper(k[:1]) + k[1:]
}

func countFiles(dir, suffix string) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") && (suffix == "" || strings.HasSuffix(e.Name(), suffix)) {
			n++
		}
	}
	return n
}

func humanCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1f million", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%d,%03d", n/1000, n%1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func agoWords(secs int64) string {
	switch {
	case secs < 120:
		return "a minute"
	case secs < 3600:
		return fmt.Sprintf("%d minutes", secs/60)
	case secs < 48*3600:
		return fmt.Sprintf("%d hours", secs/3600)
	default:
		return fmt.Sprintf("%d days", secs/86400)
	}
}

func leftWords(secs int64) string {
	switch {
	case secs < 3600:
		return fmt.Sprintf("%d minutes", (secs+59)/60)
	case secs < 48*3600:
		return fmt.Sprintf("%d hours", (secs+1799)/3600)
	default:
		return fmt.Sprintf("%d days", (secs+43199)/86400)
	}
}

// handleSourcesFetch , POST /v1/sources/fetch.
func (s *Server) handleSourcesFetch(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	if _, ok := s.mountedSlot(); !ok {
		s.appearsDown(w)
		return
	}
	var in struct {
		Step   string `json:"step"`
		Region string `json:"region"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil || !fetchStepRE.MatchString(in.Step) || !fetchRegionRE.MatchString(in.Region) {
		http.Error(w, "which step?", http.StatusBadRequest)
		return
	}
	job, err := s.fetch.start(in.Step, in.Region)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "why": err.Error()})
		return
	}
	secdLog.Info("a fetch from the mirror started from the phone", "fn", "handleSourcesFetch", "step", in.Step, "region", in.Region)
	writeJSON(w, map[string]any{"ok": true, "job": job})
}

func (f *fetchState) snapshot() *fetchJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.job == nil {
		return nil
	}
	j := *f.job
	return &j
}

// start runs update.sh <step> in the background, one at a time; its output goes to a log file
// whose last line the job carries.
func (f *fetchState) start(step, region string) (*fetchJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.job != nil && f.job.Running {
		return nil, fmt.Errorf("a fetch is already running (%s)", f.job.Step)
	}
	dir := filepath.Join(update.BoxPaths().State, "jobs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	logPath := filepath.Join(dir, step+".log")
	run := f.run
	if run == nil {
		run = runUpdateScript
	}
	cmd, err := run(step, region, logPath)
	if err != nil {
		return nil, err
	}
	job := &fetchJob{Step: step, Region: region, StartedAt: time.Now().Unix(), Running: true, Log: logPath}
	f.job, f.cmd = job, cmd
	go func() {
		err := cmd.Wait()
		f.mu.Lock()
		defer f.mu.Unlock()
		job.Running = false
		job.EndedAt = time.Now().Unix()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				job.Exit = ee.ExitCode()
			} else {
				job.Exit = -1
			}
		}
		job.Last = lastLineOf(logPath)
		secdLog.Info("the fetch from the mirror ended", "fn", "fetchState", "step", step, "exit", job.Exit, "last", job.Last)
	}()
	go f.follow(job, logPath)
	return job, nil
}

// follow keeps the job's last line current while it runs.
func (f *fetchState) follow(job *fetchJob, logPath string) {
	for {
		time.Sleep(3 * time.Second)
		f.mu.Lock()
		running := job.Running
		if running {
			job.Last = lastLineOf(logPath)
		}
		f.mu.Unlock()
		if !running {
			return
		}
	}
}

func lastLineOf(path string) string {
	fh, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer fh.Close()
	last := ""
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			last = l
		}
	}
	if len(last) > 300 {
		last = last[:300]
	}
	return last
}

func runUpdateScript(step, region, logPath string) (*exec.Cmd, error) {
	script := updateScript()
	if _, err := os.Stat(script); err != nil {
		return nil, fmt.Errorf("%s is not installed (redeploy.sh or install.sh puts the tools there)", script)
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("/bin/bash", script, step)
	cmd.Dir = filepath.Dir(script)
	cmd.Env = append(os.Environ(), "GHOST_MOUNT=/var/lib/ghost/mnt/slot0")
	if step == "maps" && region != "" {
		cmd.Env = append(cmd.Env, "GHOST_GEO_ELEVATION="+region)
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		logf.Close()
		return nil, err
	}
	logf.Close() // the child holds its own descriptor
	return cmd, nil
}

// handleNewsFeeds , GET and POST /v1/news/feeds.
func (s *Server) handleNewsFeeds(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	runDir := fmt.Sprintf("%s/mnt/slot%d/run", s.cfg.StateDir, mounted)
	args := map[string]any{}
	if r.Method == http.MethodPost {
		var in struct {
			Add    *struct{ Name, URL string } `json:"add"`
			Remove string                      `json:"remove"`
			Enable string                      `json:"enable"`
			On     *bool                       `json:"on"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch {
		case in.Add != nil:
			id := feedID(in.Add.URL)
			if id == "" || !strings.HasPrefix(in.Add.URL, "https://") || len(in.Add.URL) > 500 {
				writeJSON(w, map[string]any{"ok": false, "why": "a feed is an https address"})
				return
			}
			name := strings.TrimSpace(in.Add.Name)
			if name == "" {
				name = id
			}
			args["add"] = map[string]any{"id": id, "name": clipRunes(name, 60), "url": strings.TrimSpace(in.Add.URL)}
		case in.Remove != "":
			args["remove"] = in.Remove
		case in.Enable != "" && in.On != nil:
			args["enable"], args["on"] = in.Enable, *in.On
		default:
			http.Error(w, "add, remove or enable", http.StatusBadRequest)
			return
		}
	} else if r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	resp, err := ctlsock.NewClientTimeout("ghost.synthd", runDir, 10*time.Second).Call("news", args)
	if err != nil || !resp.OK {
		why := "the box did not answer"
		if err == nil {
			why = resp.Err
		}
		writeJSON(w, map[string]any{"ok": false, "why": why})
		return
	}
	var m map[string]any
	_ = json.Unmarshal(resp.Data, &m)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "news": m["news"]})
}

// feedID is a feed's id from its address: the host without www, and the first path word when the
// host alone would not tell feeds of one site apart ("theguardian-world").
func feedID(u string) string {
	u = strings.TrimSpace(strings.ToLower(u))
	if !strings.HasPrefix(u, "https://") {
		return ""
	}
	u = strings.TrimPrefix(u, "https://")
	host, path, _ := strings.Cut(u, "/")
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return ""
	}
	// the top-level domain off, and a second-level one with it (bbci.co.uk → bbci)
	labels = labels[:len(labels)-1]
	if len(labels) > 1 {
		switch labels[len(labels)-1] {
		case "co", "com", "org", "net", "gov", "ac", "edu":
			labels = labels[:len(labels)-1]
		}
	}
	host = cleanID(labels[len(labels)-1]) // the site's own name: feeds.content.dowjones → dowjones
	if host == "" {
		return ""
	}
	first := ""
	for _, p := range strings.Split(path, "/") {
		p = cleanID(strings.TrimSuffix(strings.TrimSuffix(p, ".xml"), ".rss"))
		if p != "" && p != "rss" && p != "feed" && p != "feeds" && p != "xml" && p != "news" {
			first = p
			break
		}
	}
	if first != "" {
		return host + "-" + first
	}
	return host
}

func cleanID(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if r == '-' || r == '_' {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// handleWeather , GET /v1/weather?lat=&lon= , the forecast nearest the phone's last fix, from the
// box's daily pull (HOME's weather card). The position goes to the box and nowhere else: the
// table was pulled for the world's larger places without it.
func (s *Server) handleWeather(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	runDir := fmt.Sprintf("%s/mnt/slot%d/run", s.cfg.StateDir, mounted)
	args := map[string]any{}
	var lat, lon float64
	if _, err := fmt.Sscan(r.URL.Query().Get("lat"), &lat); err == nil {
		if _, err := fmt.Sscan(r.URL.Query().Get("lon"), &lon); err == nil && (lat != 0 || lon != 0) {
			args["lat"], args["lon"] = lat, lon
		}
	}
	m, ok := ctlJSON("ghost.tallyd", runDir, "weather", args, 8*time.Second)
	if !ok {
		writeJSON(w, map[string]any{"ok": false, "why": "the box did not answer"})
		return
	}
	out := map[string]any{"ok": true, "table": m["table"], "noGeo": m["noGeo"]}
	if f, has := m["forecast"]; has {
		out["forecast"], out["text"] = f, m["text"]
	} else if t, has := m["text"]; has {
		out["text"] = t
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

// mapsDocCached is mapsDoc at most once a minute: the card counts the tiles with a directory
// read each (a million road tiles on a box with the world's streets), too much for every poll
// of the page; a fetch that lands shows within the minute.
var mapsCache struct {
	mu    sync.Mutex
	mount string
	at    time.Time
	doc   sourceDoc
}

func mapsDocCached(mount string) sourceDoc {
	mapsCache.mu.Lock()
	defer mapsCache.mu.Unlock()
	if mapsCache.mount == mount && time.Since(mapsCache.at) < time.Minute {
		return mapsCache.doc
	}
	mapsCache.mount, mapsCache.at, mapsCache.doc = mount, time.Now(), mapsDoc(mount)
	return mapsCache.doc
}

// mapsDoc is the INTEGRATIONS card for the maps and the heights, read off the volume.
func mapsDoc(mount string) sourceDoc {
	// THE MAPS AND THE HEIGHTS, from the volume: the coast and the roads are the tiles framed
	// cut (<mount>/landtiles/*.lgt, <mount>/roadtiles/{0,1}/*.lgr, the paths the map is served
	// from); the heights and the time zones sit under <mount>/geo as the mirror's sets
	geo := filepath.Join(mount, "geo")
	coast := countFiles(filepath.Join(mount, "landtiles"), ".lgt")
	roads := countFiles(filepath.Join(mount, "roadtiles", "0"), ".lgr") + countFiles(filepath.Join(mount, "roadtiles", "1"), ".lgr")
	packs := countFiles(filepath.Join(geo, "elevation"), ".heights")
	tiles := countFiles(filepath.Join(geo, "elevation"), ".tif")
	_, tz := os.Stat(filepath.Join(geo, "tz", "grid.bin"))
	d := sourceDoc{ID: "maps", Name: "Maps and heights", Open: "map", Action: "maps", Label: "fetch the maps from the mirror", From: []sourceFrom{
		{Name: "OpenStreetMap", Role: "the coastline, from the land polygons"},
		{Name: "Geofabrik", Role: "the roads, from extracts cut into tiles on the box"},
		{Name: "GeoNames", Role: "the places, with their populations"},
		{Name: "Copernicus DEM", Role: "the ground's height at 90 m, in packs of a 30-degree block"},
		{Name: "LocalGhost mirror", Role: "all of it, signed, at setup or when you ask here"},
	}}
	var parts []string
	if coast > 0 {
		parts = append(parts, fmt.Sprintf("coastline %s tiles", humanCount(int64(coast))))
	}
	if roads > 0 {
		parts = append(parts, fmt.Sprintf("roads %s tiles", humanCount(int64(roads))))
	}
	switch {
	case packs > 0:
		parts = append(parts, fmt.Sprintf("heights %d packs", packs))
	case tiles > 0:
		parts = append(parts, fmt.Sprintf("heights %s tiles", humanCount(int64(tiles))))
	}
	if tz == nil {
		parts = append(parts, "time zones")
	}
	switch {
	case len(parts) == 0:
		d.State, d.Line = "missing", "no map data on the box"
	case packs == 0 && tiles == 0 || tz != nil:
		d.State, d.Line = "partial", strings.Join(parts, " · ")
		var missing []string
		if packs == 0 && tiles == 0 {
			missing = append(missing, "the heights")
		}
		if tz != nil {
			missing = append(missing, "the time zones")
		}
		d.Detail = "missing: " + strings.Join(missing, " and ") + " · the heights are asked for by region (yours by default)"
	default:
		d.State, d.Line = "ready", strings.Join(parts, " · ")
		d.Label = "refresh from the mirror"
	}
	return d
}
