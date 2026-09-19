package search

// Store is the search layer's Postgres access, all through the native poltergres ghost_rw client with
// parameterized statements. The two exceptions use QuerySimple with fully-controlled inlined values
// and are marked SIMPLE-INLINE below: leg B (SET LOCAL must share the query's implicit transaction)
// and deletion phase 1 (multiple statements must share one implicit transaction, and every inlined
// value is an integer, a validated enum, or hex).

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

var validSources = map[string]bool{
	"email": true, "message": true, "document": true, "image": true, "audio": true,
}

// validModelID admits the safe charset for inlining in SIMPLE-INLINE queries: letters, digits,
// dot, dash, underscore. Model ids are our own conf strings; anything else is a conf bug.
func validModelID(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

type Store struct {
	db *poltergres.ReadWrite
}

func NewStore(db *poltergres.ReadWrite) *Store { return &Store{db: db} }

func (s *Store) Ping() error { return s.db.Ping() }

// VectorEnabled reports whether the schema applied with pgvector (search.meta 'vector').
func (s *Store) VectorEnabled() bool {
	rows, err := s.db.Query("SELECT value FROM search.meta WHERE key = 'vector'")
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return false
	}
	return *rows.Vals[0][0] == "on"
}

// Original is one T0 item for ingest.
type Original struct {
	Source     string
	SHA256     []byte
	Path       string
	CapturedAt time.Time
	Daemon     string
	Meta       map[string]any
}

// Tombstoned reports whether these bytes were deleted before (deleted content never returns, spec 4).
func (s *Store) Tombstoned(sha []byte) (bool, error) {
	rows, err := s.db.Query("SELECT 1 FROM search.tombstones WHERE sha256 = $1", hexArg(sha))
	if err != nil {
		return false, err
	}
	return len(rows.Vals) > 0, nil
}

// InsertOriginal inserts and returns (id, existed). On a dedup conflict it returns the existing id
// with existed=true so the caller can heal a half-done previous ingest (chunks missing).
func (s *Store) InsertOriginal(o Original) (int64, bool, error) {
	if !validSources[o.Source] {
		return 0, false, fmt.Errorf("invalid source %q", o.Source)
	}
	metaJSON, _ := json.Marshal(o.Meta)
	if o.Meta == nil {
		metaJSON = []byte("{}")
	}
	rows, err := s.db.Query(
		`INSERT INTO search.originals (source, sha256, path, captured_at, daemon, meta)
		 VALUES ($1, $2, $3, to_timestamp($4), $5, $6::jsonb)
		 ON CONFLICT (source, sha256) DO NOTHING RETURNING id`,
		o.Source, hexArg(o.SHA256), o.Path, o.CapturedAt.UTC().Unix(), o.Daemon, string(metaJSON))
	if err != nil {
		return 0, false, err
	}
	if len(rows.Vals) > 0 && rows.Vals[0][0] != nil {
		id, _ := strconv.ParseInt(*rows.Vals[0][0], 10, 64)
		return id, false, nil
	}
	rows, err = s.db.Query("SELECT id FROM search.originals WHERE source = $1 AND sha256 = $2",
		o.Source, hexArg(o.SHA256))
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return 0, false, fmt.Errorf("dedup row not found after conflict: %v", err)
	}
	id, _ := strconv.ParseInt(*rows.Vals[0][0], 10, 64)
	return id, true, nil
}

// ChunkCount heals the crash window between original insert and chunk insert (no cross-call
// transactions in poltergres, deliberately): an existing original with zero chunks gets re-chunked.
func (s *Store) ChunkCount(tier int, source string, origID int64) (int, error) {
	rows, err := s.db.Query(
		"SELECT count(*) FROM search.chunks WHERE tier = $1 AND orig_source = $2 AND orig_id = $3",
		tier, source, origID)
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return 0, err
	}
	n, _ := strconv.Atoi(*rows.Vals[0][0])
	return n, nil
}

// InsertChunksT0 writes tier-0 chunks for an original. Batched multi-row insert (the spec's blessed
// COPY fallback, D2).
func (s *Store) InsertChunksT0(source string, origID int64, capturedAt time.Time, chunks []Chunk) ([]int64, error) {
	return s.insertChunks(0, source, origID, 0, 0, capturedAt, chunks)
}

