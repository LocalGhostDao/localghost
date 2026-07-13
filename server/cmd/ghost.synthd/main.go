// ghost.synthd is the memory-surfacing daemon: it owns the retrieval side of the "Before You Ask"
// loop. Given a context (what the user is doing now), it ranks memories from its index and returns
// candidates for ghost.cued to gate. synthd decides WHAT is a candidate; cued decides WHEN and
// WHETHER anything reaches the user.
//
// HONEST STATE. The INDEX , embeddings, vector store, the memory corpus , is the next few months of
// work and does not exist yet. So synthd runs the real query PIPELINE over an EMPTY index: the "prime"
// command executes the whole path and returns nothing, because nothing is indexed. synthd is
// running-but-blind. When the corpus is built behind the Index interface, synthd starts returning
// candidates with no change to cued or to the pipeline.
//
// It exposes its work over the same control socket everything uses: base commands (ping/status/reload/
// log-level) plus its own , prime (the hot path cued calls), ready, and index-stats. So ghost-cli can
// query it (`ghost-cli ghost.synthd index-stats`) just like any other service.
//
// Runs only while UNLOCKED. Spawned by ghost.watchd from <mount>/bin; logs to
// <mount>/logs/ghost.synthd-YYYY-MM-DD.log.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/ghosthealth"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/rotlog"
	"github.com/LocalGhostDao/localghost/server/internal/svcconf"
	"github.com/LocalGhostDao/localghost/server/internal/synth"
	"github.com/LocalGhostDao/localghost/server/internal/synthd"
)

const service = "ghost.synthd"

