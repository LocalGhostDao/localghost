package main

// THE FRONT PAGE , what every chat starts from. Until 12 November 2026 that was the first 1,200
// characters of the note about me and my people, whatever the note went on to say and whatever
// the box had learnt since. Now the same room holds a page chosen once a day:
//
//	the opening of the note (the person's own words about who they are come first, always);
//	the anchors, the few memories the person marked as the ones their life hangs on;
//	the heaviest of the rest (weight.go): facts about me, my people, what I wrote by hand, what
//	the box noticed, a trip, and what it distilled once it has read the record again and
//	found it there. Days, outings, months and places are left to the question: they come when
//	a question touches them.
//
// WHAT THE PAGE IS TRUSTED WITH. It is the head of every prompt, so a line on it is read with
// more weight than context a question happened to match. Three things keep that from being a
// way in for words that were never the person's (a web page quoted in a chat can be distilled
// into a memory: audit run 2, still open): a distilled memory is on the page only after the
// read-again pass confirmed it against its record, or when the person anchored it; every line
// is one sentence on one line, clipped, so it cannot fake a heading or carry a paragraph; and
// the box's own lines are introduced to the model as its notes, which can be wrong, not as the
// person's words. The anchors' heading says the choice was the person's, which it was.
//
// Chosen once a day and kept (settings front_page), not once a chat: the page is the first thing
// in the prompt, and the model keeps what it has already read of a prompt that begins the same
// way, so a page that changed with every question would be read from the top every time. It is
// chosen again when the day turns, when the note changes, and when the person sets or lifts an
// anchor, edits a memory or deletes one (secd empties the setting; a deleted memory must not be
// quoted for the rest of the day). MEMORIES marks the memories on it, so the person can see what
// the box leads with and change it.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

const (
	frontBudget  = aboutClip // characters for the whole page: what the clip of the note used to take
	frontNoteMax = 700       // the most the note's opening takes when there are memories to show beside it
	frontLineMax = 170       // characters of one memory on the page
	frontAnchors = 8         // anchors on the page at most (the newest marked first)
	frontHeavy   = 8         // other memories on the page at most
)

// The two headings, in the person's voice to the model. The first says whose choice the anchors
// were; the second says whose words the rest are.
const (
	frontAnchorHead = "The memories I marked as the ones my life hangs on:"
	frontHeavyHead  = "From your own notes about me, what has been closest lately (your words, not mine; they can be wrong):"
)

// frontPage is the stored page: the day and the note it was chosen for, the memories on it, and
// its text as the chat reads it.
type frontPage struct {
	Day  string  `json:"day"`
	Sig  string  `json:"sig"`
	IDs  []int64 `json:"ids"`
	Text string  `json:"text"`
}

// frontCand is one memory that could be on the page.
type frontCand struct {
	id       int64
	kind     string
	title    string
	body     string
	anchored int64 // when it was marked (unix ms), 0 for not an anchor
	weight   float64
}

// clipNote is the opening of the note: at most n bytes, cut back to the end of a sentence or a
// line when that keeps more than half, and never through the middle of a character.
func clipNote(note string, n int) string {
	note = strings.TrimSpace(note)
	if len(note) <= n {
		return note
	}
	cut := note[:n]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	if i := strings.LastIndexAny(cut, ".\n"); i > n/2 {
		cut = cut[:i+1]
	}
	return strings.TrimSpace(cut)
}

// firstSentence is a memory's body up to its first full stop, on one line.
func firstSentence(body string) string {
	s := oneLine(body)
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i+1]
	}
	return s
}

// oneLine puts a text on one line: every run of white space, line breaks included, is one space.
// A memory's words must not be able to start a line of their own on the page.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// frontLine is one memory as a line of the page: its title and its first sentence, or the
// sentence alone when it already begins with the title ("Cristina is Vlad's wife.").
func frontLine(title, body string) string {
	title = oneLine(title)
	s := firstSentence(body)
	line := title
	switch {
	case s == "":
	case strings.HasPrefix(strings.ToLower(s), strings.ToLower(title)):
		line = s
	default:
		line = title + ": " + s
	}
	if r := []rune(line); len(r) > frontLineMax {
		line = strings.TrimSpace(string(r[:frontLineMax-1])) + "…"
	}
	return line
}

