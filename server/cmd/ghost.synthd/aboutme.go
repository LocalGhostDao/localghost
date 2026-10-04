package main

// ABOUT ME AND MY PEOPLE. The person writes a note in MEMORIES (settings about_me): who they are,
// and the people in their life. When the note changes, the model reads it once and the box keeps
// what it says as memories: facts about me (kind 'me'), and one memory per person (kind 'person',
// titled with their name). The chat starts every question from it: the box is LocalGhost, the
// person is who the note says, and "LocalGhost" in a question means the box itself. Memories are
// written with the person's name ("Vlad prefers…", "Cristina is Vlad's partner…"), so a search for
// a name finds them and they group by who they are about; "I" only until the box knows the name,
// never "the user". namePass writes the older first-person ones again with the name.

import (
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

const (
	aboutKey     = "about_me"           // the note, as written
	aboutDoneKey = "about_me_distilled" // the hash of the note the memories were made from
	ownerKey     = "owner_name"         // the name the note gives
	aboutClip    = 1200                 // characters of the note every chat starts from
	namedKey     = "memories_named"     // "<name>:<id>": how far namePass has written the older memories again
	checkinsKey  = "about_checkins"     // the newest check-in journal entry read for facts about me
	checkinsPer  = 4                    // check-ins read a pass
)

func setting(db *poltergres.ReadWrite, key string) string {
	if db == nil {
		return ""
	}
	rows, err := db.Query("SELECT value FROM settings WHERE key = $1", key)
	if err != nil || len(rows.Vals) != 1 || rows.Vals[0][0] == nil {
		return ""
	}
	return *rows.Vals[0][0]
}

func setSetting(db *poltergres.ReadWrite, key, value string) error {
	return db.Exec("INSERT INTO settings (key, value) VALUES ($1,$2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", key, value)
}

// aboutPrompt asks for what the note says, line by line, and nothing else: about me by my name,
// in the third person, so the memories read the same as the ones from the chats.
func aboutPrompt(note string) string {
	return "Below is a note I wrote about myself and the people in my life. Turn it into lines, each exactly in one of these forms:\n" +
		"NAME | my first name (once, if the note gives it)\n" +
		"ME | a short title | one sentence about me, using my first name in the third person (\"Vlad lives …\", \"Vlad's company …\"); in the first person only when the note gives no name\n" +
		"PERSON | their name | who they are to me and what I said about them, one or two sentences naming them and me (\"Cristina is Vlad's partner …\")\n" +
		"Never \"the user\". Only what the note says; nothing added, no opinion. Reply with the lines only.\n\nNOTE:\n" + note
}

// aboutFacts is what the model made of the note.
type aboutFacts struct {
	Name   string
	Me     [][2]string // title, body
	People [][2]string // name, body
}

var (
	reTheUsers  = regexp.MustCompile(`(?i)\bthe user's\b`)
	reTheUser   = regexp.MustCompile(`(?i)\bthe user\b`)
	reIam       = regexp.MustCompile(`\bI(?: am|'m)\b`)
	reIhave     = regexp.MustCompile(`\bI(?: have|'ve)\b`)
	reIwas      = regexp.MustCompile(`\bI was\b`)
	reMy        = regexp.MustCompile(`\b[Mm]y\b`)
	reMe        = regexp.MustCompile(`\b(?:me|myself)\b`)
	reFirstWord = regexp.MustCompile(`\b(?:I|[Mm]y|me|myself|mine)\b`)
)

// named mends a slip in the model's line: with my name known, "the user", "I am", "my" and "me"
// become the name ("Vlad's", "Vlad is"); without one, "the user's" becomes "my" as before. The
// prompts ask for this already; a slip is mended rather than kept. Other "I" verbs stay as they
// are (namePass has the model write those again).
func named(s, name string) string {
	if name == "" {
		return reTheUsers.ReplaceAllStringFunc(s, func(m string) string {
			if m[0] == 'T' {
				return "My"
			}
			return "my"
		})
	}
	s = reTheUsers.ReplaceAllString(s, name+"'s")
	s = reTheUser.ReplaceAllString(s, name)
	s = reIam.ReplaceAllString(s, name+" is")
	s = reIhave.ReplaceAllString(s, name+" has")
	s = reIwas.ReplaceAllString(s, name+" was")
	s = reMy.ReplaceAllString(s, name+"'s")
	return reMe.ReplaceAllString(s, name)
}

// parseAbout reads the model's lines (pure, for the tests): the name first, so every line is
// mended with it wherever it came in the reply.
func parseAbout(out string) aboutFacts {
	var f aboutFacts
	var rows [][]string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*•0123456789. "))
		parts := strings.Split(line, "|")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		if len(parts) == 2 && strings.EqualFold(parts[0], "NAME") && f.Name == "" && len(parts[1]) <= 40 && parts[1] != "" {
			f.Name = parts[1]
			continue
		}
		rows = append(rows, parts)
	}
	seen := map[string]bool{}
	for _, parts := range rows {
		switch {
		case len(parts) == 3 && strings.EqualFold(parts[0], "ME") && parts[1] != "" && parts[2] != "" && len(parts[1]) <= 120 && len(parts[2]) <= 500:
			f.Me = append(f.Me, [2]string{named(parts[1], f.Name), named(parts[2], f.Name)})
		case len(parts) == 3 && strings.EqualFold(parts[0], "PERSON") && parts[1] != "" && parts[2] != "" && len(parts[1]) <= 60 && len(parts[2]) <= 800:
			k := strings.ToLower(parts[1])
			if seen[k] {
				// the same person twice: one memory, both sentences
				for i := range f.People {
					if strings.EqualFold(f.People[i][0], parts[1]) {
						f.People[i][1] += " " + named(parts[2], f.Name)
					}
				}
				continue
			}
			seen[k] = true
			f.People = append(f.People, [2]string{parts[1], named(parts[2], f.Name)})
		}
	}
	return f
}

