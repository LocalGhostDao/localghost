package secd

// WHAT A DAEMON DID, for the Box Status drill-ins. The database says how far the archive is (the
// rows under these); only the daemon knows how much it did in the last hour. ghost.framed and
// ghost.searchd answer `work` on their control sockets (internal/workcount), and host.gpu gets
// what the box uses the card for from oracled's `models`. Each is asked with a two-second bound:
// the screen never waits on a daemon.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/gpu"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/voiced"
	"github.com/LocalGhostDao/localghost/server/internal/workcount"
)

// workLabels names each daemon's kinds in the order the drill-in lists them; a kind not named
// here is listed after, under its own name.
var workLabels = map[string][][2]string{
	"ghost.framed": {
		{"archived photo", "photos archived"},
		{"archived video", "videos archived"},
		{"archived unknown", "other files archived"},
		{"previews", "previews made"},
		{"duplicates", "duplicates dropped"},
		{"empty", "empty uploads skipped"},
		{"failed", "uploads failed (kept in the spool)"},
		{"re-derived", "re-read by the stock-take"},
		{"track points", "track points stored"},
		{"days redrawn", "days redrawn"},
	},
	"ghost.searchd": {
		{"caption", "photos described"},
		{"tag", "photos named and tagged"},
		{"categorize", "tags given a category"},
		{"chunks embedded", "chunks embedded"},
		{"embed_text", "embed batches"},
		{"ingested", "items ingested"},
		{"stock-take checks", "stock-take checks"},
		{"searches", "searches answered"},
		{"caption failed", "descriptions failed"},
		{"caption too large", "photos too large to describe"},
		{"tag failed", "tag passes failed"},
		{"categorize failed", "categorize failed"},
		{"embed_text failed", "embed batches failed"},
	},
	// a question asked aloud leaves no row anywhere (it is heard and forgotten), so what the
	// daemon counted since it started is the only account of one
	"ghost.voiced": {
		{"question heard", "questions asked aloud, heard"},
		{"question heard again", "heard again in your own language"},
		{"question no words", "questions with no words in them"},
		{"question no sound", "questions with no sound (not given to whisper)"},
		{"question failed", "questions that failed"},
		{"note transcribed", "voice notes transcribed"},
		{"note set aside", "a voice note set aside for a question"},
		{"note failed", "voice notes failed"},
	},
}

// workRows asks service for its `work` and turns the answer into rows: when the count began,
// then one row per kind, then what waits and what rests.
func workRows(service, runDir string) []hw.DaemonKV {
	c := ctlsock.NewClientTimeout(service, runDir, 2*time.Second)
	resp, err := c.Call("work", nil)
	if err != nil || !resp.OK {
		return []hw.DaemonKV{{K: "work", V: "not answering (a build before the work counts, or busy) , the counts below are from the database"}}
	}
	var m struct {
		Work    workcount.Snapshot `json:"work"`
		Waiting struct {
			Uploads         int `json:"uploads"`
			LocationBatches int `json:"locationBatches"`
		} `json:"waiting"`
		ModelLanes struct {
			Resting bool
			Why     string
		} `json:"modelLanes"`
		CaptionLane struct {
			Resting bool
			Why     string
		} `json:"captionLane"`
		Asks voiced.Asks `json:"asks"` // ghost.voiced: how long a question asked aloud takes
	}
	if json.Unmarshal(resp.Data, &m) != nil {
		return []hw.DaemonKV{{K: "work", V: "answered something this build does not read"}}
	}
	return formatWork(service, m.Work, time.Now(), func(rows []hw.DaemonKV) []hw.DaemonKV {
		if service == "ghost.framed" {
			rows = append(rows, hw.DaemonKV{K: "waiting in the spool", V: fmt.Sprintf("%d uploads · %d location batches", m.Waiting.Uploads, m.Waiting.LocationBatches), Key: true})
		}
		if m.ModelLanes.Resting {
			rows = append(rows, hw.DaemonKV{K: "model work paused", V: m.ModelLanes.Why, Key: true})
		}
		if m.CaptionLane.Resting {
			rows = append(rows, hw.DaemonKV{K: "descriptions paused", V: m.CaptionLane.Why, Key: true})
		}
		if service == "ghost.voiced" {
			rows = append(rows, askRows(m.Asks, time.Now())...)
		}
		return rows
	})
}