// InsertChunkT1 / InsertChunkT2: journal entries and memories embed whole, seq 0, one chunk (spec 5).
func (s *Store) InsertChunkT1(entryID int64, capturedAt time.Time, body string) ([]int64, error) {
	return s.insertChunks(1, "", 0, entryID, 0, capturedAt, []Chunk{{Seq: 0, Body: body}})
}
func (s *Store) InsertChunkT2(memoryID int64, capturedAt time.Time, body string) ([]int64, error) {
	return s.insertChunks(2, "", 0, 0, memoryID, capturedAt, []Chunk{{Seq: 0, Body: body}})
}

func (s *Store) insertChunks(tier int, source string, origID, entryID, memoryID int64, capturedAt time.Time, chunks []Chunk) ([]int64, error) {
	if len(chunks) == 0 {
		return nil, nil
	}
	var ids []int64
	const batch = 100
	for start := 0; start < len(chunks); start += batch {
		end := start + batch
		if end > len(chunks) {
			end = len(chunks)
		}
		sql := "INSERT INTO search.chunks (tier, orig_source, orig_id, entry_id, memory_id, seq, body, captured_at) VALUES "
		args := make([]any, 0, (end-start)*8)
		for i, c := range chunks[start:end] {
			if i > 0 {
				sql += ", "
			}
			b := i * 8
			sql += fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d,to_timestamp($%d))",
				b+1, b+2, b+3, b+4, b+5, b+6, b+7, b+8)
			args = append(args, tier, nilIfEmpty(source), nilIfZero(origID), nilIfZero(entryID),
				nilIfZero(memoryID), c.Seq, c.Body, capturedAt.UTC().Unix())
		}
		sql += " RETURNING id"
		rows, err := s.db.Query(sql, args...)
		if err != nil {
			return ids, err
		}
		for _, r := range rows.Vals {
			if len(r) > 0 && r[0] != nil {
				id, _ := strconv.ParseInt(*r[0], 10, 64)
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

// AddCitations records which originals a T1/T2 row cites, so deletion phase 1 can find and stale-mark
// it (spec 12 / invariant I4; the citations table is this implementation's addition).
func (s *Store) AddCitations(tier int, refID int64, source string, origIDs []int64) error {
	for _, oid := range origIDs {
		if err := s.db.Exec(
			`INSERT INTO search.citations (tier, ref_id, orig_source, orig_id) VALUES ($1,$2,$3,$4)
			 ON CONFLICT DO NOTHING`, tier, refID, source, oid); err != nil {
			return err
		}
	}
	return nil
}

// SetPhash stores an image's perceptual hash.
func (s *Store) SetPhash(origID int64, ph uint64) error {
	return s.db.Exec("UPDATE search.originals_image SET phash = $1 WHERE id = $2", int64(ph), origID)
}

// NearestPhash returns (origID, distance, found) of the closest existing image by Hamming distance.
// Linear scan in SQL via bit ops would need an extension; the corpus is one person's photos and the
// scan is in Go over ids+hashes , thousands of rows, microseconds, revisit if it ever shows up in a
// profile.
func (s *Store) NearestPhash(ph uint64, excludeID int64) (int64, int, bool, error) {
	rows, err := s.db.Query("SELECT id, phash FROM search.originals_image WHERE phash IS NOT NULL AND id <> $1", excludeID)
	if err != nil {
		return 0, 0, false, err
	}
	bestID, bestD, found := int64(0), 65, false
	for _, r := range rows.Vals {
		if len(r) < 2 || r[0] == nil || r[1] == nil {
			continue
		}
		id, _ := strconv.ParseInt(*r[0], 10, 64)
		hv, _ := strconv.ParseInt(*r[1], 10, 64)
		if d := Hamming(ph, uint64(hv)); d < bestD {
			bestID, bestD, found = id, d, true
		}
	}
	return bestID, bestD, found, nil
}

// --- job queue (spec 4): pure SQL, SKIP LOCKED ---

type Job struct {
	ID      int64
	Kind    string
	Payload json.RawMessage
}

func (s *Store) EnqueueJob(kind string, payload any) error {
	b, _ := json.Marshal(payload)
	return s.db.Exec("INSERT INTO search.jobs (kind, payload) VALUES ($1, $2::jsonb)", kind, string(b))
}

// ClaimJob claims one runnable job of the kind (attempts incremented atomically).
// UnclaimJob refunds a claim , the attempt did not reach the model (oracled warming), so it must
// not count against the job's five lives. Warmup storms were quietly EXHAUSTING jobs: five
// restarts and a photo would never be captioned. Deferred 20s so the lane rests while llama loads.
func (s *Store) UnclaimJob(id int64) error {
	return s.db.Exec(
		"UPDATE search.jobs SET attempts = GREATEST(attempts - 1, 0), run_after = now() + interval '20 seconds' WHERE id = $1", id)
}

// ReviveJobs resets exhausted jobs (attempts >= 5) to runnable , the paved path for what was a
// raw psql UPDATE the first time it was needed. Returns how many came back from the dead.
func (s *Store) ReviveJobs() (int64, error) {
	rows, err := s.db.Query(
		"WITH u AS (UPDATE search.jobs SET attempts = 0, run_after = now() WHERE attempts >= 5 RETURNING 1) SELECT count(*) FROM u")
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return 0, err
	}
	n, _ := strconv.ParseInt(*rows.Vals[0][0], 10, 64)
	return n, nil
}

// ReingestImages enqueues a caption job for every image original that has NO chunks and NO
// caption job already queued , the idempotent re-import. Safe to run any time: a healthy library
// enqueues nothing; after a restore or a purge it enqueues exactly the gap.
func (s *Store) ReingestImages() (int64, error) {
	rows, err := s.db.Query(`
		WITH ins AS (
			INSERT INTO search.jobs (kind, payload)
			SELECT 'caption', jsonb_build_object('origId', o.id, 'path', o.path)
			FROM search.originals o
			WHERE o.source = 'image'
			  AND NOT EXISTS (SELECT 1 FROM search.chunks c WHERE c.orig_source = 'image' AND c.orig_id = o.id)
			  AND NOT EXISTS (SELECT 1 FROM search.jobs j WHERE j.kind = 'caption' AND (j.payload->>'origId')::bigint = o.id)
			RETURNING 1)
		SELECT count(*) FROM ins`)
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return 0, err
	}
	n, _ := strconv.ParseInt(*rows.Vals[0][0], 10, 64)
	return n, nil
}

