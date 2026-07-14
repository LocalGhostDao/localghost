package secd

// /v1/status , the supervised-daemon roster for the app's Box Status screen. secd does not supervise
// the cohort itself (ghost.watchd does); this proxies watchd's snapshot, fetched over the control
// socket by the backend, into the {"services":[...]} shape the app parses. Session-authenticated and
// appears-down on every rejection, exactly like the other authenticated endpoints , a locked box or a
// missing session is indistinguishable from the box being down.

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"context"
	"fmt"
	"math"
	"sync"
	"syscall"
	"time"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"encoding/json"
	"net/http"
)

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) {
		secdLog.Warn("status rejected: invalid session", "fn", "handleStatus", "bearerPresent", bearer(r) != "")
		s.appearsDown(w)
		return
	}
	if r.Method != http.MethodGet {
		secdLog.Warn("status rejected: wrong method", "fn", "handleStatus", "method", r.Method)
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		secdLog.Warn("status rejected: box locked", "fn", "handleStatus")
		s.appearsDown(w) // locked: no daemons to report, and we do not reveal lock state anyway
		return
	}
	// watchd's live snapshot , the same data ghost-cli and health.sh see.
	services := s.unlock.SupervisorStatus()
	if services == nil {
		services = []ServiceStatus{} // never null: the app expects an array, empty means "none reported"
	}
	// Enrich each service with ITS OWN live health detail , watchd's supervisor view says the
	// process runs; the daemon's /health says whether it is WELL (searchd names its job backlog,
	// framed its frame backlog, oracled "model loading"). 250ms per probe, in parallel: a hung
	// daemon costs the status call a quarter second, not a timeout.
	ports := hw.SupervisedDaemons()
	var wg sync.WaitGroup
	for i := range services {
		port, ok := ports[services[i].Name]
		if !ok || port == 0 {
			continue
		}
		wg.Add(1)
		go func(i, port int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), 250*time.Millisecond)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet,
				fmt.Sprintf("http://127.0.0.1:%d/health", port), nil)
			if err != nil {
				return
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				services[i].Detail = "health: unreachable"
				return
			}
			defer resp.Body.Close()
			var h struct {
				Code   uint8  `json:"code"`
				Detail string `json:"detail"`
				Name   string `json:"name"`
			}
			if json.NewDecoder(resp.Body).Decode(&h) != nil {
				return
			}
			if h.Name != "" && h.Name != services[i].Name {
				services[i].Detail = "health: WRONG SERVICE on port (" + h.Name + ")"
				return
			}
			services[i].Detail = h.Detail
			if h.Code > services[i].Code {
				services[i].Code = h.Code // the daemon's own verdict can only worsen watchd's
			}
		}(i, port)
	}
	wg.Wait()
	// Volume space , the one number that quietly kills everything else when it hits zero.
	var vol map[string]any
	var st syscall.Statfs_t
	mountPath := fmt.Sprintf("%s/mnt/slot%d", s.cfg.StateDir, mounted)
	if err := syscall.Statfs(mountPath, &st); err == nil {
		freeGB := float64(st.Bavail) * float64(st.Bsize) / (1 << 30)
		totalGB := float64(st.Blocks) * float64(st.Bsize) / (1 << 30)
		vol = map[string]any{
			"freeGB":  math.Round(freeGB*10) / 10,
			"totalGB": math.Round(totalGB*10) / 10,
		}
	}
	// HOST VITALS , the machine under everything. Load and memory from /proc (no exec, no cost);
	// GPU from nvidia-smi under a hard 400ms timeout (a wedged driver must not stall status). The
	// GPU block is ABSENT rather than zeroed when not visible , "no GPU reported" and "GPU at 0%"
	// are different facts and the app should not have to guess which one it is looking at.
	host := map[string]any{"cores": runtime.NumCPU()}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			if v, perr := strconv.ParseFloat(f[0], 64); perr == nil {
				host["load1"] = v
			}
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		var totalKB, availKB float64
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			if f[0] == "MemTotal:" {
				totalKB, _ = strconv.ParseFloat(f[1], 64)
			}
			if f[0] == "MemAvailable:" {
				availKB, _ = strconv.ParseFloat(f[1], 64)
			}
		}
		if totalKB > 0 {
			host["memTotalGB"] = math.Round(totalKB/1048576*10) / 10
			host["memUsedGB"] = math.Round((totalKB-availKB)/1048576*10) / 10
		}
	}
	if var2 := (syscall.Statfs_t{}); syscall.Statfs("/", &var2) == nil {
		host["rootFreeGB"] = math.Round(float64(var2.Bavail)*float64(var2.Bsize)/(1<<30)*10) / 10
	}
	{
		gctx, gcancel := context.WithTimeout(r.Context(), 400*time.Millisecond)
		out, gerr := exec.CommandContext(gctx, "nvidia-smi",
			"--query-gpu=memory.used,memory.total,utilization.gpu", "--format=csv,noheader,nounits").Output()
		gcancel()
		if gerr == nil {
			f := strings.Split(strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]), ",")
			if len(f) == 3 {
				usedMB, e1 := strconv.ParseFloat(strings.TrimSpace(f[0]), 64)
				totalMB, e2 := strconv.ParseFloat(strings.TrimSpace(f[1]), 64)
				util, e3 := strconv.ParseFloat(strings.TrimSpace(f[2]), 64)
				if e1 == nil && e2 == nil && e3 == nil {
					host["gpu"] = map[string]any{
						"vramUsedGB":  math.Round(usedMB/1024*10) / 10,
						"vramTotalGB": math.Round(totalMB/1024*10) / 10,
						"util":        util,
					}
				}
			}
		}
	}
	// Datastores get LIVE probes, not process-is-running optimism: a Postgres that accepts
	// connections but cannot answer SELECT 1 is down where it counts, and this is where it
	// should say so , named, on the status screen, not as mystery query errors elsewhere.
	pgErr, redisErr := s.notif.DatastoreHealth(mounted)
	datastores := []map[string]string{
		{"name": "postgres", "state": dsState(pgErr), "detail": pgErr},
		{"name": "redis", "state": dsState(redisErr), "detail": redisErr},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"services": services, "datastores": datastores, "volume": vol, "host": host})
}

func dsState(errText string) string {
	if errText == "" {
		return "ok"
	}
	return "failed"
}
