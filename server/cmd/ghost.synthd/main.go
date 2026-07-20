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
	"bufio"
	"sync"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"net/http"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/ghosthealth"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/rotlog"
	"github.com/LocalGhostDao/localghost/server/internal/streamsock"
	"github.com/LocalGhostDao/localghost/server/internal/svcconf"
	"github.com/LocalGhostDao/localghost/server/internal/synth"
	"github.com/LocalGhostDao/localghost/server/internal/synthd"
)

const service = "ghost.synthd"

// chatDB is the lazy connection for chat persistence , same DB, same creds pattern as framed.
// Nil until first non-incognito message; a connect failure logs and chats simply do not persist
// (the conversation still works , persistence is a feature, not a dependency).
var (
	chatDB     *poltergres.ReadWrite
	chatDBOnce sync.Once
)

func chatStore(mount string) *poltergres.ReadWrite {
	chatDBOnce.Do(func() {
		sc, err := hw.LoadServicesConfig(mount)
		if err != nil {
			slog.Warn("chat persistence off: services.conf", "fn", "chatStore", "err", err)
			return
		}
		chatDB = poltergres.NewReadWrite(hw.SocketForMount(mount), sc.Postgres.Port, sc.Postgres.RWUser, sc.Postgres.RWPass, sc.Postgres.Name)
	})
	return chatDB
}

