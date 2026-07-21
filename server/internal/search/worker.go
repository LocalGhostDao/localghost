package search

// Worker loop (spec 4): claim -> do -> complete/fail-with-backoff. Polling, not LISTEN/NOTIFY , the
// in-house pg client has no async notification path yet (D2), and at single-user scale a poll tick
// costs nothing measurable. The interval is conf. embed concurrency is capped at ONE in-flight batch
// (spec: embed_max_concurrent_batches default 1) so interactive inference keeps the hardware.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
)

type Worker struct {
	modelHoldUntil time.Time // caption/tag lanes rest until here while oracled warms
	Store    *Store
	Embed    *Embedder // nil = vector-less; embed jobs are not claimed
	Caption  Captioner
	Tag      Tagger // nil = tags parked like vision-less captions
	Ingester *Ingester
	Log      *slog.Logger
	Interval time.Duration
}

// Run polls all job kinds until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	// Drain greedily per tick but one job at a time per kind , the single-conn store serialises anyway.
	if w.Embed != nil {
		for w.one(ctx, "embed_text", w.doEmbed) {
		}
	}
	// MODEL GATE , while oracled is warming (llama loading 7GB), model-dependent lanes REST
	// instead of machine-gunning fast-fails through the queue. The first "no backend" sets the
	// hold; nothing model-bound runs until it lapses. Embeds and reconsolidation are unaffected.
	if time.Now().Before(w.modelHoldUntil) {
		return
	}
	for w.one(ctx, "caption", w.doCaption) {
	}
	for w.one(ctx, "tag", w.doTags) {
	}
	for w.one(ctx, "reconsolidate", w.doReconsolidate) {
	}
}

func (w *Worker) one(ctx context.Context, kind string, do func(context.Context, *Job) error) bool {
	if ctx.Err() != nil {
		return false
	}
	job, err := w.Store.ClaimJob(kind)
	if err != nil || job == nil {
		return false
	}
	if err := do(ctx, job); err != nil {
		if strings.Contains(err.Error(), "no backend") {
			// Oracled is warming , refund the attempt (it never reached the model) and hold the
			// model lanes. One log line per storm, not one per job.
			_ = w.Store.UnclaimJob(job.ID)
			if time.Now().After(w.modelHoldUntil) {
				w.Log.Info("model warming , caption/tag lanes resting 20s", "fn", "one")
			}
			w.modelHoldUntil = time.Now().Add(20 * time.Second)
			return false
		}
		w.Log.Warn("job failed", "fn", "one", "kind", kind, "job", job.ID, "err", err)
		_ = w.Store.FailJob(job.ID, err)
		return true
	}
	_ = w.Store.CompleteJob(job.ID)
	return true
}

func (w *Worker) doEmbed(ctx context.Context, job *Job) error {
	var p struct {
		ChunkIDs []int64 `json:"chunkIds"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return err
	}
	bodies, err := w.Store.ChunkBodies(p.ChunkIDs)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(bodies))
	texts := make([]string, 0, len(bodies))
	for _, id := range p.ChunkIDs {
		if b, ok := bodies[id]; ok {
			ids = append(ids, id)
			texts = append(texts, b)
		}
	}
	if len(texts) == 0 {
		return nil // chunks deleted since enqueue; done
	}
	vecs, err := w.Embed.Embed(ctx, texts)
	if err != nil {
		return err
	}
	for i, id := range ids {
		if err := w.Store.SetEmbedding(id, vecs[i], w.Embed.ModelID); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) doCaption(ctx context.Context, job *Job) error {
	var p struct {
		OrigID int64  `json:"origId"`
		Path   string `json:"path"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return err
	}
	caption, err := w.Caption.Caption(ctx, p.Path)
	if err != nil {
		return err // ErrNoVision parks here, visibly, until oracled can see
	}
	// Store the caption in meta AND chunk it (spec 9.1 steps 6-7).
	if err := w.Store.db.Exec(
		`UPDATE search.originals SET meta = meta || jsonb_build_object('caption', $1::text)
		 WHERE source = 'image' AND id = $2`, caption, p.OrigID); err != nil {
		return err
	}
	_, sha, meta, captured, err := w.Store.OriginalByID("image", p.OrigID)
	if err != nil {
		return err
	}
	header := ContextHeader("photo", captured.Format("2006-01-02"), metaCamera(meta))
	// The SCENE section is the human-facing DESCRIPTION , stored on the frame so the gallery can
	// show what the photo is without re-running the model. Never overwrites a non-empty value
	// (a future user-edited description outranks the model, same rule as tags and memories).
	if scene := captionSection(caption, "SCENE:"); scene != "" {
		if err := w.Store.db.Exec(
			`UPDATE frames SET description = $1 WHERE hash = $2 AND (description IS NULL OR description = '')`,
			scene, sha); err != nil {
			w.Log.Warn("description write failed", "fn", "doCaption", "hash", sha, "err", err)
		}
	}
	chunks := ChunkText(header, caption)
	ids, err := w.Store.InsertChunksT0("image", p.OrigID, captured, chunks)
	if err != nil {
		return err
	}
	if err := w.Ingester.enqueueEmbeds(ids); err != nil {
		return err
	}
	// Chain the tag pass , text-only over the caption we just made, so it rides the same background
	// queue at a fraction of the vision pass's cost.
	return w.Store.EnqueueJob("tag", map[string]any{
		"origId": p.OrigID, "path": p.Path, "caption": caption,
		"captured": captured.Unix(),
	})
}