// CaptionState is what the ensure path needs to know about an image original without reading its
// bytes: the caption it has (if any), the representative it is a burst sibling of (if any), and
// whether a caption job for it already sits in the queue, parked or runnable.
type CaptionState struct {
	Caption   string
	DupOf     int64
	JobQueued bool
	JobParked bool
}

func (s *Store) CaptionStateOf(origID int64) (CaptionState, error) {
	var st CaptionState
	rows, err := s.db.Query(`SELECT meta->>'caption', meta->>'dup_of' FROM search.originals WHERE source = 'image' AND id = $1`, origID)
	if err != nil {
		return st, err
	}
	if len(rows.Vals) == 1 {
		if v := rows.Vals[0][0]; v != nil {
			st.Caption = *v
		}
		if v := rows.Vals[0][1]; v != nil {
			st.DupOf, _ = strconv.ParseInt(*v, 10, 64)
		}
	}
	st.JobQueued, st.JobParked, err = s.JobState("caption", origID)
	return st, err
}

// JobState reports whether a job of the kind sits in the queue for an original, and whether it is
// parked (five failed attempts, invisible to ClaimJob). The ensure path asks before enqueueing so a
// stock-take never doubles a job that is already on its way.
func (s *Store) JobState(kind string, origID int64) (queued, parked bool, err error) {
	jobs, err := s.db.Query(`SELECT attempts FROM search.jobs WHERE kind = $1 AND (payload->>'origId')::bigint = $2`, kind, origID)
	if err != nil {
		return false, false, err
	}
	for _, r := range jobs.Vals {
		if len(r) == 0 || r[0] == nil {
			continue
		}
		queued = true
		if n, perr := strconv.Atoi(*r[0]); perr == nil && n >= 5 {
			parked = true
		}
	}
	return queued, parked, nil
}

// OriginalIDByFrameHash finds an image original from framed's frame hash, which is the first 16
// bytes of the same sha256 searchd stores in full. The ensure path uses it so a stock-take does
// not re-hash a 400MB clip just to ask whether it has a description. 0 when unknown.
func (s *Store) OriginalIDByFrameHash(frameHash string) (int64, error) {
	if len(frameHash) != 32 {
		return 0, nil
	}
	rows, err := s.db.Query(
		`SELECT id FROM search.originals WHERE source = 'image' AND substring(sha256 from 1 for 16) = decode($1, 'hex')`, frameHash)
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return 0, err
	}
	id, _ := strconv.ParseInt(*rows.Vals[0][0], 10, 64)
	return id, nil
}

