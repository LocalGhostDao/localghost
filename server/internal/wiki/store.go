package wiki

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// THE STORE. The box's Wikipedia lives in two tables once the file is imported: wiki_articles (the
// title, the lead and the body as plain text, whether the page is one of meanings) and
// wiki_redirects (a redirect's title pointing at its article). The import reads the file's entries
// in path order, a slice at a time under a time budget, and keeps where it is in settings, so a
// restart carries on and a new file (another edition) starts over. Then every lookup is SQL:
// case-blind on the title, a qualified place without its comma, a prefix, a likeness when pg_trgm
// is installed, and full-text search over the title and the lead. The file is never read at a
// question; once imported it can go.

// ImportState is how far the import is, and for which file.
type ImportState struct {
	File      string `json:"file"`            // the file's name
	Size      int64  `json:"size"`            // and size: another file starts over
	Edition   string `json:"edition"`         // "Wikipedia, 2026-06"
	Next      uint32 `json:"next"`            // the next entry to read
	Total     uint32 `json:"total"`           // the file's entry count
	Articles  int64  `json:"articles"`        // written so far
	Redirects int64  `json:"redirects"`       // written so far
	Skipped   int64  `json:"skipped"`         // entries that would not read (said in the log, not fatal)
	Done      bool   `json:"done"`            // every entry read
	Removed   bool   `json:"removed"`         // the file was removed after the import
	At        int64  `json:"at"`              // when the state was last written
	StartedAt int64  `json:"startedAt"`       // when this file's import began
	Error     string `json:"error,omitempty"` // the last error that stopped a slice
}

// ImportKey is the settings key the state is kept under.
const ImportKey = "synthd_wiki_import"

const (
	LeadMax = 1500  // characters of lead kept
	BodyMax = 60000 // characters of body kept
)

// Store is the box's Wikipedia over a database connection.
type Store struct {
	DB   *poltergres.ReadWrite
	trgm int // 0 unknown, 1 pg_trgm is there, -1 it is not
}

// State reads the import's state; a zero state when none was written.
func (s *Store) State() ImportState {
	var st ImportState
	rows, err := s.DB.Query("SELECT value FROM settings WHERE key = $1", ImportKey)
	if err == nil && len(rows.Vals) == 1 && len(rows.Vals[0]) == 1 && rows.Vals[0][0] != nil {
		_ = json.Unmarshal([]byte(*rows.Vals[0][0]), &st)
	}
	return st
}

// Save writes the state.
func (s *Store) Save(st ImportState) error {
	st.At = time.Now().Unix()
	b, _ := json.Marshal(st)
	return s.DB.Exec("INSERT INTO settings (key, value) VALUES ($1,$2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", ImportKey, string(b))
}

// Ready says whether the store answers: an import finished, with articles in it.
func (s *Store) Ready() (ImportState, bool) {
	st := s.State()
	return st, st.Done && st.Articles > 0
}

// Current says whether the import's state is this file's (done or under way).
func (s *Store) Current(w *Wiki) bool {
	st := s.State()
	return st.File == filepath.Base(w.Path) && st.Size == w.Size()
}

const (
	importArticleBatch  = 40  // articles an INSERT carries (bodies are kilobytes)
	importRedirectBatch = 500 // redirects an INSERT carries
	importSaveEvery     = 20000
)

