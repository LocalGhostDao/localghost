// ghost.oracled is the box's inference broker: the model-agnostic front for anything that talks to a
// model. Callers submit capability+class requests over its control socket; oracled queues them
// (interactive before background, deadlines drop stale work), routes to a backend resolved from the
// class, and returns the result. The local backend runs llama.cpp's llama-server as a PRIVATE
// loopback child, weights loaded from the encrypted volume , so the model runs directly, exposed to
// nothing, and dies with the mount. Swapping gemma for a frontier model is a conf change here, not a
// change in any caller.
//
// Runs only while UNLOCKED (weights and conf are on the encrypted volume). Started by ghost.watchd
// from <mount>/bin/ghost.oracled; logs to <mount>/logs/ghost.oracled-YYYY-MM-DD.log.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/ghosthealth"
	"github.com/LocalGhostDao/localghost/server/internal/harden"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/oracled"
	"github.com/LocalGhostDao/localghost/server/internal/rotlog"
	"github.com/LocalGhostDao/localghost/server/internal/streamsock"
	"github.com/LocalGhostDao/localghost/server/internal/svcconf"
)

const service = "ghost.oracled"

// conf is ghost.oracled's config: the base keys plus the model wiring. Weights paths are on the
// encrypted volume.
type conf struct {
	svcconf.Base
	QueueDepth int      `json:"queueDepth"` // max waiting requests before backpressure
	LlamaBin   string   `json:"llamaBin"`   // llama-server binary path
	ModelPath  string   `json:"modelPath"`  // gemma gguf on the volume
	MmprojPath string   `json:"mmprojPath"` // multimodal projector on the volume (optional)
	ModelName  string   `json:"modelName"`  // reported in responses
	LlamaPort  int      `json:"llamaPort"`  // loopback port for the private llama-server
	ExtraArgs  []string `json:"extraArgs"`  // tuning flags appended verbatim (threads, ctx, cache types, mlock...)
	// the longest side, in pixels, of an image the model is shown (0: 1024). A full-size phone
	// photo aborts the mirror's llama.cpp v0.5.0; oracled scales every image down first.
	ImageMaxSide int `json:"imageMaxSide"`
	// THE ARBITER'S OWN MODEL (internal/oracled/lanes.go): a second model, from another lab than
	// the main one, that ghost.shadowd's cold read is asked of. Empty path: the first
	// ai-models/arbiter-*.gguf on the volume, and a box with none reads with its main model and
	// says so. It runs on the CPU beside the main model; arbiterArgs are appended to its flags
	// (threads, context; "-ngl 99" for a box with a card to spare).
	ArbiterModelPath string   `json:"arbiterModelPath"`
	ArbiterModelName string   `json:"arbiterModelName"` // reported in responses; empty: the file's name
	ArbiterPort      int      `json:"arbiterPort"`      // loopback port for its llama-server
	ArbiterArgs      []string `json:"arbiterArgs"`
}

func defaultConf(mount string) conf {
	c := conf{
		Base:       svcconf.DefaultBase(),
		QueueDepth: 64,
		// The engine lives ON THE ENCRYPTED VOLUME with everything else , seeded to <mount>/bin at
		// provision (hw.ExtraSeeded), invisible when locked, dies with the mount. A host path here
		// would also be a namespace trap: anything under /home does not exist inside secd's world.
		LlamaBin:   filepath.Join(mount, "bin", "llama-server"),
		ModelPath:  filepath.Join(mount, "ai-models", "gemma-4-12b-it-Q4_K_M.gguf"),
		MmprojPath: filepath.Join(mount, "ai-models", "mmproj-F16.gguf"),
		ModelName:  "gemma-4-12b",
		LlamaPort:  18080, // loopback only; not advertised
		// 18081 is searchd's embedder
		ArbiterPort: 18082,
	}
	return c
}

// runDirOf mirrors the cohort convention: GHOST_RUN_DIR when set, else <mount>/run.
func runDirOf(mount string) string {
	if rd := os.Getenv("GHOST_RUN_DIR"); rd != "" {
		return rd
	}
	return filepath.Join(mount, "run")
}