// FrameNeedsTagPass is true when the frame with this hash still lacks a title or tags, or is not
// in frames at all (then the pass runs, as it always did). It keeps the ensure path from paying a
// model call for a frame that only lacked its description.
func (s *Store) FrameNeedsTagPass(hash string) (bool, error) {
	rows, err := s.db.Query(
		`SELECT (display_name IS NULL OR display_name = '')
		     OR NOT EXISTS (SELECT 1 FROM frame_tags WHERE frame_tags.hash = frames.hash)
		 FROM frames WHERE hash = $1`, hash)
	if err != nil {
		return true, err
	}
	if len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return true, nil
	}
	return *rows.Vals[0][0] == "t", nil
}

// HasTagChunk is true when the original already carries its "tags: ..." search chunk, so a
// repeated tag pass does not stack a second copy into the index.
func (s *Store) HasTagChunk(origID int64) (bool, error) {
	rows, err := s.db.Query(
		`SELECT 1 FROM search.chunks WHERE tier = 0 AND orig_source = 'image' AND orig_id = $1 AND body LIKE 'tags: %' LIMIT 1`, origID)
	if err != nil {
		return false, err
	}
	return len(rows.Vals) > 0, nil
}

// RequeueCaption gives an original's caption job a fresh start with a NEW render path: parked
// attempts back to zero, or a new job when none exists. The render path is what the model looks
// at now (a preview, a frame grab), which may differ from what the old job pointed at.
func (s *Store) RequeueCaption(origID int64, render string) error {
	if err := s.db.Exec(`DELETE FROM search.jobs WHERE kind = 'caption' AND (payload->>'origId')::bigint = $1`, origID); err != nil {
		return err
	}
	return s.EnqueueJob("caption", map[string]any{"origId": origID, "path": render})
}

// SetCaption stores a caption in an original's meta (the ensure path copying a burst representative's).
func (s *Store) SetCaption(origID int64, caption string) error {
	return s.db.Exec(`UPDATE search.originals SET meta = meta || jsonb_build_object('caption', $1::text)
		WHERE source = 'image' AND id = $2`, caption, origID)
}

func (s *Store) ClaimJob(kind string) (*Job, error) {
	rows, err := s.db.Query(`
		UPDATE search.jobs SET attempts = attempts + 1
		WHERE id = (
			SELECT id FROM search.jobs
			WHERE kind = $1 AND run_after <= now() AND attempts < 5
			-- NEWEST FIRST: a person looking at their gallery today wants today's photos named,
			-- not 2019's. id DESC approximates capture order closely enough (jobs are queued as
			-- frames archive) without joining frames.
			ORDER BY id DESC
			FOR UPDATE SKIP LOCKED
			LIMIT 1)
		RETURNING id, payload`, kind)
	if err != nil || len(rows.Vals) == 0 {
		return nil, err
	}
	r := rows.Vals[0]
	if len(r) < 2 || r[0] == nil || r[1] == nil {
		return nil, nil
	}
	id, _ := strconv.ParseInt(*r[0], 10, 64)
	return &Job{ID: id, Kind: kind, Payload: json.RawMessage(*r[1])}, nil
}

func (s *Store) CompleteJob(id int64) error {
	return s.db.Exec("DELETE FROM search.jobs WHERE id = $1", id)
}

// UnparkJobs resets PARKED jobs (attempts >= 5, permanently dead to ClaimJob) so the worker claims
// them again. The parking rule protects the queue from a poison job retrying forever; it has no
// answer for the OTHER cause of five failures , an environment that was broken and is now fixed (a
// night of role errors, an oracled that could not see). This is the operator's lever for that case:
// attempts back to zero, run_after to now, per kind or across all kinds. Poison jobs will simply
// park again after five more tries , the lever costs nothing to pull wrongly.
func (s *Store) UnparkJobs(kind string) (int64, error) {
	countQ := "SELECT count(*) FROM search.jobs WHERE attempts >= 5"
	updQ := "UPDATE search.jobs SET attempts = 0, run_after = now(), last_error = '' WHERE attempts >= 5"
	args := []any{}
	if kind != "" {
		countQ += " AND kind = $1"
		updQ += " AND kind = $1"
		args = append(args, kind)
	}
	rows, err := s.db.Query(countQ, args...)
	if err != nil {
		return 0, err
	}
	var n int64
	if len(rows.Vals) > 0 && rows.Vals[0][0] != nil {
		n, _ = strconv.ParseInt(*rows.Vals[0][0], 10, 64)
	}
	if n == 0 {
		return 0, nil
	}
	if err := s.db.Exec(updQ, args...); err != nil {
		return 0, err
	}
	return n, nil
}