// doTags turns a caption into tag rows and a derived display name. The frame's identity (the 32-hex
// content hash) comes from the ARCHIVE FILENAME , framed names archived files <hash>.<ext>, so the
// path the ingest already carries is the bridge, and no schema or API grew for this.
func (w *Worker) doTags(ctx context.Context, job *Job) error {
	var p struct {
		OrigID   int64  `json:"origId"`
		Path     string `json:"path"`
		Caption  string `json:"caption"`
		Captured int64  `json:"captured"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return err
	}
	if w.Tag == nil {
		return ErrNoVision // parks visibly, same contract as caption-without-vision
	}
	tags, err := w.Tag.Tags(ctx, p.Caption)
	if err != nil {
		return err
	}
	if len(tags) == 0 {
		w.Log.Info("no tags extracted", "fn", "doTags", "origId", p.OrigID)
		return nil
	}
	hash := frameHashFromPath(p.Path)
	if hash == "" {
		w.Log.Warn("tag pass: no frame hash in path, tags kept for search only", "fn", "doTags", "path", p.Path)
	} else {
		for _, tag := range tags {
			// User corrections outrank the model FOREVER: a tag the user removed is a tombstone row
			// (source 'user_removed'), and this NOT EXISTS keeps the model from resurrecting it.
			if err := w.Store.db.Exec(
				`INSERT INTO frame_tags (hash, tag, source, created_at)
				 SELECT $1, $2, 'model', $3
				 WHERE NOT EXISTS (SELECT 1 FROM frame_tags WHERE hash = $1 AND tag = $2)`,
				hash, tag, time.Now().UTC().Unix()); err != nil {
				return err
			}
		}
		// Derived display name: date + the first three tags. Set only when EMPTY , a user rename
		// (future endpoint) must never be overwritten by a background job.
		name := time.Unix(p.Captured, 0).UTC().Format("2006-01-02")
		n := 2 // SHORT: tags, description and place carry the detail; the name is a label
		if len(tags) < n {
			n = len(tags)
		}
		for _, t := range tags[:n] {
			name += " " + t
		}
		if err := w.Store.db.Exec(
			`UPDATE frames SET display_name = $1 WHERE hash = $2 AND (display_name IS NULL OR display_name = '')`,
			name, hash); err != nil {
			return err
		}
	}
	// Tags into the search surface too , one extra chunk makes every tag retrievable.
	captured := time.Unix(p.Captured, 0).UTC()
	ids, err := w.Store.InsertChunksT0("image", p.OrigID, captured, ChunkText("", "tags: "+strings.Join(tags, ", ")))
	if err != nil {
		return err
	}
	return w.Ingester.enqueueEmbeds(ids)
}

// frameHashFromPath extracts the 32-hex content hash from an archive filename (<hash>.<ext>).
func frameHashFromPath(path string) string {
	base := path
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if len(base) != 32 {
		return ""
	}
	for _, r := range base {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	return base
}

// doReconsolidate is spec 12 phase 2. The real regeneration belongs to the consolidation daemon
// (T1/T2 producer), which does not exist yet; until it does, the honest phase 2 for a row with zero
// surviving sources is deletion, and a row WITH survivors stays stale (excluded from every search
// path) rather than being un-staled with content that still cites deleted material. Stale-forever is
// the safe failure; un-staling without regeneration would be the privacy failure.
func (w *Worker) doReconsolidate(_ context.Context, job *Job) error {
	var p struct {
		Tier  int   `json:"tier"`
		RefID int64 `json:"refId"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return err
	}
	col := "entry_id"
	if p.Tier == 2 {
		col = "memory_id"
	}
	rows, err := w.Store.db.Query(
		"SELECT count(*) FROM search.citations WHERE tier = $1 AND ref_id = $2", p.Tier, p.RefID)
	if err != nil {
		return err
	}
	surviving := 0
	if len(rows.Vals) > 0 && rows.Vals[0][0] != nil {
		if n, perr := jsonAtoi(*rows.Vals[0][0]); perr == nil {
			surviving = n
		}
	}
	if surviving == 0 {
		return w.Store.db.Exec("DELETE FROM search.chunks WHERE tier = $1 AND "+col+" = $2", p.Tier, p.RefID)
	}
	w.Log.Info("reconsolidate deferred: row has survivors, stays stale until consolidation daemon exists",
		"fn", "doReconsolidate", "tier", p.Tier, "ref", p.RefID, "surviving", surviving)
	return nil
}

func metaCamera(meta map[string]any) string {
	if meta == nil {
		return ""
	}
	if c, ok := meta["camera"].(string); ok {
		return c
	}
	return ""
}

func jsonAtoi(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errNotNumber
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

var errNotNumber = jsonErr("not a number")

type jsonErr string

func (e jsonErr) Error() string { return string(e) }

// captionSection pulls one fixed heading's body out of the structured caption , the sections are a
// contract (spec 9.2), so this is a scan to the next ALL-CAPS heading, not a parser.
func captionSection(caption, heading string) string {
	i := strings.Index(caption, heading)
	if i < 0 {
		return ""
	}
	rest := caption[i+len(heading):]
	for _, next := range []string{"OBJECTS:", "PEOPLE:", "TEXT:", "COLOURS_STYLE:", "SETTING_GUESS:"} {
		if j := strings.Index(rest, next); j >= 0 {
			rest = rest[:j]
		}
	}
	out := strings.TrimSpace(rest)
	if r := []rune(out); len(r) > 900 {
		out = string(r[:900])
	}
	return out
}