// aboutPass makes the note's memories when the note changed since the last time: the model reads
// it, the old note's memories go (the ones edited by hand stay), the new ones are written.
// Returns how many memories it wrote.
func aboutPass(db *poltergres.ReadWrite, oc *oracle.Client, lg *slog.Logger) (int, error) {
	note := strings.TrimSpace(setting(db, aboutKey))
	hash := hw.AboutHash(note) // with the version: a change in how memories are written makes them again
	if setting(db, aboutDoneKey) == hash {
		return 0, nil
	}
	if note == "" {
		// the note was cleared: so are its memories
		if err := db.Exec("UPDATE memories SET tombstoned = TRUE WHERE source_ref LIKE 'about:%' AND NOT user_edited"); err != nil {
			return 0, err
		}
		return 0, setSetting(db, aboutDoneKey, hash)
	}
	if onGPU, err := oc.OnGPU(); err != nil || !onGPU {
		return 0, nil // the note waits for the GPU
	}
	resp, err := oc.Infer(oracle.Request{
		Capability: "summarize", Class: oracle.ClassLocalSmall, Priority: oracle.PriorityBackground,
		Input: aboutPrompt(note), MaxTokens: 1200, Temperature: 0.2, DeadlineMS: 180000,
	})
	if err != nil || resp.Err != "" {
		return 0, nil
	}
	f := parseAbout(resp.Output)
	if f.Name == "" && len(f.Me) == 0 && len(f.People) == 0 {
		lg.Debug("about note: nothing usable from the model, tried again next pass", "fn", "aboutPass")
		return 0, nil
	}
	if err := db.Exec("DELETE FROM memories WHERE source_ref LIKE 'about:%' AND NOT user_edited"); err != nil {
		return 0, err
	}
	now := time.Now().UnixMilli()
	written := 0
	for i, m := range f.Me {
		if err := db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ($1,$2,'me',$3,$4,$4)",
			m[0], m[1], "about:me:"+strconv.Itoa(i), now); err != nil {
			return written, err
		}
		written++
	}
	for _, p := range f.People {
		if err := db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ($1,$2,'person',$3,$4,$4)",
			p[0], p[1], "about:person:"+strings.ToLower(p[0]), now); err != nil {
			return written, err
		}
		written++
	}
	if f.Name != "" {
		if err := setSetting(db, ownerKey, f.Name); err != nil {
			return written, err
		}
	}
	lg.Info("about note made into memories", "fn", "aboutPass", "me", len(f.Me), "people", len(f.People))
	return written, setSetting(db, aboutDoneKey, hash)
}

