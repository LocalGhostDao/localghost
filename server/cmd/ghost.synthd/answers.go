package main

// AN ANSWER BELONGS TO THE BOX ONCE THE QUESTION IS SAVED. The phone used to be the only thing
// holding a reply: the box streamed tokens to it and saved the answer at the very end, and the
// generation ran on the phone's connection. Closing the app (or the phone locking it) cut the
// connection, the model stopped mid-sentence and nothing was saved, so reopening showed the
// question with no answer. Now:
//
//   - the assistant's row is written as soon as the question is saved (state "writing"), and
//     filled in as the answer streams, every couple of seconds, with the thinking beside it;
//   - the generation runs on the box's own clock (bounded), not the phone's connection, so a
//     phone that goes away finds the whole answer when it comes back;
//   - the sources the phone found on the web are saved with the answer (title, address, kind,
//     when fetched), so a reopened chat still shows where the answer came from;
//   - STOP in the app ends it on purpose (/chat/stop), which is the only thing that does.
//
// Incognito chats save nothing, so for them the phone's connection still is the lifetime.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"
)

// answerMaxRun bounds one answer when nobody is listening: a long one at CPU speed is minutes.
const answerMaxRun = 15 * time.Minute

// running maps a chat id to the answer being written for it (STOP in the app). The value is a
// pointer so sync.Map can compare it (a func value cannot be compared).
var running sync.Map

type runHandle struct{ cancel context.CancelFunc }

