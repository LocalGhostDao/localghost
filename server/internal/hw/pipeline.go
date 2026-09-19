package hw

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// PipelineStage is one stage's count: how many photos and videos have it, out of how many.
type PipelineStage struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// PipelineQueue is searchd's work still waiting for one job kind.
type PipelineQueue struct {
	Pending int `json:"pending"` // runnable (attempts < 5)
	Parked  int `json:"parked"`  // five failures, waiting for `unpark` or an ensure
}

// PipelineProgress is the whole picture the Box Status screen draws: where the archive is, stage
// by stage, what is still queued, how fast descriptions are landing, and the stock-take's own
// state. Counts come from frames and search.jobs (flat SELECTs, a few ms at 40k rows); the
// converge block is what framed last published in daemon_state.
type PipelineProgress struct {
	PipelineVersion int `json:"pipelineVersion"` // the highest any row carries
	Photos          int `json:"photos"`
	Videos          int `json:"videos"`
	Other           int `json:"other"`
	Total           int `json:"total"` // photos + videos, the rows that have stages

	Derived   PipelineStage `json:"derived"`   // pipe_ver at the highest version
	Previewed PipelineStage `json:"previewed"` // preview + thumb on disk
	Described PipelineStage `json:"described"`
	Titled    PipelineStage `json:"titled"`
	Tagged    PipelineStage `json:"tagged"`
	AtLatest  PipelineStage `json:"atLatest"` // all of the above

	Caption PipelineQueue `json:"caption"`
	Tag     PipelineQueue `json:"tag"`
	Embed   PipelineQueue `json:"embed"`

	DescribedLastHour int   `json:"describedLastHour"`
	DescribedLastDay  int   `json:"describedLastDay"`
	LastDescribedAt   int64 `json:"lastDescribedAt"` // unix seconds, 0 = never
	// EtaSeconds is how long the undescribed rows take at the last hour's pace; -1 when there is
	// no pace to measure (nothing described in the last hour), 0 when nothing is left.
	EtaSeconds int64 `json:"etaSeconds"`

	// Converge is framed's own account of the stock-take, verbatim from daemon_state (see
	// framed.ConvergeState); null when framed has never published one (a box before this build).
	Converge          json.RawMessage `json:"converge,omitempty"`
	ConvergeUpdatedAt int64           `json:"convergeUpdatedAt"`
	Now               int64           `json:"now"`
}

// PipelineProgress reads the archive's stage counts and searchd's queue in one place.
func (s *NotifStore) PipelineProgress(slot int) (PipelineProgress, error) {
	c, err := s.pg(slot)
	if err != nil {
		return PipelineProgress{}, err
	}
	return PipelineProgressFrom(c)
}

// PipelineProgressFrom is PipelineProgress over any connection (the Postgres-backed test uses it).
func PipelineProgressFrom(c *poltergres.ReadWrite) (PipelineProgress, error) {
	var p PipelineProgress
	p.Now = time.Now().UTC().Unix()
	ints := func(q string, args ...any) []int {
		rows, qerr := c.Query(q, args...)
		if qerr != nil || len(rows.Vals) == 0 {
			return nil
		}
		out := make([]int, len(rows.Vals[0]))
		for i, v := range rows.Vals[0] {
			if v != nil {
				out[i], _ = strconv.Atoi(*v)
			}
		}
		return out
	}
	at := func(v []int, i int) int {
		if i < len(v) {
			return v[i]
		}
		return 0
	}
	// One pass over frames for every stage count. The version is read from the rows, so a box
	// where nothing has been re-derived yet reports the version it actually has.
	v := ints(`
		WITH ver AS (SELECT coalesce(max(pipe_ver), 0) AS v FROM frames)
		SELECT (SELECT v FROM ver),
		       count(*) FILTER (WHERE kind = 'photo'),
		       count(*) FILTER (WHERE kind = 'video'),
		       count(*) FILTER (WHERE kind NOT IN ('photo','video')),
		       count(*) FILTER (WHERE staged AND pipe_ver >= (SELECT v FROM ver)),
		       count(*) FILTER (WHERE staged AND preview_path <> '' AND thumb_path <> ''),
		       count(*) FILTER (WHERE staged AND description <> ''),
		       count(*) FILTER (WHERE staged AND display_name <> ''),
		       count(*) FILTER (WHERE staged AND tagged),
		       count(*) FILTER (WHERE staged AND pipe_ver >= (SELECT v FROM ver) AND preview_path <> '' AND thumb_path <> ''
		                          AND description <> '' AND display_name <> '' AND tagged),
		       count(*) FILTER (WHERE staged AND description <> '' AND described_at >= $1),
		       count(*) FILTER (WHERE staged AND description <> '' AND described_at >= $2),
		       coalesce(max(described_at), 0)
		FROM (SELECT f.*, f.kind IN ('photo','video') AS staged,
		             EXISTS (SELECT 1 FROM frame_tags t WHERE t.hash = f.hash) AS tagged
		      FROM frames f) x`, p.Now-3600, p.Now-86400)
	p.PipelineVersion = at(v, 0)
	p.Photos, p.Videos, p.Other = at(v, 1), at(v, 2), at(v, 3)
	p.Total = p.Photos + p.Videos
	p.Derived = PipelineStage{at(v, 4), p.Total}
	p.Previewed = PipelineStage{at(v, 5), p.Total}
	p.Described = PipelineStage{at(v, 6), p.Total}
	p.Titled = PipelineStage{at(v, 7), p.Total}
	p.Tagged = PipelineStage{at(v, 8), p.Total}
	p.AtLatest = PipelineStage{at(v, 9), p.Total}
	p.DescribedLastHour, p.DescribedLastDay = at(v, 10), at(v, 11)
	p.LastDescribedAt = int64(at(v, 12))

	q := ints(`
		SELECT count(*) FILTER (WHERE kind = 'caption' AND attempts < 5),
		       count(*) FILTER (WHERE kind = 'caption' AND attempts >= 5),
		       count(*) FILTER (WHERE kind = 'tag' AND attempts < 5),
		       count(*) FILTER (WHERE kind = 'tag' AND attempts >= 5),
		       count(*) FILTER (WHERE kind = 'embed_text' AND attempts < 5),
		       count(*) FILTER (WHERE kind = 'embed_text' AND attempts >= 5)
		FROM search.jobs`)
	p.Caption = PipelineQueue{at(q, 0), at(q, 1)}
	p.Tag = PipelineQueue{at(q, 2), at(q, 3)}
	p.Embed = PipelineQueue{at(q, 4), at(q, 5)}

	left := p.Described.Total - p.Described.Done
	switch {
	case left <= 0:
		p.EtaSeconds = 0
	case p.DescribedLastHour == 0:
		p.EtaSeconds = -1
	default:
		p.EtaSeconds = int64(float64(left) / float64(p.DescribedLastHour) * 3600)
	}

	if rows, qerr := c.Query(
		`SELECT value, updated_at FROM daemon_state WHERE daemon = 'ghost.framed' AND key = 'converge'`); qerr == nil && len(rows.Vals) == 1 {
		if val := rows.Vals[0][0]; val != nil && json.Valid([]byte(*val)) {
			p.Converge = json.RawMessage(*val)
		}
		if ts := rows.Vals[0][1]; ts != nil {
			p.ConvergeUpdatedAt, _ = strconv.ParseInt(*ts, 10, 64)
		}
	}
	return p, nil
}