// peopleNames is the people the box knows by name (the note's and the chats'), for the distiller.
func peopleNames(db *poltergres.ReadWrite) []string {
	rows, err := db.Query("SELECT DISTINCT title FROM memories WHERE kind = 'person' AND NOT tombstoned ORDER BY title LIMIT 60")
	if err != nil {
		return nil
	}
	var out []string
	for _, v := range rows.Vals {
		if len(v) == 1 && v[0] != nil {
			out = append(out, *v[0])
		}
	}
	return out
}

// identityText is what every chat question starts from (pure, for the tests): the box's name and
// what "LocalGhost" means, who is asking, and what they wrote about themselves and their people.
func identityText(name, note string) string {
	var b strings.Builder
	b.WriteString("You are LocalGhost: the private AI that runs on my own box at home, with my photos, notes, trail and chats; nothing I tell you leaves it. When I say LocalGhost, the ghost or the box, I mean you.")
	if name != "" {
		b.WriteString(" I am " + name + "; the memories you are given say " + name + " where they mean me.")
	}
	if note = strings.TrimSpace(note); note != "" {
		if len(note) > aboutClip {
			note = note[:aboutClip]
			if i := strings.LastIndexAny(note, ".\n"); i > aboutClip/2 {
				note = note[:i+1]
			}
		}
		b.WriteString("\nWhat I have told you about me and my people:\n" + note)
	}
	return b.String()
}

// chatIdentity reads the name and the note for the chat (the box's name alone without them),
// then what else the box holds and how it writes (voice.go).
func chatIdentity(mount string) string {
	db := chatStore(mount)
	return identityText(setting(db, ownerKey), setting(db, aboutKey)) + "\n" + holdingsText() + "\n" + voiceRules
}

// holdingsText says what the box holds beyond the archive, so the model neither denies having
// Wikipedia nor claims the web: the English Wikipedia when the file is on the box (its lead comes
// as [wikipedia] context when an article fits the question), the weather it pulls, the market
// numbers it keeps. It has no internet of its own; what the phone found on the web comes labelled.
func holdingsText() string {
	var b strings.Builder
	b.WriteString("What you hold besides my archive: ")
	if w, err := boxWiki.Get(); err == nil && w != nil {
		b.WriteString("a copy of the English Wikipedia (" + w.Name + "), which is yours to quote when an article is given to you as [wikipedia] context; ")
	}
	b.WriteString("the weather for the world's larger places, pulled once a day; the prices and the news the box keeps. ")
	b.WriteString("You have no internet of your own; anything from the web was fetched by my phone and is labelled as such. When you were given nothing on a thing, say the box has nothing on it.")
	return b.String()
}

// distillPrompt asks for what a journal entry says that is worth keeping, about me or about one
// of my people: with my name in the third person once the box knows it ("Vlad prefers…"), in the
// first person until then (pure, for the tests).
func distillPrompt(owner string, people []string, title, body string) string {
	var b strings.Builder
	b.WriteString("From this journal entry, pick out up to 3 durable facts, preferences, plans or events about me worth remembering long-term")
	if owner != "" {
		b.WriteString(" (I am " + owner + ")")
		b.WriteString(". Write each about me by my name, in the third person (\"" + owner + " prefers…\", \"" + owner + "'s sister…\"), never \"I\" or \"the user\".")
	} else {
		b.WriteString(". Write each in the first person, as I would (\"I prefer…\", \"My sister…\"), never \"the user\".")
	}
	b.WriteString(" One per line, format exactly: TITLE | one-sentence body. ")
	b.WriteString("When a fact is about one of my people rather than me, write it as: PERSON | their name | one sentence about them")
	if owner != "" {
		b.WriteString(", naming them and me (\"Cristina is " + owner + "'s partner…\")")
	}
	if len(people) > 0 {
		b.WriteString(" (people I have told you about: " + strings.Join(people, ", ") + ")")
	}
	b.WriteString(". Only genuinely durable things , a single routine photo or a pleasantry is usually NOTHING. If nothing is worth remembering, reply with exactly: NONE\n\n")
	b.WriteString(title + "\n\n" + body)
	return b.String()
}