// FailJob applies the spec's backoff: run_after = now() + attempts^2 minutes.
func (s *Store) FailJob(id int64, jobErr error) error {
	msg := jobErr.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return s.db.Exec(
		`UPDATE search.jobs SET last_error = $1,
		 run_after = now() + (attempts * attempts) * interval '1 minute' WHERE id = $2`, msg, id)
}

// ChunkBodies fetches bodies for an embed batch.
func (s *Store) ChunkBodies(ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	for _, id := range ids { // small batches (<=64); one query per id keeps SQL trivial
		rows, err := s.db.Query("SELECT body FROM search.chunks WHERE id = $1", id)
		if err != nil {
			return nil, err
		}
		if len(rows.Vals) > 0 && rows.Vals[0][0] != nil {
			out[id] = *rows.Vals[0][0]
		}
	}
	return out, nil
}

// SetEmbedding stores one vector (pgvector text format) with its model id.
func (s *Store) SetEmbedding(id int64, vec []float32, modelID string) error {
	return s.db.Exec("UPDATE search.chunks SET emb = $1::vector, emb_model = $2 WHERE id = $3",
		VecText(vec), modelID, id)
}

// --- query legs (spec 7) ---

type Hit struct {
	Tier       int
	ChunkID    int64
	OrigSource string
	OrigID     int64
	EntryID    int64
	MemoryID   int64
	Score      float64
}

type Filters struct {
	Sources []string
	From    time.Time // zero = -infinity
	To      time.Time // zero = +infinity
	Tiers   []int     // empty = all
}

func (f Filters) validate() error {
	for _, s := range f.Sources {
		if !validSources[s] {
			return fmt.Errorf("invalid source %q", s)
		}
	}
	for _, t := range f.Tiers {
		if t < 0 || t > 2 {
			return fmt.Errorf("invalid tier %d", t)
		}
	}
	return nil
}

func (f Filters) fromUnix() int64 {
	if f.From.IsZero() {
		return 0
	}
	return f.From.UTC().Unix()
}
func (f Filters) toUnix() int64 {
	if f.To.IsZero() {
		return 4102444800 // 2100-01-01; far enough
	}
	return f.To.UTC().Unix()
}

// tierPred renders the tier filter as validated inline SQL (values are checked ints).
func tierPred(tiers []int) string {
	if len(tiers) == 0 {
		return ""
	}
	parts := make([]string, len(tiers))
	for i, t := range tiers {
		parts[i] = strconv.Itoa(t)
	}
	return " AND tier IN (" + strings.Join(parts, ",") + ")"
}

// LegA is FTS (spec 7.1), parameterized.
func (s *Store) LegA(query string, f Filters, limit int) ([]Hit, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}
	sql := `SELECT tier, id, coalesce(orig_source,''), coalesce(orig_id,0), coalesce(entry_id,0),
	               coalesce(memory_id,0), ts_rank_cd(fts, q) AS r
	        FROM search.chunks, websearch_to_tsquery('simple', search.immutable_unaccent($1)) q
	        WHERE fts @@ q AND NOT stale
	          AND (cardinality($2::text[]) = 0 OR orig_source = ANY($2))
	          AND captured_at BETWEEN to_timestamp($3) AND to_timestamp($4)` +
		tierPred(f.Tiers) + `
	        ORDER BY r DESC LIMIT ` + strconv.Itoa(limit)
	rows, err := s.db.Query(sql, query, textArray(f.Sources), f.fromUnix(), f.toUnix())
	if err != nil {
		return nil, err
	}
	return parseHits(rows), nil
}

