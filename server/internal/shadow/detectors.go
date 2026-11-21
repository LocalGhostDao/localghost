package shadow

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// THE DETECTORS THAT COUNT. Each reads something the box already holds, counts it against its
// threshold, and says what it counted, once per period. None of them asks the model. The time
// of a chat message is in milliseconds, as the chat keeps it.

const day = 24 * time.Hour

func month(now time.Time) string { return now.UTC().Format("2006-01") }

func isoWeek(now time.Time) string {
	y, w := now.UTC().ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

func localZone(db DB) *time.Location {
	if tz, ok := setting(db, "local_tz"); ok {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
	}
	return time.Local // as hw.LocalZone: the box's own clock until the phone has said where it is
}

func count(db DB, q string, args ...any) int64 {
	rows, err := db.Query(q, args...)
	if err != nil {
		return 0
	}
	return int64At(rows.Vals, 0, 0)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// Interaction: the messages sent this week against last week. It was the first detector and
// never spoke: it compared the chat's milliseconds with seconds, so "the week before" was
// always empty (found 21 November 2026).
func Interaction(db DB, now time.Time) (bool, error) {
	ms := func(t time.Time) int64 { return t.UnixMilli() }
	this := count(db, "SELECT count(*) FROM chat_messages WHERE role = 'user' AND ts >= $1", ms(now.Add(-7*day)))
	prior := count(db, "SELECT count(*) FROM chat_messages WHERE role = 'user' AND ts >= $1 AND ts < $2", ms(now.Add(-14*day)), ms(now.Add(-7*day)))
	th := Threshold(db, DetInteraction)
	if prior == 0 || this < 60 || float64(this) < th*float64(prior) {
		return false, nil
	}
	data, _ := json.Marshal(map[string]any{"thisWeek": this, "weekBefore": prior})
	return Observe(db, Observation{
		Detector: DetInteraction, Subject: "week:" + isoWeek(now),
		Title: "an observation about this week",
		Body:  fmt.Sprintf("You sent the box %d messages in the last seven days, up from %d in the seven before. They were counted, not read. That is all this says.", this, prior),
		Data:  string(data), Score: float64(this) / float64(prior), Threshold: th,
	}, now)
}

// sitting is a run of the person's messages with no gap over half an hour.
type sitting struct {
	from, to time.Time
	late     bool
}

func sittings(times []time.Time, loc *time.Location) []sitting {
	var out []sitting
	for _, t := range times {
		late := t.In(loc).Hour() < 5
		if n := len(out); n > 0 && t.Sub(out[n-1].to) <= 30*time.Minute {
			out[n-1].to = t
			out[n-1].late = out[n-1].late || late
			continue
		}
		out = append(out, sitting{from: t, to: t, late: late})
	}
	return out
}

// Sessions: sittings past forty-five minutes, and sittings between midnight and five, this
// week against last. Read from the times of the messages alone.
func Sessions(db DB, now time.Time) (bool, error) {
	rows, err := db.Query("SELECT ts FROM chat_messages WHERE role = 'user' AND ts >= $1 ORDER BY ts LIMIT 20000", now.Add(-14*day).UnixMilli())
	if err != nil {
		return false, err
	}
	var times []time.Time
	for i := range rows.Vals {
		times = append(times, time.UnixMilli(int64At(rows.Vals, i, 0)))
	}
	cut := now.Add(-7 * day)
	var this, prior, long, late int
	var longest time.Duration
	for _, s := range sittings(times, localZone(db)) {
		isLong := s.to.Sub(s.from) >= 45*time.Minute
		if !isLong && !s.late {
			continue
		}
		if s.from.Before(cut) {
			prior++
			continue
		}
		this++
		if isLong {
			long++
		}
		if s.late {
			late++
		}
		if d := s.to.Sub(s.from); d > longest {
			longest = d
		}
	}
	th := Threshold(db, DetSessions)
	if float64(this) < th || this <= prior {
		return false, nil
	}
	data, _ := json.Marshal(map[string]any{"thisWeek": this, "weekBefore": prior, "long": long, "late": late, "longestMinutes": int(longest.Minutes())})
	body := fmt.Sprintf("In the last seven days you had %s with the box that ran past forty-five minutes or fell between midnight and five (%d long, %d late), against %d the week before.", plural(this, "sitting", "sittings"), long, late, prior)
	if longest >= 45*time.Minute {
		body += fmt.Sprintf(" The longest ran %d minutes.", int(longest.Minutes()))
	}
	body += " Read from the times of your messages, not their words. Whether that was time well spent is yours to say."
	return Observe(db, Observation{Detector: DetSessions, Subject: "week:" + isoWeek(now), Title: "long and late sittings with the box",
		Body: body, Data: string(data), Score: float64(this), Threshold: th}, now)
}

// said is one sentence of the person's own, with when and where they wrote it.
type said struct {
	At    int64  `json:"at"` // unix seconds
	Where string `json:"where"`
	Text  string `json:"text"`
}

var emailLike = regexp.MustCompile(`(?m)^(From|Subject|To):`)

// ownWords is what the person wrote since a time: their chat messages, their notes and
// check-ins, their voice notes' words. A note that is somebody's email is left out.
func ownWords(db DB, since time.Time) ([]said, error) {
	var out []said
	rows, err := db.Query("SELECT ts, chat_id, content FROM chat_messages WHERE role = 'user' AND ts >= $1 ORDER BY ts LIMIT 5000", since.UnixMilli())
	if err != nil {
		return nil, err
	}
	for i, v := range rows.Vals {
		out = append(out, said{At: int64At(rows.Vals, i, 0) / 1000, Where: "chat:" + deref(v[1]), Text: deref(v[2])})
	}
	rows, err = db.Query("SELECT created_at, source, left(body, 8000) FROM journal_entries WHERE source IN ('ghost.noted', 'ghost.voiced') AND created_at >= $1 ORDER BY created_at LIMIT 2000", since.UnixMilli())
	if err != nil {
		return nil, err
	}
	for i, v := range rows.Vals {
		body := deref(v[2])
		if head := body; len(emailLike.FindAllString(head[:min(len(head), 600)], 3)) >= 2 {
			continue
		}
		out = append(out, said{At: int64At(rows.Vals, i, 0) / 1000, Where: strings.TrimPrefix(deref(v[1]), "ghost."), Text: body})
	}
	// in the order they were written, wherever they were written: "most recently" is the last
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out, nil
}

func sentencesWhere(all []said, since time.Time, pick func(string) []string) []said {
	var out []said
	for _, s := range all {
		if s.At < since.Unix() {
			continue
		}
		for _, hit := range pick(s.Text) {
			out = append(out, said{At: s.At, Where: s.Where, Text: hit})
		}
	}
	return out
}

func lastOf(hits []said, n int) []said {
	if len(hits) > n {
		return hits[len(hits)-n:]
	}
	return hits
}

// ownNotes is the three detectors that read what the person wrote about other people. Each body
// says how many, shows the latest sentence, and says what the box does not know.
var ownNotes = []struct {
	det   string
	pick  func(string) []string
	title string
	body  string
}{
	{DetMemoryDenied, MemoryDenied, "you wrote that someone tells you it did not happen",
		"In the last thirty days you wrote %d times that someone told you it did not happen, or that your memory or your reading of it was the fault. Most recently: “%s” What you wrote at the time is kept here as you wrote it, and it does not change when it is disputed. The box holds nothing the other person said, so it cannot say who is right. These are your own sentences, shown together because they were written apart."},
	{DetBlameTurned, BlameTurned, "you wrote that raising it ended with you at fault",
		"In the last thirty days you wrote %d times that when you raised something it ended with you at fault or apologising. Most recently: “%s” Once is an argument. Several, the same way round, is worth seeing as several. The box heard one side, yours, and only what you wrote down."},
	{DetPermission, Permission, "you wrote about needing someone's leave",
		"In the last thirty days you wrote %d times of needing someone's leave, of being checked on, or of being kept from people or money. Most recently: “%s” The same words fit a rule at work and a person at home, and the box cannot tell which. It is showing you your own sentences side by side. What they add up to is yours to say."},
}

// OwnWords runs the detectors that read the person's own sentences: what is already
// spent given as the reason to go on, a fixed label given as the reason, and how much of what
// they bring to the box is how they feel.
func OwnWords(db DB, now time.Time) error {
	all, err := ownWords(db, now.Add(-90*day))
	if err != nil {
		return err
	}
	if hits := sentencesWhere(all, now.Add(-30*day), SunkCost); float64(len(hits)) >= Threshold(db, DetSunkCost) {
		data, _ := json.Marshal(map[string]any{"count": len(hits), "sentences": lastOf(hits, 5)})
		if _, err := Observe(db, Observation{Detector: DetSunkCost, Subject: "month:" + month(now), Title: "this looks like going on because of what is spent",
			Body: fmt.Sprintf("In the last thirty days you gave what you have already put in as the reason to go on %d times, most recently: “%s” What is spent is spent whichever way you go. What is still to gain is the part that can change. The box has not worked out either figure; it is showing you your own words.",
				len(hits), hits[len(hits)-1].Text),
			Data: string(data), Score: float64(len(hits)), Threshold: Threshold(db, DetSunkCost)}, now); err != nil {
			return err
		}
	}
	if hits := sentencesWhere(all, now.Add(-90*day), IdentityLabels); float64(len(hits)) >= Threshold(db, DetIdentity) {
		data, _ := json.Marshal(map[string]any{"count": len(hits), "sentences": lastOf(hits, 5)})
		if _, err := Observe(db, Observation{Detector: DetIdentity, Subject: "month:" + month(now), Title: "a description of you is answering for you",
			Body: fmt.Sprintf("In the last ninety days a fixed description of yourself stood where a reason would %d times, most recently: “%s” A label is a summary of what has been. It is not evidence about what you can do next. The box is not saying the label is wrong.",
				len(hits), hits[len(hits)-1].Text),
			Data: string(data), Score: float64(len(hits)), Threshold: Threshold(db, DetIdentity)}, now); err != nil {
			return err
		}
	}
	// what they wrote about other people: kept for them to find, never sent (the detectors do
	// not announce). One observation a month for each, with the sentences and their dates.
	for _, o := range ownNotes {
		hits := sentencesWhere(all, now.Add(-30*day), o.pick)
		th := Threshold(db, o.det)
		if float64(len(hits)) < th {
			continue
		}
		data, _ := json.Marshal(map[string]any{"count": len(hits), "sentences": lastOf(hits, 8)})
		if _, err := Observe(db, Observation{Detector: o.det, Subject: "month:" + month(now), Title: o.title,
			Body: fmt.Sprintf(o.body, len(hits), hits[len(hits)-1].Text),
			Data: string(data), Score: float64(len(hits)), Threshold: th}, now); err != nil {
			return err
		}
	}
	// chats about how the person feels, the last four weeks against the four before
	this, prior := map[string]bool{}, map[string]bool{}
	cut, start := now.Add(-28*day).Unix(), now.Add(-56*day).Unix()
	for _, s := range all {
		if !strings.HasPrefix(s.Where, "chat:") || s.At < start || !IsFeeling(s.Text) {
			continue
		}
		if s.At >= cut {
			this[s.Where] = true
		} else {
			prior[s.Where] = true
		}
	}
	if th := Threshold(db, DetPersonal); float64(len(this)) >= th && len(this) >= 2*len(prior) {
		data, _ := json.Marshal(map[string]any{"lastFourWeeks": len(this), "fourWeeksBefore": len(prior)})
		if _, err := Observe(db, Observation{Detector: DetPersonal, Subject: "month:" + month(now), Title: "more of how you feel is coming to the box",
			Body: fmt.Sprintf("In the last four weeks %d of your chats with the box were about how you were feeling, against %d in the four before. The box answers at any hour and never tires. A person does, and that is part of what a person gives. The box cannot see who else you talk to, so this is half a picture, and what to make of it is yours.",
				len(this), len(prior)),
			Data: string(data), Score: float64(len(this)), Threshold: th}, now); err != nil {
			return err
		}
	}
	return nil
}

// Mail reads each mail that has arrived since the last pass for the six kinds of pressure, and
// marks the ones that lean on enough of them. A marked mail is not announced: the mark is for
// the mail's own page and the detector's history. The first pass starts thirty days back.
func Mail(db DB, now time.Time) (int, error) {
	var mark struct {
		Started bool  `json:"started"`
		Upto    int64 `json:"upto"`
	}
	daemonState(db, "mail", &mark)
	if !mark.Started {
		mark.Started = true
		mark.Upto = count(db, "SELECT COALESCE(max(id), 0) FROM mail_messages WHERE direction = 'in' AND received_at < $1", now.Add(-30*day).Unix())
	}
	// the body is text already: ghost.noted reduces an HTML mail to its words when it files it
	rows, err := db.Query("SELECT id, subject, body, from_name, from_addr FROM mail_messages WHERE direction = 'in' AND id > $1 ORDER BY id LIMIT 200", mark.Upto)
	if err != nil {
		return 0, err
	}
	th := Threshold(db, DetMail)
	marked := 0
	for i, v := range rows.Vals {
		id := int64At(rows.Vals, i, 0)
		cues := MailCues(deref(v[1]), deref(v[2]))
		if float64(len(cues)) >= th {
			parts := make([]string, 0, len(cues))
			for _, c := range cues {
				parts = append(parts, c.Family+" (“"+clip(c.Words, 60)+"”)")
			}
			from := deref(v[3])
			if from == "" {
				from = deref(v[4])
			}
			data, _ := json.Marshal(map[string]any{"mail": id, "from": from, "cues": cues})
			first, err := Observe(db, Observation{Detector: DetMail, Subject: fmt.Sprintf("mail:%d", id), Link: fmt.Sprintf("mail:%d", id),
				Title: "this mail leans on pressure",
				Body:  fmt.Sprintf("This mail from %s uses %d kinds of pressure: %s. They are named, not judged: the offer may be real and the deadline true. It is easier to decide once you have seen them.", clip(from, 80), len(cues), joinWords(parts)),
				Data:  string(data), Score: float64(len(cues)), Threshold: th}, now)
			if err != nil {
				return marked, err
			}
			if first {
				marked++
			}
		}
		mark.Upto = id
	}
	return marked, setDaemonState(db, "mail", mark, now)
}

// MemoryAge: of the memories the box brought to chats in the last thirty days (kept on each
// answer's row since 21 November 2026), the share more than a year old, when newer ones exist.
func MemoryAge(db DB, now time.Time) (bool, error) {
	rows, err := db.Query("SELECT given FROM chat_messages WHERE role = 'assistant' AND ts >= $1 AND given <> '' ORDER BY id DESC LIMIT 2000", now.Add(-30*day).UnixMilli())
	if err != nil {
		return false, err
	}
	total, old := 0, 0
	for _, v := range rows.Vals {
		var g struct {
			Items []struct {
				Source string `json:"source"`
				When   string `json:"when"`
			} `json:"items"`
		}
		if json.Unmarshal([]byte(deref(v[0])), &g) != nil {
			continue
		}
		for _, it := range g.Items {
			if it.Source != "memory" || len(it.When) < 10 {
				continue
			}
			t, err := time.Parse("2006-01-02", it.When[:10])
			if err != nil {
				continue
			}
			total++
			if now.Sub(t) > 365*day {
				old++
			}
		}
	}
	th := Threshold(db, DetMemoryAge)
	if total < 20 || float64(old)/float64(total) < th {
		return false, nil
	}
	recent := count(db, "SELECT count(*) FROM memories WHERE NOT tombstoned AND created_at >= $1", now.Add(-90*day).UnixMilli())
	if recent < 5 {
		return false, nil // it brings old memories because old ones are what it has
	}
	data, _ := json.Marshal(map[string]any{"brought": total, "olderThanAYear": old, "heldFromLastThreeMonths": recent})
	return Observe(db, Observation{Detector: DetMemoryAge, Subject: "month:" + month(now), Title: "the box is answering from an older you",
		Body: fmt.Sprintf("Of the %d memories the box brought to your chats in the last thirty days, %d are more than a year old, though it holds %d from the last three months. It is drawing on who you were more than on who you are becoming, and an answer built on that can keep you there.", total, old, recent),
		Data: string(data), Score: float64(old) / float64(total), Threshold: th}, now)
}

// Agreement: how often, in the arbiter's record of the last thirty days, the box's answer to a
// question of judgement sided with the asker. Eight or more read.
func Agreement(db DB, now time.Time) (bool, error) {
	since := now.Add(-30 * day).Unix()
	n := count(db, "SELECT count(*) FROM shadow_reads WHERE ts >= $1 AND kind IN ('judgement', 'personal') AND sided IN ('yes', 'no')", since)
	yes := count(db, "SELECT count(*) FROM shadow_reads WHERE ts >= $1 AND kind IN ('judgement', 'personal') AND sided = 'yes'", since)
	th := Threshold(db, DetAgreement)
	if n < 8 || float64(yes)/float64(n) < th {
		return false, nil
	}
	// THE OTHER SIDE, from what is already held: the latest of those answers with what the cold
	// read said to the same question. Material from outside the frame the box has learned.
	type other struct {
		Chat     int64  `json:"chat"`
		Msg      int64  `json:"msg"`
		Question string `json:"question"`
		Cold     string `json:"cold"`
	}
	var others []other
	if rows, err := db.Query(`SELECT r.chat_id, r.msg_id, r.cold,
		COALESCE((SELECT content FROM chat_messages q WHERE q.chat_id = r.chat_id AND q.role = 'user' AND q.id < r.msg_id ORDER BY q.id DESC LIMIT 1), '')
		FROM shadow_reads r WHERE r.ts >= $1 AND r.kind IN ('judgement', 'personal') AND r.sided = 'yes' AND r.cold <> '' ORDER BY r.ts DESC LIMIT 3`, since); err == nil {
		for i, v := range rows.Vals {
			if len(v) < 4 {
				continue
			}
			others = append(others, other{Chat: int64At(rows.Vals, i, 0), Msg: int64At(rows.Vals, i, 1), Cold: clip(deref(v[2]), 600), Question: clip(strings.Join(strings.Fields(deref(v[3])), " "), 200)})
		}
	}
	body := fmt.Sprintf("In the last thirty days the arbiter read %d of the box's answers to questions of judgement. In %d of them the box sided with you. Either you were right %d times in %d, or it has learned which answers you take well. The record cannot tell those apart. Someone who sometimes disagrees with you can.", n, yes, yes, n)
	if len(others) > 0 {
		body += " The latest of them, with what the same question got when asked with nothing about you:"
		for _, o := range others {
			body += fmt.Sprintf("\n\nYou asked: “%s”\nRead cold: %s", o.Question, o.Cold)
		}
	}
	data, _ := json.Marshal(map[string]any{"read": n, "sidedWithYou": yes, "otherSide": others})
	return Observe(db, Observation{Detector: DetAgreement, Subject: "month:" + month(now), Title: "the box nearly always sides with you",
		Body: body, Data: string(data), Score: float64(yes) / float64(n), Threshold: th}, now)
}

// Praise: how many of the box's answers of the last thirty days open by praising the person or
// the question. Twenty answers or more.
func Praise(db DB, now time.Time) (bool, error) {
	rows, err := db.Query("SELECT left(content, 200) FROM chat_messages WHERE role = 'assistant' AND state = 'done' AND content <> '' AND ts >= $1 ORDER BY id DESC LIMIT 2000", now.Add(-30*day).UnixMilli())
	if err != nil {
		return false, err
	}
	n, praised, example := len(rows.Vals), 0, ""
	for _, v := range rows.Vals {
		if OpensWithPraise(deref(v[0])) {
			praised++
			if example == "" {
				example = clip(Sentences(deref(v[0]))[0], 80)
			}
		}
	}
	th := Threshold(db, DetPraise)
	if n < 20 || float64(praised)/float64(n) < th {
		return false, nil
	}
	data, _ := json.Marshal(map[string]any{"answers": n, "openWithPraise": praised, "example": example})
	return Observe(db, Observation{Detector: DetPraise, Subject: "month:" + month(now), Title: "the box keeps opening with praise",
		Body: fmt.Sprintf("%d of the box's last %d answers began by praising you or your question (“%s”). Praise at the top of an answer costs a model nothing, and it is not evidence that the question was good or that you were right.", praised, n, example),
		Data: string(data), Score: float64(praised) / float64(n), Threshold: th}, now)
}

// Detect runs every detector that counts, and the settings' own two (the reset, the capture).
// One failing does not stop the rest; the first error is returned for the log.
func Detect(db DB, now time.Time, lg *slog.Logger) error {
	var first error
	note := func(what string, err error) {
		if err != nil {
			lg.Warn("detector failed", "fn", "Detect", "detector", what, "err", err)
			if first == nil {
				first = err
			}
		}
	}
	_, err := ResetIfDue(db, now)
	note("reset", err)
	_, err = Capture(db, now)
	note(DetCapture, err)
	_, err = Interaction(db, now)
	note(DetInteraction, err)
	_, err = Sessions(db, now)
	note(DetSessions, err)
	note("own-words", OwnWords(db, now))
	_, err = Mail(db, now)
	note(DetMail, err)
	_, err = MemoryAge(db, now)
	note(DetMemoryAge, err)
	_, err = Agreement(db, now)
	note(DetAgreement, err)
	_, err = Praise(db, now)
	note(DetPraise, err)
	return first
}