func main() {
	harden.NoDump() // same-user processes cannot read this one through /proc; no core file
	port := flag.Int("health-port", envPort("GHOST_HEALTH_PORT"), "loopback health port")
	mount := flag.String("mount", os.Getenv("GHOST_MOUNT"), "encrypted volume mount path")
	flag.Parse()
	if *mount == "" {
		// watchd sets GHOST_LOG_DIR as <mount>/logs; derive mount if --mount absent.
		if ld := os.Getenv("GHOST_LOG_DIR"); ld != "" {
			*mount = filepath.Dir(ld)
		}
	}
	if *mount == "" {
		log.Fatalf("%s: --mount (or GHOST_MOUNT) is required", service)
	}

	// Logging through the self-rotating writer.
	logDir := filepath.Join(*mount, "logs")
	var lg *slog.Logger
	var lvl *slog.LevelVar
	if w, err := rotlog.New(logDir, service); err == nil {
		defer w.Close()
		lg, lvl = rotlog.Logger(w)
	} else {
		log.Fatalf("%s: open log: %v", service, err)
	}

	// Config: seed defaults, load the conf file over them.
	cfg := defaultConf(*mount)
	confPath := svcconf.Path(*mount, service)
	if err := svcconf.Load(confPath, &cfg); err != nil {
		lg.Warn("read conf, using defaults", "fn", "main", "err", err)
	}
	svcconf.FillBaseDefaults(&cfg.Base)
	_ = svcconf.ApplyLevel(lvl, cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Broker + the local llama-server backend, started EAGERLY so the weights-load cost is paid now
	// (on unlock) rather than on a user's first request.
	lanes := oracled.NewLanes(cfg.QueueDepth, lg)
	broker := lanes.Main
	llama := oracled.NewLlamaBackend(oracled.LlamaConfig{
		BinPath:    cfg.LlamaBin,
		ModelPath:  cfg.ModelPath,
		MmprojPath: cfg.MmprojPath,
		Port:       cfg.LlamaPort,
		ExtraArgs:  cfg.ExtraArgs,
		ModelName:  cfg.ModelName,
		// the last load's measured times, beside the conf: the next unlock's time-left estimate
		LoadTimesPath: filepath.Join(filepath.Dir(confPath), "ghost.oracled.load.json"),
		// the images the engine died on, beside the conf (strikes.go)
		StrikesPath: filepath.Join(filepath.Dir(confPath), "ghost.oracled.image-strikes"),
	})
	if s := cfg.ImageMaxSide; s >= 256 && s <= 2048 {
		oracled.ImageMaxSide = s
	}
	var modelReady atomic.Bool
	var modelWhy atomic.Value // why the model is not up, for the health line
	modelWhy.Store("model loading")
	// a main model on the CPU shares its cores with the arbiter's: there a person's chat sets
	// the arbiter's reading aside too. On the card the two do not meet.
	lanes.Shares = func() bool { return !llama.Engine().OnGPU() }
	// The arbiter's own model, when the box has one: started once the main model is up (two
	// loads at once from one disk help neither), kept up, started again when it dies.
	arbPath, arbName := cfg.ArbiterModelPath, cfg.ArbiterModelName
	if arbPath == "" {
		arbPath, _ = oracled.PickArbiter(filepath.Join(*mount, "ai-models"))
	}
	if arbPath != "" && arbName == "" {
		arbName = oracled.ArbiterName(arbPath)
	}
	var arbStop func()
	var arbStats func() oracled.StatsSummary // nil on a box with one model
	if arbPath == "" {
		lg.Info("no second model for the arbiter on this box (no ai-models/arbiter-*.gguf): ghost.shadowd's cold read is by the main model, and says so", "fn", "main")
	} else {
		arb := oracled.NewArbiterBackend(cfg.LlamaBin, arbPath, arbName, cfg.ArbiterPort, cfg.ArbiterArgs)
		arbStop = arb.Stop
		arbStats = arb.Stats
		go func() {
			for !modelReady.Load() {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
			}
			lanes.KeepArbiter(ctx, arb)
		}()
	}
	go func() {
		// A start that fails is tried again, not given up on: on 29 Sep 2026 a new engine did not
		// come up once and oracled sat there for hours without a model, captions and chat quietly
		// failing, the reason only in secd's journal. Now the reason is in this log and on the
		// health line, and the start is tried again after 30 s, 1, 2, 5, then every 10 minutes (a
		// GPU still releasing the old child's memory is one reason that clears by itself).
		backoff := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}
		crashes := 0 // deaths after ready, in a row; a child that stays up 10 minutes resets it
		for {
			for try := 0; ; try++ {
				lg.Info("starting llama-server child", "fn", "main", "model", cfg.ModelName, "try", try+1)
				err := llama.Start(ctx)
				if err == nil {
					break
				}
				if ctx.Err() != nil {
					return
				}
				if dropped, ok := llama.DropRejectedArg(err); ok {
					// a tuning flag from conf this llama.cpp does not know: without it, at once
					lg.Warn("llama-server does not know a flag from conf/ghost.oracled.conf extraArgs , starting without it; take it out of the conf", "fn", "main", "dropped", dropped, "try", try+1)
					try--
					continue
				}
				wait := backoff[len(backoff)-1]
				if try < len(backoff) {
					wait = backoff[try]
				}
				why := err.Error()
				if len(why) > 400 {
					why = why[:400]
				}
				modelWhy.Store("model not running (try " + strconv.Itoa(try+1) + ", next in " + wait.String() + "): " + why)
				lg.Error("llama-server did not become ready", "fn", "main", "try", try+1, "retryIn", wait.String(), "err", err)
				llama.Stop() // a child still running but never healthy is ended before the next try
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
			broker.SetBackend(oracle.ClassLocalSmall, llama)
			modelReady.Store(true)
			lg.Info("model ready", "fn", "main", "model", cfg.ModelName)
			// ...and WATCHED. On 29 Sep 2026 llama-server died on the first caption after a start (EOF,
			// then "connection refused" on every request) while this health line kept saying "ok":
			// nothing looked at the child once it was ready. Now its death is logged with its exit
			// state (a signal: the OOM killer, a crash) and its last lines, the health line says so,
			// and it is started again, with the same backoff when it keeps dying soon after starting.
			died, how := llama.Died()
			if died == nil {
				return
			}
			upAt := time.Now()
			select {
			case <-ctx.Done():
				return
			case <-died:
			}
			if ctx.Err() != nil {
				return // our own shutdown stopped it
			}
			modelReady.Store(false)
			why := how()
			if time.Since(upAt) > 10*time.Minute {
				crashes = 0
			}
			wait := 10 * time.Second
			if crashes > 0 {
				wait = backoff[min(crashes-1, len(backoff)-1)]
			}
			crashes++
			if len(why) > 400 {
				why = why[:400]
			}
			modelWhy.Store("model died after " + time.Since(upAt).Round(time.Second).String() + " up (starting again in " + wait.String() + "): " + why)
			lg.Error("llama-server died while serving", "fn", "main", "up", time.Since(upAt).Round(time.Second).String(), "inARow", crashes, "restartIn", wait.String(), "err", why)
			llama.Stop() // reaped already: clears the handle for the next start
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}()
	lanes.Run()
	// Shutdown order matters: the MODEL first, then the broker. The broker's Stop waits for its
	// worker, and the worker is usually mid-inference now that captions run all day; waiting on
	// llama to finish a caption before killing llama took longer than watchd's 5s grace, every
	// halt, and the SIGKILL that followed was what "wedged daemon" meant. Killing llama first
	// fails the in-flight request at once, the worker returns, and the broker stops in
	// milliseconds; a bound on the wait keeps a surprise from becoming a wedge again.
	defer func() {
		llama.Stop()
		if arbStop != nil {
			arbStop()
		}
		done := make(chan struct{})
		go func() { lanes.Stop(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			lg.Warn("broker did not stop in 2s; exiting anyway", "fn", "main")
		}
	}()

	// Control socket: base commands + infer + a models command.
	runDir := filepath.Join(*mount, "run")
	ctl := ctlsock.NewServer(service, runDir, lg)
	svcconf.BindBase(ctl, service, lvl, func() (svcconf.Base, map[string]string, error) {
		fresh := defaultConf(*mount)
		if err := svcconf.Load(confPath, &fresh); err != nil {
			return svcconf.Base{}, nil, err
		}
		svcconf.FillBaseDefaults(&fresh.Base)
		// model wiring changes need a restart (cannot reload weights live); report that.
		report := map[string]string{
			"queueDepth": "needs-restart",
			"modelPath":  "needs-restart",
			"llamaPort":  "needs-restart",
			// the arbiter's model: a file put at ai-models/arbiter-<name>.gguf is taken up at
			// the next start of this daemon, like the rest of the model wiring
			"arbiterModelPath": "needs-restart",
			"arbiterPort":      "needs-restart",
			"arbiterArgs":      "needs-restart",
		}
		return fresh.Base, report, nil
	})
	ctl.Handle("infer", func(args json.RawMessage) (ctlsock.Response, error) {
		var req oracle.Request
		if err := json.Unmarshal(args, &req); err != nil {
			return ctlsock.Response{}, err
		}
		ch, ok := lanes.Submit(req)
		if !ok {
			return ctlsock.Response{}, errFull
		}
		resp := <-ch
		data, _ := json.Marshal(resp)
		return ctlsock.Response{OK: resp.Err == "", Err: resp.Err, Data: data}, nil
	})
	// models: which model, whether it is ready, and , the GPU question answered from llama-server's
	// own startup lines and its own per-answer timings , where it runs and how fast. secd folds
	// this into the Box Status drill-in for ghost.oracled; tools/gpu.sh and health.sh print it.
	ctl.Handle("models", func(json.RawMessage) (ctlsock.Response, error) {
		info := llama.Engine()
		st := llama.Stats()
		m := map[string]any{
			"local-small": cfg.ModelName, "ready": modelReady.Load(),
			"engine":  info,
			"verdict": info.Verdict(),
			"stats":   st,
			"speed":   oracled.SpeedVerdict(st.TokPerSecAvg, info),
			"onGPU":   info.OnGPU(),
		}
		// whether it sees images: a text-only engine answers chat and tags, and describes nothing
		vis, why := llama.Vision()
		m["vision"] = vis
		if why != "" {
			m["visionWhy"] = why
		}
		// the arbiter's own model: whether the box has one, whether it is up, and which model
		// a cold read asked for now would be read by
		a := lanes.Arbiter()
		a.Main = cfg.ModelName
		if arbStats != nil {
			st := arbStats()
			a.Answers, a.TokensPerSecond, a.LastAnswerAt = st.Inferences, st.TokPerSecAvg, st.LastAt
		}
		m["arbiter"] = a
		data, _ := json.Marshal(m)
		return ctlsock.Response{OK: true, Data: data}, nil
	})
	defer ctl.Cleanup()
	go func() {
		if err := ctl.Serve(ctx); err != nil {
			lg.Error("control server exited", "fn", "main", "err", err)
		}
	}()

	// Health: OK once the process is up; degraded until the model is ready. secd/watchd poll this.
	rep := ghosthealth.ReporterFunc(func() ghosthealth.Health {
		if modelReady.Load() {
			// text only is still OK (chat and tags work, and the unlock and chat wait on this
			// code), but the detail says so, where Box Status shows it; so is a second model
			// that is not up (the cold read waits for it, then is read by the main model)
			var detail []string
			if vis, why := llama.Vision(); !vis {
				detail = append(detail, "text only, no photo is described: "+why)
			}
			if a := lanes.Arbiter(); a.Has && !a.Up {
				detail = append(detail, "the arbiter's model ("+a.Model+") is not up: "+a.Why)
			}
			return ghosthealth.Health{Code: ghosthealth.OK, Name: service, Detail: strings.Join(detail, "; ")}
		}
		why, _ := modelWhy.Load().(string)
		return ghosthealth.Health{Code: ghosthealth.Degraded, Name: service, Detail: why}
	})
	srv := ghosthealth.NewServer(service, rep)
	// /load , the model's load progress (phase, percent, time left), on the loopback health port:
	// secd reads it for GET /v1/model, which chat polls while the model loads after an unlock
	srv.Handle("/load", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(llama.Load())
	})
	// Streaming chat on the service's UNIX STREAM SOCKET (streamsock) , ctlsock is one-shot and
	// cannot stream, and a loopback TCP port would be "anything on localhost may connect"; the
	// socket carries filesystem permissions and dies with the run dir. This path
	// deliberately bypasses the priority queue (a person is watching tokens appear) and pauses the
	// background lane for as long as it runs (broker.Pause). Events out are
	// our own minimal protocol, one JSON per SSE data line: {"t":"token"} ... {"done":true,"model":x}.
	streamMux := http.NewServeMux()
	streamMux.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Prompt  string         `json:"prompt"`
			Think   string         `json:"think"`
			Image   string         `json:"image,omitempty"`   // base64 jpeg/png , flows to llama as a data URI
			History []oracled.Turn `json:"history,omitempty"` // prior turns, oldest first (synthd's cut)
			// the answer's room when the model thinks first and a long answer was asked for; 0
			// is the think level's own budget (StreamChatBudget bounds it)
			MaxTokens int `json:"maxTokens,omitempty"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&q) != nil || q.Prompt == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// A person is using the model: background work steps aside for the whole answer (a
		// running caption is cancelled and requeued without losing an attempt; none starts until
		// the stream ends, plus a short grace for the follow-up question).
		lanes.Pause()
		defer lanes.Resume()
		out, model, err := llama.StreamChatBudget(r.Context(), q.History, q.Prompt, q.Think, q.Image, q.MaxTokens)
		if err != nil {
			lg.Warn("chat stream start failed", "fn", "chat", "err", err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer out.Close()
		// The stream's timings: llama-server puts them on its final chunk; when it does not, the
		// content deltas and the clock give an estimate (marked as one in the stats).
		streamStart := time.Now()
		tokens := 0
		timed := false
		defer func() {
			if !timed && tokens > 0 {
				llama.EstimateStream("chat", tokens, time.Since(streamStart))
			}
		}()
		fl, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64<<10), 64<<10)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}
			var ev struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
						// Thinking models emit their reasoning HERE, not in content. The first
						// translator only read content, so the entire thinking phase forwarded
						// NOTHING , the app sat on its waiting label for minutes, and a budget
						// exhausted mid-reasoning meant no answer at all. Reasoning now flows as
						// its own event: visible thinking instead of invisible stalling.
						Reasoning string `json:"reasoning_content"`
					} `json:"delta"`
				} `json:"choices"`
				Timings *oracled.Timings `json:"timings"`
			}
			if json.Unmarshal([]byte(data), &ev) != nil {
				continue
			}
			if ev.Timings != nil && ev.Timings.PredictedN > 0 {
				llama.RecordStream("chat", *ev.Timings)
				timed = true
			}
			if len(ev.Choices) == 0 {
				continue
			}
			if r := ev.Choices[0].Delta.Reasoning; r != "" {
				b, _ := json.Marshal(map[string]string{"r": r})
				_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
				if fl != nil {
					fl.Flush()
				}
			}
			if t := ev.Choices[0].Delta.Content; t != "" {
				tokens++
				b, _ := json.Marshal(map[string]string{"t": t})
				_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
				if fl != nil {
					fl.Flush()
				}
			}
		}
		done, _ := json.Marshal(map[string]any{"done": true, "model": model})
		_, _ = w.Write([]byte("data: " + string(done) + "\n\n"))
		if fl != nil {
			fl.Flush()
		}
	})
	go func() {
		if err := streamsock.Serve(service, runDirOf(*mount), streamMux); err != nil {
			lg.Error("stream socket stopped", "fn", "main", "err", err)
		}
	}()
	go func() {
		if err := srv.Serve(*port); err != nil {
			lg.Error("health server stopped", "fn", "main", "err", err)
		}
	}()

	lg.Info("up", "fn", "main", "healthPort", *port, "queueDepth", cfg.QueueDepth)
	<-ctx.Done()
	lg.Info("shutting down", "fn", "main")
}

var errFull = errFullErr{}

type errFullErr struct{}

func (errFullErr) Error() string { return "inference queue full, try again" }

func envPort(key string) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}
