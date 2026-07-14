package watchd

// SYSTEM HEALTH's observability spine , this is watchd's job by design: "the only daemon with read
// access to every other daemon's metrics", writing structured health data down for the app layer to
// read. Every 10 seconds it samples every tracked target , each cohort daemon's own /health, both
// datastores, and the host's vitals , and writes ring buffers into the slot's Redis:
//
//   stats:10s:<name>   list, 600 entries  , the recent picture, 10-second grain
//   stats:1m:<name>    list, 1440 entries , the last 24 hours, minute grain (worst code, avg value)
//   stats:24h:<name>   one JSON blob      , averages computed FROM the minute list every 30 minutes
//
// secd only READS these keys to serve /v1/services/* , the edge does not sample, the health daemon
// does not serve apps; the layering the architecture doc names. Entries are compact JSON
// {"t":unixSec,"c":code,"v":number,"d":"detail"} with v and d optional. Codes come from the daemons'
// own health verdicts; a target that cannot be probed records code 2 with a named reason , absence
// of data is itself data.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/apparedis"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

const (
	stats10sMax = 600
	stats1mMax  = 1440
)

type statEntry struct {
	T int64   `json:"t"`
	C uint8   `json:"c"`
	// V is ALWAYS encoded , 0 is a real reading (an idle GPU, a 0.00 load), not absent data. The
	// first version used omitempty + a nonzero guard in the rollups, which silently excluded
	// genuine zeros and skewed every average upward. Targets without a numeric (the daemons) just
	// carry a constant 0 that the app ignores in favour of the code line.
	V float64 `json:"v"`
	D string  `json:"d,omitempty"`
}

// StatsSampler owns the loop. Construct with NewStatsSampler(mount, log) and run Loop in a
// goroutine; it lives as long as watchd does, which is as long as the volume is unlocked ,
// exactly the window in which its Redis exists.
type StatsSampler struct {
	mount string
	log   *slog.Logger
	rds   *apparedis.ReadWrite
	pg    *poltergres.ReadWrite
}

func NewStatsSampler(mount string, log *slog.Logger) (*StatsSampler, error) {
	cfg, err := hw.LoadServicesConfig(mount)
	if err != nil {
		return nil, fmt.Errorf("stats sampler: services config: %w", err)
	}
	return &StatsSampler{
		mount: mount,
		log:   log,
		rds:   apparedis.NewReadWrite(cfg.Redis.Port, cfg.Redis.RWUser, cfg.Redis.RWPass),
		pg:    poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port, cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name),
	}, nil
}

func (sp *StatsSampler) push(key, entry string, max int) error {
	if err := sp.rds.LPush(key, entry); err != nil {
		return err
	}
	return sp.rds.LTrim(key, 0, max-1)
}

// Loop runs until ctx is cancelled (watchd's shutdown), sampling every 10s.
func (sp *StatsSampler) Loop(ctx context.Context) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	minuteBuf := map[string][]statEntry{}
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		now := time.Now().Unix()
		for name, e := range sp.sampleAll(now) {
			b, _ := json.Marshal(e)
			if err := sp.push("stats:10s:"+name, string(b), stats10sMax); err != nil {
				sp.log.Warn("stats push failed", "fn", "Loop", "target", name, "err", err)
				continue
			}
			minuteBuf[name] = append(minuteBuf[name], e)
		}
		n++
		if n%6 == 0 { // a minute of samples , roll up: worst code, mean value
			for name, buf := range minuteBuf {
				if len(buf) == 0 {
					continue
				}
				roll := statEntry{T: now}
				var sum float64
				var vn int
				for _, e := range buf {
					if e.C > roll.C {
						roll.C = e.C
						roll.D = e.D // keep the detail of the worst moment, that is the story
					}
					sum += e.V // zeros included , an idle reading is a reading
					vn++
				}
				if vn > 0 {
					roll.V = math.Round(sum/float64(vn)*100) / 100
				}
				b, _ := json.Marshal(roll)
				if err := sp.push("stats:1m:"+name, string(b), stats1mMax); err != nil {
					sp.log.Warn("minute rollup push failed", "fn", "Loop", "target", name, "err", err)
				}
				minuteBuf[name] = minuteBuf[name][:0]
			}
		}
		if n%180 == 0 { // every 30 minutes, recompute the 24h averages from the minute lists
			sp.recompute24h()
		}
	}
}

