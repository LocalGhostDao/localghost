package voiced

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// DB is the part of the Postgres client the daemon uses (a *poltergres.ReadWrite).
type DB interface {
	Exec(sql string, args ...any) error
	Query(sql string, args ...any) (*poltergres.Rows, error)
}

// MaxTries: a note whisper failed on this many times is marked failed and left for the person
// (ghost-cli ghost.voiced voice-again id=<id> puts it back).
const MaxTries = 3

// IDRE is a voice note id as the phone makes it: 32 hex (a UUID without its dashes).
var IDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Meta is the sidecar secd writes beside each uploaded WAV (<id>.json).
type Meta struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`   // checkin | journal
	Day    string `json:"day"`    // the phone's day, YYYY-MM-DD
	Taken  int64  `json:"taken"`  // recording start, unix ms (the phone's clock)
	Device string `json:"device"` // which phone
}

// Stat is what the health line and `ghost-cli ghost.voiced voice` say.
type Stat struct {
	Pending int    `json:"pending"`
	Done    int    `json:"done"`
	Failed  int    `json:"failed"`
	Engine  string `json:"engine,omitempty"` // the model in use
	Why     string `json:"why,omitempty"`    // why nothing is transcribed
	Last    string `json:"last,omitempty"`   // the last thing done
}

// Daemon is ghost.voiced's work: the inbox secd fills, the archive, the transcriptions.
type Daemon struct {
	Mount string
	Log   *slog.Logger
	Open  func() (DB, error) // lazily connects; the loop reconnects after a failure
	Find  func(mount string) (Engine, string, bool)

	mu      sync.Mutex
	stat    Stat
	working string // the note whisper is on now
	// engMu: one whisper at a time, the queue's or a question's (Hear); two on the card would
	// slow both and can run the memory out
	engMu sync.Mutex
}

// State is what the phone is told beside each waiting note: why nothing is being transcribed, or
// which note is being transcribed now. voiced rewrites it every 15 s at <mount>/voiced/state.json
// and secd adds it to /v1/voice/notes. An old file means voiced is not running.
type State struct {
	Engine  string `json:"engine,omitempty"`
	Why     string `json:"why,omitempty"`
	Working string `json:"working,omitempty"`
	Pending int    `json:"pending"`
	At      int64  `json:"at"` // unix ms of the write
}

// StateFresh: how old the state file may be before voiced counts as not running.
const StateFresh = 90 * time.Second

// StatePath is the state file on the volume.
func StatePath(mount string) string { return filepath.Join(mount, "voiced", "state.json") }

// ReadState reads the state file; ok is false when there is none.
func ReadState(mount string) (State, bool) {
	var st State
	b, err := os.ReadFile(StatePath(mount))
	if err != nil || json.Unmarshal(b, &st) != nil {
		return st, false
	}
	return st, true
}

func (d *Daemon) writeState() {
	d.mu.Lock()
	st := State{Engine: d.stat.Engine, Why: d.stat.Why, Working: d.working, Pending: d.stat.Pending, At: time.Now().UnixMilli()}
	d.mu.Unlock()
	b, _ := json.Marshal(st)
	p := StatePath(d.Mount)
	if err := os.WriteFile(p+".tmp", b, 0o640); err == nil {
		_ = os.Rename(p+".tmp", p)
	}
}

func (d *Daemon) setWorking(id string) {
	d.mu.Lock()
	d.working = id
	d.mu.Unlock()
	d.writeState()
}

func (d *Daemon) inbox() string    { return filepath.Join(d.Mount, "voiced", "inbox") }
func (d *Daemon) rejected() string { return filepath.Join(d.Mount, "voiced", "inbox", "rejected") }
func (d *Daemon) work() string     { return filepath.Join(d.Mount, "voiced", "work") }

// Stat returns the last pass's numbers.
func (d *Daemon) Stat() Stat {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stat
}

func (d *Daemon) setLast(s string) {
	d.mu.Lock()
	d.stat.Last = time.Now().Format("15:04") + " " + s
	d.mu.Unlock()
}

// Run polls until ctx ends: ingest what secd spooled, then transcribe the pending notes one after
// another, newest first (the note just recorded at the check-in is the one someone is waiting for).
func (d *Daemon) Run(ctx context.Context) {
	for _, dir := range []string{d.inbox(), d.rejected(), d.work(), filepath.Join(d.Mount, "voiced", "archive")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			d.Log.Error("voice dirs", "fn", "Run", "err", err)
			return
		}
	}
	// a crash mid-transcription leaves whisper's output and a converted copy behind: gone
	if ents, err := os.ReadDir(d.work()); err == nil {
		for _, e := range ents {
			_ = os.Remove(filepath.Join(d.work(), e.Name()))
		}
	}
	// the state file is kept fresh on its own clock, so a long transcription does not look like a
	// dead daemon to the phone
	go func() {
		st := time.NewTicker(15 * time.Second)
		defer st.Stop()
		for {
			d.writeState()
			select {
			case <-ctx.Done():
				return
			case <-st.C:
			}
		}
	}()
	var db DB
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		if db == nil {
			if c, err := d.Open(); err == nil {
				db = c
			} else {
				d.Log.Warn("database not reachable yet", "fn", "Run", "err", err)
			}
		}
		if db != nil {
			if err := d.Pass(ctx, db); err != nil {
				d.Log.Warn("voice pass failed, reconnecting next tick", "fn", "Run", "err", err)
				db = nil
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Pass is one tick: ingest, then transcribe until nothing is pending, the engine is missing, a
// transcription fails, or ctx ends.
func (d *Daemon) Pass(ctx context.Context, db DB) error {
	if n, err := d.IngestInbox(db); err != nil {
		return err
	} else if n > 0 {
		d.Log.Info("voice notes archived", "fn", "Pass", "n", n)
	}
	eng, why, ok := d.Find(d.Mount)
	d.mu.Lock()
	if ok {
		d.stat.Engine, d.stat.Why = eng.Name(), ""
	} else {
		d.stat.Engine, d.stat.Why = "", why
	}
	d.mu.Unlock()
	for ok && ctx.Err() == nil {
		did, err := d.TranscribeNext(ctx, db, eng)
		if err != nil {
			d.Log.Warn("transcription failed", "fn", "Pass", "err", err)
			break
		}
		if !did {
			break
		}
	}
	return d.count(db)
}

func (d *Daemon) count(db DB) error {
	rows, err := db.Query("SELECT status, count(*) FROM voice_notes GROUP BY status")
	if err != nil {
		return err
	}
	var p, dn, f int
	for _, v := range rows.Vals {
		if len(v) < 2 || v[0] == nil || v[1] == nil {
			continue
		}
		n, _ := strconv.Atoi(*v[1])
		switch *v[0] {
		case "pending":
			p = n
		case "done":
			dn = n
		case "failed":
			f = n
		}
	}
	d.mu.Lock()
	d.stat.Pending, d.stat.Done, d.stat.Failed = p, dn, f
	d.mu.Unlock()
	return nil
}

// IngestInbox moves each complete upload (<id>.wav with its <id>.json) into the archive and records
// it as pending. A file that is not a WAV goes to inbox/rejected with the reason logged, never
// deleted. Idempotent by id: the same note uploaded twice is one row and one file.
func (d *Daemon) IngestInbox(db DB) (int, error) {
	ents, err := os.ReadDir(d.inbox())
	if err != nil {
		return 0, nil
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	n := 0
	for _, name := range names {
		id := strings.TrimSuffix(name, ".json")
		metaPath := filepath.Join(d.inbox(), name)
		wav := filepath.Join(d.inbox(), id+".wav")
		if !IDRE.MatchString(id) {
			d.reject(metaPath, "", "not a voice note id")
			continue
		}
		if _, err := os.Stat(wav); err != nil {
			if st, serr := os.Stat(metaPath); serr == nil && time.Since(st.ModTime()) > time.Hour {
				d.reject(metaPath, "", "sidecar without its WAV for an hour")
			}
			continue
		}
		var m Meta
		if b, err := os.ReadFile(metaPath); err != nil || json.Unmarshal(b, &m) != nil {
			d.reject(metaPath, wav, "unreadable sidecar")
			continue
		}
		info, sum, size, err := inspect(wav)
		if err != nil {
			d.reject(metaPath, wav, err.Error())
			continue
		}
		taken := m.Taken / 1000
		if taken <= 0 {
			if st, err := os.Stat(wav); err == nil {
				taken = st.ModTime().Unix()
			}
		}
		day := m.Day
		if !validDay(day) {
			day = time.Unix(taken, 0).Format("2006-01-02")
		}
		kind := m.Kind
		if kind != "checkin" {
			kind = "journal"
		}
		rel := filepath.Join("voiced", "archive", time.Unix(taken, 0).Format("2006"), time.Unix(taken, 0).Format("01"), id+".wav")
		dst := filepath.Join(d.Mount, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return n, err
		}
		if err := os.Rename(wav, dst); err != nil {
			return n, err
		}
		err = db.Exec(`INSERT INTO voice_notes (id, kind, day, taken_at, duration_ms, bytes, sha256, path, device, status, received_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',$10) ON CONFLICT (id) DO NOTHING`,
			id, kind, day, taken, info.Duration().Milliseconds(), size, sum, rel, m.Device, time.Now().UnixMilli())
		if err != nil {
			_ = os.Rename(dst, wav) // back where it was: the next pass tries again
			return n, err
		}
		_ = os.Remove(metaPath)
		n++
		d.setLast(fmt.Sprintf("archived %s (%s)", id[:8], info.Duration().Round(time.Second)))
	}
	return n, nil
}

