package wiki

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	// Shards are the readers' ranges and where each is; Next is their reads added up.
	Shards []Shard `json:"shards,omitempty"`
}

// ImportKey is the settings key the state is kept under.
const ImportKey = "synthd_wiki_import"

const (
	LeadMax = 1500  // characters of lead kept
	BodyMax = 60000 // characters of body kept
)

// Store is the box's Wikipedia over a database connection.
type Store struct {
	DB *poltergres.ReadWrite
	// NewConn makes another connection for an import's readers beyond the first (one connection
	// takes one INSERT at a time); nil shares DB, which still reads the file in parallel.
	NewConn func() *poltergres.ReadWrite

	trgm    int  // 0 unknown, 1 pg_trgm is there, -1 it is not
	dropped bool // the lookup indexes were dropped for the import running in this process
	connMu  sync.Mutex
	conns   []*poltergres.ReadWrite
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

// lookupIndexes are the store's indexes beyond the tables' keys: made by EnsureIndexes once an
// import is done, dropped by Import while one runs (an insert into a table with five indexes is
// several times the work, and the trigram GINs the worst of it). The trigram ones fail harmlessly
// without pg_trgm; the lookups then go without likeness.
var lookupIndexes = []struct{ name, create string }{
	{"wiki_articles_lc", "CREATE INDEX IF NOT EXISTS wiki_articles_lc ON wiki_articles (title_lc text_pattern_ops)"},
	{"wiki_articles_trgm", "CREATE INDEX IF NOT EXISTS wiki_articles_trgm ON wiki_articles USING gin (title_lc gin_trgm_ops)"},
	{"wiki_articles_fts", "CREATE INDEX IF NOT EXISTS wiki_articles_fts ON wiki_articles USING gin (to_tsvector('english', title || ' ' || lead))"},
	{"wiki_redirects_lc", "CREATE INDEX IF NOT EXISTS wiki_redirects_lc ON wiki_redirects (title_lc text_pattern_ops)"},
	{"wiki_redirects_trgm", "CREATE INDEX IF NOT EXISTS wiki_redirects_trgm ON wiki_redirects USING gin (title_lc gin_trgm_ops)"},
}

// EnsureIndexes makes the lookup indexes (minutes over a whole Wikipedia, the first time; nothing
// when they are there). Called when an import finishes and at every start with a finished import.
// A trigram index that cannot be made (no pg_trgm) is skipped; any other failure is returned.
func (s *Store) EnsureIndexes() error {
	for _, ix := range lookupIndexes {
		if err := s.DB.Exec(ix.create); err != nil {
			if strings.Contains(ix.name, "trgm") {
				continue
			}
			return fmt.Errorf("%s: %w", ix.name, err)
		}
	}
	return nil
}

// DropIndexes takes the lookup indexes off for an import.
func (s *Store) DropIndexes() error {
	for _, ix := range lookupIndexes {
		if err := s.DB.Exec("DROP INDEX IF EXISTS " + ix.name); err != nil {
			return err
		}
	}
	return nil
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
	importArticleBatch  = 40    // articles an INSERT carries (bodies are kilobytes)
	importRedirectBatch = 500   // redirects an INSERT carries
	importSaveEvery     = 20000 // entries a reader goes between saves of the state
	importCheckEvery    = 1000  // entries a reader goes between looks at the clock
)

// Shard is one worker's range of the entries and where it is in it.
type Shard struct {
	From, To, Next uint32
}

// Shards cuts the entries into n ranges of a size.
func Shards(total uint32, n int) []Shard {
	if n < 1 {
		n = 1
	}
	if uint32(n) > total {
		n = int(total)
	}
	out := make([]Shard, 0, n)
	step := total / uint32(n)
	for i := 0; i < n; i++ {
		from := uint32(i) * step
		to := from + step
		if i == n-1 {
			to = total
		}
		out = append(out, Shard{From: from, To: to, Next: from})
	}
	return out
}

// Read is how many entries the shards have read together.
func (st ImportState) Read() uint32 {
	if len(st.Shards) == 0 {
		return st.Next
	}
	var n uint32
	for _, sh := range st.Shards {
		n += sh.Next - sh.From
	}
	return n
}

// batch is the rows of one worker waiting for an INSERT.
type batch struct {
	arts, reds       []string
	artArgs, redArgs []any
}

// flush writes the batch. A batch the database refuses (a row it will not take: a byte sequence
// that is not UTF-8 got past the cleaning, a value too long) is written a row at a time and the
// rows refused are dropped and counted, so one bad page never stops Wikipedia; any other error
// (the connection, the disk) comes back.
func (b *batch) flush(db *poltergres.ReadWrite) (dropped int, err error) {
	if len(b.arts) > 0 {
		q := "INSERT INTO wiki_articles (idx, title, title_lc, lead, body, disamb) VALUES " + strings.Join(b.arts, ",") + " ON CONFLICT (idx) DO NOTHING"
		if err := db.Exec(q, b.artArgs...); err != nil {
			if !dataError(err) {
				return dropped, err
			}
			for i := 0; i < len(b.artArgs); i += 6 {
				if err := db.Exec("INSERT INTO wiki_articles (idx, title, title_lc, lead, body, disamb) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (idx) DO NOTHING", b.artArgs[i:i+6]...); err != nil {
					if !dataError(err) {
						return dropped, err
					}
					dropped++
				}
			}
		}
		b.arts, b.artArgs = b.arts[:0], b.artArgs[:0]
	}
	if len(b.reds) > 0 {
		q := "INSERT INTO wiki_redirects (title_lc, idx) VALUES " + strings.Join(b.reds, ",") + " ON CONFLICT (title_lc, idx) DO NOTHING"
		if err := db.Exec(q, b.redArgs...); err != nil {
			if !dataError(err) {
				return dropped, err
			}
			for i := 0; i < len(b.redArgs); i += 2 {
				if err := db.Exec("INSERT INTO wiki_redirects (title_lc, idx) VALUES ($1,$2) ON CONFLICT (title_lc, idx) DO NOTHING", b.redArgs[i:i+2]...); err != nil {
					if !dataError(err) {
						return dropped, err
					}
					dropped++
				}
			}
		}
		b.reds, b.redArgs = b.reds[:0], b.redArgs[:0]
	}
	return dropped, nil
}

// dataError says whether the database refused the data itself (class 22, data exception: a bad
// byte sequence, a value out of range) rather than failing to take it.
func dataError(err error) bool {
	var pe *poltergres.PGError
	return errors.As(err, &pe) && strings.HasPrefix(pe.Code, "22")
}

// clean makes a string one the database takes: valid UTF-8 (a byte sequence that is not becomes
// the replacement character; some pages carry a stray Latin-1 byte) and no NUL.
func clean(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}

// Import reads the file from where the state left off, for the time given at most, into the
// tables, workers at a time: each over its own range of the entries (Shards), with its own open
// file (its own cluster cache) and, when NewConn is set, its own connection; the state comes back
// and is saved. A state for another file (or an older size of it) starts over: the tables are
// emptied and the file is read from its first entry. An entry whose content will not read is
// skipped and counted, never fatal: a single bad cluster must not stop Wikipedia. The lookup
// indexes are off while the import runs (once a process) and made when it is done.
func (s *Store) Import(w *Wiki, budget time.Duration) (ImportState, error) {
	return s.ImportWith(w, 1, budget)
}

// ImportWith is Import with workers readers at once; a state from a single-reader import (no
// shards) goes on as one shard from where it was.
func (s *Store) ImportWith(w *Wiki, workers int, budget time.Duration) (ImportState, error) {
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
	if len(st.Shards) == 0 {
		if st.Next > 0 {
			st.Shards = []Shard{{From: 0, To: st.Total, Next: st.Next}}
		} else {
			st.Shards = Shards(st.Total, workers)
		}
	}
	if !s.dropped {
		// no lookup indexes to keep current through the import (made at the end); once a process
		s.dropped = true
		if err := s.DropIndexes(); err != nil {
			st.Error = err.Error()
			_ = s.Save(st)
			return st, err
		}
	}
	deadline := time.Now().Add(budget)
	var mu sync.Mutex // st, while the workers run
	var wg sync.WaitGroup
	errs := make(chan error, len(st.Shards))
	for i := range st.Shards {
		if st.Shards[i].Next >= st.Shards[i].To {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.readShard(w, i, &st, &mu, deadline); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		st.Error = err.Error()
		_ = s.Save(st)
		return st, err
	}
	st.Next = st.Read()
	done := true
	for _, sh := range st.Shards {
		if sh.Next < sh.To {
			done = false
		}
	}
	if !done {
		return st, s.Save(st)
	}
	st.Done = true
	st.Next = st.Total
	if err := s.Save(st); err != nil {
		return st, err
	}
	// the lookup indexes, over the whole of it (minutes); the import is done without them, the
	// lookups want them
	if err := s.EnsureIndexes(); err != nil {
		st.Error = "the lookup indexes: " + err.Error()
		_ = s.Save(st)
		return st, err
	}
	return st, nil
}

// readShard is one worker: shard i of st, from its Next to its To or the deadline, with its own
// open file (the coordinator's for shard 0, so a test's file serves) and its own connection when
// the store can make one. Progress goes into st under mu every importSaveEvery entries, and the
// coordinator saves the whole.
func (s *Store) readShard(w *Wiki, i int, st *ImportState, mu *sync.Mutex, deadline time.Time) error {
	mu.Lock()
	sh := st.Shards[i]
	mu.Unlock()
	src := w
	if i > 0 {
		own, err := Open(w.Path)
		if err != nil {
			return err
		}
		defer own.Close()
		src = own
	}
	db := s.DB
	if i > 0 && s.NewConn != nil {
		s.connMu.Lock()
		for len(s.conns) < i {
			s.conns = append(s.conns, s.NewConn())
		}
		db = s.conns[i-1]
		s.connMu.Unlock()
	}
	var b batch
	var articles, redirects, skipped int64
	report := func() {
		mu.Lock()
		st.Shards[i].Next = sh.Next
		st.Articles += articles
		st.Redirects += redirects
		st.Skipped += skipped
		mu.Unlock()
		articles, redirects, skipped = 0, 0, 0
	}
	for sh.Next < sh.To {
		e, err := src.z.EntryAt(sh.Next)
		sh.Next++
		if err != nil {
			skipped++
			continue
		}
		if e.Namespace != src.ns || e.Title == "" {
			continue
		}
		switch {
		case e.Redirect:
			t, err := src.z.Resolve(e)
			if err != nil || !strings.HasPrefix(t.Mime, "text/html") {
				skipped++
				continue
			}
			n := len(b.redArgs)
			b.reds = append(b.reds, "($"+strconv.Itoa(n+1)+",$"+strconv.Itoa(n+2)+")")
			b.redArgs = append(b.redArgs, strings.ToLower(clean(e.Title)), int64(t.Index))
			redirects++
			if len(b.reds) >= importRedirectBatch {
				d, err := b.flush(db)
				skipped += int64(d)
				if err != nil {
					return err
				}
			}
		case strings.HasPrefix(e.Mime, "text/html"):
			body, err := src.z.Content(e)
			if err != nil {
				skipped++
				continue
			}
			page := string(body)
			lead := Lead(page, LeadMax)
			if lead == "" {
				skipped++
				continue
			}
			n := len(b.artArgs)
			title := clean(e.Title)
			b.arts = append(b.arts, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d)", n+1, n+2, n+3, n+4, n+5, n+6))
			b.artArgs = append(b.artArgs, int64(e.Index), title, strings.ToLower(title), clean(lead), clean(Body(page, BodyMax)), disamb(lead))
			articles++
			if len(b.arts) >= importArticleBatch {
				d, err := b.flush(db)
				skipped += int64(d)
				if err != nil {
					return err
				}
			}
		}
		if n := sh.Next - sh.From; n%importCheckEvery == 0 && (n%importSaveEvery == 0 || time.Now().After(deadline)) {
			d, err := b.flush(db)
			skipped += int64(d)
			if err != nil {
				return err
			}
			report()
			mu.Lock()
			err = s.Save(*st)
			mu.Unlock()
			if err != nil {
				return err
			}
			if time.Now().After(deadline) {
				return nil
			}
		}
	}
	d, err := b.flush(db)
	skipped += int64(d)
	if err != nil {
		return err
	}
	report()
	return nil
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

// Likeness says whether lookups by likeness are on (pg_trgm installed in the database).
func (s *Store) Likeness() bool { return s.hasTrgm() }

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
