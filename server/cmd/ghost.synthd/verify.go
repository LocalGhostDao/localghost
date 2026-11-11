package main

// READ AGAIN , the box checks its memories against the records they were made from.
//
// A memory distilled from a chat or a note was written once and then believed. If the model
// misread the record that day, the mistake lived on: every later answer trusted the memory, and
// nothing ever went back to the chat to see whether it said that. Days and outings do not have
// this problem (they are rebuilt from the rows every time the rows change); the distilled facts
// and the facts about people did.
//
// This pass closes the loop. Once a day, on the GPU, a few at a time, it takes a memory, fetches
// the journal entry it was made from (the ref the memory carries), and asks the model one
// question: does the record say this? YES, NO or PARTLY, and on NO or PARTLY one sentence of what
// the record actually says, in the record's own words. The sentence is kept only when its names
// and numbers are in the record (the same discipline as the prose). YES stamps the memory
// verified; NO or PARTLY puts a DOUBT on it: the memory stays, marked, out of the chat's
// grounding and out of a person's rendered body, with what was found beside it on the phone, and
// the person decides (edit, delete, or leave it). A memory the person edited by hand is never
// read again: their words are theirs. Nothing is deleted by this pass, ever.
//
// What the pass does not judge: a memory or a fact that was not made from a journal entry (the
// about note, a hand edit) has nothing to be read against and is left as it is; a person's fact
// whose record is gone (a deleted chat) is left too, since nothing contradicts it. A distilled
// memory whose record is gone IS put in doubt, with nothing quoted: it can no longer be shown
// to be so. A YES stands only when the memory's own names and numbers are in the record.
//
// A memory is read again once; again if it changes after that (updated_at past verified_at).
// The record is immutable (journal entries are written once), so one reading per version of the
// memory is the whole job.