func main() {
	port := flag.Int("health-port", envPort("GHOST_HEALTH_PORT"), "loopback health/status port (required)")
	flag.Parse()
	if *port <= 0 {
		log.Fatalf("%s: no health port (set --health-port or GHOST_HEALTH_PORT)", service)
	}

	var lg *slog.Logger
	var lvl *slog.LevelVar
	if dir := os.Getenv("GHOST_LOG_DIR"); dir != "" {
		w, err := rotlog.New(dir, service)
		if err != nil {
			log.Fatalf("%s: open log: %v", service, err)
		}
		defer w.Close()
		lg, lvl = rotlog.Logger(w)
	} else {
		lvl = new(slog.LevelVar)
		lvl.Set(rotlog.LevelFromEnv())
		lg = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// The query engine over the EMPTY index (the corpus is not built). Runs the full pipeline; returns
	// nothing until the real index slots in behind the Index interface.
	engine := synthd.New(synthd.NewEmptyIndex(), lg)
	if !engine.Ready() {
		lg.Info("running, but the memory index is EMPTY until the corpus is built , prime returns "+
			"nothing (cued stays silent); the query pipeline is live and ready for the index", "fn", "main")
	}

	srv := ghosthealth.NewServer(service, ghosthealth.ReporterFunc(func() ghosthealth.Health {
		d := ""
		if !engine.Ready() {
			d = "index empty (corpus not built)"
		}
		return ghosthealth.Health{Code: ghosthealth.OK, Name: service, Detail: d}
	}))
	go func() {
		if err := srv.Serve(*port); err != nil {
			lg.Error("health server stopped", "fn", "main", "err", err)
		}
	}()

	runDir := os.Getenv("GHOST_RUN_DIR")
	if runDir == "" {
		if ld := os.Getenv("GHOST_LOG_DIR"); ld != "" {
			runDir = filepath.Join(filepath.Dir(ld), "run")
		}
	}
	if runDir != "" {
		mount := filepath.Dir(runDir)
		ctl := ctlsock.NewServer(service, runDir, lg)
		svcconf.BindBase(ctl, service, lvl, func() (svcconf.Base, map[string]string, error) {
			base := svcconf.DefaultBase()
			_ = svcconf.Load(svcconf.Path(mount, service), &base)
			svcconf.FillBaseDefaults(&base)
			return base, nil, nil
		})
		// prime: the hot path cued calls on every context change. Runs the query pipeline.
		ctl.Handle("prime", func(args json.RawMessage) (ctlsock.Response, error) {
			var q synth.Query
			if len(args) > 0 {
				if err := json.Unmarshal(args, &q); err != nil {
					return ctlsock.Response{}, err
				}
			}
			cands, err := engine.Query(context.Background(), q)
			if err != nil {
				return ctlsock.Response{}, err
			}
			data, _ := json.Marshal(cands)
			return ctlsock.Response{OK: true, Data: data}, nil
		})
		// ready: whether the index is actually queryable (cued's SocketClient.Ready reads this).
		ctl.Handle("ready", func(json.RawMessage) (ctlsock.Response, error) {
			data, _ := json.Marshal(map[string]bool{"ready": engine.Ready()})
			return ctlsock.Response{OK: true, Data: data}, nil
		})
		// chat: the app's question -> oracled -> answer. TODAY a PURE PASSTHROUGH , synthd adds no
		// context because the index is empty. This is deliberately the seam where retrieval joins:
		// when the corpus exists, this handler looks up relevant memories and injects them into the
		// prompt before oracled sees it, with zero change to secd or the app. Interactive priority ,
		// a person is waiting.
		oc := oracle.NewClient(runDir, 120*time.Second)
		ctl.Handle("chat", func(args json.RawMessage) (ctlsock.Response, error) {
			var q struct {
				Prompt string `json:"prompt"`
				Think  string `json:"think"`
			}
			if err := json.Unmarshal(args, &q); err != nil {
				return ctlsock.Response{}, err
			}
			// CONTEXT INJECTION , the seam, now live-but-empty. Ask ghost.searchd for archive matches
			// and prepend them; today the index is empty so this returns nothing and the request is a
			// byte-identical passthrough. The moment framed's captions start flowing through ingest,
			// chat answers become grounded in the archive with no further change here or above.
			// Retrieval is time-boxed and failure is SILENT-but-logged: a slow or dead searchd must
			// never stall or fail a chat that the model alone could answer.
			items := gatherContext(runDir, q.Prompt)
			input := q.Prompt
			if block := formatContext(items); block != "" {
				input = block + "\n\nUsing the context above only where it is actually relevant, answer:\n" + q.Prompt
			}
			resp, err := oc.Infer(oracle.Request{
				Capability: "chat",
				Class:      oracle.ClassLocalSmall,
				Priority:   oracle.PriorityInteractive,
				Input:      input,
				Think:      q.Think,
			})
			if err != nil {
				return ctlsock.Response{}, err
			}
			// TRANSPARENCY PROTOCOL: the reply carries exactly what was injected and why, so the app
			// can show "answered using these memories" instead of the grounding being invisible. The
			// context array is EMPTY (not absent) when nothing was injected , the app can rely on
			// the field existing. secd passes this JSON through untouched.
			data, _ := json.Marshal(chatReply{Output: resp.Output, Model: resp.Model, Context: items})
			return ctlsock.Response{OK: true, Data: data}, nil
		})
		// index-stats: operator view of the corpus (empty today).
		ctl.Handle("index-stats", func(json.RawMessage) (ctlsock.Response, error) {
			data, _ := json.Marshal(map[string]any{"ready": engine.Ready(), "size": engine.Size()})
			return ctlsock.Response{OK: true, Data: data}, nil
		})
		defer ctl.Cleanup()
		go func() {
			if err := ctl.Serve(ctx); err != nil {
				lg.Error("control server exited", "fn", "main", "err", err)
			}
		}()
	}

	lg.Info("up", "fn", "main", "healthPort", *port, "indexReady", engine.Ready())

	<-ctx.Done()
	lg.Info("shutting down", "fn", "main")
}

func envPort(key string) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

// --- context injection , sources, protocol, formatting ---------------------------------------
//
// A context SOURCE inspects the prompt and returns candidate items. Sources are consulted in order
// with one shared time budget; empty answers are normal. PLACEHOLDERS below mark the planned ones ,
// each lands as a function, gets appended to contextSources, and both the injection and the
// transparency protocol pick it up with no other change.

// ctxItem is one injected piece of context AND its transparency record , the same struct feeds the
// model's prompt block and the app's "what I used and why" display, so they can never disagree.
type ctxItem struct {
	When    string  `json:"when,omitempty"`   // "2026-04-12" , the item's own date, not today's
	Source  string  `json:"source"`           // "image", "note", "chat", "location"
	Snippet string  `json:"snippet"`          // what the model actually saw (truncated, sanitised)
	Why     string  `json:"why"`              // human-readable reason it was selected
	Score   float64 `json:"score,omitempty"`  // retrieval score when the source has one
}

// chatReply is the chat command's wire shape: the answer plus the transparency record.
type chatReply struct {
	Output  string    `json:"output"`
	Model   string    `json:"model,omitempty"`
	Context []ctxItem `json:"context"`
}

type contextSource func(runDir, prompt string) []ctxItem

var contextSources = []contextSource{
	searchdSource,
	// PLACEHOLDER recentChatsSource: last N turns of this conversation (needs chat storage first ,
	//   see docs/context-injection-design.md phase 3).
	// PLACEHOLDER locationDaySource: "where was I on <date>" prompts answered from framed's day
	//   GeoJSON , cheap, no model, high precision for a narrow question class.
	// PLACEHOLDER calendarish sources as noted/voiced start producing text.
}

// gatherContext runs the sources under one budget and caps the total. Order matters: earlier
// sources get first claim on the cap.
func gatherContext(runDir, prompt string) []ctxItem {
	const maxItems = 6
	var out []ctxItem
	for _, src := range contextSources {
		if len(out) >= maxItems {
			break
		}
		for _, it := range src(runDir, prompt) {
			if it.Snippet == "" {
				continue
			}
			out = append(out, sanitize(it))
			if len(out) >= maxItems {
				break
			}
		}
	}
	if len(out) > 0 {
		slog.Info("context injected into chat", "fn", "gatherContext", "items", len(out))
	}
	if out == nil {
		out = []ctxItem{} // protocol: empty, never absent
	}
	return out
}

// sanitize bounds what a stored chunk can do to the prompt: length-capped, newlines flattened , a
// pathological caption must not be able to spend the token budget or fake structure in the block.
func sanitize(it ctxItem) ctxItem {
	s := strings.ReplaceAll(it.Snippet, "\n", " ")
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	it.Snippet = s
	return it
}

func formatContext(items []ctxItem) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Context from the user's personal archive (retrieved automatically, may be irrelevant):")
	for _, it := range items {
		when := ""
		if it.When != "" {
			when = it.When + ", "
		}
		b.WriteString("\n- [" + when + it.Source + "] " + it.Snippet)
	}
	return b.String()
}

