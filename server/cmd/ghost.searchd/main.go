// ghost.searchd is the search layer daemon (Search Layer SPEC v1.1): one shared index over T0
// originals, T1 journal entries, and T2 memories, with two entry points , interactive search (FTS +
// vector legs fused with RRF) and ambient retrieval for ghost.cued (vector only, tiers 1+2).
//
// It is wire-compatible with ghost.synthd's prime/ready commands, so cued's synth.SocketClient can
// point here unchanged; synthd's retirement in its favour is a separate decision, not taken here.
//
// The embeddings model runs as a private loopback llama-server child (CPU by default) from the
// encrypted volume, same pattern as oracled's. Missing weights or missing pgvector degrade the daemon
// to FTS-only , documented modes reported in health, never silent.
//
// Runs only while UNLOCKED, supervised by ghost.watchd, connects as ghost_rw over scram.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/ghosthealth"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/rotlog"
	"github.com/LocalGhostDao/localghost/server/internal/search"
	"github.com/LocalGhostDao/localghost/server/internal/svcconf"
	"github.com/LocalGhostDao/localghost/server/internal/synth"
)

const service = "ghost.searchd"

type conf struct {
	svcconf.Base
	PollSeconds      int    `json:"pollSeconds"`      // worker tick
	EfSearch         int    `json:"efSearch"`         // hnsw.ef_search per query
	EmbedPort        int    `json:"embedPort"`        // loopback port for the embeddings child
	EmbedBin         string `json:"embedBin"`         // llama-server binary
	EmbedModelPath   string `json:"embedModelPath"`   // gguf on the volume
	EmbedModelID     string `json:"embedModelID"`     // recorded per row (spec 2.2)
	EmbedThreads     int    `json:"embedThreads"`     // 0 = llama default
	EmbedExternalURL string `json:"embedExternalURL"` // set = use this server, do not spawn
}

func defaultConf(mount string) conf {
	return conf{
		Base:           svcconf.DefaultBase(),
		PollSeconds:    5,
		EfSearch:       80,
		EmbedPort:      18081,
		EmbedBin:       "/usr/local/bin/llama-server",
		EmbedModelPath: filepath.Join(mount, "ai-models", "embeddinggemma-300m-q8.gguf"),
		EmbedModelID:   "embeddinggemma-300m-q8",
	}
}