func (d *Daemon) reject(meta, wav, why string) {
	d.Log.Warn("voice upload rejected", "fn", "IngestInbox", "file", filepath.Base(meta), "why", why)
	_ = os.MkdirAll(d.rejected(), 0o750)
	for _, p := range []string{meta, wav} {
		if p != "" {
			_ = os.Rename(p, filepath.Join(d.rejected(), filepath.Base(p)))
		}
	}
}

func inspect(path string) (WavInfo, string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return WavInfo{}, "", 0, err
	}
	defer f.Close()
	info, err := ReadWavInfo(f)
	if err != nil {
		return info, "", 0, err
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return info, "", 0, err
	}
	return info, hex.EncodeToString(h.Sum(nil)), size, nil
}

// TranscribeNext transcribes the newest pending note. did is false when nothing is pending. A
// failure counts a try (and marks the note failed at MaxTries) and is returned, so the pass stops
// until the next tick instead of spinning on it.
func (d *Daemon) TranscribeNext(ctx context.Context, db DB, eng Engine) (did bool, err error) {
	rows, err := db.Query(`SELECT id, path, duration_ms FROM voice_notes WHERE status = 'pending' ORDER BY taken_at DESC LIMIT 1`)
	if err != nil {
		return false, err
	}
	if len(rows.Vals) == 0 || len(rows.Vals[0]) < 3 || rows.Vals[0][0] == nil || rows.Vals[0][1] == nil {
		return false, nil
	}
	id, rel := *rows.Vals[0][0], *rows.Vals[0][1]
	d.setWorking(id)
	defer d.setWorking("")
	d.engMu.Lock()
	res, terr := d.transcribeFile(ctx, filepath.Join(d.Mount, rel), id, eng)
	d.engMu.Unlock()
	if terr != nil {
		if ctx.Err() != nil {
			return false, ctx.Err() // shutting down: not the note's fault, no try counted
		}
		msg := terr.Error()
		if len(msg) > 400 {
			msg = msg[:400]
		}
		_ = db.Exec(`UPDATE voice_notes SET tries = tries + 1, error = $2,
			status = CASE WHEN tries + 1 >= $3 THEN 'failed' ELSE 'pending' END WHERE id = $1`, id, msg, MaxTries)
		d.setLast("failed on " + id[:8])
		return false, fmt.Errorf("%s: %w", id[:8], terr)
	}
	back, err := db.Query(`UPDATE voice_notes SET status = 'done', transcript = $2, lang = $3, model = $4, transcribed_at = $5, error = ''
		WHERE id = $1 AND status = 'pending' RETURNING kind, day, taken_at, duration_ms`,
		id, res.Text, res.Lang, eng.Name(), time.Now().UnixMilli())
	if err != nil {
		return false, err
	}
	if len(back.Vals) == 0 {
		return true, nil // deleted while whisper ran: nothing to journal
	}
	v := back.Vals[0]
	if err := Journal(db, id, str(v[0]), str(v[1]), atoi(str(v[2])), atoi(str(v[3])), res.Text); err != nil {
		return false, err
	}
	d.Log.Info("voice note transcribed", "fn", "TranscribeNext", "id", id[:8], "lang", res.Lang,
		"words", len(strings.Fields(res.Text)), "took", res.Took.Round(time.Second).String())
	d.setLast(fmt.Sprintf("transcribed %s (%d words, %s)", id[:8], len(strings.Fields(res.Text)), res.Took.Round(time.Second)))
	return true, nil
}