// searchdSource , the archive index via ghost.searchd. 3s budget: retrieval must never hold a
// person's question hostage; on any failure the chat proceeds bare, logged at debug.
func searchdSource(runDir, prompt string) []ctxItem {
	c := ctlsock.NewClientTimeout("ghost.searchd", runDir, 3*time.Second)
	resp, err := c.Call("search", map[string]any{"query": prompt, "limit": 6})
	if err != nil {
		slog.Debug("searchd unavailable, chat proceeds bare", "fn", "searchdSource", "err", err)
		return nil
	}
	var results []struct {
		Label      string   `json:"label"`
		CapturedAt int64    `json:"capturedAt"`
		Score      float64  `json:"score"`
		Snippets   []string `json:"snippets"`
		OrigSource string   `json:"origSource"`
	}
	if err := json.Unmarshal(resp.Data, &results); err != nil {
		return nil
	}
	var out []ctxItem
	for _, r := range results {
		text := r.Label
		for _, sn := range r.Snippets {
			if sn != "" {
				text = sn
				break
			}
		}
		if text == "" {
			continue
		}
		src := r.OrigSource
		if src == "" {
			src = "archive"
		}
		when := ""
		if r.CapturedAt > 0 {
			when = time.Unix(r.CapturedAt, 0).UTC().Format("2006-01-02")
		}
		out = append(out, ctxItem{
			When: when, Source: src, Snippet: text, Score: r.Score,
			Why: "matched your question in the archive index",
		})
	}
	return out
}
