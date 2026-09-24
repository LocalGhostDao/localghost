package main

// GROUNDING , what the box adds to a question before the model sees it, beyond the archive search:
//
//   - the person's own memories, matched on the words that carry meaning (a question's "the",
//     "what" and "did" used to match every memory the box had);
//   - when the question asks for a recommendation or a plan ("what should I do today", "anywhere
//     good for a swim near here"), the TASTE , what the photos say the person likes , and, when
//     the phone sent where it is, the places around it that fit (internal/outings.Rank over the
//     box's own GeoNames spots);
//   - a BUDGET: the context is cut to what the model can read in about twenty seconds at the
//     prefill speed oracled has measured, so a question on a CPU-only box still starts answering
//     in a reasonable time instead of spending two minutes reading eight web pages first. The
//     phone is told how long the reading will take, so the wait is explained, not dead air.

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/outings"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// hereT is the phone's last fix, sent with a question so "near here" means somewhere.
type hereT struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

func (h *hereT) valid() bool {
	return h != nil && h.Lat >= -90 && h.Lat <= 90 && h.Lon >= -180 && h.Lon <= 180 && !(h.Lat == 0 && h.Lon == 0)
}

// stopwords are the words a memory match must not hang on: they appear in every memory, so they
// select nothing. English only , the questions are English; a Greek phrase simply matches less.
var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`the and for you your yours are was were what when where which who whom whose why how
		did does doing done has have had having can could would should will shall may might must not
		but with from into onto about above after again against all any also been before being below
		between both each few more most other some such than that them then there these they this those
		through too under until very our ours out over own same she her hers him his its it's i'm i've
		just like get got give tell show find know think want need make made much many one two lot lots
		please thanks thank okay yes any anything something everything nothing here now today tonight
		ghost box me my mine myself we us`) {
		m[w] = true
	}
	return m
}()

// memoryTerms are the question's words worth matching a memory on: lowercase, trimmed of
// punctuation, three letters or more, not a stopword, deduplicated, at most six.
func memoryTerms(prompt string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(prompt)) {
		w = strings.Trim(w, ".,!?\"'()[]:;“”‘’")
		if len([]rune(w)) < 3 || stopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if len(out) == 6 {
			break
		}
	}
	return out
}

// wantsTaste is whether the question asks for a suggestion, a plan, or somewhere to go , the
// questions the person's own taste should shape.
var tasteAsk = regexp.MustCompile(`(?i)\b(recommend\w*|suggest\w*|should i|shall i|what (can|could|to|should) (i|we) do|where (can|could|should|to) (i|we)|what to (do|see|visit)|things to do|worth (a )?(visit|seeing|going)|ideas?|plan\w*|day out|weekend|trip|explore|visit|go (to|for)|near(by| me| here)?|around here|close by|fun|swim\w*|hike|hiking|walk|beach\w*|eat|lunch|dinner|i('d)? like|i love|my favou?rite|what do i like|my taste)\b`)

func wantsTaste(prompt string) bool { return tasteAsk.MatchString(prompt) }

// tasteItems are the taste line and, with a position, the places near it that fit.
func tasteItems(db *poltergres.ReadWrite, prompt string, here *hereT) []ctxItem {
	if db == nil || !wantsTaste(prompt) {
		return nil
	}
	rows, err := db.Query("SELECT value FROM settings WHERE key = 'synthd_taste'")
	if err != nil || len(rows.Vals) != 1 || rows.Vals[0][0] == nil {
		return nil
	}
	var taste outings.Taste
	if json.Unmarshal([]byte(*rows.Vals[0][0]), &taste) != nil || len(taste.Likes) == 0 {
		return nil
	}
	out := []ctxItem{{Source: "taste", Snippet: taste.Summary, Why: "what your photos say you like: the tags on the days you had a camera out"}}
	if !here.valid() || len(taste.Interests) == 0 {
		return out
	}
	spots, err := hw.QuerySpotsNear(db, here.Lat, here.Lon, 15)
	if err != nil || len(spots) == 0 {
		return out
	}
	cells, _ := hw.QueryPhotoCellsNear(db, here.Lat, here.Lon, 15)
	for i, s := range outings.Rank(spots, taste, here.Lat, here.Lon, 15, hw.PhotosNearFunc(cells)) {
		if i == 4 {
			break
		}
		been := "new to you"
		if s.BeenThere > 0 {
			been = fmt.Sprintf("you have %d photos there", s.BeenThere)
		}
		out = append(out, ctxItem{Source: "nearby",
			Snippet: fmt.Sprintf("%s (%s, %.1f km %s from where you are) , %s", s.Name, s.Kind, s.DistanceKm, s.Bearing, been),
			Why:     "near your phone's last position, and it fits what you photograph: " + s.Why})
	}
	return out
}

// --- the budget ---