// chooseFront lays the page out (pure, for the tests): the note's opening, the anchors, then the
// heaviest of the rest, within the budget. A memory whose line the note's opening already says
// is left out, and so is one that would not fit: the page never ends mid-line.
func chooseFront(note string, cands []frontCand, budget int) (string, []int64) {
	var anchors, rest []frontCand
	for _, c := range cands {
		if c.anchored > 0 {
			anchors = append(anchors, c)
		} else {
			rest = append(rest, c)
		}
	}
	sort.SliceStable(anchors, func(i, j int) bool {
		if anchors[i].anchored != anchors[j].anchored {
			return anchors[i].anchored > anchors[j].anchored
		}
		return anchors[i].id < anchors[j].id
	})
	sort.SliceStable(rest, func(i, j int) bool {
		if rest[i].weight != rest[j].weight {
			return rest[i].weight > rest[j].weight
		}
		return rest[i].id < rest[j].id
	})
	if len(anchors) > frontAnchors {
		anchors = anchors[:frontAnchors]
	}
	noteRoom := budget
	if len(anchors)+len(rest) > 0 && noteRoom > frontNoteMax {
		noteRoom = frontNoteMax
	}
	opening := clipNote(note, noteRoom)
	var b strings.Builder
	if opening != "" {
		b.WriteString("What I have told you about me and my people:\n" + opening)
	}
	said := strings.ToLower(opening)
	var ids []int64
	// named: a memory whose line does not fit is still given by its title when that does. For
	// the anchors: the person marked them, so the page names every one it has room to name.
	section := func(head string, list []frontCand, max int, named bool) {
		wrote := 0
		for _, c := range list {
			if wrote == max {
				break
			}
			line := frontLine(c.title, c.body)
			// what the page already says is not said again: the memory's sentence when it has
			// one (the note's own facts come back as memories), else its title
			again := strings.ToLower(strings.TrimRight(firstSentence(c.body), ".…"))
			if again == "" {
				again = strings.ToLower(line)
			}
			if line == "" || strings.Contains(said, again) {
				continue
			}
			lead := "\n- "
			if wrote == 0 {
				lead = "\n" + head + lead
			}
			if b.Len()+len(lead)+len(line) > budget {
				line = oneLine(c.title)
				if !named || line == "" || b.Len()+len(lead)+len(line) > budget {
					continue
				}
			}
			b.WriteString(lead + line)
			said += "\n" + strings.ToLower(line)
			ids = append(ids, c.id)
			wrote++
		}
	}
	section(frontAnchorHead, anchors, frontAnchors, true)
	section(frontHeavyHead, rest, frontHeavy, false)
	if len(ids) == 0 && noteRoom < budget {
		// nothing went beside the note after all: it has the whole page, as it used to
		return chooseFront(note, nil, budget)
	}
	return strings.TrimPrefix(b.String(), "\n"), ids
}

// frontSig is what a stored page was chosen for: the day, and the note as it was.
func frontSig(day, note string) string {
	h := sha256.Sum256([]byte(note))
	return day + "|" + hex.EncodeToString(h[:6])
}

// frontCands reads the memories that could be on the page: every anchor, and of the rest the
// kinds a chat is better for knowing from its first word, with nothing the box has come to doubt
// and nothing distilled that it has not yet read again and confirmed (see the head of this file).
func frontCands(db *poltergres.ReadWrite, nowMs int64) ([]frontCand, error) {
	rows, err := db.Query(`SELECT id, kind, title, body, COALESCE(anchored_at, 0) FROM memories
		WHERE NOT tombstoned AND (meta IS NULL OR NOT jsonb_exists(meta, 'doubt'))
		AND (anchored_at IS NOT NULL OR kind IN ('me','person','user','insight','trip')
			OR (kind = 'distilled' AND meta IS NOT NULL AND jsonb_exists(meta, 'verified_at')))`)
	if err != nil {
		return nil, err
	}
	w, err := memWeights(db, nil, nowMs)
	if err != nil {
		return nil, err
	}
	out := make([]frontCand, 0, len(rows.Vals))
	for _, v := range rows.Vals {
		if len(v) < 5 || v[0] == nil {
			continue
		}
		c := frontCand{kind: str(v[1]), title: str(v[2]), body: str(v[3])}
		c.id, _ = strconv.ParseInt(*v[0], 10, 64)
		c.anchored, _ = strconv.ParseInt(str(v[4]), 10, 64)
		c.weight = w[c.id]
		out = append(out, c)
	}
	return out, nil
}

// frontPageFor is today's page: the stored one when it was chosen for this day and this note,
// else chosen now and kept. When the memories cannot be read the note's opening stands alone,
// as it did before there was a page: a chat never waits on this.
func frontPageFor(db *poltergres.ReadWrite, now time.Time) string {
	if db == nil {
		return ""
	}
	note := setting(db, aboutKey)
	day := now.In(hw.LocalZone(db)).Format("2006-01-02")
	sig := frontSig(day, note)
	if raw := setting(db, hw.FrontPageKey); raw != "" {
		var fp frontPage
		if json.Unmarshal([]byte(raw), &fp) == nil && fp.Sig == sig {
			return fp.Text
		}
	}
	cands, err := frontCands(db, now.UnixMilli())
	if err != nil {
		text, _ := chooseFront(note, nil, frontBudget)
		return text
	}
	text, ids := chooseFront(note, cands, frontBudget)
	if b, merr := json.Marshal(frontPage{Day: day, Sig: sig, IDs: ids, Text: text}); merr == nil {
		_ = setSetting(db, hw.FrontPageKey, string(b))
	}
	return text
}