// LegB is the vector leg (spec 7.2). SIMPLE-INLINE: SET LOCAL must share the SELECT's implicit
// transaction, so this is one multi-statement simple query. Inlined values are fully controlled: the
// vector literal is generated by VecText (digits and punctuation), sources are validated enum words,
// tiers are validated ints, timestamps are ints, and modelID is charset-validated below.
//
// The emb_model predicate is the spec 14.3 invariant: a query vector from the configured model is
// NEVER compared against stored vectors from a different model. Mid-migration, old-model rows simply
// do not participate , they rejoin as the background re-embed replaces them.
func (s *Store) LegB(vec []float32, f Filters, limit, efSearch int, modelID string) ([]Hit, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}
	if !validModelID(modelID) {
		return nil, fmt.Errorf("invalid model id %q", modelID)
	}
	srcPred := ""
	if len(f.Sources) > 0 {
		quoted := make([]string, len(f.Sources))
		for i, src := range f.Sources {
			quoted[i] = "'" + src + "'" // validated enum, no quoting hazard
		}
		srcPred = " AND orig_source IN (" + strings.Join(quoted, ",") + ")"
	}
	sql := fmt.Sprintf(`SET LOCAL hnsw.ef_search = %d;
SET LOCAL hnsw.iterative_scan = 'relaxed_order';
SELECT tier, id, coalesce(orig_source,''), coalesce(orig_id,0), coalesce(entry_id,0),
       coalesce(memory_id,0), 1 - (emb <=> '%s'::vector) AS score
FROM search.chunks
WHERE emb IS NOT NULL AND emb_model = '%s' AND NOT stale%s%s
  AND captured_at BETWEEN to_timestamp(%d) AND to_timestamp(%d)
ORDER BY emb <=> '%s'::vector
LIMIT %d`,
		efSearch, VecText(vec), modelID, srcPred, tierPred(f.Tiers), f.fromUnix(), f.toUnix(), VecText(vec), limit)
	rows, err := s.db.QuerySimple(sql)
	if err != nil {
		return nil, err
	}
	return parseHits(rows), nil
}

// Snippets returns highlighted excerpts for chunk ids (spec 7.5), via ts_headline.
func (s *Store) Snippets(query string, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	for _, id := range ids {
		rows, err := s.db.Query(
			`SELECT ts_headline('simple', body, websearch_to_tsquery('simple', search.immutable_unaccent($1)),
			        'MaxWords=30, MinWords=15')
			 FROM search.chunks WHERE id = $2`, query, id)
		if err != nil {
			return nil, err
		}
		if len(rows.Vals) > 0 && rows.Vals[0][0] != nil {
			out[id] = *rows.Vals[0][0]
		}
	}
	return out, nil
}

// ParentLabel fetches display info for a group parent.
func (s *Store) ParentLabel(h Hit) (label, path string, capturedAt int64) {
	switch h.Tier {
	case 0:
		rows, err := s.db.Query(
			`SELECT coalesce(meta->>'title', path), path, extract(epoch from captured_at)::bigint
			 FROM search.originals WHERE source = $1 AND id = $2`, h.OrigSource, h.OrigID)
		if err == nil && len(rows.Vals) > 0 {
			r := rows.Vals[0]
			if r[0] != nil {
				label = *r[0]
			}
			if r[1] != nil {
				path = *r[1]
			}
			if r[2] != nil {
				capturedAt, _ = strconv.ParseInt(*r[2], 10, 64)
			}
		}
	default:
		// T1/T2 labels live with their producing tables (outside this spec); until then the chunk body's
		// first line is the label. Honest placeholder, not a fake.
		rows, err := s.db.Query("SELECT body FROM search.chunks WHERE tier = $1 AND id = $2", h.Tier, h.ChunkID)
		if err == nil && len(rows.Vals) > 0 && rows.Vals[0][0] != nil {
			label = firstLine(*rows.Vals[0][0])
		}
	}
	return
}

// --- deletion (spec 12, phase 1) ---