func main() {
	port := flag.Int("health-port", envPort("GHOST_HEALTH_PORT"), "loopback health port")
	mount := flag.String("mount", os.Getenv("GHOST_MOUNT"), "encrypted volume mount path")
	flag.Parse()
	if *mount == "" {
		if ld := os.Getenv("GHOST_LOG_DIR"); ld != "" {
			*mount = filepath.Dir(ld)
		}
	}
	if *mount == "" {
		log.Fatalf("%s: --mount (or GHOST_MOUNT) is required", service)
	}

	logDir := filepath.Join(*mount, "logs")
	w, err := rotlog.New(logDir, service)
	if err != nil {
		log.Fatalf("%s: open log: %v", service, err)
	}
	defer w.Close()
	lg, lvl := rotlog.Logger(w)

	cfg := defaultConf(*mount)
	confPath := svcconf.Path(*mount, service)
	if err := svcconf.Load(confPath, &cfg); err != nil {
		lg.Warn("read conf, using defaults", "fn", "main", "err", err)
	}
	svcconf.FillBaseDefaults(&cfg.Base)
	_ = svcconf.ApplyLevel(lvl, cfg.LogLevel)
	if cfg.PollSeconds <= 0 {
		cfg.PollSeconds = 5
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	sc, err := hw.LoadServicesConfig(*mount)
	if err != nil {
		log.Fatalf("%s: read services.conf: %v", service, err)
	}
	sockDir := hw.SocketForMount(*mount)
	newRW := func() *poltergres.ReadWrite {
		return poltergres.NewReadWrite(sockDir, sc.Postgres.Port, sc.Postgres.RWUser, sc.Postgres.RWPass, sc.Postgres.Name)
	}
	// Two stores = two connections, so leg A and leg B genuinely run concurrently (spec 7.6), and the
	// worker gets its own so a slow embed UPDATE never queues behind a user query.
	storeA := search.NewStore(newRW())
	storeB := search.NewStore(newRW())
	storeW := search.NewStore(newRW())

	vectorOn := storeA.VectorEnabled()

	// Embeddings child (or external URL). Degradation is a named mode, not a crash: no vector schema or
	// no weights = FTS-only, reported in health.
	var embedder *search.Embedder
	var embedNote string
	if !vectorOn {
		embedNote = "pgvector absent: FTS-only mode"
		lg.Warn("degraded", "fn", "main", "why", embedNote)
	} else if cfg.EmbedExternalURL != "" {
		embedder = search.NewEmbedder(cfg.EmbedExternalURL, cfg.EmbedModelID)
	} else {
		es := search.NewEmbedServer(search.EmbedServerConfig{
			BinPath: cfg.EmbedBin, ModelPath: cfg.EmbedModelPath, Port: cfg.EmbedPort, Threads: cfg.EmbedThreads,
		})
		if err := es.Start(2 * time.Minute); err != nil {
			embedNote = "embedding server unavailable: FTS-only mode (" + err.Error() + ")"
			lg.Warn("degraded", "fn", "main", "why", embedNote)
		} else {
			defer es.Stop()
			embedder = search.NewEmbedder(es.BaseURL(), cfg.EmbedModelID)
		}
	}

	ing := &search.Ingester{Store: storeW, Log: lg}
	api := &search.API{
		StoreA: storeA, StoreB: storeB, Embed: embedder,
		Anchors: search.ZeroRankSignals{}, Warmth: search.ZeroRankSignals{},
		Log: lg, EfSearch: cfg.EfSearch,
	}
	oc := oracle.NewClient(filepath.Join(*mount, "run"), 2*time.Minute)
	wk := &search.Worker{
		Store: storeW, Embed: embedder,
		Caption:  &search.VisionOracle{Client: oc, Timeout: 2 * time.Minute},
		Tag:      &search.TagOracle{Client: oc, Timeout: time.Minute},
		Ingester: ing, Log: lg,
		Interval: time.Duration(cfg.PollSeconds) * time.Second,
	}
	go wk.Run(ctx)

	runDir := os.Getenv("GHOST_RUN_DIR")
	if runDir == "" {
		runDir = filepath.Join(*mount, "run")
	}
	ctl := ctlsock.NewServer(service, runDir, lg)
	svcconf.BindBase(ctl, service, lvl, func() (svcconf.Base, map[string]string, error) {
		fresh := defaultConf(*mount)
		if err := svcconf.Load(confPath, &fresh); err != nil {
			return svcconf.Base{}, nil, err
		}
		svcconf.FillBaseDefaults(&fresh.Base)
		api.EfSearch = fresh.EfSearch // hot key
		return fresh.Base, map[string]string{
			"efSearch": "applied", "pollSeconds": "needs-restart", "embed*": "needs-restart",
		}, nil
	})

	// search: the interactive pipeline.
	ctl.Handle("reingest", func(args json.RawMessage) (ctlsock.Response, error) {
		n, err := storeA.ReingestImages()
		if err != nil {
			return ctlsock.Response{}, err
		}
		return ctlsock.Response{OK: true, Text: fmt.Sprintf("queued %d image(s) for captioning (gap only , already-described images untouched)", n)}, nil
	})
	ctl.Handle("revive", func(args json.RawMessage) (ctlsock.Response, error) {
		n, err := storeA.ReviveJobs()
		if err != nil {
			return ctlsock.Response{}, err
		}
		return ctlsock.Response{OK: true, Text: fmt.Sprintf("revived %d exhausted job(s)", n)}, nil
	})
	ctl.Handle("search", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			Query   string `json:"query"`
			Sources json.RawMessage `json:"sources"` // CSV "email,image"; raw because ghost-cli coerces scalars
			FromTS  int64           `json:"from"`
			ToTS    int64           `json:"to"`
			Tiers   json.RawMessage `json:"tiers"` // CSV "0,2"; tiers=0 arrives as a NUMBER from ghost-cli
			Limit   int             `json:"limit"`
		}
		if len(args) > 0 {
			if err := json.Unmarshal(args, &a); err != nil {
				return ctlsock.Response{}, err
			}
		}
		if strings.TrimSpace(a.Query) == "" {
			return ctlsock.Response{}, fmt.Errorf("search requires query=...")
		}
		f := search.Filters{Sources: splitCSV(flexStr(a.Sources)), Tiers: splitCSVInts(flexStr(a.Tiers))}
		if a.FromTS > 0 {
			f.From = time.Unix(a.FromTS, 0)
		}
		if a.ToTS > 0 {
			f.To = time.Unix(a.ToTS, 0)
		}
		res, err := api.Search(ctx, a.Query, f, a.Limit)
		if err != nil {
			return ctlsock.Response{}, err
		}
		data, _ := json.Marshal(res)
		return ctlsock.Response{OK: true, Data: data}, nil
	})

	// ingest: other daemons hand items in here , one writer to the search schema.
	// categorize: queue the category backfill for frames whose tags still lack one (the
	// stock-take calls it once per pass; safe any time, queues nothing on a healthy archive).
	ctl.Handle("categorize", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			Limit int `json:"limit"`
		}
		if len(args) > 0 {
			_ = json.Unmarshal(args, &a)
		}
		n, err := storeW.EnqueueCategorize(a.Limit)
		if err != nil {
			return ctlsock.Response{}, err
		}
		data, _ := json.Marshal(map[string]int{"queued": n})
		return ctlsock.Response{OK: true, Text: fmt.Sprintf("queued %d frames for tag categories", n), Data: data}, nil
	})
	// digest: the tags of a set of frames grouped by category , the prompt-sized summary synthd
	// injects when a question matches photos. hashes=<csv>.
	ctl.Handle("digest", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			Hashes json.RawMessage `json:"hashes"`
			Per    int             `json:"per"`
		}
		if len(args) > 0 {
			if err := json.Unmarshal(args, &a); err != nil {
				return ctlsock.Response{}, err
			}
		}
		hashes := splitCSV(flexStr(a.Hashes))
		var clean []string
		for _, h := range hashes {
			if len(h) == 32 && strings.Trim(h, "0123456789abcdef") == "" {
				clean = append(clean, h)
			}
		}
		d, err := storeA.TagDigest(clean, a.Per)
		if err != nil {
			return ctlsock.Response{}, err
		}
		data, _ := json.Marshal(d)
		return ctlsock.Response{OK: true, Data: data}, nil
	})
	ctl.Handle("ingest", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			Source     string         `json:"source"`
			Path       string         `json:"path"`
			CapturedAt int64          `json:"capturedAt"`
			Daemon     string         `json:"daemon"`
			Meta       map[string]any `json:"meta"`
			Text       string         `json:"text"`   // text sources: body (else read from path)
			Header     string         `json:"header"` // optional context header
			// Images only. Render: the picture the model should LOOK at (framed's preview or a
			// video's frame grab); identity stays the archived file at Path. Ensure: the frame is
			// already known, fill whatever stage it is missing (caption, title, tags).
			Render string `json:"render"`
			Ensure bool   `json:"ensure"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return ctlsock.Response{}, err
		}
		captured := time.Unix(a.CapturedAt, 0).UTC()
		if a.CapturedAt == 0 {
			captured = time.Now().UTC()
		}
		o := search.Original{Source: a.Source, Path: a.Path, CapturedAt: captured, Daemon: a.Daemon, Meta: a.Meta}
		var id int64
		var err error
		if a.Source == "image" {
			// Streams the identity hash; never loads the archived bytes whole (a clip is not a
			// photo). The old ReadFile here would have put a 500MB video on the heap.
			id, err = ing.IngestImageFile(o, a.Path, a.Render, a.Ensure)
		} else {
			body := a.Text
			if body == "" {
				b, rerr := os.ReadFile(a.Path)
				if rerr != nil {
					return ctlsock.Response{}, rerr
				}
				body = string(b)
			}
			header := a.Header
			if header == "" {
				header = search.ContextHeader(a.Source, captured.Format("2006-01-02"))
			}
			id, err = ing.IngestText(o, header, body)
		}
		if err != nil {
			return ctlsock.Response{}, err
		}
		return ctlsock.Response{OK: true, Text: "ingested id " + strconv.FormatInt(id, 10)}, nil
	})

	// ingest-t1 / ingest-t2: the interpretation tiers. Journal (T1) and consolidation (T2) producers
	// hand their summaries in here with the originals they cite; the citations are what make deletion's
	// stale-marking (spec 12 phase 1, invariant I4) work. Without these commands the tiers could never
	// populate and ambient ready would honestly stay false forever.
	ingestTier := func(tier int) ctlsock.Handler {
		return func(args json.RawMessage) (ctlsock.Response, error) {
			var a struct {
				RefID      int64  `json:"refId"`      // the entry/memory id in its producer's table
				Body       string `json:"body"`       // the summary text (embeds whole, seq 0)
				CapturedAt int64  `json:"capturedAt"` // period the interpretation covers
				CiteSource string `json:"citeSource"` // source of the cited originals
				CiteIDs    json.RawMessage `json:"citeIds"` // CSV of original ids; raw for the same coercion reason
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return ctlsock.Response{}, err
			}
			if a.RefID == 0 || strings.TrimSpace(a.Body) == "" {
				return ctlsock.Response{}, fmt.Errorf("ingest-t%d requires refId=... body=...", tier)
			}
			captured := time.Unix(a.CapturedAt, 0).UTC()
			if a.CapturedAt == 0 {
				captured = time.Now().UTC()
			}
			var ids []int64
			var err error
			if tier == 1 {
				ids, err = storeW.InsertChunkT1(a.RefID, captured, a.Body)
			} else {
				ids, err = storeW.InsertChunkT2(a.RefID, captured, a.Body)
			}
			if err != nil {
				return ctlsock.Response{}, err
			}
			if citeIDs := flexStr(a.CiteIDs); a.CiteSource != "" && citeIDs != "" {
				var cites []int64
				for _, part := range strings.Split(citeIDs, ",") {
					if n, perr := strconv.ParseInt(strings.TrimSpace(part), 10, 64); perr == nil {
						cites = append(cites, n)
					}
				}
				if err := storeW.AddCitations(tier, a.RefID, a.CiteSource, cites); err != nil {
					return ctlsock.Response{}, err
				}
			}
			for _, id := range ids {
				if err := storeW.EnqueueJob("embed_text", map[string]any{"chunkIds": []int64{id}}); err != nil {
					return ctlsock.Response{}, err
				}
			}
			return ctlsock.Response{OK: true, Text: fmt.Sprintf("t%d ref %d indexed", tier, a.RefID)}, nil
		}
	}
	ctl.Handle("ingest-t1", ingestTier(1))
	ctl.Handle("ingest-t2", ingestTier(2))

	// delete: spec 12 phase 1 (tombstone, cascade, stale-mark, enqueue reconsolidate).
	ctl.Handle("delete", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			Source string `json:"source"`
			ID     int64  `json:"id"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return ctlsock.Response{}, err
		}
		path, sha, meta, _, err := storeW.OriginalByID(a.Source, a.ID)
		_ = meta
		if err != nil {
			return ctlsock.Response{}, err
		}
		if err := storeW.DeleteOriginal(a.Source, a.ID, sha); err != nil {
			return ctlsock.Response{}, err
		}
		_ = path // thumbnail/file removal is the owner daemon's job (framed owns its archive)
		return ctlsock.Response{OK: true, Text: "deleted; citing interpretations stale-marked"}, nil
	})

	// unpark: reset jobs dead at the 5-attempt ceiling so the worker claims them again. The ceiling
	// protects against poison jobs; it has no answer for "the environment was broken all night and
	// is fixed now" (role errors, a blind oracled) , this is that answer. Optional kind filter.
	// Poison jobs re-park after five more tries; pulling this lever wrongly costs nothing.
	ctl.Handle("unpark", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			Kind string `json:"kind"`
		}
		if len(args) > 0 {
			_ = json.Unmarshal(args, &a)
		}
		n, err := storeW.UnparkJobs(a.Kind)
		if err != nil {
			return ctlsock.Response{OK: false, Err: err.Error()}, nil
		}
		return ctlsock.Response{OK: true, Text: fmt.Sprintf("unparked %d jobs", n)}, nil
	})
	// rebuild: spec 13.3, the proof indexes are derived state.
	ctl.Handle("rebuild", func(json.RawMessage) (ctlsock.Response, error) {
		n, err := ing.Rebuild()
		if err != nil {
			return ctlsock.Response{}, err
		}
		return ctlsock.Response{OK: true, Text: fmt.Sprintf("re-chunked %d originals, embeds enqueued", n)}, nil
	})

	// prime/ready: wire-compatible with ghost.synthd so cued's SocketClient can point here unchanged.
	ctl.Handle("prime", func(args json.RawMessage) (ctlsock.Response, error) {
		var q synth.Query
		if len(args) > 0 {
			if err := json.Unmarshal(args, &q); err != nil {
				return ctlsock.Response{}, err
			}
		}
		synthetic := q.Summary
		for _, v := range q.Signals {
			synthetic += " " + v
		}
		limit := q.Limit
		if limit <= 0 || limit > 10 {
			limit = 5
		}
		hits, err := api.Ambient(ctx, synthetic, limit)
		if err != nil {
			return ctlsock.Response{}, err
		}
		cands := make([]synth.Candidate, 0, len(hits))
		for i, h := range hits {
			gap := h.Score
			if i+1 < len(hits) {
				gap = h.Score - hits[i+1].Score
			}
			body := ""
			if bodies, berr := storeA.ChunkBodies([]int64{h.ChunkID}); berr == nil {
				body = bodies[h.ChunkID]
			}
			label, _, _ := storeA.ParentLabel(h)
			refID := h.MemoryID
			if refID == 0 {
				refID = h.EntryID
			}
			cands = append(cands, synth.Candidate{
				ID:              fmt.Sprintf("t%d-%d", h.Tier, refID),
				Title:           label,
				Body:            body,
				Relevance:       h.Score,
				Distinctiveness: gap,
			})
		}
		data, _ := json.Marshal(cands)
		return ctlsock.Response{OK: true, Data: data}, nil
	})
	ctl.Handle("ready", func(json.RawMessage) (ctlsock.Response, error) {
		n, _ := storeA.TierCount(1, 2)
		ready := vectorOn && embedder != nil && n > 0
		data, _ := json.Marshal(map[string]bool{"ready": ready})
		return ctlsock.Response{OK: true, Data: data}, nil
	})

	// queue: the ops view (spec 13.4).
	ctl.Handle("queue", func(json.RawMessage) (ctlsock.Response, error) {
		pending, stale, parked, runnable, err := storeA.HealthRow()
		if err != nil {
			return ctlsock.Response{}, err
		}
		data, _ := json.Marshal(map[string]int64{
			"pendingEmbeds": pending, "staleChunks": stale, "parkedJobs": parked, "runnableJobs": runnable,
		})
		return ctlsock.Response{OK: true, Data: data}, nil
	})
	defer ctl.Cleanup()
	go func() {
		if err := ctl.Serve(ctx); err != nil {
			lg.Error("control server exited", "fn", "main", "err", err)
		}
	}()

	rep := ghosthealth.ReporterFunc(func() ghosthealth.Health {
		if err := storeA.Ping(); err != nil {
			return ghosthealth.Health{Code: ghosthealth.Degraded, Name: service, Detail: "pg unreachable: " + err.Error()}
		}
		detail := ""
		if embedNote != "" {
			detail = embedNote
		}
		if pending, _, parked, _, err := storeA.HealthRow(); err == nil && (pending > 0 || parked > 0) {
			if detail != "" {
				detail += "; "
			}
			detail += fmt.Sprintf("pending embeds %d, parked jobs %d", pending, parked)
		}
		code := ghosthealth.OK
		if embedNote != "" {
			code = ghosthealth.Degraded
		}
		return ghosthealth.Health{Code: code, Name: service, Detail: detail}
	})
	hsrv := ghosthealth.NewServer(service, rep)
	go func() {
		if err := hsrv.Serve(*port); err != nil {
			lg.Error("health server stopped", "fn", "main", "err", err)
		}
	}()

	lg.Info("up", "fn", "main", "healthPort", *port, "vector", vectorOn, "embedder", embedder != nil)
	<-ctx.Done()
	lg.Info("shutting down", "fn", "main")
}

// flexStr decodes a JSON value that may be a string OR a bare number/bool literal , ghost-cli's
// parseKV coerces "tiers=0" to a number, and a handler that only accepts strings breaks from the CLI.
func flexStr(r json.RawMessage) string {
	if len(r) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(r, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(r))
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func splitCSVInts(s string) []int {
	var out []int
	for _, p := range splitCSV(s) {
		if n, err := strconv.Atoi(p); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func envPort(key string) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}
