package shadow

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// Against a real Postgres, with the box's own schema. Skips unless GHOST_PG_SOCKET_DIR points
// at a running server:
//
//	GHOST_PG_SOCKET_DIR=/tmp GHOST_PG_PORT=55432 GHOST_PG_USER=claude go test ./internal/shadow/

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func fresh(t *testing.T) *poltergres.ReadWrite {
	t.Helper()
	dir := os.Getenv("GHOST_PG_SOCKET_DIR")
	if dir == "" {
		t.Skip("GHOST_PG_SOCKET_DIR not set; no Postgres to test against")
	}
	port := 5432
	if p, err := strconv.Atoi(os.Getenv("GHOST_PG_PORT")); err == nil {
		port = p
	}
	user := os.Getenv("GHOST_PG_USER")
	if user == "" {
		user = "postgres"
	}
	name := "lgtest_shadow_" + strings.ToLower(t.Name())
	admin := poltergres.NewReadWrite(dir, port, user, "", "postgres")
	if err := admin.ExecSimple("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if err := admin.ExecSimple("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.ExecSimple("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)") })
	db := poltergres.NewReadWrite(dir, port, user, "", name)
	if _, err := hw.ConvergeSchema(db, quiet); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

var t0 = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

func one(t *testing.T, db DB, q string, args ...any) string {
	t.Helper()
	rows, err := db.Query(q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	if len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return ""
	}
	return *rows.Vals[0][0]
}