// notePerson adds a fact to the person's memory from the chats and the journal (one per person,
// apart from the note's, which the note rewrites), or starts it.
func notePerson(db *poltergres.ReadWrite, name, fact, ref string, srcChat int64, now int64) error {
	if name == "" || fact == "" || len(name) > 60 || len(fact) > 500 {
		return nil
	}
	key := "person:" + strings.ToLower(name)
	rows, err := db.Query("SELECT id, body FROM memories WHERE source_ref = $1 AND NOT tombstoned LIMIT 1", key)
	if err != nil {
		return err
	}
	if len(rows.Vals) == 1 && rows.Vals[0][0] != nil {
		old := ""
		if rows.Vals[0][1] != nil {
			old = *rows.Vals[0][1]
		}
		if strings.Contains(strings.ToLower(old), strings.ToLower(fact)) || len(old)+len(fact) > 1500 {
			return nil
		}
		return db.Exec("UPDATE memories SET body = $2, updated_at = $3 WHERE id = $1", *rows.Vals[0][0], strings.TrimSpace(old+" "+fact), now)
	}
	return db.Exec("INSERT INTO memories (title, body, kind, source_chat, source_ref, created_at, updated_at) VALUES ($1,$2,'person',NULLIF($3,0),$4,$5,$5)",
		name, fact, srcChat, key, now)
}

// namePrompt asks for older memories again, by my name in the third person, nothing else changed
// (pure, for the tests).
func namePrompt(owner string, rows [][3]string) string {
	var b strings.Builder
	b.WriteString("Below are notes about me, written in the first person. My name is " + owner + ". Write each again about me by my name, in the third person (\"" +
		owner + " prefers…\", \"" + owner + "'s sister…\"), keeping everything else as it is: the same facts, the same numbers, nothing added or left out. " +
		"Keep each note's number. One per line, exactly: N | title | body. Reply with the lines only.\n\n")
	for i, r := range rows {
		b.WriteString(strconv.Itoa(i+1) + " | " + r[1] + " | " + strings.ReplaceAll(r[2], "\n", " ") + "\n")
	}
	return b.String()
}

// parseNamed reads namePrompt's answer against the rows it was given (pure, for the tests): a line
// is kept when its number is one of the rows', it names me, it says no number the old note did
// not, and it is about the old note's length. Returns index to {title, body}.
func parseNamed(out, owner string, rows [][3]string) map[int][2]string {
	got := map[int][2]string{}
	for _, line := range strings.Split(out, "\n") {
		p := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(p) != 3 {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(p[0]), "-*#. ")))
		if err != nil || n < 1 || n > len(rows) {
			continue
		}
		old := rows[n-1]
		t, b := named(strings.TrimSpace(p[1]), owner), named(strings.TrimSpace(p[2]), owner)
		if t == "" || b == "" || len(t) > 120 || len(b) > 2*len(old[2])+80 || len(b) < len(old[2])/2 {
			continue
		}
		if !strings.Contains(t+" "+b, owner) {
			continue
		}
		allowed := map[string]bool{}
		for _, x := range numberRe.FindAllString(old[1]+" "+old[2], -1) {
			allowed[x] = true
		}
		ok := true
		for _, x := range numberRe.FindAllString(t+" "+b, -1) {
			if !allowed[x] {
				ok = false
				break
			}
		}
		if ok {
			got[n-1] = [2]string{t, b}
		}
	}
	return got
}