// askRows is how long a question asked aloud takes on this box: the last one, and the mean of
// the last twenty (pure, for the tests). Nothing until one has been asked since the daemon started.
func askRows(a voiced.Asks, now time.Time) []hw.DaemonKV {
	if a.N == 0 || a.Last == nil {
		return []hw.DaemonKV{{K: "a question takes", V: "none asked aloud since the daemon started"}}
	}
	sec := func(ms int64) string { return fmt.Sprintf("%.1f s", float64(ms)/1000) }
	last := sec(a.Last.AudioMS) + " of speech · heard in " + sec(a.Last.TookMS)
	if a.Last.Lang != "" {
		last += " · " + a.Last.Lang
	}
	if a.Last.Again {
		last += " (heard twice: the first guess at the language was not one of yours)"
	}
	if a.Last.Words == 0 {
		last += " · no words in it"
	}
	last += " · " + span(now.Sub(time.Unix(a.Last.At, 0))) + " ago"
	rows := []hw.DaemonKV{{K: "the last question", V: last, Key: true}}
	if a.Over > 1 {
		rows = append(rows, hw.DaemonKV{K: "a question takes", V: fmt.Sprintf("%s on average over the last %d", sec(a.AvgTookMS), a.Over), Key: true})
	}
	return rows
}

// formatWork is the rows for one snapshot (pure, for the tests).
func formatWork(service string, w workcount.Snapshot, now time.Time, tail func([]hw.DaemonKV) []hw.DaemonKV) []hw.DaemonKV {
	up := now.Sub(time.Unix(w.Since, 0))
	rows := []hw.DaemonKV{{K: "counting since", V: "the daemon started, " + span(up) + " ago (an unlock starts the count again)"}}
	byKind := map[string]workcount.Kind{}
	for _, k := range w.Kinds {
		byKind[k.Kind] = k
	}
	line := func(k workcount.Kind) string {
		s := fmt.Sprintf("%s last hour", thousands(k.Hour))
		if up > 24*time.Hour {
			s += fmt.Sprintf(" · %s last 24 h · %s since start", thousands(k.Day), thousands(k.Total))
		} else {
			s += fmt.Sprintf(" · %s since start", thousands(k.Total))
		}
		if k.LastAt > 0 {
			s += " · last " + span(now.Sub(time.Unix(k.LastAt, 0))) + " ago"
		}
		return s
	}
	seen := map[string]bool{}
	for _, l := range workLabels[service] {
		if k, ok := byKind[l[0]]; ok {
			rows = append(rows, hw.DaemonKV{K: l[1], V: line(k), Key: true})
			seen[l[0]] = true
		}
	}
	for _, k := range w.Kinds {
		if !seen[k.Kind] {
			rows = append(rows, hw.DaemonKV{K: k.Kind, V: line(k), Key: true})
		}
	}
	if len(w.Kinds) == 0 {
		rows = append(rows, hw.DaemonKV{K: "done", V: "nothing yet since it started", Key: true})
	}
	if tail != nil {
		rows = tail(rows)
	}
	return rows
}

// span is a duration the way a person says it: 40 s, 12 min, 3 h 10 min, 2 days 4 h.
func span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		h := int(d.Hours())
		return fmt.Sprintf("%d h %d min", h, int(d.Minutes())-h*60)
	default:
		days := int(d.Hours()) / 24
		return fmt.Sprintf("%d days %d h", days, int(d.Hours())-days*24)
	}
}

// thousands writes 12345 as 12,345.
func thousands(n int64) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return "-" + thousands(-n)
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// gpuUseRows is what the box does with the card, from oracled's `models`: the model on it and how
// much of the card it holds, whether it sees photos, and what runs on the CPU by choice.
func gpuUseRows(runDir string) []hw.DaemonKV {
	c := ctlsock.NewClientTimeout("ghost.oracled", runDir, 2*time.Second)
	resp, err := c.Call("models", nil)
	if err != nil || !resp.OK {
		return []hw.DaemonKV{{K: "used for", V: "oracled not answering , see ghost.oracled"}}
	}
	var m struct {
		Model     string `json:"local-small"`
		Ready     bool   `json:"ready"`
		OnGPU     bool   `json:"onGPU"`
		Vision    *bool  `json:"vision"`
		VisionWhy string `json:"visionWhy"`
		Engine    struct {
			GPUMiB float64 `json:"gpuMiB"`
		} `json:"engine"`
		Arbiter *oracle.ArbiterInfo `json:"arbiter"`
	}
	if json.Unmarshal(resp.Data, &m) != nil {
		return nil
	}
	arbiter := ""
	if m.Arbiter != nil && m.Arbiter.Has {
		arbiter = m.Arbiter.Model
	}
	return formatGPUUse(m.Model, m.Ready, m.OnGPU, m.Vision, m.VisionWhy, m.Engine.GPUMiB, gpu.CapsNow().MemoryMiB, arbiter)
}