// Import reads the file from where the state left off, for the time given at most, into the tables;
// the state comes back and is saved. A state for another file (or an older size of it) starts over:
// the tables are emptied and the file is read from its first entry. An entry whose content will not
// read is skipped and counted, never fatal: a single bad cluster must not stop Wikipedia.
func (s *Store) Import(w *Wiki, budget time.Duration) (ImportState, error) {
	st := s.State()
	name, size := filepath.Base(w.Path), w.Size()
	if st.File != name || st.Size != size {
		if err := s.DB.Exec("DELETE FROM wiki_articles"); err != nil {
			return st, err
		}
		if err := s.DB.Exec("DELETE FROM wiki_redirects"); err != nil {
			return st, err
		}
		st = ImportState{File: name, Size: size, Edition: w.Name, Total: w.Entries(), StartedAt: time.Now().Unix()}
	}
	st.Error = ""
	if st.Done {
		return st, s.Save(st)
	}
	deadline := time.Now().Add(budget)
	var arts, reds []string
	var artArgs, redArgs []any
	flush := func() error {
		if len(arts) > 0 {
			q := "INSERT INTO wiki_articles (idx, title, title_lc, lead, body, disamb) VALUES " + strings.Join(arts, ",") + " ON CONFLICT (idx) DO NOTHING"
			if err := s.DB.Exec(q, artArgs...); err != nil {
				return err
			}
			arts, artArgs = arts[:0], artArgs[:0]
		}
		if len(reds) > 0 {
			q := "INSERT INTO wiki_redirects (title_lc, idx) VALUES " + strings.Join(reds, ",") + " ON CONFLICT (title_lc, idx) DO NOTHING"
			if err := s.DB.Exec(q, redArgs...); err != nil {
				return err
			}
			reds, redArgs = reds[:0], redArgs[:0]
		}
		return nil
	}
	for st.Next < st.Total {
		e, err := w.z.EntryAt(st.Next)
		st.Next++
		if err != nil {
			st.Skipped++
			continue
		}
		if e.Namespace != w.ns || e.Title == "" {
			continue
		}
		switch {
		case e.Redirect:
			t, err := w.z.Resolve(e)
			if err != nil || !strings.HasPrefix(t.Mime, "text/html") {
				st.Skipped++
				continue
			}
			n := len(redArgs)
			reds = append(reds, "($"+strconv.Itoa(n+1)+",$"+strconv.Itoa(n+2)+")")
			redArgs = append(redArgs, strings.ToLower(e.Title), int64(t.Index))
			st.Redirects++
			if len(reds) >= importRedirectBatch {
				if err := flush(); err != nil {
					st.Error = err.Error()
					_ = s.Save(st)
					return st, err
				}
			}
		case strings.HasPrefix(e.Mime, "text/html"):
			body, err := w.z.Content(e)
			if err != nil {
				st.Skipped++
				continue
			}
			page := string(body)
			lead := Lead(page, LeadMax)
			if lead == "" {
				st.Skipped++
				continue
			}
			n := len(artArgs)
			arts = append(arts, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d)", n+1, n+2, n+3, n+4, n+5, n+6))
			artArgs = append(artArgs, int64(e.Index), e.Title, strings.ToLower(e.Title), lead, Body(page, BodyMax), disamb(lead))
			st.Articles++
			if len(arts) >= importArticleBatch {
				if err := flush(); err != nil {
					st.Error = err.Error()
					_ = s.Save(st)
					return st, err
				}
			}
		}
		if st.Next%importSaveEvery == 0 {
			if err := flush(); err != nil {
				st.Error = err.Error()
				_ = s.Save(st)
				return st, err
			}
			if err := s.Save(st); err != nil {
				return st, err
			}
			if time.Now().After(deadline) {
				return st, nil
			}
		}
	}
	if err := flush(); err != nil {
		st.Error = err.Error()
		_ = s.Save(st)
		return st, err
	}
	st.Done = true
	return st, s.Save(st)
}

// Hit is one article a lookup found.
type Hit struct {
	Idx    uint32 `json:"idx"`
	Title  string `json:"title"`
	Lead   string `json:"lead"`
	Disamb bool   `json:"disamb,omitempty"`
	How    string `json:"how"` // exact | redirect | qualified | prefix | like | text
}

// Lookup finds the articles a phrase names, n at most, each once, the surest first: the title in
// any case, a redirect of that name, a qualified place written without its comma ("greenwich
// london"), titles starting with it, titles like it (pg_trgm), and last the words in the titles
// and leads (full-text search). Leads come cut to maxLead.
func (s *Store) Lookup(q string, n, maxLead int) ([]Hit, error) {
	lq := strings.ToLower(strings.Join(strings.Fields(q), " "))
	if lq == "" || n <= 0 {
		return nil, nil
	}
	var out []Hit
	seen := map[uint32]bool{}
	take := func(query, how string, args ...any) error {
		if len(out) >= n {
			return nil
		}
		rows, err := s.DB.Query(query, args...)
		if err != nil {
			return err
		}
		for _, v := range rows.Vals {
			if len(out) >= n {
				break
			}
			if len(v) < 4 || v[0] == nil || v[1] == nil {
				continue
			}
			idx, _ := strconv.ParseUint(*v[0], 10, 32)
			if seen[uint32(idx)] {
				continue
			}
			seen[uint32(idx)] = true
			h := Hit{Idx: uint32(idx), Title: *v[1], How: how}
			if v[2] != nil {
				h.Lead = cutAt(*v[2], maxLead)
			}
			h.Disamb = v[3] != nil && *v[3] == "t"
			out = append(out, h)
		}
		return nil
	}
	const cols = "SELECT a.idx, a.title, a.lead, a.disamb FROM wiki_articles a"
	if err := take(cols+" WHERE a.title_lc = $1 ORDER BY a.disamb, a.idx LIMIT 4", "exact", lq); err != nil {
		return out, err
	}
	if err := take(cols+" JOIN wiki_redirects r ON r.idx = a.idx WHERE r.title_lc = $1 ORDER BY a.disamb, a.idx LIMIT 4", "redirect", lq); err != nil {
		return out, err
	}
	if words := strings.Fields(lq); len(words) >= 2 && len(out) < n {
		head, last := strings.Join(words[:len(words)-1], " "), words[len(words)-1]
		if err := take(cols+" WHERE a.title_lc IN ($1, $2) ORDER BY a.disamb, a.idx LIMIT 4", "qualified", head+", "+last, head+" ("+last+")"); err != nil {
			return out, err
		}
	}
	if len(out) < n {
		if err := take(cols+" WHERE a.title_lc LIKE $1 ORDER BY a.disamb, length(a.title_lc), a.title_lc LIMIT $2", "prefix", likeEscape(lq)+"%", int64(n*2)); err != nil {
			return out, err
		}
	}
	if len(out) < n && s.hasTrgm() {
		if err := take(cols+" WHERE a.title_lc % $1 ORDER BY similarity(a.title_lc, $1) DESC, a.disamb, length(a.title_lc) LIMIT $2", "like", lq, int64(n*2)); err != nil {
			return out, err
		}
		if len(out) < n {
			if err := take(cols+" JOIN wiki_redirects r ON r.idx = a.idx WHERE r.title_lc % $1 ORDER BY similarity(r.title_lc, $1) DESC, a.disamb LIMIT $2", "like", lq, int64(n)); err != nil {
				return out, err
			}
		}
	}
	if len(out) < n {
		if err := take(cols+" WHERE to_tsvector('english', a.title || ' ' || a.lead) @@ plainto_tsquery('english', $1) ORDER BY ts_rank(to_tsvector('english', a.title || ' ' || a.lead), plainto_tsquery('english', $1)) DESC, a.disamb LIMIT $2", "text", lq, int64(n)); err != nil {
			return out, err
		}
	}
	return out, nil
}