// engineSpeed is what oracled has measured: prompt tokens read per second, and whether the model
// is on the GPU. Cached for a minute; a box whose oracled does not answer is assumed to be slow.
type engineSpeed struct {
	PromptTPS float64
	GenTPS    float64
	OnGPU     bool
	Known     bool
	at        time.Time
}

var (
	speedMu    sync.Mutex
	speedCache engineSpeed
)

func currentSpeed(runDir string) engineSpeed {
	speedMu.Lock()
	defer speedMu.Unlock()
	if !speedCache.at.IsZero() && time.Since(speedCache.at) < time.Minute {
		return speedCache
	}
	sp := engineSpeed{at: time.Now()}
	c := ctlsock.NewClientTimeout("ghost.oracled", runDir, 1500*time.Millisecond)
	if resp, err := c.Call("models", nil); err == nil && resp.OK {
		var m struct {
			OnGPU bool `json:"onGPU"`
			Stats struct {
				TokPerSecAvg    float64 `json:"tokPerSecAvg"`
				PromptTokPerSec float64 `json:"promptTokPerSec"`
			} `json:"stats"`
		}
		if json.Unmarshal(resp.Data, &m) == nil {
			sp.OnGPU = m.OnGPU
			sp.PromptTPS = m.Stats.PromptTokPerSec
			sp.GenTPS = m.Stats.TokPerSecAvg
			sp.Known = true
		}
	}
	speedCache = sp
	return sp
}

const (
	readTargetS   = 20.0 // what a question may spend reading its context before the first word
	cpuPromptTPS  = 40.0 // a CPU-only 4B model, prefill, when nothing was measured yet
	gpuPromptTPS  = 1500.0
	charsPerToken = 4.0
	minWebBudget  = 1800 // below this the web block is not worth sending at all; never cut under it
)

// promptTPS is the measured prefill speed, or the default for where the model runs.
func (s engineSpeed) promptTPS() float64 {
	if s.PromptTPS > 0 {
		return s.PromptTPS
	}
	if s.OnGPU {
		return gpuPromptTPS
	}
	return cpuPromptTPS
}

// contextBudget is how many characters of context the model can read in readTargetS.
func (s engineSpeed) contextBudget() int {
	return int(s.promptTPS() * readTargetS * charsPerToken)
}

// readSeconds is how long the model will take to read n characters.
func (s engineSpeed) readSeconds(n int) float64 {
	return float64(n) / charsPerToken / s.promptTPS()
}

// fitWeb trims the phone's findings to budget characters of excerpt and snippet: the lowest-ranked
// hits lose their page text first (title, URL and search snippet stay, so the source is still
// citable), then every excerpt is shortened evenly. Returns the hits and whether anything was cut.
func fitWeb(hits []webHit, budget int) ([]webHit, bool) {
	if budget < minWebBudget {
		budget = minWebBudget
	}
	size := func(hs []webHit) int {
		n := 0
		for _, h := range hs {
			n += len(h.Title) + len(h.URL) + len(h.Snippet) + len(h.Excerpt) + 24
		}
		return n
	}
	if size(hits) <= budget {
		return hits, false
	}
	out := make([]webHit, len(hits))
	copy(out, hits)
	// drop excerpts from the bottom up, keeping the top two's page text as long as possible
	for i := len(out) - 1; i >= 2 && size(out) > budget; i-- {
		out[i].Excerpt = ""
	}
	if size(out) > budget {
		// shorten what is left evenly
		var withEx int
		for _, h := range out {
			if h.Excerpt != "" {
				withEx++
			}
		}
		if withEx > 0 {
			over := size(out) - budget
			cut := int(math.Ceil(float64(over) / float64(withEx)))
			for i := range out {
				if out[i].Excerpt == "" {
					continue
				}
				keep := len(out[i].Excerpt) - cut
				if keep < 200 {
					keep = 200
				}
				if keep < len(out[i].Excerpt) {
					out[i].Excerpt = cutAt(out[i].Excerpt, keep) + "…"
				}
			}
		}
	}
	// last resort: fewer hits (never below three)
	for len(out) > 3 && size(out) > budget {
		out = out[:len(out)-1]
	}
	return out, true
}

// readingNote is the line the phone shows while the model reads, when the read is long enough to
// notice; "" otherwise.
func readingNote(s engineSpeed, promptChars int, trimmed bool) string {
	secs := s.readSeconds(promptChars)
	where := "on the GPU"
	if !s.OnGPU {
		where = "on the CPU (no GPU right now)"
	}
	note := ""
	if secs >= 8 {
		note = fmt.Sprintf("reading %d words of context %s , about %.0fs before the first word", promptChars/6, where, math.Round(secs/5)*5)
	}
	if trimmed {
		if note == "" {
			note = "web pages shortened to keep the answer quick"
		} else {
			note += " · web pages shortened to keep it there"
		}
	}
	return note
}

// cutAt is s cut to at most n bytes on a rune boundary (a web page is anything but ASCII).
func cutAt(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
