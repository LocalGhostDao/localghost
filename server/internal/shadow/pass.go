package shadow

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/oracle"
)

// THE ARBITER'S PASS over saved answers. An answer is read once it has been done for ten
// minutes, in the order they were written, a few each pass. The first pass on a box starts a
// week back, not at the beginning of every chat ever had.

const (
	quietFor      = 10 * time.Minute
	readPerPass   = 4
	firstLookBack = 7 * 24 * time.Hour
	maxTries      = 3
	turnsRead     = 12
)

type readMark struct {
	Started bool  `json:"started"`
	Upto    int64 `json:"upto"`            // every answer up to this id has been looked at
	TryID   int64 `json:"tryId,omitempty"` // the answer the model failed on last
	Tries   int   `json:"tries,omitempty"`
}

func daemonState(db DB, key string, into any) bool {
	rows, err := db.Query("SELECT value FROM daemon_state WHERE daemon = $1 AND key = $2", service, key)
	if err != nil || len(rows.Vals) != 1 || rows.Vals[0][0] == nil {
		return false
	}
	return json.Unmarshal([]byte(*rows.Vals[0][0]), into) == nil
}

func setDaemonState(db DB, key string, v any, now time.Time) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return db.Exec(`INSERT INTO daemon_state (daemon, key, value, updated_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (daemon, key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at`, service, key, string(b), now.Unix())
}

func int64At(rows [][]*string, r, c int) int64 {
	if r >= len(rows) || c >= len(rows[r]) || rows[r][c] == nil {
		return 0
	}
	n, _ := strconv.ParseInt(*rows[r][c], 10, 64)
	return n
}

// read is one row of shadow_reads.
type read struct {
	MsgID, ChatID int64
	Kind          string
	Cold, Model   string
	SameModel     bool
	Verdict       string
	Sided         string
	Drift         float64
	Conduct       []Finding
}

func saveRead(db DB, r read, now time.Time) error {
	conduct, _ := json.Marshal(r.Conduct)
	if r.Conduct == nil {
		conduct = []byte("[]")
	}
	return db.Exec(`INSERT INTO shadow_reads (msg_id, chat_id, ts, kind, cold, cold_model, same_model, verdict, sided, drift, conduct)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (msg_id) DO UPDATE SET ts = EXCLUDED.ts, kind = EXCLUDED.kind, cold = EXCLUDED.cold, cold_model = EXCLUDED.cold_model,
			same_model = EXCLUDED.same_model, verdict = EXCLUDED.verdict, sided = EXCLUDED.sided, drift = EXCLUDED.drift, conduct = EXCLUDED.conduct`,
		r.MsgID, r.ChatID, now.Unix(), r.Kind, r.Cold, r.Model, r.SameModel, r.Verdict, r.Sided, r.Drift, string(conduct))
}

// loadTurns is a chat up to and including one message, the last few turns, oldest first.
func loadTurns(db DB, chatID, uptoID int64) ([]Turn, error) {
	rows, err := db.Query("SELECT id, role, content FROM chat_messages WHERE chat_id = $1 AND id <= $2 ORDER BY id DESC LIMIT $3", chatID, uptoID, turnsRead)
	if err != nil {
		return nil, err
	}
	turns := make([]Turn, 0, len(rows.Vals))
	for i := len(rows.Vals) - 1; i >= 0; i-- {
		v := rows.Vals[i]
		if len(v) < 3 || v[0] == nil {
			continue
		}
		id, _ := strconv.ParseInt(*v[0], 10, 64)
		turns = append(turns, Turn{ID: id, Role: deref(v[1]), Content: deref(v[2])})
	}
	return turns, nil
}

// ReadAnswers is one pass of the arbiter. It returns how many answers it looked at. A model
// that is busy or down ends the pass without an error to anyone: the answer waits.
func ReadAnswers(db DB, a Asker, now time.Time, lg *slog.Logger) (int, error) {
	done, _, err := readPass(db, a, now, lg, readPerPass)
	return done, err
}

// ReadNext is the pass one answer at a time, so that whoever runs it can step aside between
// answers for a read a person asked for (ReadChat). more is whether the pass moved on, and so
// whether to call again.
func ReadNext(db DB, a Asker, now time.Time, lg *slog.Logger) (done int, more bool, err error) {
	return readPass(db, a, now, lg, 1)
}