// AskDir is where secd puts a question asked aloud for Hear: <mount>/voiced/ask.
func AskDir(mount string) string { return filepath.Join(mount, "voiced", "ask") }

// Hear transcribes one WAV under AskDir and keeps nothing: no row, no archive, no journal entry.
// The file is removed whatever happens. A question asked aloud is heard once and goes into the
// chat as words, like a typed one. The queue's whisper waits while it runs (engMu), and it waits
// for the queue's.
func (d *Daemon) Hear(ctx context.Context, path string) (Result, error) {
	defer os.Remove(path)
	ask := AskDir(d.Mount)
	if rel, err := filepath.Rel(ask, path); err != nil || strings.HasPrefix(rel, "..") || strings.ContainsRune(rel, filepath.Separator) {
		return Result{}, fmt.Errorf("not a file of %s", ask)
	}
	eng, why, ok := d.Find(d.Mount)
	if !ok {
		return Result{}, fmt.Errorf("no speech engine: %s", why)
	}
	id := strings.TrimSuffix(filepath.Base(path), ".wav")
	d.engMu.Lock()
	defer d.engMu.Unlock()
	res, err := d.transcribeFile(ctx, path, "ask-"+id, eng)
	if err != nil {
		return Result{}, err
	}
	d.Log.Info("question heard", "fn", "Hear", "lang", res.Lang, "words", len(strings.Fields(res.Text)), "took", res.Took.Round(time.Second).String())
	return res, nil
}

