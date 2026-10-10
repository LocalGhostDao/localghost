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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/LocalGhostDao/localghost/server/internal/feeds"
	"github.com/LocalGhostDao/localghost/server/internal/harden"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/rates"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

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

// chatConn is one more connection to the same database (the Wikipedia import's readers each take
// one: a connection carries one INSERT at a time), nil when services.conf will not read.
func chatConn(mount string) *poltergres.ReadWrite {
	sc, err := hw.LoadServicesConfig(mount)
	if err != nil {
		return nil
	}
	return poltergres.NewReadWrite(hw.SocketForMount(mount), sc.Postgres.Port, sc.Postgres.RWUser, sc.Postgres.RWPass, sc.Postgres.Name)
}

// chatTurn is one persisted half of an exchange, in the shape oracled's chat endpoint takes.
type chatTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Bounds on what a conversation's past can cost the model. Twelve messages is six exchanges , far
// enough back for "the second one you mentioned"; the character cap keeps a pasted document from
// eating the context window on every later turn. Oldest turns fall off first.
const (
	historyMaxMsgs  = 12
	historyMaxChars = 8000
)

// chatHistory loads the last turns of a persisted chat, oldest first, so the model answers the
// conversation and not just the sentence. Read BEFORE the current prompt is persisted, so the
// prompt is never duplicated as history. Empty on any trouble , continuity is a feature, not a
// dependency, same rule as persistence itself.
func chatHistory(mount string, chatID int64) []chatTurn {
	if chatID == 0 {
		return nil
	}
	db := chatStore(mount)
	if db == nil {
		return nil
	}
	// an answer still being written (the app was closed on it and a new question came) is not
	// history yet; a box whose schema has no state column yet reads the old way
	rows, err := db.Query(`SELECT role, content FROM chat_messages WHERE chat_id = $1 AND state <> 'writing' ORDER BY id DESC LIMIT $2`,
		strconv.FormatInt(chatID, 10), strconv.Itoa(historyMaxMsgs))
	if err != nil {
		rows, err = db.Query(`SELECT role, content FROM chat_messages WHERE chat_id = $1 ORDER BY id DESC LIMIT $2`,
			strconv.FormatInt(chatID, 10), strconv.Itoa(historyMaxMsgs))
	}
	if err != nil {
		slog.Warn("chat history load failed, answering without it", "fn", "chatHistory", "err", err)
		return nil
	}
	return trimHistory(rows.Vals)
}