// DeleteOriginal runs phase 1 atomically. SIMPLE-INLINE: one multi-statement simple query = one
// implicit transaction. Inlined values: validated source enum, integer id, hex sha. It tombstones,
// deletes (FK cascades chunks), stale-marks citing T1/T2 chunks, and enqueues reconsolidate jobs from
// the citations table. Returns the count of stale-marked interpretation rows.
func (s *Store) DeleteOriginal(source string, id int64, sha []byte) error {
	if !validSources[source] {
		return fmt.Errorf("invalid source %q", source)
	}
	shaHex := fmt.Sprintf("%x", sha)
	sql := fmt.Sprintf(`
INSERT INTO search.tombstones (sha256) VALUES ('\x%s') ON CONFLICT DO NOTHING;
UPDATE search.chunks c SET stale = true
  FROM search.citations ct
  WHERE ct.orig_source = '%s' AND ct.orig_id = %d
    AND ((ct.tier = 1 AND c.tier = 1 AND c.entry_id = ct.ref_id)
      OR (ct.tier = 2 AND c.tier = 2 AND c.memory_id = ct.ref_id));
INSERT INTO search.jobs (kind, payload)
  SELECT 'reconsolidate', jsonb_build_object('tier', ct.tier, 'refId', ct.ref_id)
  FROM search.citations ct WHERE ct.orig_source = '%s' AND ct.orig_id = %d;
DELETE FROM search.citations WHERE orig_source = '%s' AND orig_id = %d;
DELETE FROM search.originals WHERE source = '%s' AND id = %d;`,
		shaHex, source, id, source, id, source, id, source, id)
	_, err := s.db.QuerySimple(sql)
	return err
}

// OriginalByID fetches source/path/sha/meta for delete and rebuild flows.
func (s *Store) OriginalByID(source string, id int64) (path string, sha []byte, meta map[string]any, capturedAt time.Time, err error) {
	rows, qerr := s.db.Query(
		`SELECT path, encode(sha256,'hex'), meta::text, extract(epoch from captured_at)::bigint
		 FROM search.originals WHERE source = $1 AND id = $2`, source, id)
	if qerr != nil {
		return "", nil, nil, time.Time{}, qerr
	}
	if len(rows.Vals) == 0 {
		return "", nil, nil, time.Time{}, fmt.Errorf("original %s/%d not found", source, id)
	}
	r := rows.Vals[0]
	if r[0] != nil {
		path = *r[0]
	}
	if r[1] != nil {
		sha = fromHex(*r[1])
	}
	if r[2] != nil {
		_ = json.Unmarshal([]byte(*r[2]), &meta)
	}
	if r[3] != nil {
		ts, _ := strconv.ParseInt(*r[3], 10, 64)
		capturedAt = time.Unix(ts, 0).UTC()
	}
	return
}

// --- rebuild (spec 13.3) ---

// RebuildTruncate drops all derived chunk rows; callers then re-chunk from originals.
func (s *Store) RebuildTruncate() error {
	_, err := s.db.QuerySimple("TRUNCATE search.chunks_t0, search.chunks_t1, search.chunks_t2; DELETE FROM search.jobs WHERE kind IN ('embed_text','caption')")
	return err
}

// EachOriginal streams (source,id,path,meta,capturedAt) for rebuild.
type OrigRow struct {
	Source     string
	ID         int64
	Path       string
	Meta       map[string]any
	CapturedAt time.Time
}

func (s *Store) AllOriginals() ([]OrigRow, error) {
	rows, err := s.db.Query(
		`SELECT source, id, path, meta::text, extract(epoch from captured_at)::bigint
		 FROM search.originals ORDER BY source, id`)
	if err != nil {
		return nil, err
	}
	out := make([]OrigRow, 0, len(rows.Vals))
	for _, r := range rows.Vals {
		if len(r) < 5 || r[0] == nil || r[1] == nil {
			continue
		}
		var o OrigRow
		o.Source = *r[0]
		o.ID, _ = strconv.ParseInt(*r[1], 10, 64)
		if r[2] != nil {
			o.Path = *r[2]
		}
		if r[3] != nil {
			_ = json.Unmarshal([]byte(*r[3]), &o.Meta)
		}
		if r[4] != nil {
			ts, _ := strconv.ParseInt(*r[4], 10, 64)
			o.CapturedAt = time.Unix(ts, 0).UTC()
		}
		out = append(out, o)
	}
	return out, nil
}

// --- cue history (spec 11.4) ---

func (s *Store) RecordSurface(memoryID int64, contextType string) error {
	return s.db.Exec("INSERT INTO search.cue_history (memory_id, context_type) VALUES ($1, $2)",
		memoryID, contextType)
}