func (d *Daemon) transcribeFile(ctx context.Context, path, id string, eng Engine) (Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	info, err := ReadWavInfo(f)
	if err != nil {
		f.Close()
		return Result{}, err
	}
	src := path
	if !info.IsSpeechFormat() {
		_ = os.MkdirAll(d.work(), 0o700)
		conv := filepath.Join(d.work(), id+".16k.wav")
		err = ToSpeechFormat(f, info, conv)
		f.Close()
		if err != nil {
			return Result{}, err
		}
		defer os.Remove(conv)
		src = conv
	} else {
		f.Close()
	}
	return eng.Transcribe(ctx, src, d.work(), info.Duration())
}

// Journal writes (or rewrites, after a new transcription) the note's journal entry: source
// ghost.voiced, ref voice:<id>, at the time it was recorded. An empty transcript (nothing said, or
// nothing whisper could hear) writes nothing. synthd reads the entry like any other: into the day's
// summary, and into memories when it holds something durable.
func Journal(db DB, id, kind, day string, taken, durMS int64, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	dur := durText(durMS)
	var body string
	if kind == "checkin" {
		body = fmt.Sprintf("Said at the daily check-in of %s (%s):\n%s", day, dur, text)
	} else {
		body = fmt.Sprintf("A voice note (%s):\n%s", dur, text)
	}
	if r := []rune(body); len(r) > 8000 {
		body = string(r[:8000]) + " …"
	}
	return db.Exec(`INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.voiced', $1, $2, $3, $4, $5)
		ON CONFLICT (source, ref) DO UPDATE SET title = EXCLUDED.title, body = EXCLUDED.body`,
		"voice:"+id, taken, "Voice note: "+FirstWords(text, 60), body, time.Now().UnixMilli())
}

// FirstWords: the start of the text, cut at a word boundary, with an ellipsis when cut.
func FirstWords(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	cut := string(r[:max])
	if i := strings.LastIndex(cut, " "); i > max/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}

func durText(ms int64) string {
	s := (ms + 500) / 1000
	if s < 60 {
		return fmt.Sprintf("%d s", s)
	}
	return fmt.Sprintf("%d min %02d s", s/60, s%60)
}

func validDay(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// Again puts notes back in the queue: one by id, or every failed one (id "failed").
func Again(db DB, id string) (int, error) {
	var rows *poltergres.Rows
	var err error
	switch {
	case id == "failed":
		rows, err = db.Query(`UPDATE voice_notes SET status = 'pending', tries = 0, error = '' WHERE status = 'failed' RETURNING id`)
	case IDRE.MatchString(id):
		rows, err = db.Query(`UPDATE voice_notes SET status = 'pending', tries = 0, error = '' WHERE id = $1 RETURNING id`, id)
	default:
		return 0, errors.New("voice-again takes id=<32 hex> or id=failed")
	}
	if err != nil {
		return 0, err
	}
	return len(rows.Vals), nil
}

// Report is the `voice` control command's text: the counts, the engine, the newest notes.
func Report(db DB, st Stat) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "voice notes: %d pending, %d transcribed, %d failed\n", st.Pending, st.Done, st.Failed)
	if st.Engine != "" {
		fmt.Fprintf(&b, "engine: whisper.cpp with %s (threads %d, CPU, nice 10)\n", st.Engine, Threads())
	} else {
		fmt.Fprintf(&b, "engine: none , %s\n", st.Why)
	}
	if st.Last != "" {
		fmt.Fprintf(&b, "last: %s\n", st.Last)
	}
	rows, err := db.Query(`SELECT id, kind, day, duration_ms, status, lang, left(transcript, 70), error FROM voice_notes ORDER BY taken_at DESC LIMIT 8`)
	if err != nil {
		return b.String(), err
	}
	for _, v := range rows.Vals {
		if len(v) < 8 {
			continue
		}
		line := fmt.Sprintf("  %s  %s  %-7s %7s  %-7s", str(v[0])[:8], str(v[2]), str(v[1]), durText(atoi(str(v[3]))), str(v[4]))
		if t := str(v[6]); t != "" {
			line += "  " + str(v[5]) + "  " + t
		} else if e := str(v[7]); e != "" {
			line += "  " + FirstWords(e, 70)
		}
		b.WriteString(line + "\n")
	}
	return b.String(), nil
}