// namePass writes the memories made before the box knew my name again with it, eight at a time
// and three batches a pass at most, on the GPU: the distilled ones and my people's, never one I
// edited by hand nor the note's (aboutPass makes those). A memory with no "I", "my" or "me" in it
// is passed over; a line the model got wrong leaves its memory as it was. The cursor is kept with
// the name, so a new name starts again from the first. Returns how many it wrote again.
func namePass(db *poltergres.ReadWrite, oc *oracle.Client, lg *slog.Logger) (int, error) {
	owner := setting(db, ownerKey)
	if owner == "" {
		return 0, nil
	}
	var from int64
	if name, id, ok := strings.Cut(setting(db, namedKey), ":"); ok && name == owner {
		from, _ = strconv.ParseInt(id, 10, 64)
	}
	if onGPU, err := oc.OnGPU(); err != nil || !onGPU {
		return 0, nil
	}
	wrote := 0
	for batch := 0; batch < 3; batch++ {
		res, err := db.Query(`SELECT id, title, body FROM memories WHERE id > $1 AND NOT tombstoned AND NOT user_edited
			AND kind IN ('distilled','person') AND source_ref NOT LIKE 'about:%' ORDER BY id LIMIT 60`, from)
		if err != nil {
			return wrote, err
		}
		if len(res.Vals) == 0 {
			break
		}
		var rows [][3]string
		last := from
		for _, v := range res.Vals {
			if len(v) < 3 || v[0] == nil {
				continue
			}
			if len(rows) == 8 {
				break
			}
			last, _ = strconv.ParseInt(*v[0], 10, 64)
			r := [3]string{*v[0], "", ""}
			if v[1] != nil {
				r[1] = *v[1]
			}
			if v[2] != nil {
				r[2] = *v[2]
			}
			if reFirstWord.MatchString(r[1] + " " + r[2]) {
				rows = append(rows, r)
			}
		}
		if len(rows) > 0 {
			resp, err := oc.Infer(oracle.Request{
				Capability: "summarize", Class: oracle.ClassLocalSmall, Priority: oracle.PriorityBackground,
				Input: namePrompt(owner, rows), MaxTokens: 900, Temperature: 0.1, DeadlineMS: 120000,
			})
			if err != nil || resp.Err != "" {
				break // tried again next pass, from the same place
			}
			for i, tb := range parseNamed(resp.Output, owner, rows) {
				if err := db.Exec("UPDATE memories SET title = $2, body = $3 WHERE id = $1 AND NOT user_edited", rows[i][0], tb[0], tb[1]); err != nil {
					return wrote, err
				}
				wrote++
			}
		}
		from = last
		if err := setSetting(db, namedKey, owner+":"+strconv.FormatInt(from, 10)); err != nil {
			return wrote, err
		}
	}
	if wrote > 0 {
		lg.Info("older memories written again with the name", "fn", "namePass", "memories", wrote)
	}
	return wrote, nil
}

// checkinAboutPrompt asks what a check-in says about who I am and my people, nothing about how the
// day went (pure, for the tests).
func checkinAboutPrompt(owner, text string) string {
	var b strings.Builder
	b.WriteString("Below is what I said or wrote at a daily check-in. Pick out only what it says about who I am and the people in my life that stays true for months: ")
	b.WriteString("my name, where I live and come from, my work, my family and friends, lasting likes, habits and plans. How the day went, how I feel today and what I did today are not that. ")
	b.WriteString("Reply with lines, each exactly in one of these forms:\n")
	b.WriteString("NAME | my first name (only if I say it)\n")
	if owner != "" {
		b.WriteString("ME | a short title | one sentence about me, by my name in the third person (\"" + owner + " lives …\")\n")
		b.WriteString("PERSON | their name | one sentence about them, naming them and me (\"Cristina is " + owner + "'s partner …\")\n")
	} else {
		b.WriteString("ME | a short title | one sentence about me, in the first person (\"I live …\")\n")
		b.WriteString("PERSON | their name | one sentence about them, who they are to me\n")
	}
	b.WriteString("Never \"the user\". Only what the text says; nothing added. If it says nothing of that kind, reply with exactly: NONE\n\nCHECK-IN:\n")
	b.WriteString(text)
	return b.String()
}

// noteMe keeps a fact about me from a check-in: added to the check-ins' 'me' memory of the same
// title when there is one (once), else a memory of its own. The note's own memories are never
// touched. Returns whether it kept anything.
func noteMe(db *poltergres.ReadWrite, title, fact, ref string, now int64) (bool, error) {
	if title == "" || fact == "" || len(title) > 120 || len(fact) > 500 {
		return false, nil
	}
	// not the note's (aboutPass makes those again when the note changes, and what was added would go)
	rows, err := db.Query("SELECT id, body FROM memories WHERE kind = 'me' AND NOT tombstoned AND source_ref NOT LIKE 'about:%' AND lower(title) = lower($1) ORDER BY id LIMIT 1", title)
	if err != nil {
		return false, err
	}
	if len(rows.Vals) == 1 && rows.Vals[0][0] != nil {
		old := ""
		if rows.Vals[0][1] != nil {
			old = *rows.Vals[0][1]
		}
		if strings.Contains(strings.ToLower(old), strings.ToLower(strings.TrimRight(fact, "."))) || len(old)+len(fact) > 1500 {
			return false, nil
		}
		return true, db.Exec("UPDATE memories SET body = $2, updated_at = $3 WHERE id = $1", *rows.Vals[0][0], strings.TrimSpace(old+" "+fact), now)
	}
	return true, db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ($1,$2,'me',$3,$4,$4)", title, fact, ref, now)
}