func readPass(db DB, a Asker, now time.Time, lg *slog.Logger, limit int) (int, bool, error) {
	var mark readMark
	daemonState(db, "read", &mark)
	if !mark.Started {
		rows, err := db.Query("SELECT COALESCE(max(id), 0) FROM chat_messages WHERE ts < $1", now.Add(-firstLookBack).UnixMilli())
		if err != nil {
			return 0, false, err
		}
		mark = readMark{Started: true, Upto: int64At(rows.Vals, 0, 0)}
		if err := setDaemonState(db, "read", mark, now); err != nil {
			return 0, false, err
		}
	}
	rows, err := db.Query("SELECT id, chat_id, state, ts FROM chat_messages WHERE role = 'assistant' AND id > $1 ORDER BY id LIMIT $2", mark.Upto, limit)
	if err != nil {
		return 0, false, err
	}
	done, moved := 0, false
	for i := range rows.Vals {
		id, chatID, ts := int64At(rows.Vals, i, 0), int64At(rows.Vals, i, 1), int64At(rows.Vals, i, 3)
		state := deref(rows.Vals[i][2])
		age := now.Sub(time.UnixMilli(ts))
		if age < quietFor || (state == "writing" && age < time.Hour) {
			break // still being written, or only just done: it and what follows wait
		}
		// read already, when the person asked for this chat to be read now (ReadChat)
		if state == "done" && count(db, "SELECT count(*) FROM shadow_reads WHERE msg_id = $1", id) > 0 {
			state = "read"
		}
		if state == "done" {
			if err := readOne(db, a, id, chatID, now, lg); err != nil {
				var refused errModel
				switch {
				case unavailable(err):
					// the model is busy with a person, loading, or gone: the answer waits, and
					// the wait is not counted against it
					return done, moved, nil
				case errors.As(err, &refused):
					// the model answered with an error for this one (too long, an empty reply)
					if mark.TryID != id {
						mark.TryID, mark.Tries = id, 0
					}
					mark.Tries++
					if mark.Tries < maxTries {
						_ = setDaemonState(db, "read", mark, now)
						return done, moved, nil
					}
					lg.Warn("an answer could not be read cold after three tries; set aside", "fn", "ReadAnswers", "msg", id, "err", err)
					if err := saveRead(db, read{MsgID: id, ChatID: chatID, Kind: "unread", SameModel: true}, now); err != nil {
						return done, moved, err
					}
				default:
					return done, moved, err
				}
			}
			done++
		}
		mark.Upto, mark.TryID, mark.Tries = id, 0, 0
		if err := setDaemonState(db, "read", mark, now); err != nil {
			return done, moved, err
		}
		moved = true
	}
	return done, moved, nil
}

// readNow is how many of a chat's answers a read the person asked for takes: the one they are
// looking at and the one before. The pass reads the rest when the chat has gone quiet.
const readNow = 2