import (
	"encoding/json"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

const (
	verifyPer    = 6 // memories a pass (facts count one each); the GPU's time is the budget
	verifyDayKey = "synthd_verified_day"
	foundMax     = 300 // characters of "what the record says" kept
	recordMax    = 6000
)

// doubt is what the reading found when it did not agree, kept in the memory's meta under "doubt"
// (a distilled memory) or on the fact itself (a person's fact).
type doubt struct {
	At    int64  `json:"at"`              // unix ms
	Found string `json:"found,omitempty"` // what the record says instead, in its words; "" when the record is gone
	Ref   string `json:"ref,omitempty"`   // the record read
}

// verdict is one reading's result.
type verdict struct {
	Word  string // YES, NO, PARTLY
	Found string // grounded sentence, or ""
}

// readAgainPass runs once a day when the model is on the GPU. Returns how many memories it
// confirmed and how many it put in doubt.
func readAgainPass(db *poltergres.ReadWrite, oc *oracle.Client, lg *slog.Logger) (confirmed, doubted int, err error) {
	today := time.Now().Format("2006-01-02")
	if setting(db, verifyDayKey) == today {
		return 0, 0, nil
	}
	if onGPU, gerr := oc.OnGPU(); gerr != nil || !onGPU {
		return 0, 0, nil
	}
	budget := verifyPer
	owner := setting(db, ownerKey)

	// distilled memories first, newest unverified first
	rows, err := db.Query(`SELECT id, title, body, source_ref, coalesce(meta::text, '') FROM memories
		WHERE kind = 'distilled' AND NOT tombstoned AND NOT user_edited AND source_ref <> ''
		  AND (meta IS NULL OR NOT jsonb_exists(meta, 'verified_at') OR (meta->>'verified_at')::bigint < updated_at)
		ORDER BY created_at DESC LIMIT ` + strconv.Itoa(budget))
	if err != nil {
		return 0, 0, err
	}
	now := time.Now().UnixMilli()
	for _, v := range rows.Vals {
		if budget == 0 {
			break
		}
		if len(v) < 5 || v[0] == nil || v[3] == nil {
			continue
		}
		id, _ := strconv.ParseInt(*v[0], 10, 64)
		title, body, ref := str(v[1]), str(v[2]), *v[3]
		meta := map[string]any{}
		if v[4] != nil && *v[4] != "" {
			_ = json.Unmarshal([]byte(*v[4]), &meta)
		}
		record, kind := recordFor(db, ref)
		if kind == recordNotJournal {
			// made from something that is not a journal entry (the about note, a hand
			// edit): there is nothing to read it against, and that is not a doubt. Stamped
			// so the pass does not come back to it; no verdict either way.
			meta["verified_at"] = now
			meta["unread"] = "not made from a record"
			mb, _ := json.Marshal(meta)
			if err := db.Exec("UPDATE memories SET meta = $2::jsonb WHERE id = $1", id, string(mb)); err != nil {
				return confirmed, doubted, err
			}
			continue
		}
		vd, rerr := readAgain(oc, owner, record, title+" | "+body)
		if rerr != nil {
			lg.Warn("read again: inference failed, left for a later pass", "fn", "readAgainPass", "id", id, "err", rerr)
			continue
		}
		budget--
		meta["verified_at"] = now
		delete(meta, "unread")
		if vd.Word == "YES" {
			delete(meta, "doubt")
			confirmed++
		} else {
			meta["doubt"] = doubt{At: now, Found: vd.Found, Ref: ref}
			doubted++
			// the id and sizes at INFO; the words are the person's and go no further than DEBUG
			lg.Info("read again: a memory in doubt", "fn", "readAgainPass", "id", id, "verdict", vd.Word, "foundChars", len(vd.Found))
			lg.Debug("read again: a memory in doubt", "fn", "readAgainPass", "id", id, "title", title, "found", vd.Found)
		}
		mb, _ := json.Marshal(meta)
		if err := db.Exec("UPDATE memories SET meta = $2::jsonb WHERE id = $1", id, string(mb)); err != nil {
			return confirmed, doubted, err
		}
	}

	// then the facts about people, one fact at a time
	if budget > 0 {
		people, perr := loadPeople(db)
		if perr != nil {
			return confirmed, doubted, perr
		}
		for _, p := range people {
			if budget == 0 {
				break
			}
			if p.userEdited {
				continue
			}
			changed := false
			for i := range p.meta.Facts {
				f := &p.meta.Facts[i]
				if budget == 0 {
					break
				}
				if f.Ref == "" || f.V > 0 {
					continue
				}
				record, kind := recordFor(db, f.Ref)
				if kind != recordFound {
					// a fact folded from the about note or a hand edit ("about", "edited",
					// "memory") has no record to be read against, and a record that is gone
					// (a deleted chat) cannot contradict anything: neither is a doubt. The
					// audit of this pass found it doubting every fact learnt from a check-in
					// without asking the model. The fact stays as it is, unread.
					continue
				}
				vd, rerr := readAgain(oc, owner, record, p.title+" | "+f.T)
				if rerr != nil {
					lg.Warn("read again: inference failed, left for a later pass", "fn", "readAgainPass", "personId", p.id, "err", rerr)
					continue
				}
				budget--
				f.V = now
				if vd.Word == "YES" {
					f.Doubt = ""
					confirmed++
				} else {
					f.Doubt = vd.Found
					if f.Doubt == "" {
						f.Doubt = "the record does not say this"
					}
					doubted++
					lg.Info("read again: a fact in doubt", "fn", "readAgainPass", "personId", p.id, "fact", i, "verdict", vd.Word, "foundChars", len(vd.Found))
					lg.Debug("read again: a fact in doubt", "fn", "readAgainPass", "person", p.title, "fact", f.T, "found", vd.Found)
				}
				changed = true
			}
			if changed {
				if err := db.Exec("UPDATE memories SET body = $2, meta = $3::jsonb WHERE id = $1", p.id, personBody(p.meta), p.meta.json()); err != nil {
					return confirmed, doubted, err
				}
			}
		}
	}
	if budget == verifyPer {
		// nothing left to read: the day is done and tomorrow's pass starts fresh
		return 0, 0, setSetting(db, verifyDayKey, today)
	}
	// more may be waiting: the day is marked only when a pass finds nothing, so a backlog drains
	// at one pass every ten minutes while the GPU is up
	return confirmed, doubted, nil
}

// What recordFor found.
const (
	recordFound      = iota // the entry, with words in it
	recordGone              // a journal ref whose entry is no longer there (a deleted chat)
	recordNotJournal        // a ref that never named a journal entry ("about", "edited", "memory")
)

// recordFor fetches the journal entry a memory or a fact was made from, by its ref: an entry's
// own ref ("chat:12", "note:…") or "checkin:<id>", the entry's row id (a check-in's facts
// carry that). A ref with no kind before a colon was never a journal ref.
func recordFor(db *poltergres.ReadWrite, ref string) (string, int) {
	kind, rest, ok := strings.Cut(ref, ":")
	if !ok || kind == "" || rest == "" || kind == "person" {
		return "", recordNotJournal
	}
	var rows *poltergres.Rows
	var err error
	if kind == "checkin" {
		id, perr := strconv.ParseInt(rest, 10, 64)
		if perr != nil {
			return "", recordNotJournal
		}
		rows, err = db.Query("SELECT title, body FROM journal_entries WHERE id = $1", id)
	} else {
		rows, err = db.Query("SELECT title, body FROM journal_entries WHERE ref = $1 ORDER BY id DESC LIMIT 1", ref)
	}
	if err != nil || len(rows.Vals) == 0 || len(rows.Vals[0]) < 2 {
		return "", recordGone
	}
	s := strings.TrimSpace(str(rows.Vals[0][0]) + "\n" + str(rows.Vals[0][1]))
	if s == "" {
		return "", recordGone
	}
	if r := []rune(s); len(r) > recordMax {
		s = string(r[:recordMax])
	}
	return s, recordFound
}

// readAgain asks the model whether the record says what the memory says. A record that is gone
// needs no model: the memory cannot be checked, and says so.
func readAgain(oc *oracle.Client, owner, record, memory string) (verdict, error) {
	if strings.TrimSpace(record) == "" {
		return verdict{Word: "NO", Found: ""}, nil
	}
	resp, err := oc.Infer(oracle.Request{
		Capability:  "summarize",
		Priority:    oracle.PriorityBackground,
		Input:       readAgainPrompt(owner, record, memory),
		MaxTokens:   120,
		Temperature: 0,
		DeadlineMS:  90000,
	})
	if err != nil {
		return verdict{}, err
	}
	v := parseVerdict(resp.Output, record)
	if v.Word == "YES" && !supported(memory, record, owner) {
		// a bare YES is the model's word alone. The memory's own names and numbers must be in
		// the record, the same test the "found" sentence has to pass; one that is not there
		// makes it PARTLY, with nothing quoted
		v = verdict{Word: "PARTLY"}
	}
	return v, nil
}

// supported reports whether every number and every capitalised word of the memory (after its
// title) is somewhere in the record. It cannot say the memory is right; it can say a YES is
// not contradicted by a name or a figure the record never had. The owner's own name is not
// looked for: a note or a check-in is written in the first person and never has it.
func supported(memory, record, owner string) bool {
	_, body, ok := strings.Cut(memory, " | ")
	if !ok {
		body = memory
	}
	low := strings.ToLower(record)
	for _, loc := range capWordRe.FindAllStringIndex(body, -1) {
		if loc[0] == 0 || strings.HasSuffix(strings.TrimRight(body[:loc[0]], " "), ".") {
			continue // the first word of a sentence is capitalised for that alone
		}
		w := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(body[loc[0]:loc[1]], "'s"), "’s"))
		if owner != "" && w == strings.ToLower(owner) {
			continue
		}
		if !strings.Contains(low, w) {
			return false
		}
	}
	for _, n := range numberRe.FindAllString(body, -1) {
		if !strings.Contains(record, n) {
			return false
		}
	}
	return true
}

