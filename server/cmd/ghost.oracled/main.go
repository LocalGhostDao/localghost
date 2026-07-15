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
	"strings"
	"net/http"
	"context"
	"encoding/json"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/ghosthealth"
	"github.com/LocalGhostDao/localghost/server/internal/streamsock"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/oracled"
	"github.com/LocalGhostDao/localghost/server/internal/rotlog"
	"github.com/LocalGhostDao/localghost/server/internal/svcconf"
)

const service = "ghost.oracled"

// conf is ghost.oracled's config: the base keys plus the model wiring. Weights paths are on the
// encrypted volume.
type conf struct {
	svcconf.Base
	QueueDepth int    `json:"queueDepth"` // max waiting requests before backpressure
	LlamaBin   string `json:"llamaBin"`   // llama-server binary path
	ModelPath  string `json:"modelPath"`  // gemma gguf on the volume
	MmprojPath string `json:"mmprojPath"` // multimodal projector on the volume (optional)
	ModelName  string `json:"modelName"`  // reported in responses
	LlamaPort  int    `json:"llamaPort"`  // loopback port for the private llama-server
	ExtraArgs  []string `json:"extraArgs"` // tuning flags appended verbatim (threads, ctx, cache types, mlock...)
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
	broker := oracled.NewBroker(cfg.QueueDepth, lg)
	llama := oracled.NewLlamaBackend(oracled.LlamaConfig{
		BinPath:    cfg.LlamaBin,
		ModelPath:  cfg.ModelPath,
		MmprojPath: cfg.MmprojPath,
		Port:       cfg.LlamaPort,
		ExtraArgs:  cfg.ExtraArgs,
		ModelName:  cfg.ModelName,
	})
	var modelReady atomic.Bool
	go func() {
		lg.Info("starting llama-server child", "fn", "main", "model", cfg.ModelName)
		if err := llama.Start(ctx); err != nil {
			lg.Error("llama-server did not become ready", "fn", "main", "err", err)
			return
		}
		broker.SetBackend(oracle.ClassLocalSmall, llama)
		modelReady.Store(true)
		lg.Info("model ready", "fn", "main", "model", cfg.ModelName)
	}()
	broker.Run()
	defer func() { broker.Stop(); llama.Stop() }()

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
		}
		return fresh.Base, report, nil
	})
	ctl.Handle("infer", func(args json.RawMessage) (ctlsock.Response, error) {
		var req oracle.Request
		if err := json.Unmarshal(args, &req); err != nil {
			return ctlsock.Response{}, err
		}
		ch, ok := broker.Submit(req)
		if !ok {
			return ctlsock.Response{}, errFull
		}
		resp := <-ch
		data, _ := json.Marshal(resp)
		return ctlsock.Response{OK: resp.Err == "", Err: resp.Err, Data: data}, nil
	})
	ctl.Handle("models", func(json.RawMessage) (ctlsock.Response, error) {
		m := map[string]any{"local-small": cfg.ModelName, "ready": modelReady.Load()}
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
			return ghosthealth.Health{Code: ghosthealth.OK, Name: service}
		}
		return ghosthealth.Health{Code: ghosthealth.Degraded, Name: service, Detail: "model loading"}
	})
	srv := ghosthealth.NewServer(service, rep)
	// Streaming chat on the service's UNIX STREAM SOCKET (streamsock) , ctlsock is one-shot and
	// cannot stream, and a loopback TCP port would be "anything on localhost may connect"; the
	// socket carries filesystem permissions and dies with the run dir. This path
	// deliberately bypasses the priority queue (a person is watching tokens appear; background work
	// arrives via ctlsock and llama-server's --parallel slots absorb the overlap). Events out are
	// our own minimal protocol, one JSON per SSE data line: {"t":"token"} ... {"done":true,"model":x}.
	streamMux := http.NewServeMux()
	streamMux.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Prompt string `json:"prompt"`
			Think  string `json:"think"`
			Image  string `json:"image,omitempty"` // base64 jpeg/png , flows to llama as a data URI
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&q) != nil || q.Prompt == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		out, model, err := llama.StreamChat(r.Context(), q.Prompt, q.Think, q.Image)
		if err != nil {
			lg.Warn("chat stream start failed", "fn", "chat", "err", err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer out.Close()
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
			}
			if json.Unmarshal([]byte(data), &ev) != nil || len(ev.Choices) == 0 {
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