func (s *Store) RecordEngagement(memoryID int64, contextType string) error {
	return s.db.Exec(
		`UPDATE search.cue_history SET engaged = true WHERE id = (
		   SELECT id FROM search.cue_history WHERE memory_id = $1 AND context_type = $2
		   ORDER BY surfaced_at DESC LIMIT 1)`, memoryID, contextType)
}

func (s *Store) SurfaceCount(memoryID int64, contextType string, window time.Duration) (int, error) {
	rows, err := s.db.Query(
		`SELECT count(*) FROM search.cue_history
		 WHERE memory_id = $1 AND context_type = $2 AND surfaced_at > now() - ($3 * interval '1 second')`,
		memoryID, contextType, int64(window.Seconds()))
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return 0, err
	}
	n, _ := strconv.Atoi(*rows.Vals[0][0])
	return n, nil
}

func (s *Store) EngagementCount(memoryID int64, contextType string) (int, error) {
	rows, err := s.db.Query(
		"SELECT count(*) FROM search.cue_history WHERE memory_id = $1 AND context_type = $2 AND engaged",
		memoryID, contextType)
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return 0, err
	}
	n, _ := strconv.Atoi(*rows.Vals[0][0])
	return n, nil
}

func (s *Store) LastSurfacedAt() (time.Time, error) {
	rows, err := s.db.Query("SELECT extract(epoch from max(surfaced_at))::bigint FROM search.cue_history")
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return time.Time{}, err
	}
	ts, _ := strconv.ParseInt(*rows.Vals[0][0], 10, 64)
	return time.Unix(ts, 0), nil
}

// HealthRow reads the ops view (spec 13.4).
func (s *Store) HealthRow() (pending, stale, parked, runnable int64, err error) {
	rows, qerr := s.db.Query("SELECT pending_embeds, stale_chunks, parked_jobs, runnable_jobs FROM search.health")
	if qerr != nil || len(rows.Vals) == 0 {
		return 0, 0, 0, 0, qerr
	}
	get := func(i int) int64 {
		if rows.Vals[0][i] == nil {
			return 0
		}
		n, _ := strconv.ParseInt(*rows.Vals[0][i], 10, 64)
		return n
	}
	return get(0), get(1), get(2), get(3), nil
}

// --- helpers ---

func parseHits(rows *poltergres.Rows) []Hit {
	out := make([]Hit, 0, len(rows.Vals))
	for _, r := range rows.Vals {
		if len(r) < 7 {
			continue
		}
		cell := func(i int) string {
			if r[i] == nil {
				return ""
			}
			return *r[i]
		}
		var h Hit
		h.Tier, _ = strconv.Atoi(cell(0))
		h.ChunkID, _ = strconv.ParseInt(cell(1), 10, 64)
		h.OrigSource = cell(2)
		h.OrigID, _ = strconv.ParseInt(cell(3), 10, 64)
		h.EntryID, _ = strconv.ParseInt(cell(4), 10, 64)
		h.MemoryID, _ = strconv.ParseInt(cell(5), 10, 64)
		h.Score, _ = strconv.ParseFloat(cell(6), 64)
		out = append(out, h)
	}
	return out
}

// hexArg renders bytes as a Postgres bytea text literal parameter (\x..). Extended-protocol text
// format accepts this for bytea.
func hexArg(b []byte) string { return fmt.Sprintf("\\x%x", b) }

func fromHex(h string) []byte {
	out := make([]byte, len(h)/2)
	for i := 0; i < len(out); i++ {
		hi := hexNib(h[2*i])
		lo := hexNib(h[2*i+1])
		out[i] = hi<<4 | lo
	}
	return out
}

func hexNib(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

// textArray renders a Go slice as a Postgres text[] literal for a parameter.
func textArray(ss []string) string {
	if len(ss) == 0 {
		return "{}"
	}
	return "{" + strings.Join(ss, ",") + "}" // validated enum words, no quoting needed
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func nilIfZero(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return strings.TrimSpace(s)
}

// TierCount counts live (not stale) chunks in the given tiers , searchd's readiness signal for the
// ambient path (no T1/T2 corpus = ambient has nothing to prime from).
func (s *Store) TierCount(tiers ...int) (int64, error) {
	rows, err := s.db.Query("SELECT count(*) FROM search.chunks WHERE NOT stale" + tierPred(tiers))
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return 0, err
	}
	n, _ := strconv.ParseInt(*rows.Vals[0][0], 10, 64)
	return n, nil
}