// sourceRef is one web source as saved with an answer; the numbers match the [n] in the text.
type sourceRef struct {
	N       int    `json:"n"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Kind    string `json:"kind,omitempty"`
	Fetched string `json:"fetched,omitempty"`
}

// sourcesJSON is the web findings as saved beside the answer ("" when there were none).
func sourcesJSON(web []webHit) string {
	if len(web) == 0 {
		return ""
	}
	refs := make([]sourceRef, 0, len(web))
	for i, h := range web {
		refs = append(refs, sourceRef{N: i + 1, Title: clip(h.Title, 200), URL: clip(h.URL, 500), Kind: h.Kind, Fetched: h.Fetched})
	}
	b, _ := json.Marshal(refs)
	return string(b)
}

// chatStartAnswer writes the assistant's row before the first word, state "writing". 0 when it
// could not be written (the answer then streams unsaved, as before).
func chatStartAnswer(mount string, chatID int64, sources string) int64 {
	db := chatStore(mount)
	if db == nil || chatID == 0 {
		return 0
	}
	rows, err := db.Query(`INSERT INTO chat_messages (chat_id, role, content, ts, reasoning, sources, state)
		VALUES ($1, 'assistant', '', $2, '', $3, 'writing') RETURNING id`,
		strconv.FormatInt(chatID, 10), strconv.FormatInt(time.Now().UTC().UnixMilli(), 10), sources)
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		slog.Warn("answer row not started, the answer is saved at the end instead", "fn", "chatStartAnswer", "err", err)
		return 0
	}
	id, _ := strconv.ParseInt(*rows.Vals[0][0], 10, 64)
	return id
}

// answerSaver keeps the saved row up with the answer: the newest text at most every two seconds,
// and the final state once. One goroutine writes, so the rows never go back in time.
type answerSaver struct {
	mount  string
	chatID int64
	msgID  int64
	mu     sync.Mutex
	text   string
	think  string
	state  string // writing | done | stopped
	dirty  bool
	kick   chan struct{}
	closed chan struct{}
}

func newAnswerSaver(mount string, chatID, msgID int64) *answerSaver {
	s := &answerSaver{mount: mount, chatID: chatID, msgID: msgID, state: "writing", kick: make(chan struct{}, 1), closed: make(chan struct{})}
	go s.loop()
	return s
}

// givenJSON is what the box had been given when it wrote an answer, as the saved row keeps it:
// the trail's steps, and each context item's source and date. Never the items' words: those
// stay where they came from. ghost.shadowd reads the dates to say how old the memories the box
// brings are, and the chat's audit trail shows the steps (pure, for the tests).
func givenJSON(steps []string, items []ctxItem) string {
	type item struct {
		Source string `json:"source"`
		When   string `json:"when,omitempty"`
	}
	out := struct {
		Steps []string `json:"steps"`
		Items []item   `json:"items"`
	}{Steps: steps, Items: make([]item, 0, len(items))}
	if out.Steps == nil {
		out.Steps = []string{}
	}
	for _, it := range items {
		out.Items = append(out.Items, item{Source: it.Source, When: it.When})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

// given keeps what the answer was written from on its row. Best effort: an answer is not held
// up for it, and a row without it is an answer from before this was kept.
func (s *answerSaver) given(steps []string, items []ctxItem) {
	db := chatStore(s.mount)
	if db == nil || s.msgID == 0 {
		return
	}
	if err := db.Exec(`UPDATE chat_messages SET given = $1 WHERE id = $2`, givenJSON(steps, items), strconv.FormatInt(s.msgID, 10)); err != nil {
		slog.Warn("what the answer was given was not kept", "fn", "given", "err", err)
	}
}

// update records the answer so far; final is "" while writing, else "done" or "stopped".
func (s *answerSaver) update(text, think, final string) {
	s.mu.Lock()
	s.text, s.think, s.dirty = text, think, true
	if final != "" {
		s.state = final
	}
	s.mu.Unlock()
	if final != "" {
		select {
		case s.kick <- struct{}{}:
		default:
		}
	}
}

// wait blocks until the final state is written (bounded), so the done event follows the save.
func (s *answerSaver) wait(d time.Duration) {
	select {
	case <-s.closed:
	case <-time.After(d):
	}
}

func (s *answerSaver) loop() {
	defer close(s.closed)
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-s.kick:
		}
		s.mu.Lock()
		text, think, state, dirty := s.text, s.think, s.state, s.dirty
		s.dirty = false
		s.mu.Unlock()
		if dirty || state != "writing" {
			chatSaveAnswer(s.mount, s.chatID, s.msgID, text, think, state)
		}
		if state != "writing" {
			return
		}
	}
}

// chatSaveAnswer writes the answer's text, thinking and state, and touches the chat.
func chatSaveAnswer(mount string, chatID, msgID int64, text, think, state string) {
	db := chatStore(mount)
	if db == nil || msgID == 0 {
		return
	}
	if err := db.Exec(`UPDATE chat_messages SET content = $1, reasoning = $2, state = $3 WHERE id = $4`,
		text, think, state, strconv.FormatInt(msgID, 10)); err != nil {
		slog.Warn("answer not saved", "fn", "chatSaveAnswer", "state", state, "err", err)
		return
	}
	if state != "writing" {
		_ = db.Exec(`UPDATE chats SET updated_at = $1 WHERE id = $2`,
			strconv.FormatInt(time.Now().UTC().UnixMilli(), 10), strconv.FormatInt(chatID, 10))
	}
}

// answerContext is the generation's lifetime: the box's own for a saved chat, the phone's
// connection for an incognito one. The cancel is registered for STOP.
func answerContext(phone context.Context, chatID int64, saved bool) (context.Context, context.CancelFunc) {
	if !saved || chatID == 0 {
		return context.WithCancel(phone)
	}
	ctx, cancel := context.WithTimeout(context.Background(), answerMaxRun)
	h := &runHandle{cancel: cancel}
	if old, ok := running.Swap(chatID, h); ok {
		old.(*runHandle).cancel() // a newer question in the same chat supersedes an older answer still running
	}
	return ctx, func() {
		cancel()
		running.CompareAndDelete(chatID, h)
	}
}

// stopAnswer ends the answer being written for a chat (STOP in the app). False when none runs.
func stopAnswer(chatID int64) bool {
	v, ok := running.LoadAndDelete(chatID)
	if !ok {
		return false
	}
	v.(*runHandle).cancel()
	return true
}

// settleOrphans marks answers left "writing" by a previous synthd (a restart mid-answer) as
// stopped, so the app does not wait on them forever. Run once the database answers.
func settleOrphans(mount string) {
	for i := 0; i < 20; i++ {
		if db := chatStore(mount); db != nil {
			if err := db.Exec(`UPDATE chat_messages SET state = 'stopped' WHERE state = 'writing'`); err == nil {
				return
			}
		}
		time.Sleep(3 * time.Second)
	}
}