// chatPersist appends a message, creating the chat first when id is 0. Title , first words of the
// first prompt, cheap and editable later; an oracled-generated title is a planned refinement.
// Returns the chat id (0 = persistence unavailable). Never fails the conversation.
func chatPersist(mount string, chatID int64, role, content string) int64 {
	db := chatStore(mount)
	if db == nil || content == "" {
		return chatID
	}
	now := time.Now().UTC().UnixMilli()
	if chatID == 0 {
		title := content
		if ws := strings.Fields(title); len(ws) > 6 {
			title = strings.Join(ws[:6], " ") + "…"
		}
		if len(title) > 60 {
			title = title[:60] + "…"
		}
		rows, err := db.Query(`INSERT INTO chats (title, created_at, updated_at) VALUES ($1,$2,$2) RETURNING id`, title, strconv.FormatInt(now, 10))
		if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
			slog.Warn("chat create failed", "fn", "chatPersist", "err", err)
			return 0
		}
		chatID, _ = strconv.ParseInt(*rows.Vals[0][0], 10, 64)
	}
	if err := db.Exec(`INSERT INTO chat_messages (chat_id, role, content, ts) VALUES ($1,$2,$3,$4)`,
		strconv.FormatInt(chatID, 10), role, content, strconv.FormatInt(now, 10)); err != nil {
		slog.Warn("chat append failed", "fn", "chatPersist", "err", err)
	}
	if err := db.Exec(`UPDATE chats SET updated_at = $1 WHERE id = $2`, strconv.FormatInt(now, 10), strconv.FormatInt(chatID, 10)); err != nil {
		slog.Warn("chat touch failed", "fn", "chatPersist", "err", err)
	}
	return chatID
}

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
	runDir := os.Getenv("GHOST_RUN_DIR")
	if runDir == "" {
		if ld := os.Getenv("GHOST_LOG_DIR"); ld != "" {
			runDir = filepath.Join(filepath.Dir(ld), "run")
		}
	}

	// Streaming chat , the SAME seam as the ctlsock chat command (context gathered and injected
	// here, transparency first on the wire), token-by-token. Event protocol downstream:
	//   data: {"context":[...]}        first, always (empty array when nothing injected)
	//   data: {"t":"..."}              tokens, oracled's translation of llama's SSE
	//   data: {"done":true,"model":x}  last
	streamMux := http.NewServeMux()
	streamMux.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Prompt    string `json:"prompt"`
			Think     string `json:"think"`
			Incognito bool   `json:"incognito"`
			ChatID    int64  `json:"chatId"`
			Image     string `json:"image,omitempty"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&q) != nil || q.Prompt == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		items := gatherContext(runDir, q.Prompt)
		input := q.Prompt
		if block := formatContext(items); block != "" {
			input = block + "\n\nUsing the context above only where it is actually relevant, answer:\n" + q.Prompt
		}
		body, _ := json.Marshal(map[string]string{"prompt": input, "think": q.Think, "image": q.Image})
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
			"http://ghost/chat", bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := streamsock.Client("ghost.oracled", runDir).Do(req)
		if err != nil {
			lg.Warn("chat stream: oracled unreachable", "fn", "chat", "err", err)
			http.Error(w, "model unavailable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lg.Warn("chat stream refused by oracled", "fn", "chat", "code", resp.StatusCode)
			http.Error(w, "model unavailable", http.StatusBadGateway)
			return
		}
		fl, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		ctxEv, _ := json.Marshal(map[string]any{"context": items})
		_, _ = w.Write([]byte("data: " + string(ctxEv) + "\n\n"))
		if fl != nil {
			fl.Flush()
		}
		// Persist the question now (incognito conversations never touch the tables); accumulate the
		// answer from the token events while piping them through, save on done , and rewrite the
		// done event to carry the chatId so the app can keep the conversation in one row.
		mount := filepath.Dir(runDir)
		chatID := q.ChatID
		if !q.Incognito {
			// BOUNDED: persistence is a feature, the stream is the product. A slow or wedged DB
			// connect must not hold a person's question , 800ms and we stream without it (logged;
			// that conversation just does not persist).
			persisted := make(chan int64, 1)
			go func() { persisted <- chatPersist(mount, chatID, "user", q.Prompt) }()
			select {
			case id := <-persisted:
				chatID = id
			case <-time.After(800 * time.Millisecond):
				lg.Warn("chat persist slow, streaming without it", "fn", "chat")
			}
		}
		var answer strings.Builder
		// REASONING SPLIT. This gemma reasons IN-BAND (prompt-injected think, no native
		// reasoning_content channel), so its thinking arrives as ordinary answer tokens wrapped in
		// a <think>...</think> block. The app's thinking toggle listens for {"r":...} events that
		// were never emitted , which is why the panel stayed empty. This streaming splitter re-tags
		// tokens INSIDE the block as reasoning and everything after </think> as the answer, so the
		// toggle fills live and only the real answer is persisted. Tag-boundary tokens can straddle
		// two SSE chunks, so a small carry buffer holds a partial "<think" / "</think" across the
		// seam. Models WITHOUT the block just stream answer tokens , the splitter is transparent.
		var inThink, sawThink bool
		var carry string
		const openTag, closeTag = "<think>", "</think>"
		emit := func(kind, text string) { // kind: "r" or "t"
			if text == "" {
				return
			}
			key := "t"
			if kind == "r" {
				key = "r"
			}
			nb, _ := json.Marshal(map[string]string{key: text})
			_, _ = w.Write([]byte("data: " + string(nb) + "\n"))
			if fl != nil {
				fl.Flush()
			}
		}
		// route classifies a token's text into reasoning/answer, honoring the open/close tags and
		// the cross-chunk carry. Returns the answer-visible portion (for persistence).
		route := func(t string) string {
			s := carry + t
			carry = ""
			var answerOut strings.Builder
			for len(s) > 0 {
				if !inThink {
					i := strings.Index(s, openTag)
					if i < 0 {
						// hold a possible partial "<think" tail across the seam
						if k := partialTail(s, openTag); k > 0 {
							carry = s[len(s)-k:]
							s = s[:len(s)-k]
						}
						emit("t", s)
						answerOut.WriteString(s)
						break
					}
					emit("t", s[:i])
					answerOut.WriteString(s[:i])
					s = s[i+len(openTag):]
					inThink, sawThink = true, true
				} else {
					i := strings.Index(s, closeTag)
					if i < 0 {
						if k := partialTail(s, closeTag); k > 0 {
							carry = s[len(s)-k:]
							s = s[:len(s)-k]
						}
						emit("r", s)
						break
					}
					emit("r", s[:i])
					s = s[i+len(closeTag):]
					inThink = false
				}
			}
			return answerOut.String()
		}
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 64<<10)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "data: ") {
				payload := strings.TrimPrefix(line, "data: ")
				var tok struct {
					T    string `json:"t"`
					Done bool   `json:"done"`
				}
				if json.Unmarshal([]byte(payload), &tok) == nil {
					if tok.T != "" {
						// Split reasoning from answer; only the answer portion is persisted.
						answer.WriteString(route(tok.T))
						_ = sawThink
						if !tok.Done {
							continue // token already emitted (r or t) by route; skip the raw line
						}
					}
					if tok.Done {
						if !q.Incognito && chatID != 0 {
							// Fire and forget , the done event must not wait on the DB. chatID != 0
							// guard: if the user-message persist failed or timed out, appending the
							// assistant alone would CREATE a chat titled by the answer , worse than
							// losing one exchange.
							cid := chatID
							text := answer.String()
							go func() { chatPersist(mount, cid, "assistant", text) }()
						}
						var doneEv map[string]any
						if err := json.Unmarshal([]byte(payload), &doneEv); err != nil || doneEv == nil {
							// Our own oracled emits this event, so this is a should-never; if it
							// happens anyway, a synthesized done beats a nil-map panic mid-stream.
							lg.Warn("done event unparseable, synthesizing", "fn", "chat", "err", err)
							doneEv = map[string]any{"done": true}
						}
						doneEv["chatId"] = chatID
						nb, _ := json.Marshal(doneEv)
						line = "data: " + string(nb)
					}
				}
			}
			_, _ = w.Write([]byte(line + "\n"))
			if fl != nil {
				fl.Flush()
			}
		}
	})
	go func() {
		if err := streamsock.Serve(service, runDir, streamMux); err != nil {
			lg.Error("stream socket stopped", "fn", "main", "err", err)
		}
	}()
	go func() {
		if err := srv.Serve(*port); err != nil {
			lg.Error("health server stopped", "fn", "main", "err", err)
		}
	}()
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
		// ON THIS DAY , the retrospective. "What was I doing on July 15th, every year the box
		// knows about" , photos (hashes, for the app's thumb endpoint), places, journal notes, and
		// a short model narrative per year. Cached per month-day, regenerated past 20h.
		ctl.Handle("onthisday", func(args json.RawMessage) (ctlsock.Response, error) {
			var a struct {
				Day string `json:"day"` // MM-DD; empty = today
			}
			if len(args) > 0 {
				_ = json.Unmarshal(args, &a)
			}
			if a.Day == "" {
				a.Day = time.Now().UTC().Format("01-02")
			}
			if _, perr := time.Parse("01-02", a.Day); perr != nil {
				return ctlsock.Response{OK: false, Err: "day must be MM-DD"}, nil
			}
			body, err := onThisDay(runDir, a.Day, lg)
			if err != nil {
				return ctlsock.Response{OK: false, Err: err.Error()}, nil
			}
			return ctlsock.Response{OK: true, Text: body}, nil
		})
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

	// MEMORY-MAKING , the first real memory source. synthd's charter (how-memory-gets-made) is
	// journal entries -> entities -> memories -> episodes, fed by ghost.noted; noted is still a
	// stub, so the first corpus source is the one that already exists: saved conversations,
	// distilled through oracled. Sovereignty is structural: tombstones never resurrected (chats
	// with ANY memory rows are never re-distilled), user_edited never overwritten, incognito
	// invisible by inheritance (never reaches the chats table).
	mountDir := ""
	if ld := os.Getenv("GHOST_LOG_DIR"); ld != "" {
		mountDir = filepath.Dir(ld)
	} else if runDir != "" {
		mountDir = filepath.Dir(runDir)
	}
	if mountDir != "" {
		go distillLoop(ctx, mountDir, runDir, lg)
	}

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
	memoriesSource, // FIRST: what the box knows about the PERSON outranks document search
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
	if len(out) == 0 {
		// SAMPLES , the index is empty (captions have not been generated yet), so these three are
		// PLACEHOLDERS to exercise the transparency UI end to end. Every one is labeled sample and
		// says so in why; they are NOT injected into the model's prompt (formatContext skips the
		// sample source) , the display pipeline gets real traffic, the model gets nothing fake.
		out = []ctxItem{
			{When: "2026-04-12", Source: "sample", Snippet: "photo caption placeholder , captions land here once framed's backlog is processed", Why: "sample: index is empty"},
			{When: "2026-05-03", Source: "sample", Snippet: "note placeholder , notes and voice memos will surface here", Why: "sample: index is empty"},
			{When: "2026-06-21", Source: "sample", Snippet: "location placeholder , day summaries from your tracks", Why: "sample: index is empty"},
		}
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
	wrote := false
	for _, it := range items {
		if it.Source == "sample" {
			continue // display-only placeholders , the model NEVER sees fake memories
		}
		wrote = true
		when := ""
		if it.When != "" {
			when = it.When + ", "
		}
		b.WriteString("\n- [" + when + it.Source + "] " + it.Snippet)
	}
	if !wrote {
		return ""
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

// partialTail returns the length of the longest suffix of s that is a strict prefix of tag , the
// number of trailing bytes to carry across the SSE seam so a tag split between two token chunks
// ("<thi" + "nk>") is still recognised. 0 when no suffix could begin the tag.
func partialTail(s, tag string) int {
	max := len(tag) - 1
	if max > len(s) {
		max = len(s)
	}
	for k := max; k > 0; k-- {
		if s[len(s)-k:] == tag[:k] {
			return k
		}
	}
	return 0
}

// distillLoop is the writer's heartbeat: every 10 minutes, find finished conversations without
// memories and distill them. Lazy connections, per-pass reconnect on failure, bounded work per
// pass (5 chats) so a first run over a long backlog spreads across passes instead of hammering
// the model for an hour.
func distillLoop(ctx context.Context, mount, runDir string, lg *slog.Logger) {
	var db *poltergres.ReadWrite
	oc := oracle.NewClient(runDir, 2*time.Minute)
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if db == nil {
			cfg, err := hw.LoadServicesConfig(mount)
			if err != nil {
				lg.Warn("services.conf unreadable, pass skipped", "fn", "distillLoop", "err", err)
				continue
			}
			db = poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port,
				cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name)
		}
		n, err := distillPass(db, oc, lg)
		if err != nil {
			lg.Warn("distill pass failed, will reconnect next tick", "fn", "distillLoop", "err", err)
			db = nil
			continue
		}
		if n > 0 {
			lg.Info("distilled", "fn", "distillLoop", "memories", n)
		}
	}
}

func distillPass(db *poltergres.ReadWrite, oc *oracle.Client, lg *slog.Logger) (int, error) {
	// ONE SOURCE: the journal. Every ingester (framed, noted , which journals chats too , voiced,
	// tallyd) writes entries; this pass distills undistilled ones through the model and flips the
	// distilled flag , which IS the sentinel: flipped even when the model finds nothing durable
	// (NONE), so nothing is re-summarized; left unflipped on model failure, so it retries. The
	// person's deletions are never re-litigated because the ENTRY stays distilled regardless of
	// what happens to the memories it produced.
	// Per-photo entries from framed are diary lines, not memory candidates , a single routine
	// photo almost never yields a durable memory, and a full-archive reprocess journals TENS OF
	// THOUSANDS of them. Feeding each through the model at 8/pass would occupy the GPU for weeks
	// answering NONE. They are flipped distilled in bulk here; day-level EPISODES over frames are
	// the real plan (TODO 30d) and will read frames directly, not these entries.
	if err := db.Exec(
		"UPDATE journal_entries SET distilled = TRUE WHERE NOT distilled AND source = 'ghost.framed' AND ref NOT LIKE 'timeline:%'"); err != nil {
		return 0, err
	}
	rows, err := db.Query(
		"SELECT id, source, ref, title, body FROM journal_entries WHERE NOT distilled ORDER BY ts DESC LIMIT 8")
	if err != nil {
		return 0, err
	}
	written := 0
	for _, v := range rows.Vals {
		if len(v) < 5 || v[0] == nil || v[2] == nil {
			continue
		}
		entryID, ref := *v[0], *v[2]
		title, body := "", ""
		if v[3] != nil {
			title = *v[3]
		}
		if v[4] != nil {
			body = *v[4]
		}
		resp, ierr := oc.Infer(oracle.Request{
			Capability: "summarize",
			Priority:   oracle.PriorityBackground,
			Input: "From this journal entry, extract up to 3 durable facts, preferences, plans, or events about the USER worth remembering long-term. " +
				"One per line, format exactly: TITLE | one-sentence body. Only genuinely durable things , a single routine photo or a pleasantry is usually NOTHING. " +
				"If nothing is worth remembering, reply with exactly: NONE\n\n" + title + "\n\n" + body,
		})
		if ierr != nil {
			lg.Warn("distill inference failed, entry left for a later pass", "fn", "distillPass", "ref", ref, "err", ierr)
			continue
		}
		now := time.Now().UnixMilli()
		var srcChat int64
		if strings.HasPrefix(ref, "chat:") {
			srcChat, _ = strconv.ParseInt(strings.TrimPrefix(ref, "chat:"), 10, 64)
		}
		for _, line := range strings.Split(resp.Output, "\n") {
			line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*0123456789. "))
			if line == "" || strings.EqualFold(line, "NONE") {
				continue
			}
			parts := strings.SplitN(line, "|", 2)
			if len(parts) != 2 {
				continue
			}
			t := strings.TrimSpace(parts[0])
			b := strings.TrimSpace(parts[1])
			if t == "" || b == "" || len(t) > 120 || len(b) > 500 {
				continue
			}
			if err := db.Exec(
				"INSERT INTO memories (title, body, kind, source_chat, source_ref, created_at, updated_at) VALUES ($1,$2,'distilled',NULLIF($3,0),$4,$5,$5)",
				t, b, srcChat, ref, now); err != nil {
				return written, err
			}
			written++
		}
		if err := db.Exec("UPDATE journal_entries SET distilled = TRUE WHERE id = $1", entryID); err != nil {
			return written, err
		}
	}
	return written, nil
}

// memDB is memoriesSource's lazy pg handle , same pattern as every volume daemon, package-level
// because contextSources are plain funcs. Dropped on error, re-dialed next call.
var memDB *poltergres.ReadWrite

// memoriesSource , the retrieval half of the memory system finally meeting the injection path the
// architecture built months ago. Keyword term-overlap over the memories table: honest v1 recall
// (the emb column and semantic ranking are the recorded upgrade), which is exactly enough for
// "what did I decide about the boat" to surface the boat memory. Live rows only , tombstones are
// invisible here as everywhere , and user-authored rows compete equally with distilled ones. Top 2
// by term hits then recency: memories season the chat, they do not flood it.
func memoriesSource(runDir, prompt string) []ctxItem {
	terms := make([]string, 0, 6)
	for _, w := range strings.Fields(strings.ToLower(prompt)) {
		w = strings.Trim(w, ".,!?\"'()[]:;")
		if len(w) > 2 {
			terms = append(terms, w)
		}
		if len(terms) == 6 {
			break
		}
	}
	if len(terms) == 0 {
		return nil
	}
	if memDB == nil {
		mount := filepath.Dir(runDir)
		cfg, err := hw.LoadServicesConfig(mount)
		if err != nil {
			return nil
		}
		memDB = poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port,
			cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name)
	}
	var sb strings.Builder
	args := make([]any, 0, len(terms))
	sb.WriteString("SELECT title, body, created_at FROM memories WHERE NOT tombstoned AND (")
	for i, t := range terms {
		if i > 0 {
			sb.WriteString(" OR ")
		}
		p := "$" + strconv.Itoa(i+1)
		sb.WriteString("title ILIKE " + p + " OR body ILIKE " + p)
		args = append(args, "%"+t+"%")
	}
	sb.WriteString(") ORDER BY created_at DESC LIMIT 12")
	rows, err := memDB.Query(sb.String(), args...)
	if err != nil {
		memDB = nil
		return nil
	}
	type scored struct {
		it   ctxItem
		hits int
	}
	cand := make([]scored, 0, len(rows.Vals))
	for _, v := range rows.Vals {
		if len(v) < 3 || v[0] == nil {
			continue
		}
		title := *v[0]
		body := ""
		if v[1] != nil {
			body = *v[1]
		}
		low := strings.ToLower(title + " " + body)
		hits := 0
		for _, t := range terms {
			if strings.Contains(low, t) {
				hits++
			}
		}
		when := ""
		if v[2] != nil {
			if ms, perr := strconv.ParseInt(*v[2], 10, 64); perr == nil {
				when = time.UnixMilli(ms).UTC().Format("2006-01-02")
			}
		}
		snip := title
		if body != "" {
			snip = title + " , " + body
		}
		if r := []rune(snip); len(r) > 240 {
			snip = string(r[:240]) + "…"
		}
		cand = append(cand, scored{ctxItem{
			When: when, Source: "memory", Snippet: snip,
			Why:   "remembered , matched " + strconv.Itoa(hits) + " of your words",
			Score: float64(hits) / float64(len(terms)),
		}, hits})
	}
	sort.SliceStable(cand, func(a, b int) bool { return cand[a].hits > cand[b].hits })
	out := make([]ctxItem, 0, 2)
	for _, c := range cand {
		if c.hits == 0 {
			continue
		}
		out = append(out, c.it)
		if len(out) == 2 {
			break
		}
	}
	return out
}

// otdYear is one year's slice of an On This Day report.
type otdYear struct {
	Year      int      `json:"year"`
	YearsAgo  int      `json:"years_ago"`
	Narrative string   `json:"narrative,omitempty"`
	Places    []string `json:"places,omitempty"`
	Photos    []string `json:"photos,omitempty"` // frame hashes , the app renders via /v1/frames/thumb
	Notes     []string `json:"notes,omitempty"`  // journal entry titles
}

// onThisDay composes (or returns the cached) report for one month-day. All queries lean on the
// existing lazy memDB handle. Narrative is BEST-EFFORT per year: an oracled miss leaves that year
// factual (places + photos + notes stand on their own) rather than failing the report.
func onThisDay(runDir, day string, lg *slog.Logger) (string, error) {
	if memDB == nil {
		mount := filepath.Dir(runDir)
		cfg, err := hw.LoadServicesConfig(mount)
		if err != nil {
			return "", err
		}
		memDB = poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port,
			cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name)
	}
	// fresh cache wins
	if rows, err := memDB.Query("SELECT body, generated_at FROM reports WHERE day = $1", day); err == nil &&
		len(rows.Vals) == 1 && rows.Vals[0][0] != nil && rows.Vals[0][1] != nil {
		if gen, perr := strconv.ParseInt(*rows.Vals[0][1], 10, 64); perr == nil &&
			time.Since(time.UnixMilli(gen)) < 20*time.Hour {
			return *rows.Vals[0][0], nil
		}
	}
	nowYear := time.Now().UTC().Year()
	years := map[int]*otdYear{}
	get := func(y int) *otdYear {
		if years[y] == nil {
			years[y] = &otdYear{Year: y, YearsAgo: nowYear - y}
		}
		return years[y]
	}
	// photos + places for this month-day, every year except the current one
	if rows, err := memDB.Query(`
		SELECT hash, COALESCE(place,''), extract(year from to_timestamp(taken_at))::int AS y
		FROM frames WHERE kind = 'photo' AND to_char(to_timestamp(taken_at), 'MM-DD') = $1
		ORDER BY taken_at ASC LIMIT 400`, day); err == nil {
		for _, v := range rows.Vals {
			if len(v) < 3 || v[0] == nil || v[2] == nil {
				continue
			}
			y, _ := strconv.Atoi(*v[2])
			if y == 0 || y == nowYear {
				continue
			}
			yr := get(y)
			yr.Photos = append(yr.Photos, *v[0]) // ALL candidates; hour-spread picks 12 below
			if v[1] != nil && *v[1] != "" {
				seen := false
				for _, p := range yr.Places {
					if p == *v[1] {
						seen = true
						break
					}
				}
				if !seen && len(yr.Places) < 4 {
					yr.Places = append(yr.Places, *v[1])
				}
			}
		}
	}
	// journal notes for this month-day (chats, dropped texts, jots)
	if rows, err := memDB.Query(`
		SELECT title, extract(year from to_timestamp(ts))::int FROM journal_entries
		WHERE to_char(to_timestamp(ts), 'MM-DD') = $1 AND title <> ''
		ORDER BY ts ASC LIMIT 100`, day); err == nil {
		for _, v := range rows.Vals {
			if len(v) < 2 || v[0] == nil || v[1] == nil {
				continue
			}
			y, _ := strconv.Atoi(*v[1])
			if y == 0 || y == nowYear {
				continue
			}
			if yr := get(y); len(yr.Notes) < 6 {
				yr.Notes = append(yr.Notes, *v[0])
			}
		}
	}
	// SMARTER photo pick: 12 spread ACROSS the day, not the first 12 , the first-N cut showed
	// twelve frames of breakfast and none of the summit. Photos arrived taken_at-ordered, so
	// even striding preserves chronology while sampling the whole arc of the day.
	for _, yr := range years {
		if n := len(yr.Photos); n > 12 {
			picked := make([]string, 0, 12)
			for i := 0; i < 12; i++ {
				picked = append(picked, yr.Photos[i*n/12])
			}
			yr.Photos = picked
		}
	}
	// order years newest-first, narrate the ones with substance (cap 5 model calls)
	keys := make([]int, 0, len(years))
	for y := range years {
		keys = append(keys, y)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(keys)))
	oc := oracle.NewClient(runDir, 90*time.Second)
	narrated := 0
	out := make([]otdYear, 0, len(keys))
	for _, y := range keys {
		yr := years[y]
		if narrated < 5 && (len(yr.Places) > 0 || len(yr.Notes) > 0) {
			wd := ""
			if t, terr := time.Parse("2006-01-02", strconv.Itoa(yr.Year)+"-"+day); terr == nil {
				wd = "It was a " + t.Weekday().String() + ". "
			}
			facts := wd + "Places: " + strings.Join(yr.Places, "; ") + ". Notes: " + strings.Join(yr.Notes, "; ") +
				". Photo count: " + strconv.Itoa(len(yr.Photos)) + "."
			if resp, ierr := oc.Infer(oracle.Request{
				Capability: "summarize", Priority: oracle.PriorityBackground,
				Input: "Write 2-3 warm, concrete sentences telling the USER what they were doing on this day " +
					strconv.Itoa(yr.YearsAgo) + " year(s) ago, from these facts. Speak to them as 'you'. No preamble, no invented details.\n\n" + facts,
			}); ierr == nil {
				yr.Narrative = strings.TrimSpace(resp.Output)
				narrated++
			}
		}
		out = append(out, *yr)
	}
	b, _ := json.Marshal(map[string]any{"day": day, "years": out})
	body := string(b)
	if err := memDB.Exec(
		"INSERT INTO reports (day, generated_at, body) VALUES ($1,$2,$3::jsonb) ON CONFLICT (day) DO UPDATE SET generated_at = EXCLUDED.generated_at, body = EXCLUDED.body",
		day, time.Now().UnixMilli(), body); err != nil {
		lg.Warn("report cache write failed (report still served)", "fn", "onThisDay", "err", err)
	}
	return body, nil
}