// arbiterRows is the arbiter's own model as ghost.oracled's drill-in says it (pure, for the
// tests): which model, where it runs, whether it is up and what it has done; or that the box
// has none and which model reads cold instead. nil for an oracled from before it said.
func arbiterRows(a *oracle.ArbiterInfo) []hw.DaemonKV {
	if a == nil {
		return nil
	}
	if !a.Has {
		return []hw.DaemonKV{{K: "the arbiter's model", V: "none on this box: ghost.shadowd's cold read is by " + orWord(a.Main, "the main model") +
			", with nothing about you. A second model at ai-models/arbiter-<name>.gguf makes it a second opinion (docs/SHADOW.md)"}}
	}
	if !a.Up {
		return []hw.DaemonKV{
			{K: "the arbiter's model", V: a.Model + " · NOT up: " + orWord(a.Why, "starting"), Key: true},
			{K: "the cold read is by", V: a.ServedBy},
		}
	}
	rows := []hw.DaemonKV{{K: "the arbiter's model", V: a.Model + " · up, on the CPU beside " + orWord(a.Main, "the main model") + ", in a queue of its own", Key: true}}
	if a.Answers > 0 {
		rows = append(rows, hw.DaemonKV{K: "it has answered", V: fmt.Sprintf("%d since start · %.1f tok/s · last %s", a.Answers, a.TokensPerSecond, a.LastAnswerAt)})
	} else {
		rows = append(rows, hw.DaemonKV{K: "it has answered", V: "nothing yet since it started"})
	}
	return rows
}

func orWord(s, otherwise string) string {
	if strings.TrimSpace(s) == "" {
		return otherwise
	}
	return s
}

// readByRows is which model ghost.shadowd's cold read is by, for its own drill-in.
func readByRows(runDir string) []hw.DaemonKV {
	a, err := oracle.NewClient(runDir, 2*time.Second).Arbiter()
	if err != nil {
		return []hw.DaemonKV{{K: "the cold read is by", V: "unknown: ghost.oracled is not answering"}}
	}
	switch {
	case a.Has && a.Up:
		return []hw.DaemonKV{{K: "the cold read is by", V: a.Model + ", a second model beside " + orWord(a.Main, "the main one"), Key: true}}
	case a.Has:
		return []hw.DaemonKV{{K: "the cold read is by", V: a.ServedBy + " (" + a.Model + " is not up: " + orWord(a.Why, "starting") + ")", Key: true}}
	}
	return []hw.DaemonKV{{K: "the cold read is by", V: orWord(a.Main, "the main model") + ", the box's one model, with nothing about you (not a second opinion: docs/SHADOW.md)", Key: true}}
}

// formatGPUUse is the rows (pure, for the tests). vision is nil for an oracled that does not say.
func formatGPUUse(model string, ready, onGPU bool, vision *bool, visionWhy string, gpuMiB, totalMiB float64, arbiter string) []hw.DaemonKV {
	var rows []hw.DaemonKV
	state := "loading"
	switch {
	case ready && onGPU:
		state = "on the card"
	case ready:
		state = "running, but NOT on the card (see ghost.oracled)"
	}
	use := model + " · " + state
	if gpuMiB > 0 {
		use += fmt.Sprintf(" · holds %.1f GB", gpuMiB/1024)
		if totalMiB > 0 {
			use += fmt.Sprintf(" of %.1f GB", totalMiB/1024)
		}
	}
	rows = append(rows, hw.DaemonKV{K: "model", V: use, Key: true})
	rows = append(rows, hw.DaemonKV{K: "used for", V: "chat, photo names and tags, tag categories, day stories, trail and web answers"})
	if vision != nil {
		if *vision {
			rows = append(rows, hw.DaemonKV{K: "sees photos", V: "yes (the projector is loaded): photos are described"})
		} else {
			rows = append(rows, hw.DaemonKV{K: "sees photos", V: "NO , " + visionWhy, Key: true})
		}
	}
	byChoice := "search embeddings and voice transcription"
	if arbiter != "" {
		byChoice += ", and the arbiter's own model (" + arbiter + ")"
	}
	rows = append(rows, hw.DaemonKV{K: "on the CPU by choice", V: byChoice + " (the card's memory is the chat model's)"})
	return rows
}