// trimHistory turns newest-first rows into an oldest-first, character-bounded turn list.
func trimHistory(vals [][]*string) []chatTurn {
	out := make([]chatTurn, 0, len(vals))
	chars := 0
	for _, v := range vals { // newest first: stop once the budget is spent
		if len(v) < 2 || v[0] == nil || v[1] == nil || *v[1] == "" {
			continue
		}
		if chars+len(*v[1]) > historyMaxChars {
			break
		}
		chars += len(*v[1])
		out = append(out, chatTurn{Role: *v[0], Content: *v[1]})
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 { // back to oldest first
		out[i], out[j] = out[j], out[i]
	}
	return out
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
	harden.NoDump() // same-user processes cannot read this one through /proc; no core file
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

	runDir := os.Getenv("GHOST_RUN_DIR")
	if runDir == "" {
		if ld := os.Getenv("GHOST_LOG_DIR"); ld != "" {
			runDir = filepath.Join(filepath.Dir(ld), "run")
		}
	}
	if runDir != "" {
		wikiMount = filepath.Dir(runDir) // the volume: its Wikipedia folder and database (wikipedia.go)
	}
	// THE HEALTH LINE says what the daemon has done, from the last pass's own record: the
	// memories it keeps live and when it last ran. Until 10 October 2026 it said "index empty
	// (corpus not built)", the line of the first week's query engine over an empty index, long
	// after the memories, the days, the news and Wikipedia were real.
	srv := ghosthealth.NewServer(service, ghosthealth.Cached(service, time.Minute, func() string {
		if wikiMount == "" {
			return "memories, days, people, the news and Wikipedia: no volume yet"
		}
		db := chatStore(wikiMount)
		if db == nil {
			return "the volume's database is not up"
		}
		live := "?"
		if rows, err := db.Query("SELECT count(*) FROM memories WHERE NOT tombstoned"); err == nil && len(rows.Vals) == 1 && rows.Vals[0][0] != nil {
			live = *rows.Vals[0][0]
		}
		st, ok := hw.LoadSynthStatus(db)
		if !ok {
			return "memories " + live + " · the first pass runs ten minutes after unlock"
		}
		ago := time.Since(time.Unix(st.At, 0)).Round(time.Minute)
		return "memories " + live + " · last pass " + ago.String() + " ago"
	}))

	// Streaming chat , the SAME seam as the ctlsock chat command (context gathered and injected
	// here, transparency first on the wire), token-by-token. Event protocol downstream:
	//   data: {"context":[...]}        first, always (empty array when nothing injected)
	//   data: {"t":"..."}              tokens, oracled's translation of llama's SSE
	//   data: {"done":true,"model":x}  last
	streamMux := http.NewServeMux()
	// /plan: what the question needs from the web and which searches would find it, from the
	// model, in one short call (websmart.go). The phone asks before it searches; on any failure
	// it plans by itself as before. {"ok":false} is an honest "plan it yourself".
	planClient := oracle.NewClient(runDir, 25*time.Second)
	// STOP in the app: the answer being written for this chat ends here (answers.go); what was
	// written is kept, marked stopped. Closing the app does not stop an answer, this does.
	streamMux.HandleFunc("/chat/stop", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			ChatID int64 `json:"chatId"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&q) != nil || q.ChatID <= 0 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		stopped := stopAnswer(q.ChatID)
		lg.Info("answer stopped by the person", "fn", "chatStop", "chat", q.ChatID, "wasRunning", stopped)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "stopped": stopped})
	})
	if runDir != "" {
		go settleOrphans(filepath.Dir(runDir)) // answers a previous synthd left mid-sentence
	}
	streamMux.HandleFunc("/plan", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Prompt  string     `json:"prompt"`
			History []chatTurn `json:"history,omitempty"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&q) != nil || q.Prompt == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// THE BOX'S SPEED rides with every answer, so the phone can decide who reads the pages
		// (the phone's model when the box is on its CPU). On the CPU the plan itself would take a
		// minute: the box says so at once and the phone plans by itself.
		sp := currentSpeed(runDir)
		box := planBox(sp)
		// WHAT THE BOX KEEPS needs no search: a coin's price, the top coins, crypto as a whole, a
		// rate between currencies, the day's headlines. Said before the model is asked (and on
		// the CPU too), so the phone leaves the web alone and the chat puts the box's numbers in.
		if why, has := boxCovers(runDir, q.Prompt); has {
			lg.Info("web plan", "fn", "plan", "search", false, "boxHas", true) // the why names what was asked: debug only
			lg.Debug("web plan: the box has it", "fn", "plan", "why", why)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "search": false, "boxHas": why, "need": "", "shape": "figure", "fresh": true, "queries": []string{}, "box": box})
			return
		}
		if sp.Known && !sp.OnGPU {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "box": box, "why": "the model is on the CPU"})
			return
		}
		t0 := time.Now()
		resp, err := planClient.Infer(oracle.Request{
			Capability: "chat", Class: oracle.ClassLocalSmall, Priority: oracle.PriorityInteractive,
			Input: planPrompt(q.Prompt, q.History, time.Now()), MaxTokens: 220, Temperature: 0.1, DeadlineMS: 8000,
		})
		if err != nil || resp.Err != "" {
			lg.Warn("web plan: no answer from the model", "fn", "plan", "err", err, "modelErr", resp.Err)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "box": box})
			return
		}
		p, ok := parsePlan(resp.Output)
		if !ok {
			lg.Warn("web plan: unusable answer", "fn", "plan", "out", clip(resp.Output, 200))
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "box": box})
			return
		}
		// what was asked stays out of the INFO log: the log outlives a deleted chat by a week, and
		// the plan does not know an incognito question from any other (found 30 Sep 2026)
		lg.Info("web plan", "fn", "plan", "search", p.Search, "queries", len(p.Queries), "took", time.Since(t0).Round(time.Millisecond))
		lg.Debug("web plan asked", "fn", "plan", "need", p.Need, "queries", p.Queries)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "search": p.Search, "need": p.Need, "shape": p.Shape, "fresh": p.Fresh, "queries": p.Queries, "box": box})
	})
	streamMux.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Prompt    string `json:"prompt"`
			Think     string `json:"think"`
			Incognito bool   `json:"incognito"`
			ChatID    int64  `json:"chatId"`
			Image     string `json:"image,omitempty"`
			// The phone's own copy of the conversation , used only when the box holds none
			// (incognito, or a chat that never persisted). Bounded the same way as the box's.
			History []chatTurn `json:"history,omitempty"`
			// Web results the PHONE fetched for this question. The box has no internet by design,
			// so anything from the outside world arrives this way, labelled as such in the prompt
			// and in the transparency record; the person chose to search on the phone.
			Web []webHit `json:"web,omitempty"`
			// The phone's last fix (recent ones only), so a question about going somewhere can be
			// answered with places near it that fit what the person likes. Never leaves the box.
			Here *hereT `json:"here,omitempty"`
			// THE SMARTER SEARCH (websmart.go): what the model said the question needs (/plan),
			// which round this is (1, or 2 after the box asked for more), and the plan's searches
			// the phone has not run yet, so the box can ask for one when what it read is thin.
			Need  string   `json:"need,omitempty"`
			Round int      `json:"round,omitempty"`
			Spare []string `json:"spare,omitempty"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&q) != nil || q.Prompt == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		mount := filepath.Dir(runDir)
		// THE CONVERSATION, not the sentence. Every turn was persisted and none was ever shown to
		// the model , each message opened as a one-shot with no memory of the last, which reads
		// exactly like a chat that has stopped working. Box history outranks the phone's copy
		// when a persisted chat exists; incognito hands the phone's copy through and stores none.
		var history []chatTurn
		if !q.Incognito && q.ChatID != 0 {
			history = chatHistory(mount, q.ChatID)
		}
		if len(history) == 0 && len(q.History) > 0 {
			vals := make([][]*string, 0, len(q.History))
			for i := len(q.History) - 1; i >= 0; i-- { // trimHistory expects newest first
				t := q.History[i]
				if t.Role != "user" && t.Role != "assistant" {
					continue
				}
				role, content := t.Role, t.Content
				vals = append(vals, []*string{&role, &content})
			}
			history = trimHistory(vals)
		}
		items := gatherContext(runDir, q.Prompt)
		// THE PERSON'S TASTE, when the question asks for a suggestion or a plan, and the places
		// around the phone that fit it; THE WEATHER the box pulled, for a weather question
		// (weather.go, the fix or the trail says where). Samples (the empty-index placeholders)
		// make way.
		extra := tasteItems(chatStore(mount), q.Prompt, q.Here)
		for _, it := range weatherItems(chatStore(mount), q.Prompt, q.Here, time.Now()) {
			extra = append(extra, sanitize(it))
		}
		if len(extra) > 0 {
			real := items[:0:0]
			for _, it := range items {
				if it.Source != "sample" {
					real = append(real, it)
				}
			}
			items = append(real, extra...)
		}
		web := boundWeb(q.Web)
		// THE PARAGRAPHS, ranked against the need: each page's excerpt becomes the passages that
		// say what is needed. Thin, with a search still unrun: ask the phone for one more round
		// instead of answering from nothing.
		webNote := ""
		if hasParagraphs(web) {
			need := clip(strings.TrimSpace(q.Need), 300)
			if need == "" {
				need = clip(q.Prompt, 300)
			}
			best, embedded := rankParagraphs(runDir, need, web)
			how := "by meaning"
			if !embedded {
				how = "by the words"
			}
			webNote = fmt.Sprintf("read %d pages' paragraphs %s, best match %.2f", len(web), how, best)
			lg.Info("web findings ranked", "fn", "chat", "pages", len(web), "best", fmt.Sprintf("%.2f", best), "embedded", embedded)
			lg.Debug("web findings for", "fn", "chat", "need", need)
			if q.Round <= 1 && len(q.Spare) > 0 && best < rankThinBelow {
				moreWeb(w, q.Spare, fmt.Sprintf("nothing read comes close to what is needed (best %.2f); searching once more", best))
				return
			}
		} else if len(web) == 0 && q.Round <= 1 && len(q.Spare) > 0 && q.Need != "" {
			moreWeb(w, q.Spare, "the first searches found nothing; searching once more")
			return
		}
		// THE BUDGET: what the model can read in about twenty seconds at its measured prefill
		// speed. Only asked when there is something big to fit (the web, a long history).
		var speed engineSpeed
		trimmed := false
		histChars := 0
		for _, t := range history {
			histChars += len(t.Content)
		}
		if len(web) > 0 || histChars > 4000 {
			speed = currentSpeed(runDir)
			used := len(formatContext(items)) + len(q.Prompt) + histChars
			web, trimmed = fitWeb(web, speed.contextBudget()-used)
		}
		for i, h := range web {
			why := "searched on your phone; the box itself never reaches the internet"
			if h.Kind != "page" && h.Kind != "" {
				why = h.Kind + " fetched by your phone; the box itself never reaches the internet"
			}
			items = append(items, sanitize(ctxItem{When: h.Fetched, Source: "web", Snippet: fmt.Sprintf("[%d] %s , %s", i+1, h.Title, h.URL), Why: why}))
		}
		input := q.Prompt
		block := formatContext(items)
		if wb := formatWeb(web); wb != "" {
			block += wb
		}
		if block != "" {
			ask := "answer:"
			if len(history) > 0 {
				// the web findings are loud; the conversation is what says what a short follow-up
				// ("how about now?") is asking, so it is named here, with the need when there is one
				ask = "answer the next message of the conversation above, read as its next turn (a short one repeats or adjusts the last question)"
				if n := strings.TrimSpace(q.Need); n != "" {
					ask += "; what it asks for: " + clip(n, 300)
				}
				ask += ":"
			}
			input = block + "\n\nUsing the context above only where it is actually relevant (say which source when you use one), " + ask + "\n" + q.Prompt
		}
		// who the box is and who is asking: "LocalGhost" in a question is the box itself
		input = chatIdentity(mount) + "\n\n" + input
		// Persist the question FIRST (incognito conversations never touch the tables), then the
		// answer's row, so the answer is the box's from here on (answers.go): the chat id goes to
		// the phone in the first event, and the generation no longer dies with the connection.
		chatID := q.ChatID
		var saver *answerSaver
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
			if chatID != 0 {
				if msgID := chatStartAnswer(mount, chatID, sourcesJSON(web)); msgID != 0 {
					saver = newAnswerSaver(mount, chatID, msgID)
				}
			}
		}
		genCtx, endGen := answerContext(r.Context(), chatID, saver != nil)
		defer endGen()
		body, _ := json.Marshal(map[string]any{"prompt": input, "think": q.Think, "image": q.Image, "history": history})
		req, err := http.NewRequestWithContext(genCtx, http.MethodPost,
			"http://ghost/chat", bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := streamsock.Client("ghost.oracled", runDir).Do(req)
		if err != nil {
			lg.Warn("chat stream: oracled unreachable", "fn", "chat", "err", err)
			if saver != nil {
				saver.update("", "", "stopped")
			}
			http.Error(w, "model unavailable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lg.Warn("chat stream refused by oracled", "fn", "chat", "code", resp.StatusCode)
			if saver != nil {
				saver.update("", "", "stopped")
			}
			http.Error(w, "model unavailable", http.StatusBadGateway)
			return
		}
		fl, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		ev := map[string]any{"context": items, "steps": chatSteps(items, web, webNote)}
		if !speed.at.IsZero() {
			if note := readingNote(speed, len(input)+histChars, trimmed); note != "" {
				ev["note"] = note
			}
		}
		if webNote != "" {
			if n, _ := ev["note"].(string); n != "" {
				ev["note"] = n + " · " + webNote
			} else {
				ev["note"] = webNote
			}
		}
		if chatID != 0 {
			ev["chatId"] = chatID // at once: an app closed mid-answer reopens on this chat
		}
		ctxEv, _ := json.Marshal(ev)
		_, _ = w.Write([]byte("data: " + string(ctxEv) + "\n\n"))
		if fl != nil {
			fl.Flush()
		}
		// Accumulate the answer and the thinking from the token events while piping them through;
		// the saver keeps the row up with them, and the done event carries the chatId.
		var answer, thinking strings.Builder
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
		vf := &voiceFilter{}                     // the answer's em dashes become commas on the way out (voice.go)
		emit := func(kind, text string) string { // kind: "r" or "t"; what was written, for the persisted answer
			if kind == "t" {
				text = vf.pass(text)
			}
			if text == "" {
				return ""
			}
			key := "t"
			if kind == "r" {
				key = "r"
				thinking.WriteString(text)
			}
			nb, _ := json.Marshal(map[string]string{key: text})
			_, _ = w.Write([]byte("data: " + string(nb) + "\n"))
			if fl != nil {
				fl.Flush()
			}
			return text
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
						answerOut.WriteString(emit("t", s))
						break
					}
					answerOut.WriteString(emit("t", s[:i]))
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
		finished := false
		var lastSave time.Time
		defer func() {
			// the stream ended without its done event: the model died, STOP was pressed, or the
			// run hit its bound; what was written stays, marked as stopped
			if !finished && saver != nil {
				saver.update(answer.String(), thinking.String(), "stopped")
			}
		}()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 64<<10)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "data: ") {
				payload := strings.TrimPrefix(line, "data: ")
				var tok struct {
					T    string `json:"t"`
					R    string `json:"r"`
					Done bool   `json:"done"`
				}
				if json.Unmarshal([]byte(payload), &tok) == nil {
					if tok.R != "" {
						thinking.WriteString(tok.R) // native reasoning events pass through as they are
					}
					if tok.T != "" {
						// Split reasoning from answer; only the answer portion is persisted.
						answer.WriteString(route(tok.T))
						_ = sawThink
						if saver != nil && time.Since(lastSave) > 500*time.Millisecond {
							saver.update(answer.String(), thinking.String(), "")
							lastSave = time.Now()
						}
						if !tok.Done {
							continue // token already emitted (r or t) by route; skip the raw line
						}
					}
					if tok.Done {
						finished = true
						if tail := vf.flush(); tail != "" {
							answer.WriteString(emit("t", tail))
						}
						if saver != nil {
							saver.update(answer.String(), thinking.String(), "done")
							saver.wait(3 * time.Second) // the row is whole before the phone is told
						} else if !q.Incognito && chatID != 0 {
							// No row was started (it failed): save the answer the old way, once.
							// chatID != 0 guard: appending the assistant alone would CREATE a chat
							// titled by the answer , worse than losing one exchange.
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
	// the notification store the digests post through (the mount's own Postgres and Redis)
	newsProduce := func(n hw.Notification) error { return errors.New("no run dir: notifications off") }
	if runDir != "" {
		m := filepath.Dir(runDir)
		store := hw.NewNotifStore(func(int) string { return hw.SocketForMount(m) })
		slot := slotOf(m)
		newsProduce = func(n hw.Notification) error { return store.Produce(slot, n) }
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
			input = chatIdentity(mount) + "\n\n" + input
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
			data, _ := json.Marshal(chatReply{Output: plainDashes(resp.Output), Model: resp.Model, Context: items})
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
		// OUTINGS , the memories made from the photos, and the taste they add up to; "rebuild"
		// forces the pass now (the loop otherwise waits for the archive to change and 30 minutes).
		ctl.Handle("outings", func(args json.RawMessage) (ctlsock.Response, error) {
			var a struct {
				Rebuild bool `json:"rebuild"`
			}
			if len(args) > 0 {
				_ = json.Unmarshal(args, &a)
			}
			db := chatStore(mount)
			if db == nil {
				return ctlsock.Response{OK: false, Err: "no database (box locked?)"}, nil
			}
			out := map[string]any{}
			if a.Rebuild {
				lastOutingPass = time.Time{}
				_ = db.Exec("DELETE FROM settings WHERE key = 'synthd_outings_sig'")
				n, err := outingPass(db, lg)
				if err != nil {
					return ctlsock.Response{OK: false, Err: err.Error()}, nil
				}
				out["written"] = n
			}
			for k, v := range outingsSummary(db) {
				out[k] = v
			}
			data, _ := json.Marshal(out)
			return ctlsock.Response{OK: true, Data: data}, nil
		})
		// days: the prebuilt day summaries , how many, how many the model wrote, the newest and
		// the oldest told; day=YYYY-MM-DD marks one day to be built again at the next pass (its
		// signature and tries cleared; rewrite=true also lets the model write it again at once);
		// pass=true runs a pass now.
		ctl.Handle("days", func(args json.RawMessage) (ctlsock.Response, error) {
			var a struct {
				Day     string `json:"day"`
				Rewrite bool   `json:"rewrite"`
				Pass    bool   `json:"pass"`
			}
			if len(args) > 0 {
				_ = json.Unmarshal(args, &a)
			}
			db := chatStore(mount)
			if db == nil {
				return ctlsock.Response{OK: false, Err: "no database (box locked?)"}, nil
			}
			out := map[string]any{}
			if a.Day != "" {
				if _, err := time.Parse("2006-01-02", a.Day); err != nil {
					return ctlsock.Response{OK: false, Err: "day=YYYY-MM-DD"}, nil
				}
				q := "UPDATE day_summaries SET signature = '', prose_tries = 0, tries_sig = ''"
				if a.Rewrite {
					q += ", written_by = 'template', model_at = 0"
				}
				if err := db.Exec(q+" WHERE day = $1", a.Day); err != nil {
					return ctlsock.Response{OK: false, Err: err.Error()}, nil
				}
				out["cleared"] = a.Day
				a.Pass = true
			}
			if a.Pass {
				lastDayPass = time.Time{}
				oc := oracle.NewClient(runDir, 2*time.Minute)
				var built, wrote int
				var err error
				if a.Day != "" {
					built, wrote, err = daySummaryPass(db, oc, mount, lg, a.Day) // that day, now, whatever the hour
				} else {
					built, wrote, err = daySummaryPass(db, oc, mount, lg)
				}
				if err != nil {
					return ctlsock.Response{OK: false, Err: err.Error()}, nil
				}
				out["built"] = built
				out["byModelNow"] = wrote
			}
			if rows, err := db.Query("SELECT count(*), count(*) FILTER (WHERE written_by = 'model'), coalesce(min(day),''), coalesce(max(day),'') FROM day_summaries"); err == nil && len(rows.Vals) == 1 && len(rows.Vals[0]) >= 4 {
				v := rows.Vals[0]
				out["days"], _ = strconv.Atoi(str(v[0]))
				out["byModel"], _ = strconv.Atoi(str(v[1]))
				out["oldest"] = str(v[2])
				out["newest"] = str(v[3])
			}
			if rows, err := db.Query("SELECT value FROM settings WHERE key = 'synthd_days_watermark'"); err == nil && len(rows.Vals) == 1 && len(rows.Vals[0]) > 0 {
				out["backfillAt"] = str(rows.Vals[0][0])
			}
			data, _ := json.Marshal(out)
			return ctlsock.Response{OK: true, Data: data}, nil
		})
		// news: the feeds and their health, the counts; feeds can be added (add={id,name,url}),
		// removed (remove=id) or switched (enable=id on=true|false); digest=true posts the digest
		// now whatever the hour; brief=true writes the day's brief now (and says why when it
		// cannot: no summaries yet, the model on the CPU, an answer that did not hold).
		ctl.Handle("news", func(args json.RawMessage) (ctlsock.Response, error) {
			var a struct {
				Add    *feeds.Source `json:"add"`
				Remove string        `json:"remove"`
				Enable string        `json:"enable"`
				On     *bool         `json:"on"`
				Digest bool          `json:"digest"`
				Fetch  bool          `json:"fetch"` // the box fetches the feeds now, whatever the phone is on
				Brief  bool          `json:"brief"` // the brief written now, whatever its age (the phone's button)
			}
			if len(args) > 0 {
				_ = json.Unmarshal(args, &a)
			}
			db := chatStore(mount)
			if db == nil {
				return ctlsock.Response{OK: false, Err: "no database (box locked?)"}, nil
			}
			if err := seedFeeds(db); err != nil {
				return ctlsock.Response{OK: false, Err: err.Error()}, nil
			}
			if a.Fetch {
				select {
				case newsForce <- struct{}{}:
				default:
				}
			}
			if a.Add != nil {
				if a.Add.ID == "" || !strings.HasPrefix(a.Add.URL, "https://") {
					return ctlsock.Response{OK: false, Err: "add wants id, name and an https url"}, nil
				}
				if a.Add.Name == "" {
					a.Add.Name = a.Add.ID
				}
				if err := db.Exec("INSERT INTO news_feeds (id, name, url, added_at) VALUES ($1,$2,$3,$4) ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, url = EXCLUDED.url, enabled = true",
					a.Add.ID, a.Add.Name, a.Add.URL, time.Now().Unix()); err != nil {
					return ctlsock.Response{OK: false, Err: err.Error()}, nil
				}
			}
			if a.Remove != "" {
				if err := db.Exec("DELETE FROM news_feeds WHERE id = $1", a.Remove); err != nil {
					return ctlsock.Response{OK: false, Err: err.Error()}, nil
				}
			}
			if a.Enable != "" && a.On != nil {
				if err := db.Exec("UPDATE news_feeds SET enabled = $2 WHERE id = $1", a.Enable, *a.On); err != nil {
					return ctlsock.Response{OK: false, Err: err.Error()}, nil
				}
			}
			out := map[string]any{}
			if a.Fetch {
				out["fetching"] = "the box fetches the feeds now; ask again in a minute"
			}
			if a.Brief {
				// one brief at a time: a second press while one is being written waits for it
				briefNowMu.Lock()
				wrote, why, err := briefPass(db, oracle.NewClient(runDir, 2*time.Minute), time.Now(), lg, true)
				briefNowMu.Unlock()
				if err != nil {
					return ctlsock.Response{OK: false, Err: err.Error()}, nil
				}
				if wrote {
					putHotNews(db, mount, lg)
				}
				out["briefWritten"], out["briefWhy"] = wrote, why
			}
			if a.Digest {
				now := time.Now()
				n, err := postDigest(db, now, now.In(hw.LocalZone(db)).Format("15:04"), now.In(hw.LocalZone(db)).Format("2006-01-02"), newsProduce, lg)
				if err != nil {
					return ctlsock.Response{OK: false, Err: err.Error()}, nil
				}
				out["digested"] = n
			}
			st, err := newsStatus(db, time.Now())
			if err != nil {
				return ctlsock.Response{OK: false, Err: err.Error()}, nil
			}
			out["news"] = st
			if c := coinDescStatus(db); c != "" {
				out["coins"] = c // the coin pages' descriptions (coindesc.go)
			}
			data, _ := json.Marshal(out)
			return ctlsock.Response{OK: true, Data: data}, nil
		})
		// rates: the box's market numbers (ghost.tallyd's), and convert amount= from= to=.
		// wiki: the box's own Wikipedia, and a title's lead (wikipedia.go)
		ctl.Handle("wiki", wikiCtl)
		// consolidate: what belongs together, together (consolidate.go). Without arguments, the
		// people and the trips as they stand; run=true merges the people and folds the trips now;
		// write=true also has the model write the people whose facts grew (the GPU, minutes).
		ctl.Handle("consolidate", func(args json.RawMessage) (ctlsock.Response, error) {
			var a struct {
				Run   bool `json:"run"`
				Write bool `json:"write"`
			}
			if len(args) > 0 {
				_ = json.Unmarshal(args, &a)
			}
			db := chatStore(mount)
			if db == nil {
				return ctlsock.Response{OK: false, Err: "no database (box locked?)"}, nil
			}
			out := map[string]any{}
			if a.Run || a.Write {
				folded, err := mergePeople(db, lg)
				if err != nil {
					return ctlsock.Response{OK: false, Err: err.Error()}, nil
				}
				trips, err := tripPass(db, lg)
				if err != nil {
					return ctlsock.Response{OK: false, Err: err.Error()}, nil
				}
				out["peopleFolded"], out["tripsWritten"] = folded, trips
				if a.Write {
					oc := oracle.NewClient(runDir, 3*time.Minute)
					wrote, err := peopleProse(db, oc, setting(db, ownerKey), lg)
					if err != nil {
						return ctlsock.Response{OK: false, Err: err.Error()}, nil
					}
					out["peopleWritten"] = wrote
				}
			}
			for k, v := range consolidateSummary(db) {
				out[k] = v
			}
			data, _ := json.Marshal(out)
			return ctlsock.Response{OK: true, Data: data}, nil
		})
		ctl.Handle("rates", func(args json.RawMessage) (ctlsock.Response, error) {
			db := chatStore(mount)
			if db == nil {
				return ctlsock.Response{OK: false, Err: "no database (box locked?)"}, nil
			}
			var a struct {
				Amount float64 `json:"amount"`
				From   string  `json:"from"`
				To     string  `json:"to"`
			}
			if len(args) > 0 {
				_ = json.Unmarshal(args, &a)
			}
			snap, err := hw.RatesNow(db)
			if err != nil {
				return ctlsock.Response{OK: false, Err: err.Error()}, nil
			}
			out := map[string]any{"rates": snap}
			if a.Amount != 0 && a.From != "" && a.To != "" {
				v, err := rates.Convert(a.Amount, a.From, a.To, snap.FX, snap.USD())
				if err != nil {
					out["convertErr"] = err.Error()
				} else {
					out["converted"] = v
				}
			}
			data, _ := json.Marshal(out)
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
		wikiMount = mountDir
		go distillLoop(ctx, mountDir, runDir, lg)
		// THE NEWS (news.go): the phone's feed bytes in, stories and two digests a day out
		go newsLoop(ctx, mountDir, runDir, newsProduce, lg)
		// WIKIPEDIA (wikipedia.go): the mirror's file into the database, a slice a minute
		go wikiImportLoop(ctx, mountDir, lg)
	}

	<-ctx.Done()
	lg.Info("shutting down", "fn", "main")
}

// slotOf is the slot number in a mount path (<state>/mnt/slot0), 0 when it has none.
func slotOf(mount string) int {
	base := filepath.Base(mount)
	if strings.HasPrefix(base, "slot") {
		if n, err := strconv.Atoi(strings.TrimPrefix(base, "slot")); err == nil {
			return n
		}
	}
	return 0
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
	When    string  `json:"when,omitempty"`  // "2026-04-12" , the item's own date, not today's
	Source  string  `json:"source"`          // "image", "note", "chat", "location"
	Snippet string  `json:"snippet"`         // what the model actually saw (truncated, sanitised)
	Why     string  `json:"why"`             // human-readable reason it was selected
	Score   float64 `json:"score,omitempty"` // retrieval score when the source has one
}

// chatReply is the chat command's wire shape: the answer plus the transparency record.
type chatReply struct {
	Output  string    `json:"output"`
	Model   string    `json:"model,omitempty"`
	Context []ctxItem `json:"context"`
}

type contextSource func(runDir, prompt string) []ctxItem

var contextSources = []contextSource{
	memoriesSource,    // FIRST: what the box knows about the PERSON outranks document search
	photoDigestSource, // the matched photo SET, summarised by category , one item, always fits
	wikiSource,        // "what is X": the article's lead from the box's own Wikipedia (wikipedia.go)
	searchdSource,
	// PLACEHOLDER recentChatsSource: last N turns of this conversation (needs chat storage first ,
	//   see docs/context-injection-design.md phase 3).
	// PLACEHOLDER locationDaySource: "where was I on <date>" prompts answered from framed's day
	//   GeoJSON , cheap, no model, high precision for a narrow question class.
	// PLACEHOLDER calendarish sources as noted/voiced start producing text.
}

// factSources are the box's own figures: its market numbers for a money question (rates.go) and
// the news it gathered (news.go). They say nothing unless the question is about them, and when
// it is they ARE the answer, so they keep their place outside the cap the memories and the
// archive share: "how is the btc price now" used to read two memories, the photos and three
// archive matches, fill the six, and leave the model to say the box had nothing on the price
// (10 Oct 2026). At most maxFacts of them, the money lines first.
var factSources = []contextSource{ratesSource, newsSource}

const maxFacts = 4

// gatherContext runs the sources under one budget and caps the total. Order matters: earlier
// sources get first claim on the cap; the facts and the box's Wikipedia keep their places.
func gatherContext(runDir, prompt string) []ctxItem {
	const maxItems = 6
	var facts []ctxItem
	for _, src := range factSources {
		for _, it := range src(runDir, prompt) {
			if it.Snippet != "" && len(facts) < maxFacts {
				facts = append(facts, sanitize(it))
			}
		}
	}
	// the box's Wikipedia keeps its place: for "tell me about Greenwich" the article's lead is
	// the backbone and the memories are the personal layer, and six memories used to fill the
	// cap before the wiki source had its turn (4 Oct 2026)
	var wiki []ctxItem
	for _, it := range wikiSource(runDir, prompt) {
		if it.Snippet != "" {
			wiki = append(wiki, sanitize(it))
		}
	}
	out := facts
	for _, src := range contextSources {
		if len(out)+len(wiki) >= maxItems+len(facts) {
			break
		}
		for _, it := range src(runDir, prompt) {
			if it.Snippet == "" || it.Source == "wikipedia" {
				continue
			}
			out = append(out, sanitize(it))
			if len(out)+len(wiki) >= maxItems+len(facts) {
				break
			}
		}
	}
	out = append(out, wiki...)
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

// chatSteps says in a few lines what the box drew on for an answer, for the chat to show as the
// trail of the turn beside the phone's own steps: the memories read, the photos matched, the
// article from the box's Wikipedia, the archive, the day's news, the box's own numbers, and the
// web findings the phone handed over with how they were ranked. Nothing for a turn with no context.
func chatSteps(items []ctxItem, web []webHit, webNote string) []string {
	var steps []string
	count := map[string]int{}
	var wikiTitles, rateWhat []string
	for _, it := range items {
		switch it.Source {
		case "sample", "web":
			continue
		case "wikipedia":
			if t, _, ok := strings.Cut(it.Snippet, ":"); ok {
				wikiTitles = append(wikiTitles, strings.TrimSpace(t))
			}
		case "rates":
			if w := strings.TrimSpace(it.Why); w != "" && len(rateWhat) < 2 {
				rateWhat = append(rateWhat, w)
			}
		}
		count[it.Source]++
	}
	if n := count["memory"]; n > 0 {
		steps = append(steps, fmt.Sprintf("read %d %s", n, plural2(n, "memory", "memories")))
	}
	if count["photos"] > 0 {
		steps = append(steps, "matched the photos")
	}
	if len(wikiTitles) > 0 {
		steps = append(steps, "the box's Wikipedia: "+strings.Join(wikiTitles, ", "))
	}
	archive := 0
	for src, n := range count {
		switch src {
		case "memory", "photos", "wikipedia", "rates", "news":
		default:
			archive += n
		}
	}
	if archive > 0 {
		steps = append(steps, fmt.Sprintf("%d %s from the archive index", archive, plural2(archive, "match", "matches")))
	}
	if count["news"] > 0 {
		steps = append(steps, "the day's news on the box")
	}
	if count["rates"] > 0 {
		what := "the box's own numbers"
		if len(rateWhat) > 0 {
			what += " (" + strings.Join(rateWhat, "; ") + ")"
		}
		steps = append(steps, what)
	}
	if len(web) > 0 {
		pages := 0
		for _, h := range web {
			if h.Kind == "page" || h.Kind == "" {
				pages++
			}
		}
		line := fmt.Sprintf("%d web %s from the phone", len(web), plural2(len(web), "finding", "findings"))
		if pages > 0 && pages != len(web) {
			line += fmt.Sprintf(", %d of them pages", pages)
		}
		if webNote != "" {
			line += ": " + webNote
		}
		steps = append(steps, line)
	}
	return steps
}

func plural2(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// sanitize bounds what a stored chunk can do to the prompt: length-capped, newlines flattened , a
// pathological caption must not be able to spend the token budget or fake structure in the block.
func sanitize(it ctxItem) ctxItem {
	s := strings.ReplaceAll(it.Snippet, "\n", " ")
	limit := 240
	if it.Source == "photos" {
		limit = 600 // one line for the whole matched set, eleven categories at most
	}
	if it.Source == "wikipedia" {
		limit = wikiChatLead + 120 // the article's lead, which is what was asked for
	}
	if it.Source == "weather" {
		limit = 700 // now and four days, one line each
	}
	if len(s) > limit {
		s = s[:limit] + "…"
	}
	it.Snippet = s
	return it
}

func formatContext(items []ctxItem) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Context from my archive (found automatically, may be irrelevant):")
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

// photoDigestSource turns the photos that match the question into ONE prompt-sized line: how
// many, when, where, and their tags grouped by category (people, place, object, activity, food,
// animal, vehicle, nature, event, text, style). Six caption snippets tell the model about six
// photos; this tells it what the whole matched set is made of. Same searchd query the snippet
// source runs, limited to images, then searchd's digest over the frame hashes.
func photoDigestSource(runDir, prompt string) []ctxItem {
	c := ctlsock.NewClientTimeout("ghost.searchd", runDir, 3*time.Second)
	resp, err := c.Call("search", map[string]any{"query": prompt, "limit": 24, "sources": "image"})
	if err != nil {
		return nil
	}
	var results []struct {
		Path       string  `json:"path"`
		CapturedAt int64   `json:"capturedAt"`
		Score      float64 `json:"score"`
	}
	if err := json.Unmarshal(resp.Data, &results); err != nil || len(results) == 0 {
		return nil
	}
	var hashes []string
	var first, last int64
	for _, r := range results {
		h := frameHash(r.Path)
		if h == "" {
			continue
		}
		hashes = append(hashes, h)
		if r.CapturedAt > 0 {
			if first == 0 || r.CapturedAt < first {
				first = r.CapturedAt
			}
			if r.CapturedAt > last {
				last = r.CapturedAt
			}
		}
	}
	if len(hashes) == 0 {
		return nil
	}
	dresp, err := c.Call("digest", map[string]any{"hashes": strings.Join(hashes, ","), "per": 6})
	if err != nil {
		return nil
	}
	var digest map[string][]string
	if err := json.Unmarshal(dresp.Data, &digest); err != nil || len(digest) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d photos match", len(hashes))
	if first > 0 {
		f, l := time.Unix(first, 0).UTC().Format("2006-01-02"), time.Unix(last, 0).UTC().Format("2006-01-02")
		if f == l {
			b.WriteString(" (" + f + ")")
		} else {
			b.WriteString(" (" + f + " to " + l + ")")
		}
	}
	b.WriteString(": ")
	sep := ""
	for _, cat := range digestOrder(digest) {
		b.WriteString(sep + cat + ": " + strings.Join(digest[cat], ", "))
		sep = " · "
	}
	when := ""
	if last > 0 {
		when = time.Unix(last, 0).UTC().Format("2006-01-02")
	}
	return []ctxItem{{When: when, Source: "photos", Snippet: b.String(), Why: "the matched photos, summarised by category"}}
}

// digestOrder lists categories the way a person reads them: who, where, what, doing, eating,
// then the rest, "other" last.
func digestOrder(d map[string][]string) []string {
	order := []string{"people", "place", "object", "activity", "food", "animal", "vehicle", "nature", "event", "text", "style", "other"}
	var out []string
	for _, k := range order {
		if _, ok := d[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

// frameHash is the 32-hex identity in an archive filename (<hash>.<ext>), or "".
func frameHash(path string) string {
	base := path
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if len(base) != 32 || strings.Trim(base, "0123456789abcdef") != "" {
		return ""
	}
	return base
}

// webHit is one thing the phone fetched. Title, URL and snippet come from the search page; the
// excerpt is the page's own text, cut on the phone. Kind says what it is , a page the phone read,
// a Wikipedia summary, a weather forecast, an exchange rate , and published is the page's own
// date when it had one, so the model can tell a fresh figure from an old article. Everything is
// bounded again here: the phone is trusted to be the person's, not to be tidy.
type webHit struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	Snippet   string `json:"snippet,omitempty"`
	Excerpt   string `json:"excerpt,omitempty"`
	Kind      string `json:"kind,omitempty"`      // page | summary | weather | rate
	Source    string `json:"source,omitempty"`    // duckduckgo | wikipedia (open-meteo and frankfurter until the box took those over)
	Published string `json:"published,omitempty"` // the page's own date, when it had one
	Fetched   string `json:"fetched,omitempty"`   // "2026-09-20 10:41 UTC", set by the phone
	// Paragraphs is the page as the phone's readability pass cut it, when the phone sends them
	// (websmart.go ranks them against the need and writes the excerpt from the best; an old
	// phone sends only its keyword excerpt, which then stands as it is).
	Paragraphs []string `json:"paragraphs,omitempty"`
	// Quote is the page's own words beside a phone's notes (kind note): the paragraph that best
	// matches what was needed, verbatim, so a small model's slip can be seen for what it is.
	Quote string `json:"quote,omitempty"`
}

const (
	webMaxHits    = 8
	webMaxExcerpt = 1500
	webMaxSnippet = 300
)

// note: a page the PHONE'S model read into notes (the box was slow or far), with the page's own
// best paragraph as a quote beside them so the notes can be checked against the page's words.
var webKinds = map[string]bool{"page": true, "summary": true, "weather": true, "rate": true, "note": true}

func boundWeb(hits []webHit) []webHit {
	var out []webHit
	for _, h := range hits {
		if strings.TrimSpace(h.URL) == "" && strings.TrimSpace(h.Title) == "" {
			continue
		}
		h.Title = clip(strings.ReplaceAll(h.Title, "\n", " "), 160)
		h.URL = clip(h.URL, 300)
		h.Snippet = clip(strings.ReplaceAll(h.Snippet, "\n", " "), webMaxSnippet)
		if h.Kind == "note" {
			// a phone's notes are lines; keep them apart
			h.Excerpt = strings.ReplaceAll(strings.TrimSpace(h.Excerpt), "\n", " / ")
		}
		h.Excerpt = clip(strings.ReplaceAll(h.Excerpt, "\n", " "), webMaxExcerpt)
		h.Fetched = clip(h.Fetched, 32)
		h.Published = clip(h.Published, 32)
		h.Source = clip(h.Source, 32)
		if !webKinds[h.Kind] {
			h.Kind = "page"
		}
		h.Quote = clip(strings.ReplaceAll(h.Quote, "\n", " "), 600)
		if len(h.Paragraphs) > 16 {
			h.Paragraphs = h.Paragraphs[:16]
		}
		for i, p := range h.Paragraphs {
			h.Paragraphs[i] = clip(strings.ReplaceAll(p, "\n", " "), rankParaMaxLen)
		}
		out = append(out, h)
		if len(out) == webMaxHits {
			break
		}
	}
	return out
}

func hasParagraphs(hits []webHit) bool {
	for _, h := range hits {
		if len(h.Paragraphs) > 0 {
			return true
		}
	}
	return false
}

// moreWeb ends a chat stream before the model spoke: the phone should run these searches and ask
// again (round 2). The stream shape stays the phone's: a context event, then done.
func moreWeb(w http.ResponseWriter, queries []string, why string) {
	if len(queries) > 3 {
		queries = queries[:3]
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ev, _ := json.Marshal(map[string]any{"context": []ctxItem{}, "note": why})
	more, _ := json.Marshal(map[string]any{"more": map[string]any{"queries": queries, "why": why}})
	done, _ := json.Marshal(map[string]any{"done": true, "more": true})
	fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: %s\n\n", ev, more, done)
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
}

// webSite is the host of a hit, the name the model attributes it by.
func webSite(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(p.Host), "www.")
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	// back to a rune boundary: a byte cut through "ș" or "ț" is invalid UTF-8 in the prompt and in
	// the stored JSON (it came back as U+FFFD)
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// formatWeb is the prompt block for the phone's findings. Labelled for what it is, so the model
// can attribute ("according to <site> [2]") and never mistakes it for the archive; numbered, and
// the app shows the same numbers, so a citation in the answer is a link the person can open. A
// figure (weather, a rate) is marked as such: it is a measurement, not somebody's prose, and it
// is dated. The fetched time is printed once so "today" in a page means the right day.
func formatWeb(hits []webHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nFound on the web by my phone for this question (this box has no internet; these are the only outside facts available). Cite by number, e.g. [2], and by site name, whenever you use one; prefer a dated figure to an undated page; say when the findings do not settle the question.")
	for _, h := range hits {
		if h.Kind == "note" {
			b.WriteString(" Some pages were read by the phone's own small model and come as its NOTES with a verbatim QUOTE from the page: trust the quote over the notes where they differ, and treat a note nothing else supports with care.")
			break
		}
	}
	if f := hits[0].Fetched; f != "" {
		b.WriteString(" Fetched " + f + ".")
	}
	for i, h := range hits {
		site := webSite(h.URL)
		if h.Source != "" && h.Source != "duckduckgo" {
			site = h.Source
		}
		label := h.Kind
		switch h.Kind {
		case "weather":
			label = "weather forecast"
		case "rate":
			label = "exchange rate"
		case "summary":
			label = "encyclopedia summary"
		case "note":
			label = "page, read by the phone's model"
		}
		fmt.Fprintf(&b, "\n[%d] %s", i+1, h.Title)
		if site != "" {
			b.WriteString(" (" + site)
			if h.Published != "" {
				b.WriteString(", " + h.Published)
			}
			b.WriteString(")")
		} else if h.Published != "" {
			b.WriteString(" (" + h.Published + ")")
		}
		b.WriteString(" , " + label + " , " + h.URL)
		if h.Snippet != "" && h.Kind == "page" {
			b.WriteString("\n    " + h.Snippet)
		}
		if h.Kind == "note" {
			if h.Excerpt != "" {
				b.WriteString("\n    NOTES: " + h.Excerpt)
			}
			if h.Quote != "" {
				b.WriteString("\n    QUOTE: \"" + h.Quote + "\"")
			}
			continue
		}
		if h.Excerpt != "" {
			b.WriteString("\n    " + h.Excerpt)
		}
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
		// what this pass did, step by step, for Box Status (hw.SynthStatus, written at the end)
		did := map[string]int{}
		// the note about me and my people, when it changed: before the journal, so the names it
		// gives are known when the entries are read
		if an, aerr := aboutPass(db, oc, lg); aerr != nil {
			lg.Warn("about note pass failed", "fn", "distillLoop", "err", aerr)
		} else if an > 0 {
			lg.Info("about note made into memories", "fn", "distillLoop", "memories", an)
			did["about"] = an
		}
		// what the check-ins say about me and my people joins the note's
		if cn, cerr := checkinAboutPass(db, oc, lg); cerr != nil {
			lg.Warn("check-in about pass failed", "fn", "distillLoop", "err", cerr)
		} else if cn > 0 {
			did["checkins"] = cn
		}
		// the memories made before the box knew the name, written again with it
		if nn, nerr := namePass(db, oc, lg); nerr != nil {
			lg.Warn("name pass failed", "fn", "distillLoop", "err", nerr)
		} else if nn > 0 {
			did["named"] = nn
		}
		n, err := distillPass(db, oc, lg)
		if err != nil {
			lg.Warn("distill pass failed, will reconnect next tick", "fn", "distillLoop", "err", err)
			db = nil
			continue
		}
		if n > 0 {
			lg.Info("distilled", "fn", "distillLoop", "memories", n)
			did["distilled"] = n
		}
		outingsChanged := false
		if on, oerr := outingPass(db, lg); oerr != nil {
			lg.Warn("outing pass failed", "fn", "distillLoop", "err", oerr)
		} else if on > 0 {
			lg.Info("outings updated", "fn", "distillLoop", "outings", on)
			outingsChanged = true
			did["outings"] = on
		}
		// what belongs together, together: one memory per person, the trips the outings make
		// (every pass), and once a day the model writes the people whose facts grew
		if folded, trips, people, cerr := consolidatePass(db, oc, outingsChanged, lg); cerr != nil {
			lg.Warn("consolidation failed", "fn", "distillLoop", "err", cerr)
		} else if folded+trips+people > 0 {
			lg.Info("consolidated", "fn", "distillLoop", "peopleFolded", folded, "trips", trips, "peopleWritten", people)
			did["folded"], did["trips"], did["people"] = folded, trips, people
		}
		// the places the person keeps going to, counted; and once a day, what the box notices
		if pn, perr := placesPass(db, mount, lg); perr != nil {
			lg.Warn("places pass failed", "fn", "distillLoop", "err", perr)
		} else if pn > 0 {
			did["places"] = pn
		}
		if in, ierr := insightPass(db, oc, mount, lg); ierr != nil {
			lg.Warn("insight pass failed", "fn", "distillLoop", "err", ierr)
		} else if in > 0 {
			did["insights"] = in
		}
		if pn, perr := prosePass(db, oc, mount, lg); perr != nil {
			lg.Warn("prose pass failed", "fn", "distillLoop", "err", perr)
		} else if pn > 0 {
			lg.Info("memories written by the model", "fn", "distillLoop", "memories", pn)
			did["prose"] = pn
		}
		// THE DAYS, after the outings (a day's sheet names the outing it belongs to)
		if built, wrote, derr := daySummaryPass(db, oc, mount, lg); derr != nil {
			lg.Warn("day summary pass failed", "fn", "distillLoop", "err", derr)
		} else if built > 0 {
			lg.Info("day summaries built", "fn", "distillLoop", "days", built, "byModel", wrote)
			did["days"], did["daysByModel"] = built, wrote
		}
		// and what Box Status shows of this daemon: the pass, the Wikipedia file, the consolidation
		if serr := hw.SaveSynthStatus(db, hw.SynthStatus{At: time.Now().Unix(), Pass: did, Wiki: wikiStatus(), ConsolidatedDay: setting(db, consolidateKey)}); serr != nil {
			lg.Warn("status not written", "fn", "distillLoop", "err", serr)
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
	// A FULL-HISTORY health import journals thousands of "slept 7h, 9k steps" days. The model
	// only sees the recent ones (they inform current memories); the deep past flips straight to
	// distilled , day-level episodes (30d) will read health_metrics directly when they arrive.
	if err := db.Exec(
		"UPDATE journal_entries SET distilled = TRUE WHERE NOT distilled AND source = 'ghost.tallyd' AND ts < (extract(epoch from now())::bigint - 60*86400)"); err != nil {
		return 0, err
	}
	// MEMORIES ARE NOT JOURNAL ENTRIES , the line, drawn: mechanical entries (health day
	// summaries, daily check-ins, timeline lines) are RAW MATERIAL that episodes already carry;
	// running them through the model 1:1 manufactures memory spam that reads like a diary
	// photocopied. They flip distilled SILENTLY. Only substantive writing reaches the model:
	// real notes, chat journals, anything a person actually authored , the things a memory
	// could plausibly be MADE from.
	if err := db.Exec(
		"UPDATE journal_entries SET distilled = TRUE WHERE NOT distilled AND (source = 'ghost.tallyd' OR (source = 'ghost.framed') OR (source = 'ghost.noted' AND title LIKE 'Daily check-in %'))"); err != nil {
		return 0, err
	}
	rows, err := db.Query(
		"SELECT id, source, ref, title, body FROM journal_entries WHERE NOT distilled ORDER BY ts DESC LIMIT 8")
	if err != nil {
		return 0, err
	}
	written := 0
	owner, people := setting(db, ownerKey), peopleNames(db)
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
			Input:      distillPrompt(owner, people, title, body),
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
			// one of my people: their own memory, one per person, the facts added to it
			if p := strings.SplitN(line, "|", 3); len(p) == 3 && strings.EqualFold(strings.TrimSpace(p[0]), "PERSON") {
				if err := notePerson(db, strings.TrimSpace(p[1]), named(strings.TrimSpace(p[2]), owner), ref, srcChat, now); err != nil {
					return written, err
				}
				written++
				continue
			}
			parts := strings.SplitN(line, "|", 2)
			if len(parts) != 2 {
				continue
			}
			t := named(strings.TrimSpace(parts[0]), owner)
			b := named(strings.TrimSpace(parts[1]), owner)
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
	terms := memoryTerms(prompt) // the words that carry meaning; "the" matched every memory
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
	Narrative string   `json:"narrative,omitempty"` // the day's stored summary (day_summaries), model or template
	Title     string   `json:"title,omitempty"`     // "Friday 25 September 2026 · Corner Café, Voutoumi"
	Line      string   `json:"line,omitempty"`      // the route in one line, when the day had one
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
	// fresh cache wins , an hour: the rows underneath change only when a day pass rebuilds one
	if rows, err := memDB.Query("SELECT body, generated_at FROM reports WHERE day = $1", day); err == nil &&
		len(rows.Vals) == 1 && rows.Vals[0][0] != nil && rows.Vals[0][1] != nil {
		if gen, perr := strconv.ParseInt(*rows.Vals[0][1], 10, 64); perr == nil &&
			time.Since(time.UnixMilli(gen)) < time.Hour {
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
	// THE PREBUILT DAYS FIRST: the summary the day pass wrote (the model's when it passed the
	// check, the template otherwise), its covers, places and notes. No model at request time.
	told := map[int]bool{}
	if rows, err := memDB.Query(`SELECT day, summary, title, facts::text FROM day_summaries WHERE substr(day, 6, 5) = $1 AND summary <> '' ORDER BY day`, day); err == nil {
		for _, v := range rows.Vals {
			if len(v) < 4 || v[0] == nil || v[1] == nil {
				continue
			}
			y, _ := strconv.Atoi((*v[0])[:4])
			if y == 0 || y == nowYear {
				continue
			}
			yr := get(y)
			yr.Narrative = *v[1]
			if v[2] != nil {
				yr.Title = *v[2]
			}
			if v[3] != nil {
				var f dayFacts
				if json.Unmarshal([]byte(*v[3]), &f) == nil {
					yr.Photos = f.Covers
					yr.Places = f.Places
					yr.Notes = f.Notes
					yr.Line = f.Line
				}
			}
			told[y] = true
		}
	}
	// years the day pass has not reached yet: the quick facts, no narrative (the backfill will
	// tell them; the report refreshes within the hour)
	if rows, err := memDB.Query(`
		SELECT hash, COALESCE(place,''), extract(year from to_timestamp(taken_at))::int AS y
		FROM frames WHERE kind = 'photo' AND to_char(to_timestamp(taken_at), 'MM-DD') = $1
		ORDER BY taken_at ASC LIMIT 400`, day); err == nil {
		for _, v := range rows.Vals {
			if len(v) < 3 || v[0] == nil || v[2] == nil {
				continue
			}
			y, _ := strconv.Atoi(*v[2])
			if y == 0 || y == nowYear || told[y] {
				continue
			}
			yr := get(y)
			yr.Photos = append(yr.Photos, *v[0])
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
	if rows, err := memDB.Query(`
		SELECT title, extract(year from to_timestamp(ts))::int FROM journal_entries
		WHERE to_char(to_timestamp(ts), 'MM-DD') = $1 AND title <> ''
		ORDER BY ts ASC LIMIT 100`, day); err == nil {
		for _, v := range rows.Vals {
			if len(v) < 2 || v[0] == nil || v[1] == nil {
				continue
			}
			y, _ := strconv.Atoi(*v[1])
			if y == 0 || y == nowYear || told[y] {
				continue
			}
			if yr := get(y); len(yr.Notes) < 6 {
				yr.Notes = append(yr.Notes, *v[0])
			}
		}
	}
	// the untold years' photos: twelve spread across the day, not the first twelve
	for y, yr := range years {
		if told[y] {
			continue
		}
		if n := len(yr.Photos); n > 12 {
			picked := make([]string, 0, 12)
			for i := 0; i < 12; i++ {
				picked = append(picked, yr.Photos[i*n/12])
			}
			yr.Photos = picked
		}
	}
	keys := make([]int, 0, len(years))
	for y := range years {
		keys = append(keys, y)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(keys)))
	out := make([]otdYear, 0, len(keys))
	for _, y := range keys {
		out = append(out, *years[y])
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