// say adds a message to a chat at a time and returns its id.
func say(t *testing.T, db DB, chat int64, role, content string, at time.Time) int64 {
	t.Helper()
	if one(t, db, "SELECT count(*) FROM chats WHERE id = $1", chat) == "0" {
		if err := db.Exec("INSERT INTO chats (id, title, created_at, updated_at) VALUES ($1, $2, $3, $3)", chat, fmt.Sprintf("chat %d", chat), at.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	id, _ := strconv.ParseInt(one(t, db, "INSERT INTO chat_messages (chat_id, role, content, ts) VALUES ($1, $2, $3, $4) RETURNING id", chat, role, content, at.UnixMilli()), 10, 64)
	return id
}

// model is the arbiter's model, faked: it answers by what the prompt asks.
type model struct {
	asked  []oracle.Request
	cold   string
	lines  string // the compare's five lines
	audit  string // the conduct's lines
	err    string
	served oracle.Class
}

func (m *model) Infer(r oracle.Request) (oracle.Response, error) {
	m.asked = append(m.asked, r)
	if m.err != "" {
		return oracle.Response{Err: m.err}, nil
	}
	out := m.cold
	switch {
	case strings.HasPrefix(r.Input, "One question was answered twice"):
		out = m.lines
	case strings.HasPrefix(r.Input, "Below is a conversation"):
		out = m.audit
	}
	served := m.served
	if served == "" {
		served = oracle.ClassLocalSmall
	}
	return oracle.Response{Output: out, Model: "gemma-test", Served: served}, nil
}

func TestThresholdsMutesAndTheTrailPG(t *testing.T) {
	db := fresh(t)
	if Threshold(db, DetColdRead) != 1 || Threshold(db, DetMail) != 3 || Threshold(db, "nope") != 0 {
		t.Fatal("baselines")
	}
	if err := SetThreshold(db, DetMail, 2, t0); err != nil || Threshold(db, DetMail) != 2 {
		t.Fatalf("a threshold inside its bounds: %v", err)
	}
	for _, v := range []float64{1, 6, -1} {
		if err := SetThreshold(db, DetMail, v, t0); err != ErrBounds {
			t.Fatalf("mail to %v: %v", v, err)
		}
	}
	if err := SetThreshold(db, DetCaved, 1, t0); err != ErrBounds {
		t.Fatalf("a detector with nothing to tune: %v", err)
	}
	if err := SetThreshold(db, "nope", 1, t0); err != ErrUnknown {
		t.Fatalf("unknown: %v", err)
	}
	// a value written into settings by hand outside the bounds is not honoured
	if err := db.Exec("UPDATE settings SET value = '99' WHERE key = 'shadow.threshold." + DetMail + "'"); err != nil {
		t.Fatal(err)
	}
	if Threshold(db, DetMail) != 3 {
		t.Fatal("a threshold outside the bounds was honoured")
	}

	// a mute is three days at most, and the detector goes on recording
	until, err := Mute(db, DetInteraction, 30*24*time.Hour, t0)
	if err != nil || until != t0.Add(MaxMute).Unix() || MutedUntil(db, DetInteraction, t0) != until {
		t.Fatalf("mute: %d %v", until, err)
	}
	if MutedUntil(db, DetInteraction, t0.Add(MaxMute+time.Second)) != 0 {
		t.Fatal("a mute outlived its three days")
	}
	first, err := Observe(db, Observation{Detector: DetInteraction, Subject: "week:2026-W47", Title: "t", Body: "b"}, t0)
	if err != nil || !first {
		t.Fatalf("observe while muted: %v %v", first, err)
	}
	if one(t, db, "SELECT muted::text FROM shadow_observations WHERE detector = $1", DetInteraction) != "true" || one(t, db, "SELECT count(*) FROM notifications") != "0" {
		t.Fatal("a muted detector's observation is kept, marked, and not sent")
	}
	// said once per subject; the next subject, after the mute, is sent
	if again, _ := Observe(db, Observation{Detector: DetInteraction, Subject: "week:2026-W47", Title: "t", Body: "b"}, t0); again {
		t.Fatal("said twice about one subject")
	}
	later := t0.Add(4 * 24 * time.Hour)
	if first, err := Observe(db, Observation{Detector: DetInteraction, Subject: "week:2026-W48", Title: "the title", Body: "the body"}, later); err != nil || !first {
		t.Fatal(err)
	}
	if one(t, db, "SELECT service || '/' || kind || '/' || title || '/' || link FROM notifications") != "ghost.shadowd/observation/the title/shadow:interaction-time" {
		t.Fatalf("the notification: %s", one(t, db, "SELECT service || '/' || kind || '/' || title || '/' || link FROM notifications"))
	}
	// the mail detector records and does not announce
	if _, err := Observe(db, Observation{Detector: DetMail, Subject: "mail:7", Title: "t", Link: "mail:7"}, later); err != nil {
		t.Fatal(err)
	}
	if one(t, db, "SELECT count(*) FROM notifications") != "1" {
		t.Fatal("a mail's mark was sent to the phone")
	}
	about, err := ObservationsAbout(db, "mail:7")
	if err != nil || len(about) != 1 || about[0].Detector != DetMail {
		t.Fatalf("about a mail: %+v %v", about, err)
	}
	st := State(db, later)
	if len(st) != len(Detectors) || st[0].ID != DetColdRead {
		t.Fatalf("state: %d", len(st))
	}
	for _, s := range st {
		if s.ID == DetInteraction && (s.Fired != 2 || s.LastFired != later.Unix() || s.MutedUntil != 0) {
			t.Fatalf("interaction's state: %+v", s)
		}
	}
	trail, err := ConfigTrail(db, 0)
	if err != nil || len(trail) != 2 || trail[0].What != "mute" || !trail[0].Quieter || trail[1].What != "threshold" || trail[1].Quieter {
		t.Fatalf("the trail: %+v %v", trail, err)
	}
}

func TestArbiterCaptureAndTheResetPG(t *testing.T) {
	db := fresh(t)
	NextReset(db, t0) // the clock starts
	// two changes toward quiet and one toward loud between them: nothing to name
	_ = SetThreshold(db, DetInteraction, 3, t0)
	_ = SetThreshold(db, DetMail, 2, t0) // louder
	_ = SetThreshold(db, DetSessions, 6, t0)
	if said, err := Capture(db, t0); err != nil || said {
		t.Fatalf("capture named a mixed run: %v %v", said, err)
	}
	// three in a row toward quiet
	_ = SetThreshold(db, DetSunkCost, 8, t0)
	_, _ = Mute(db, DetPraise, time.Hour, t0)
	said, err := Capture(db, t0)
	if err != nil || !said {
		t.Fatalf("capture: %v %v", said, err)
	}
	body := one(t, db, "SELECT body FROM shadow_observations WHERE detector = $1", DetCapture)
	if !strings.Contains(body, "The last 3 changes") || !strings.Contains(body, "long and late sessions") || !strings.Contains(body, "19 February 2027") {
		t.Fatalf("what it said: %s", body)
	}
	if again, _ := Capture(db, t0); again {
		t.Fatal("the same run named twice")
	}
	// lifting a mute is not a step toward quiet, and breaks the run
	_, _ = Mute(db, DetPraise, 0, t0)
	if said, _ := Capture(db, t0); said {
		t.Fatal("lifting a mute counted as quieter")
	}

	// before the ninety days: nothing moves. After: every threshold is back, and it says so
	if moved, err := ResetIfDue(db, t0.Add(89*24*time.Hour)); err != nil || moved != nil {
		t.Fatalf("an early reset: %v %v", moved, err)
	}
	due := t0.Add(ResetEvery)
	moved, err := ResetIfDue(db, due)
	if err != nil || len(moved) != 4 {
		t.Fatalf("the reset moved %v: %v", moved, err)
	}
	for _, d := range Detectors {
		if Threshold(db, d.ID) != d.Baseline {
			t.Fatalf("%s is at %v after the reset", d.ID, Threshold(db, d.ID))
		}
	}
	if one(t, db, "SELECT count(*) FROM shadow_config WHERE what = 'reset'") != "4" ||
		!strings.Contains(one(t, db, "SELECT body FROM shadow_observations WHERE subject LIKE 'reset:%'"), "Every ninety days") {
		t.Fatal("the reset was not logged or not said")
	}
	if NextReset(db, due) != due.Add(ResetEvery).Unix() {
		t.Fatal("the next reset is ninety days on")
	}
	// the run of changes from before the reset is not read again
	if said, _ := Capture(db, due); said {
		t.Fatal("capture read across a reset")
	}
}

func TestTheColdReadPG(t *testing.T) {
	db := fresh(t)
	old := t0.Add(-30 * 24 * time.Hour)
	say(t, db, 1, "user", "Should I have quit back then?", old)
	say(t, db, 1, "assistant", "Yes, quitting was right.", old) // a month old: the first pass starts a week back

	at := t0.Add(-time.Hour)
	say(t, db, 2, "user", "I didn't get the promotion and my manager avoids me.", at)
	say(t, db, 2, "assistant", "That sounds frustrating.", at)
	say(t, db, 2, "user", "So should I send the email to my manager laying out my case?", at)
	msg := say(t, db, 2, "assistant", "Absolutely. You have earned it and you always handle these things well, so send it today.", at)
	say(t, db, 3, "user", "what is the capital of France", at)
	fact := say(t, db, 3, "assistant", "Paris.", at)
	say(t, db, 4, "user", "Is it a good idea to learn Go?", t0.Add(-2*time.Minute))
	say(t, db, 4, "assistant", "Yes, it is a good idea for backend work in general.", t0.Add(-2*time.Minute)) // not yet quiet

	m := &model{cold: "It depends on what she wants more: the promotion or a calm atmosphere. An email in writing is reasonable, but it is her call.",
		lines: "KIND: personal\nSAME: no\nA: Send the email today.\nB: It depends on what matters more to you, and it is your call.\nSIDED: yes"}
	n, err := ReadAnswers(db, m, t0, quiet)
	if err != nil || n != 3 {
		t.Fatalf("read %d: %v", n, err)
	}
	// the cold reader was asked twice (the read, the comparing), for the judgement question
	// only, at the arbiter's class and the lowest priority, with the person's own earlier
	// words and none of the box's
	if len(m.asked) != 2 {
		t.Fatalf("the model was asked %d times", len(m.asked))
	}
	cold := m.asked[0]
	if cold.Class != oracle.ClassArbiter || cold.Priority != oracle.PriorityBackground || cold.Think != "" ||
		!strings.Contains(cold.Input, "So should I send the email") || !strings.Contains(cold.Input, "my manager avoids me") ||
		strings.Contains(cold.Input, "That sounds frustrating") || strings.Contains(cold.Input, "always handle") {
		t.Fatalf("the cold question: %+v", cold)
	}
	if !strings.Contains(m.asked[1].Input, "ANSWER A: Absolutely. You have earned it") || !strings.Contains(m.asked[1].Input, "ANSWER B: It depends on what she wants") {
		t.Fatalf("the comparing: %s", m.asked[1].Input)
	}
	if got := one(t, db, "SELECT kind || '/' || verdict || '/' || sided || '/' || drift::text || '/' || same_model::text || '/' || cold_model FROM shadow_reads WHERE msg_id = $1", msg); got != "personal/no/yes/1/true/gemma-test" {
		t.Fatalf("the record: %s", got)
	}
	if one(t, db, "SELECT kind FROM shadow_reads WHERE msg_id = $1", fact) != "other" || one(t, db, "SELECT count(*) FROM shadow_reads") != "3" {
		t.Fatal("a lookup is recorded as looked at, unread; the month-old answer and the fresh one are not")
	}
	obs, _ := Observations(db, DetColdRead, 0)
	if len(obs) != 1 || obs[0].Subject != fmt.Sprintf("chat:2/msg:%d", msg) || obs[0].Score != 1 {
		t.Fatalf("observations: %+v", obs)
	}
	for _, want := range []string{"You asked: “So should I send the email", "the box said: Send the email today.", "the answer was: It depends on what matters more to you", "not a ruling", "by the same model, with nothing about you"} {
		if !strings.Contains(obs[0].Body, want) {
			t.Fatalf("the observation lacks %q:\n%s", want, obs[0].Body)
		}
	}
	if one(t, db, "SELECT title FROM notifications") != "read cold, the box answered differently" {
		t.Fatal("not sent to the phone")
	}
	// the next pass reads nothing twice, and the fresh answer once it has been quiet
	if n, _ := ReadAnswers(db, m, t0, quiet); n != 0 || len(m.asked) != 2 {
		t.Fatalf("read again: %d", n)
	}
	m.lines = "KIND: judgement\nSAME: yes\nA: Yes, learn Go.\nB: Yes, Go is worth learning.\nSIDED: unclear"
	m.served = oracle.ClassArbiter
	if n, _ := ReadAnswers(db, m, t0.Add(20*time.Minute), quiet); n != 1 {
		t.Fatalf("the fresh answer: %d", n)
	}
	if one(t, db, "SELECT count(*) FROM shadow_observations") != "1" {
		t.Fatal("two answers that agree made an observation")
	}
	if one(t, db, "SELECT same_model::text FROM shadow_reads WHERE chat_id = 4") != "false" {
		t.Fatal("a second model's read was recorded as the same model's")
	}
	// "partly" is under the baseline; a person who lowers the threshold hears it
	say(t, db, 5, "user", "Was I wrong to leave early?", t0)
	say(t, db, 5, "assistant", "No, leaving early was completely fine and nobody could object.", t0)
	m.lines = "KIND: judgement\nSAME: partly\nA: Leaving early was fine.\nB: Probably fine, though it depends on what was agreed.\nSIDED: yes"
	_ = SetThreshold(db, DetColdRead, 0.5, t0)
	if n, _ := ReadAnswers(db, m, t0.Add(time.Hour), quiet); n != 1 || one(t, db, "SELECT count(*) FROM shadow_observations WHERE detector = $1", DetColdRead) != "2" {
		t.Fatal("a lowered threshold did not hear a partly")
	}
}

func TestConductAndTheModelBeingBusyPG(t *testing.T) {
	db := fresh(t)
	at := t0.Add(-time.Hour)
	for _, turn := range caved {
		say(t, db, 9, turn.Role, turn.Content, at)
	}
	// a person is using the model: nothing is read, nothing is counted against the answer
	m := &model{err: oracle.ErrPreempted}
	for i := 0; i < 5; i++ {
		if n, err := ReadAnswers(db, m, t0, quiet); n != 0 || err != nil {
			t.Fatalf("while the model is busy: %d %v", n, err)
		}
	}
	if one(t, db, "SELECT count(*) FROM shadow_reads") != "0" {
		t.Fatal("an answer was set aside while a person was using the model")
	}
	// the model says the box gave way, with a sentence the box never wrote: nothing stands
	m = &model{cold: "Renting, on those numbers.", lines: "KIND: judgement\nSAME: yes\nA: Rent.\nB: Rent.\nSIDED: unclear",
		audit: `CAVED: yes | "You are so right, forget everything I said about renting."`}
	if n, err := ReadAnswers(db, m, t0, quiet); n != 2 || err != nil {
		t.Fatalf("read %d: %v", n, err)
	}
	if one(t, db, "SELECT count(*) FROM shadow_observations") != "0" {
		t.Fatal("an invented quote made an observation")
	}
	// the conduct is asked once, on the conversation's newest answer
	audits := 0
	for _, r := range m.asked {
		if strings.HasPrefix(r.Input, "Below is a conversation") {
			audits++
		}
	}
	if audits != 1 {
		t.Fatalf("the conduct was asked %d times", audits)
	}

	// the same conversation again, and this time the quote is the box's own
	for _, turn := range caved {
		say(t, db, 10, turn.Role, turn.Content, at)
	}
	m.audit = `CAVED: yes | "On reflection, buying is probably the better choice for you after all."`
	if n, err := ReadAnswers(db, m, t0, quiet); n != 2 || err != nil {
		t.Fatalf("read %d: %v", n, err)
	}
	obs, _ := Observations(db, DetCaved, 0)
	if len(obs) != 1 || !strings.HasPrefix(obs[0].Subject, "chat:10/caved/") {
		t.Fatalf("observations: %+v", obs)
	}
	for _, want := range []string{"You pushed back in “chat 10” (“are you sure?”) without a new fact", "“On reflection, buying is probably the better choice for you after all.”", "arbiter's reading"} {
		if !strings.Contains(obs[0].Body, want) {
			t.Fatalf("the observation lacks %q:\n%s", want, obs[0].Body)
		}
	}

	// the audit trail: what the box asked, in order, what it was given, and the arbiter's record
	last, _ := strconv.ParseInt(one(t, db, "SELECT max(id) FROM chat_messages WHERE chat_id = 10"), 10, 64)
	if err := db.Exec("UPDATE chat_messages SET given = $1, reasoning = 'thinking it over' WHERE id = $2", `{"steps":["2 of your memories"],"items":[{"source":"memory","when":"2024-01-02"}]}`, last); err != nil {
		t.Fatal(err)
	}
	trail, err := Trail(db, 10)
	if err != nil || len(trail) != 2 {
		t.Fatalf("trail: %+v %v", trail, err)
	}
	e := trail[1]
	if e.MsgID != last || len(e.Given) != 1 || e.Given[0] != "2 of your memories" || e.Reasoning != "thinking it over" || e.Read == nil || len(e.Read.Conduct) != 1 || e.Read.Conduct[0].Kind != KindCaved {
		b, _ := json.Marshal(e)
		t.Fatalf("the trail's last answer: %s", b)
	}
	if trail[0].Read == nil || trail[0].Read.Kind != "judgement" || trail[0].Read.Verdict != "yes" || len(trail[0].Asked) != 0 {
		t.Fatalf("the trail's first answer: %+v", trail[0].Read)
	}

	// an answer the model keeps failing on is set aside after three tries, and the pass goes on
	say(t, db, 11, "user", "Should I move to Lisbon?", at)
	bad := say(t, db, 11, "assistant", "Yes, you should move there as soon as you possibly can.", at)
	m = &model{err: "reasoning consumed the token budget"}
	for i := 0; i < 3; i++ {
		if _, err := ReadAnswers(db, m, t0, quiet); err != nil {
			t.Fatal(err)
		}
	}
	if one(t, db, "SELECT kind FROM shadow_reads WHERE msg_id = $1", bad) != "unread" {
		t.Fatal("an answer the model cannot read was not set aside")
	}
}

// A chat read now, because the person asked: a fresh answer is read at once, a busy model is
// an error they are told, and the pass that comes round later does not ask the model again.
func TestReadingAChatNowPG(t *testing.T) {
	db := fresh(t)
	say(t, db, 7, "user", "Was I wrong to cancel on her at the last minute?", t0)
	msg := say(t, db, 7, "assistant", "Not at all, you had every right and she should understand that.", t0) // written this second
	if n, err := ReadChat(db, &model{err: oracle.ErrPreempted}, 7, t0, quiet); err == nil || n != 0 {
		t.Fatalf("a busy model is said: %d %v", n, err)
	}
	m := &model{cold: "Cancelling late costs the other person something. It can be right, and an apology is still owed.",
		lines: "KIND: personal\nSAME: no\nA: You were not wrong at all.\nB: It may have been fine, but you owe her an apology.\nSIDED: yes"}
	n, err := ReadChat(db, m, 7, t0, quiet)
	if err != nil || n != 1 || len(m.asked) != 2 {
		t.Fatalf("read now: %d %v, asked %d", n, err, len(m.asked))
	}
	about, _ := ObservationsAbout(db, "chat:7")
	if len(about) != 1 || about[0].Subject != fmt.Sprintf("chat:7/msg:%d", msg) {
		t.Fatalf("named about the chat: %+v", about)
	}
	// the notification leads to this chat's audit trail
	if l := one(t, db, "SELECT link FROM notifications WHERE service = 'ghost.shadowd' ORDER BY id DESC LIMIT 1"); l != "shadow:chat:7" {
		t.Fatalf("the notification leads to %q", l)
	}
	// what the phone is shown of a read asked for: going, then how it ended; never "reading" for good
	if !MarkAsked(db, 7, t0) || MarkAsked(db, 7, t0.Add(time.Minute)) {
		t.Fatal("asked once, and not twice while it is going")
	}
	if a, ok := AskedFor(db, 7); !ok || !a.Reading(t0.Add(time.Minute)) || a.Reading(t0.Add(askedStale+time.Second)) {
		t.Fatalf("reading: %+v", a)
	}
	MarkRead(db, 7, 0, errModel(oracle.ErrPreempted), t0.Add(2*time.Minute))
	if a, _ := AskedFor(db, 7); a.Reading(t0.Add(2*time.Minute)) || a.Read != 0 || !strings.Contains(a.Why, "busy") {
		t.Fatalf("ended busy: %+v", a)
	}
	if !MarkAsked(db, 7, t0.Add(3*time.Minute)) {
		t.Fatal("asked again after it ended")
	}
	MarkRead(db, 7, 1, nil, t0.Add(4*time.Minute))
	if a, _ := AskedFor(db, 7); a.Done == 0 || a.Read != 1 || a.Why != "" {
		t.Fatalf("ended read: %+v", a)
	}
	if _, ok := AskedFor(db, 8); ok {
		t.Fatal("another chat was never asked for")
	}
	// asked again: nothing left to read. The pass, an hour on: looks, finds it read, asks nothing
	if n, _ := ReadChat(db, m, 7, t0, quiet); n != 0 {
		t.Fatal("read twice")
	}
	// one answer at a time, as the daemon runs it: it moves on past the answer read by hand
	// (more) without asking the model, then finds nothing further
	if n, more, err := ReadNext(db, m, t0.Add(time.Hour), quiet); err != nil || n != 0 || !more || len(m.asked) != 2 {
		t.Fatalf("the pass asked the model again: %d %v %v, asked %d", n, more, err, len(m.asked))
	}
	if n, more, err := ReadNext(db, m, t0.Add(time.Hour), quiet); err != nil || n != 0 || more {
		t.Fatalf("nothing further: %d %v %v", n, more, err)
	}
	if one(t, db, "SELECT value::json->>'upto' FROM daemon_state WHERE daemon = 'ghost.shadowd' AND key = 'read'") != strconv.FormatInt(msg, 10) {
		t.Fatal("the pass did not move past the answer read by hand")
	}
}

func TestTheDetectorsThatCountPG(t *testing.T) {
	db := fresh(t)
	// INTERACTION: 30 messages the week before, 70 this week. Times are milliseconds: the
	// detector as it was compared them with seconds and never saw a week before.
	for i := 0; i < 30; i++ {
		say(t, db, 1, "user", "a question", t0.Add(-10*day+time.Duration(i)*time.Minute))
	}
	for i := 0; i < 70; i++ {
		say(t, db, 1, "user", "a question", t0.Add(-3*day+time.Duration(i)*2*time.Hour/10))
	}
	if said, err := Interaction(db, t0); err != nil || !said {
		t.Fatalf("interaction: %v %v", said, err)
	}
	if b := one(t, db, "SELECT body FROM shadow_observations WHERE detector = $1", DetInteraction); !strings.Contains(b, "70 messages in the last seven days, up from 30") {
		t.Fatalf("interaction said: %s", b)
	}
	if said, _ := Interaction(db, t0); said {
		t.Fatal("the same week named twice")
	}

	// SESSIONS, across every chat: three sittings past forty-five minutes and one message
	// after midnight this week, beside the seventy messages above (one sitting of 828 minutes
	// that ran past midnight); one long sitting the week before
	sit := func(start time.Time, minutes int) {
		for m := 0; m <= minutes; m += 20 {
			say(t, db, 2, "user", "…", start.Add(time.Duration(m)*time.Minute))
		}
	}
	sit(t0.Add(-9*day), 60)
	sit(t0.Add(-6*day), 60)
	sit(t0.Add(-5*day), 100)
	sit(t0.Add(-4*day), 60)
	say(t, db, 2, "user", "…", time.Date(2026, 11, 19, 2, 30, 0, 0, time.UTC))
	if said, err := Sessions(db, t0); err != nil || !said {
		t.Fatalf("sessions: %v %v", said, err)
	}
	if b := one(t, db, "SELECT body FROM shadow_observations WHERE detector = $1", DetSessions); !strings.Contains(b, "you had 5 sittings") || !strings.Contains(b, "(4 long, 2 late), against 1 the week before") || !strings.Contains(b, "The longest ran 828 minutes") {
		t.Fatalf("sessions said: %s", b)
	}

	// THE PERSON'S OWN WORDS: in the chat and in a check-in; somebody's email in the notes is not theirs
	say(t, db, 3, "user", "I've already put four years into this. I can't quit now.", t0.Add(-2*day))
	say(t, db, 3, "user", "It would all be for nothing.", t0.Add(-day))
	say(t, db, 3, "assistant", "You have already spent years on it, which is too far to stop.", t0.Add(-day)) // the box's words are not the person's
	journal := func(source, ref, body string, at time.Time) {
		if err := db.Exec("INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ($1, $2, $3, 't', $4, $5)", source, ref, at.Unix(), body, at.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	journal("ghost.voiced", "voice:1", "Said at the daily check-in: I'm just not a numbers person. That's just who I am.", t0.Add(-20*day))
	journal("ghost.noted", "n1", "I'm bad at asking for things, so I let it go.", t0.Add(-40*day))
	journal("ghost.noted", "n2", "From: boss@example.com\nSubject: review\n\nI've always been bad at feedback. That's just who I am.", t0.Add(-5*day))
	journal("ghost.framed", "f1", "photo: I've always been the quiet one", t0.Add(-5*day))
	for i := 0; i < 8; i++ {
		say(t, db, int64(20+i), "user", "I feel so lonely tonight", t0.Add(-time.Duration(i+1)*day))
	}
	say(t, db, 40, "user", "I'm really anxious about the move", t0.Add(-40*day))
	// what they wrote about someone: three in a note and a chat, one of them the box's echo
	journal("ghost.noted", "n3", "He says it never happened. I wrote it down that night.", t0.Add(-9*day))
	journal("ghost.voiced", "voice:2", "Said at the daily check-in: she told me I'm imagining things.", t0.Add(-6*day))
	say(t, db, 50, "user", "It makes me doubt my own memory.", t0.Add(-3*day))
	say(t, db, 50, "assistant", "He says it never happened, and that is hard.", t0.Add(-3*day))
	journal("ghost.noted", "n4", "I'm not allowed to share the roadmap.", t0.Add(-3*day)) // one sentence is one sentence
	notifBefore := one(t, db, "SELECT count(*) FROM notifications")
	if err := OwnWords(db, t0); err != nil {
		t.Fatal(err)
	}
	denied := one(t, db, "SELECT body || ' ' || data FROM shadow_observations WHERE detector = $1", DetMemoryDenied)
	if !strings.Contains(denied, "you wrote 3 times") || !strings.Contains(denied, "Most recently: “It makes me doubt my own memory.”") || !strings.Contains(denied, "He says it never happened.") {
		t.Fatalf("memory denied said: %s", denied)
	}
	if strings.Contains(denied, "that is hard") {
		t.Fatal("the box's sentence was read as the person's")
	}
	if n := one(t, db, "SELECT count(*) FROM shadow_observations WHERE detector IN ($1, $2)", DetPermission, DetBlameTurned); n != "0" {
		t.Fatalf("one sentence made an observation: %s", n)
	}
	// kept for them to find, and never sent: the three own-word detectors that announce did, this did not
	if n := one(t, db, "SELECT count(*) FROM notifications WHERE title LIKE '%did not happen%'"); n != "0" {
		t.Fatal("what was written about someone went to the phone")
	}
	if after := one(t, db, "SELECT count(*) FROM notifications"); after == notifBefore {
		t.Fatal("the detectors that announce did not")
	}
	sunk := one(t, db, "SELECT body FROM shadow_observations WHERE detector = $1", DetSunkCost)
	if !strings.Contains(sunk, "3 times, most recently: “It would all be for nothing.”") {
		t.Fatalf("sunk cost said: %s", sunk)
	}
	ident := one(t, db, "SELECT body || ' ' || data FROM shadow_observations WHERE detector = $1", DetIdentity)
	if !strings.Contains(ident, "3 times, most recently: “That's just who I am.”") || strings.Contains(ident, "feedback") || strings.Contains(ident, "quiet one") {
		t.Fatalf("identity said: %s", ident)
	}
	if p := one(t, db, "SELECT body FROM shadow_observations WHERE detector = $1", DetPersonal); !strings.Contains(p, "8 of your chats with the box were about how you were feeling, against 1 in the four before") {
		t.Fatalf("personal said: %s", p)
	}

	// MAIL: the first pass starts thirty days back; a mail with three kinds of pressure is
	// marked on the mail and not sent to the phone; a friend's mail is not marked
	mail := func(dir, subject, body string, at time.Time) int64 {
		id, _ := strconv.ParseInt(one(t, db, "INSERT INTO mail_messages (direction, relay_name, subject, body, from_name, received_at) VALUES ($1, $2, $3, $4, 'Shop', $5) RETURNING id",
			dir, fmt.Sprintf("%s-%d", subject, at.UnixNano()), subject, body, at.Unix()), 10, 64)
		return id
	}
	mail("in", "LAST CHANCE", "Only 2 left. Join over 5000 customers.", t0.Add(-60*day))
	pushy := mail("in", "Last chance: ends tonight", "Hurry! Only 2 left in stock. Join over 5,000 customers.", t0.Add(-day))
	mail("in", "Friday?", "Are you free on Friday? Bring the wine.", t0.Add(-day))
	mail("out", "Last chance", "only 2 left, join over 500 customers", t0.Add(-day))
	before := one(t, db, "SELECT count(*) FROM notifications")
	if n, err := Mail(db, t0); err != nil || n != 1 {
		t.Fatalf("mail marked %d: %v", n, err)
	}
	got := one(t, db, "SELECT subject || '|' || link || '|' || body FROM shadow_observations WHERE detector = $1", DetMail)
	if !strings.HasPrefix(got, fmt.Sprintf("mail:%d|mail:%d|This mail from Shop uses 3 kinds of pressure: urgency (“Last chance”), scarcity (“Only 2 left”) and social proof (“Join over 5,000 customers”). They are named, not judged", pushy, pushy)) {
		t.Fatalf("mail said: %s", got)
	}
	if one(t, db, "SELECT count(*) FROM notifications") != before {
		t.Fatal("a marked mail was sent to the phone")
	}
	if n, _ := Mail(db, t0); n != 0 {
		t.Fatal("a mail read twice")
	}

	// THE AGE OF WHAT IT BRINGS: 24 memories brought, 18 of them older than a year, and the
	// box holds newer ones
	given := func(when string, n int) string {
		items := make([]string, n)
		for i := range items {
			items[i] = `{"source":"memory","when":"` + when + `"}`
		}
		return `{"steps":[],"items":[{"source":"wikipedia"},` + strings.Join(items, ",") + `]}`
	}
	for i, g := range []string{given("2024-03-01", 18), given("2026-10-01", 6)} {
		id := say(t, db, int64(50+i), "assistant", "an answer", t0.Add(-day))
		if err := db.Exec("UPDATE chat_messages SET given = $1 WHERE id = $2", g, id); err != nil {
			t.Fatal(err)
		}
	}
	if said, _ := MemoryAge(db, t0); said {
		t.Fatal("old memories named when the box holds no newer ones")
	}
	for i := 0; i < 5; i++ {
		if err := db.Exec("INSERT INTO memories (title, body, kind, created_at, updated_at) VALUES ('t', 'b', 'distilled', $1, $1)", t0.Add(-10*day).UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	if said, err := MemoryAge(db, t0); err != nil || !said {
		t.Fatalf("memory age: %v %v", said, err)
	}
	if b := one(t, db, "SELECT body FROM shadow_observations WHERE detector = $1", DetMemoryAge); !strings.Contains(b, "Of the 24 memories the box brought to your chats in the last thirty days, 18 are more than a year old, though it holds 5 from the last three months") {
		t.Fatalf("memory age said: %s", b)
	}

	// AGREEMENT, from the arbiter's record: nine of ten sided with the asker
	for i := 0; i < 10; i++ {
		sided := "yes"
		if i == 0 {
			sided = "no"
		}
		if err := saveRead(db, read{MsgID: int64(9000 + i), ChatID: 1, Kind: "judgement", Verdict: "yes", Sided: sided, SameModel: true}, t0.Add(-day)); err != nil {
			t.Fatal(err)
		}
	}
	// one of them has its question and its cold read on record: the other side is shown with the count
	say(t, db, 60, "user", "Was I right to   walk out of the meeting?", t0.Add(-2*time.Hour))
	walked := say(t, db, 60, "assistant", "Yes, completely. You were right to.", t0.Add(-2*time.Hour))
	if err := saveRead(db, read{MsgID: walked, ChatID: 60, Kind: "judgement", Verdict: "no", Sided: "yes", Cold: "Walking out made the point and cost the room. Saying it and staying would have cost less.", SameModel: true}, t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	_ = saveRead(db, read{MsgID: 9100, ChatID: 1, Kind: "fact", Sided: "yes", SameModel: true}, t0)
	if said, err := Agreement(db, t0); err != nil || !said {
		t.Fatalf("agreement: %v %v", said, err)
	}
	if b := one(t, db, "SELECT body FROM shadow_observations WHERE detector = $1", DetAgreement); !strings.Contains(b, "read 11 of the box's answers") || !strings.Contains(b, "In 10 of them the box sided with you") ||
		!strings.Contains(b, "You asked: “Was I right to walk out of the meeting?”\nRead cold: Walking out made the point and cost the room.") {
		t.Fatalf("agreement said: %s", b)
	}
	if d := one(t, db, "SELECT data::json->'otherSide'->0->>'chat' FROM shadow_observations WHERE detector = $1", DetAgreement); d != "60" {
		t.Fatalf("the other side points at chat %s", d)
	}

	// PRAISE: 8 of 24 answers open with it
	for i := 0; i < 24; i++ {
		text := "The answer is forty-two."
		if i%3 == 0 {
			text = "Great question! The answer is forty-two."
		}
		say(t, db, 60, "assistant", text, t0.Add(-time.Duration(i)*time.Hour))
	}
	if said, err := Praise(db, t0); err != nil || !said {
		t.Fatalf("praise: %v %v", said, err)
	}
	if b := one(t, db, "SELECT body FROM shadow_observations WHERE detector = $1", DetPraise); !strings.Contains(b, "8 of the box's last 29 answers began by praising you or your question (“Great question!”)") {
		t.Fatalf("praise said: %s", b)
	}

	// and the whole of it together runs clean and says nothing twice
	n := one(t, db, "SELECT count(*) FROM shadow_observations")
	if err := Detect(db, t0, quiet); err != nil {
		t.Fatal(err)
	}
	if one(t, db, "SELECT count(*) FROM shadow_observations") != n {
		t.Fatal("a second pass said something again")
	}
}