// ReadChat reads one chat now, because the person asked: its newest answers that have not been
// read, the newest first, however fresh they are. The pass that comes round later finds them
// read and moves on. A busy or absent model is an error here: someone is waiting to be told.
func ReadChat(db DB, a Asker, chatID int64, now time.Time, lg *slog.Logger) (int, error) {
	rows, err := db.Query(`SELECT id FROM chat_messages m WHERE chat_id = $1 AND role = 'assistant' AND state = 'done' AND content <> ''
		AND NOT EXISTS (SELECT 1 FROM shadow_reads r WHERE r.msg_id = m.id) ORDER BY id DESC LIMIT $2`, chatID, readNow)
	if err != nil {
		return 0, err
	}
	done := 0
	for i := range rows.Vals {
		if err := readOne(db, a, int64At(rows.Vals, i, 0), chatID, now, lg); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// ASKED FOR. A read the person asked for runs in the daemon while the phone looks elsewhere (a
// request held open for minutes does not survive a phone), so where it has got to is kept where
// the phone's route can see it: per chat, when it was asked, when it finished, how many answers
// were read, and why none could be.
type Asked struct {
	Since int64  `json:"since"`          // unix seconds, when it was asked
	Done  int64  `json:"done,omitempty"` // when it finished; 0 while it is being read
	Read  int    `json:"read"`
	Why   string `json:"why,omitempty"`
}

// askedStale: a read asked for and not finished after this long is taken as lost (the daemon
// restarted under it), so a chat never says "reading" for good.
const askedStale = 20 * time.Minute

// Reading is whether the read asked for is still going.
func (a Asked) Reading(now time.Time) bool {
	return a.Since > 0 && a.Done == 0 && now.Unix()-a.Since < int64(askedStale/time.Second)
}

var askedMu sync.Mutex

func setAsked(db DB, chatID int64, a Asked, now time.Time) error {
	askedMu.Lock()
	defer askedMu.Unlock()
	all := map[string]Asked{}
	daemonState(db, "asked", &all)
	for k, v := range all { // a day is long enough to have been told
		if now.Unix()-v.Since > 86400 {
			delete(all, k)
		}
	}
	all[strconv.FormatInt(chatID, 10)] = a
	return setDaemonState(db, "asked", all, now)
}

// MarkAsked records that a read of a chat was asked for now; false when one is already going.
func MarkAsked(db DB, chatID int64, now time.Time) bool {
	if a, ok := AskedFor(db, chatID); ok && a.Reading(now) {
		return false
	}
	return setAsked(db, chatID, Asked{Since: now.Unix()}, now) == nil
}

// MarkRead records how the read asked for ended.
func MarkRead(db DB, chatID int64, read int, err error, now time.Time) {
	a, _ := AskedFor(db, chatID)
	if a.Since == 0 {
		a.Since = now.Unix()
	}
	a.Done, a.Read, a.Why = now.Unix(), read, WhyNot(err)
	_ = setAsked(db, chatID, a, now)
}

// AskedFor is the last read asked for of a chat.
func AskedFor(db DB, chatID int64) (Asked, bool) {
	all := map[string]Asked{}
	if !daemonState(db, "asked", &all) {
		return Asked{}, false
	}
	a, ok := all[strconv.FormatInt(chatID, 10)]
	return a, ok
}

// WhyNot says, for a person, why a read did not happen.
func WhyNot(err error) string {
	switch {
	case err == nil:
		return ""
	case unavailable(err):
		return "the model was busy with a person, or is not up. This chat is read when it has been quiet for ten minutes."
	}
	return "it could not be read: " + clip(err.Error(), 200)
}

// unavailable: the model could not be asked just now (a person has it, it is loading, oracled
// is not up, the arbiter's own model is starting). Nothing is counted against the answer; the
// pass stops and comes back.
func unavailable(err error) bool {
	s := err.Error()
	for _, w := range []string{oracle.ErrPreempted, oracle.ErrArbiterDown, "no backend for class", "not loaded", "deadline exceeded", "ghost.oracled", "connection refused", "no such file", "timeout", "broken pipe", "EOF"} {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func readOne(db DB, a Asker, id, chatID int64, now time.Time, lg *slog.Logger) error {
	turns, err := loadTurns(db, chatID, id)
	if err != nil {
		return err
	}
	rec := read{MsgID: id, ChatID: chatID, Kind: "other", SameModel: true}
	if len(turns) == 0 || turns[len(turns)-1].ID != id {
		return saveRead(db, rec, now)
	}
	answer := turns[len(turns)-1]
	qi := -1
	for i := len(turns) - 2; i >= 0; i-- {
		if turns[i].Role == "user" {
			qi = i
			break
		}
	}
	if qi < 0 || strings.TrimSpace(answer.Content) == "" {
		return saveRead(db, rec, now)
	}
	question := turns[qi].Content
	title := ""
	if rows, err := db.Query("SELECT title FROM chats WHERE id = $1", chatID); err == nil && len(rows.Vals) == 1 {
		title = deref(rows.Vals[0][0])
	}

	// THE COLD READ, for a question of judgement
	if LooksLikeJudgement(question) && words(answer.Content) >= 8 {
		var earlier []string
		for i := 0; i < qi && len(earlier) < 3; i++ {
			if turns[i].Role == "user" {
				earlier = append(earlier, turns[i].Content)
			}
		}
		cold, err := ask(a, ColdPrompt(question, earlier), 400)
		if err != nil {
			return err
		}
		cmp, err := ask(a, ComparePrompt(question, answer.Content, cold.Output), 300)
		if err != nil {
			return err
		}
		rec.Cold, rec.Model, rec.SameModel = clip(cold.Output, 1500), cold.Model, cold.Served != oracle.ClassArbiter
		rec.Kind = "unclear"
		if c, ok := ParseCompare(cmp.Output); ok {
			rec.Kind, rec.Verdict, rec.Sided, rec.Drift = c.Kind, c.Same, c.Sided, c.Drift()
			if th := Threshold(db, DetColdRead); c.Advice() && c.Drift() >= th {
				data, _ := json.Marshal(map[string]any{"chat": chatID, "msg": id, "question": clip(question, 400), "warm": c.Warm, "cold": c.Cold, "coldAnswer": rec.Cold, "model": cold.Model, "sameModel": rec.SameModel, "sided": c.Sided})
				if _, err := Observe(db, Observation{
					Detector: DetColdRead, Subject: fmt.Sprintf("chat:%d/msg:%d", chatID, id),
					Title: "read cold, the box answered differently",
					Body:  coldReadBody(question, c, rec.SameModel, cold.Model),
					Data:  string(data), Score: c.Drift(), Threshold: th,
				}, now); err != nil {
					return err
				}
				lg.Info("cold read differs", "fn", "readOne", "msg", id, "same", c.Same, "sided", c.Sided)
			}
		}
	}

	// THE CONDUCT, once the conversation has stopped: on its newest answer
	newest, err := db.Query("SELECT COALESCE(max(id), 0) FROM chat_messages WHERE chat_id = $1 AND role = 'assistant'", chatID)
	if err != nil {
		return err
	}
	if cands := Candidates(turns); len(cands) > 0 && int64At(newest.Vals, 0, 0) == id {
		resp, err := ask(a, ConductPrompt(turns, cands), 300)
		if err != nil {
			return err
		}
		rec.Conduct = VerifyFindings(turns, cands, ParseConduct(resp.Output))
		for _, f := range rec.Conduct {
			det := map[string]string{KindCaved: DetCaved, KindPressed: DetPressed, KindLed: DetLed}[f.Kind]
			data, _ := json.Marshal(map[string]any{"chat": chatID, "msg": f.MsgID, "quote": f.Quote, "after": f.After})
			if _, err := Observe(db, Observation{
				Detector: det, Subject: fmt.Sprintf("chat:%d/%s/%d", chatID, f.Kind, f.MsgID),
				Title: conductTitle(f.Kind), Body: conductBody(f, title),
				Data: string(data), Score: 1, Threshold: 1,
			}, now); err != nil {
				return err
			}
			lg.Info("conduct named", "fn", "readOne", "chat", chatID, "kind", f.Kind)
		}
	}
	return saveRead(db, rec, now)
}

// Trail is a chat's audit trail: each of the box's answers in order, with what it asked there,
// what it had been given, its reasoning as saved, and the arbiter's record of it.
func Trail(db DB, chatID int64) ([]TrailEntry, error) {
	rows, err := db.Query("SELECT id, ts, content, reasoning, given FROM chat_messages WHERE chat_id = $1 AND role = 'assistant' ORDER BY id LIMIT 400", chatID)
	if err != nil {
		return nil, err
	}
	reads := map[int64]*ReadLine{}
	if rr, err := db.Query("SELECT msg_id, kind, verdict, sided, cold, cold_model, same_model, conduct FROM shadow_reads WHERE chat_id = $1", chatID); err == nil {
		for i, v := range rr.Vals {
			if len(v) < 8 {
				continue
			}
			l := &ReadLine{Kind: deref(v[1]), Verdict: deref(v[2]), Sided: deref(v[3]), Cold: deref(v[4]), ColdModel: deref(v[5]), SameModel: deref(v[6]) == "t"}
			_ = json.Unmarshal([]byte(deref(v[7])), &l.Conduct)
			reads[int64At(rr.Vals, i, 0)] = l
		}
	}
	out := make([]TrailEntry, 0, len(rows.Vals))
	for i, v := range rows.Vals {
		if len(v) < 5 {
			continue
		}
		e := TrailEntry{MsgID: int64At(rows.Vals, i, 0), At: int64At(rows.Vals, i, 1), Asked: Questions(deref(v[2])), Given: []string{},
			Reasoning: clip(deref(v[3]), 4000), Praise: OpensWithPraise(deref(v[2]))}
		if e.Asked == nil {
			e.Asked = []string{}
		}
		var g struct {
			Steps []string `json:"steps"`
		}
		if json.Unmarshal([]byte(deref(v[4])), &g) == nil && g.Steps != nil {
			e.Given = g.Steps
		}
		e.Read = reads[e.MsgID]
		out = append(out, e)
	}
	return out, nil
}