// sampleAll gathers one 10-second snapshot of every tracked target, probes in parallel with tight
// budgets , the whole pass must comfortably fit inside its own interval.
func (sp *StatsSampler) sampleAll(now int64) map[string]statEntry {
	out := map[string]statEntry{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	put := func(name string, e statEntry) {
		e.T = now
		if len(e.D) > 80 {
			e.D = e.D[:80]
		}
		mu.Lock()
		out[name] = e
		mu.Unlock()
	}

	for name, port := range hw.SupervisedDaemons() {
		if port == 0 {
			continue
		}
		wg.Add(1)
		go func(name string, port int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
				fmt.Sprintf("http://127.0.0.1:%d/health", port), nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				put(name, statEntry{C: 2, D: "unreachable"})
				return
			}
			defer resp.Body.Close()
			var h struct {
				Code   uint8  `json:"code"`
				Detail string `json:"detail"`
			}
			if json.NewDecoder(resp.Body).Decode(&h) != nil {
				put(name, statEntry{C: 2, D: "bad health payload"})
				return
			}
			put(name, statEntry{C: h.Code, D: h.Detail})
		}(name, port)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		t0 := time.Now()
		if err := sp.pg.Ping(); err != nil {
			put("postgres", statEntry{C: 2, D: err.Error()})
		} else {
			put("postgres", statEntry{C: 0, V: float64(time.Since(t0).Microseconds()) / 1000})
		}
		t1 := time.Now()
		if err := sp.rds.Ping(); err != nil {
			put("redis", statEntry{C: 2, D: err.Error()})
		} else {
			put("redis", statEntry{C: 0, V: float64(time.Since(t1).Microseconds()) / 1000})
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if b, err := os.ReadFile("/proc/loadavg"); err == nil {
			if f := strings.Fields(string(b)); len(f) > 0 {
				if v, perr := strconv.ParseFloat(f[0], 64); perr == nil {
					c := uint8(0)
					if v > float64(runtime.NumCPU()) {
						c = 1
					}
					put("host.cpu", statEntry{C: c, V: v})
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
				used := (totalKB - availKB) / 1048576
				c := uint8(0)
				if used/(totalKB/1048576) > 0.92 {
					c = 1
				}
				put("host.mem", statEntry{C: c, V: math.Round(used*10) / 10})
			}
		}
		var st syscall.Statfs_t
		if syscall.Statfs("/", &st) == nil {
			free := float64(st.Bavail) * float64(st.Bsize) / (1 << 30)
			c := uint8(0)
			if free < 2 {
				c = 2
			}
			put("host.disk", statEntry{C: c, V: math.Round(free*10) / 10})
		}
		if syscall.Statfs(sp.mount, &st) == nil {
			free := float64(st.Bavail) * float64(st.Bsize) / (1 << 30)
			c := uint8(0)
			if free < 2 {
				c = 2
			}
			put("volume", statEntry{C: c, V: math.Round(free*10) / 10})
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		outB, err := exec.CommandContext(ctx, "nvidia-smi",
			"--query-gpu=memory.used,memory.total,utilization.gpu", "--format=csv,noheader,nounits").Output()
		if err != nil {
			put("host.gpu", statEntry{C: 2, D: "not visible"})
			return
		}
		f := strings.Split(strings.TrimSpace(strings.SplitN(string(outB), "\n", 2)[0]), ",")
		if len(f) != 3 {
			put("host.gpu", statEntry{C: 2, D: "unparseable"})
			return
		}
		used, _ := strconv.ParseFloat(strings.TrimSpace(f[0]), 64)
		total, _ := strconv.ParseFloat(strings.TrimSpace(f[1]), 64)
		util, _ := strconv.ParseFloat(strings.TrimSpace(f[2]), 64)
		c := uint8(0)
		if total > 0 && used/total > 0.97 {
			c = 1
		}
		put("host.gpu", statEntry{C: c, V: util, D: fmt.Sprintf("%.1f/%.1f GB", used/1024, total/1024)})
	}()

	wg.Wait()
	return out
}

// recompute24h derives the averages blob from each minute list , uptime as the fraction of clean
// minutes, mean value, worst detail. Its own key so summary reads are one GET, not a 1440-row scan.
func (sp *StatsSampler) recompute24h() {
	for name := range hw.StatsTargets() {
		rows, err := sp.rds.LRange("stats:1m:"+name, 0, stats1mMax-1)
		if err != nil || len(rows) == 0 {
			continue
		}
		var clean, total, vn int
		var vsum float64
		worst := statEntry{}
		for _, raw := range rows {
			var e statEntry
			if json.Unmarshal([]byte(raw), &e) != nil {
				continue
			}
			total++
			if e.C == 0 {
				clean++
			}
			if e.C > worst.C {
				worst = e
			}
			vsum += e.V // zeros included , see statEntry.V
			vn++
		}
		if total == 0 {
			continue
		}
		blob := map[string]any{
			"uptimePct": math.Round(float64(clean)/float64(total)*1000) / 10,
			"minutes":   total,
		}
		if vn > 0 {
			blob["avgV"] = math.Round(vsum/float64(vn)*100) / 100
		}
		if worst.C > 0 {
			blob["worstCode"] = worst.C
			blob["worstDetail"] = worst.D
		}
		b, _ := json.Marshal(blob)
		if err := sp.rds.Set("stats:24h:"+name, string(b)); err != nil {
			sp.log.Warn("24h blob store failed", "fn", "recompute24h", "target", name, "err", err)
		}
	}
}