// readAgainPrompt is the one question (pure, for the tests).
func readAgainPrompt(owner, record, memory string) string {
	who := owner
	if who == "" {
		who = "the person"
	}
	var b strings.Builder
	b.WriteString("You check one line of memory against the record it was made from. The record is a conversation or a note of ")
	b.WriteString(who)
	b.WriteString("'s.\n\nRECORD:\n")
	b.WriteString(record)
	b.WriteString("\n\nMEMORY: ")
	b.WriteString(memory)
	b.WriteString("\n\nAnswer with one word on the first line: YES if the record says this, NO if the record says otherwise or does not say it at all, PARTLY if some of it is in the record and some is not.")
	b.WriteString(" If NO or PARTLY, write on the second line one plain sentence of what the record actually says about it, using only words that are in the record. Nothing else.")
	return b.String()
}

var capWordRe = regexp.MustCompile(`\b\p{Lu}[\p{L}'’-]+`)

// parseVerdict reads the model's answer: the first word is the verdict (anything else is
// PARTLY, the cautious reading); the rest is what it found, kept only when every capitalised
// word and every number in it is in the record, so the sentence shown to the person is the
// record's and not the model's.
func parseVerdict(out, record string) verdict {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 {
		return verdict{Word: "PARTLY"}
	}
	first := strings.ToUpper(strings.Trim(strings.Fields(lines[0] + " x")[0], ".,:;!\"'*-"))
	v := verdict{Word: "PARTLY"}
	switch first {
	case "YES", "NO", "PARTLY":
		v.Word = first
	}
	if v.Word == "YES" {
		return v
	}
	rest := strings.TrimSpace(strings.Join(lines[1:], " "))
	if rest == "" && len(strings.Fields(lines[0])) > 1 {
		rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(lines[0], first), strings.ToLower(first)), ":"))
		rest = strings.TrimLeft(rest, " .,:;-")
	}
	rest = strings.Trim(rest, "\"“”")
	if rest == "" {
		return v
	}
	low := strings.ToLower(record)
	for _, loc := range capWordRe.FindAllStringIndex(rest, -1) {
		if loc[0] == 0 || strings.HasSuffix(strings.TrimRight(rest[:loc[0]], " "), ".") {
			continue // the first word of a sentence is capitalised for that alone
		}
		if !strings.Contains(low, strings.ToLower(rest[loc[0]:loc[1]])) {
			return v // a name the record does not have: the sentence is not the record's
		}
	}
	for _, n := range numberRe.FindAllString(rest, -1) {
		if !strings.Contains(record, n) {
			return v
		}
	}
	if r := []rune(rest); len(r) > foundMax {
		rest = string(r[:foundMax-1]) + "…"
	}
	v.Found = rest
	return v
}