// Article reads one article whole, by its place.
func (s *Store) Article(idx uint32) (Article, bool, error) {
	rows, err := s.DB.Query("SELECT title, lead, body, disamb FROM wiki_articles WHERE idx = $1", int64(idx))
	if err != nil || len(rows.Vals) == 0 || len(rows.Vals[0]) < 4 {
		return Article{}, false, err
	}
	v := rows.Vals[0]
	a := Article{Idx: idx}
	if v[0] != nil {
		a.Title = *v[0]
	}
	if v[1] != nil {
		a.Lead = *v[1]
	}
	if v[2] != nil {
		a.Body = *v[2]
	}
	a.Disamb = v[3] != nil && *v[3] == "t"
	return a, true, nil
}

// Best is the one article a phrase most likely names: the first hit that is not a page of
// meanings, else the first hit; ok false when nothing was found.
func (s *Store) Best(q string, maxLead int) (Hit, bool, error) {
	hits, err := s.Lookup(q, 4, maxLead)
	if err != nil || len(hits) == 0 {
		return Hit{}, false, err
	}
	for _, h := range hits {
		if !h.Disamb {
			return h, true, nil
		}
	}
	return hits[0], true, nil
}

// SectionFor is the section of an article a question points at, from its body: the heading and
// the text (cut to max), "" when the question points at none (BestSection).
func SectionFor(body, question string, max int) (string, string) {
	secs := parseBody(body)
	best := BestSection(secs, question)
	if best == nil {
		return "", ""
	}
	return best.Heading, cutAt(best.Text, max)
}

// parseBody reads the sections back from a stored body ("== Heading ==" lines).
func parseBody(body string) []Section {
	var out []Section
	var cur *Section
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "== ") && strings.HasSuffix(line, " ==") {
			out = append(out, Section{Heading: strings.TrimSuffix(strings.TrimPrefix(line, "== "), " ==")})
			cur = &out[len(out)-1]
			continue
		}
		if cur != nil && strings.TrimSpace(line) != "" {
			if cur.Text != "" {
				cur.Text += "\n"
			}
			cur.Text += line
		}
	}
	return out
}

// Counts is how many articles and redirects the tables hold.
func (s *Store) Counts() (articles, redirects int64) {
	if rows, err := s.DB.Query("SELECT (SELECT count(*) FROM wiki_articles), (SELECT count(*) FROM wiki_redirects)"); err == nil && len(rows.Vals) == 1 && len(rows.Vals[0]) == 2 {
		if rows.Vals[0][0] != nil {
			articles, _ = strconv.ParseInt(*rows.Vals[0][0], 10, 64)
		}
		if rows.Vals[0][1] != nil {
			redirects, _ = strconv.ParseInt(*rows.Vals[0][1], 10, 64)
		}
	}
	return
}

// hasTrgm says whether pg_trgm is installed (asked once).
func (s *Store) hasTrgm() bool {
	if s.trgm == 0 {
		s.trgm = -1
		if rows, err := s.DB.Query("SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm'"); err == nil && len(rows.Vals) == 1 {
			s.trgm = 1
		}
	}
	return s.trgm == 1
}

func likeEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	return strings.ReplaceAll(s, "_", `\_`)
}