// checkinAboutPass reads the check-ins (what was said, the voice note's transcript, and what was
// written) for facts about me and my people, oldest first, a few a pass, on the GPU, each once
// (the newest read is kept in settings about_checkins). What it finds joins the note's: 'me'
// memories by title, one memory per person, the name when the box has none. Returns how many
// facts it kept.
func checkinAboutPass(db *poltergres.ReadWrite, oc *oracle.Client, lg *slog.Logger) (int, error) {
	from, _ := strconv.ParseInt(setting(db, checkinsKey), 10, 64)
	rows, err := db.Query(`SELECT id, body FROM journal_entries WHERE id > $1 AND (
		(source = 'ghost.voiced' AND body LIKE 'Said at the daily check-in%') OR
		(source = 'ghost.noted' AND title LIKE 'Daily check-in %')) ORDER BY id LIMIT $2`, from, checkinsPer)
	if err != nil || len(rows.Vals) == 0 {
		return 0, err
	}
	if onGPU, err := oc.OnGPU(); err != nil || !onGPU {
		return 0, nil
	}
	kept := 0
	for _, v := range rows.Vals {
		if len(v) < 2 || v[0] == nil {
			continue
		}
		id := *v[0]
		text := ""
		if v[1] != nil {
			text = checkinWords(*v[1])
		}
		if len(strings.Fields(text)) >= 4 {
			owner := setting(db, ownerKey)
			resp, ierr := oc.Infer(oracle.Request{
				Capability: "summarize", Class: oracle.ClassLocalSmall, Priority: oracle.PriorityBackground,
				Input: checkinAboutPrompt(owner, text), MaxTokens: 600, Temperature: 0.2, DeadlineMS: 120000,
			})
			if ierr != nil || resp.Err != "" {
				return kept, nil // the same check-in next pass
			}
			f := parseAbout(resp.Output)
			if owner == "" && f.Name != "" {
				if err := setSetting(db, ownerKey, f.Name); err != nil {
					return kept, err
				}
				owner = f.Name
				f = parseAbout(resp.Output) // mended with the name now known
			}
			now := time.Now().UnixMilli()
			for i, m := range f.Me {
				ok, err := noteMe(db, m[0], m[1], "checkin:"+id+":"+strconv.Itoa(i), now)
				if err != nil {
					return kept, err
				}
				if ok {
					kept++
				}
			}
			for _, p := range f.People {
				if err := notePerson(db, p[0], p[1], "checkin:"+id, 0, now); err != nil {
					return kept, err
				}
				kept++
			}
		}
		if err := setSetting(db, checkinsKey, id); err != nil {
			return kept, err
		}
	}
	if kept > 0 {
		lg.Info("facts about me from the check-ins", "fn", "checkinAboutPass", "kept", kept)
	}
	return kept, nil
}

// checkinWords is what a check-in entry says, without its form: the voice note's transcript after
// its first line, or the written check-in's "Why:" and any free lines (feelings and ids out).
func checkinWords(body string) string {
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, "Said at the daily check-in") {
		if i := strings.IndexByte(body, '\n'); i >= 0 {
			return strings.TrimSpace(body[i+1:])
		}
		return ""
	}
	var keep []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "", strings.HasPrefix(line, "Daily check-in "), strings.HasPrefix(line, "Feeling: "),
			strings.HasPrefix(line, "Preselected: "), strings.HasPrefix(line, "Voice: "):
		case strings.HasPrefix(line, "Why: "):
			keep = append(keep, strings.TrimPrefix(line, "Why: "))
		default:
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}
