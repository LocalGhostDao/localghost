package main

// outingPass , MEMORIES FROM THE PHOTOS. Every pass (once the archive changed since the last
// one) it reads the photo frames , when, where, which place the box resolved, which tags the
// pipeline gave , groups them into outings (internal/outings: runs close in time and place, a
// home cell to say which are trips), asks the trail how far the person moved over each, and
// writes one memory per outing (kind='outing', source_ref='outing:<day>', body from a template
// over real numbers, meta as JSON for the app's cards). Then the TASTE: which tags recur across
// the days with a camera out, folded onto the fixed interests, written to settings for /v1/taste
// and /v1/nearby. No model call anywhere: a memory of a trip exists the week it happened,
// whether or not a GPU is alive; the model may polish prose later. User edits and tombstones
// outrank regeneration, the standing rule; outings that dissolve on a re-clustering (a merge, a
// split) are removed unless the person touched them.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/framed"
	"github.com/LocalGhostDao/localghost/server/internal/outings"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

const outingMinInterval = 30 * time.Minute

var lastOutingPass time.Time

// outingPass returns how many outing memories were written or refreshed.
func outingPass(db *poltergres.ReadWrite, lg *slog.Logger) (int, error) {
	if time.Since(lastOutingPass) < outingMinInterval {
		return 0, nil
	}
	// The signature of the archive: a pass only when something changed since the last one.
	sig := ""
	if rows, err := db.Query("SELECT count(*)::text || ':' || coalesce(max(taken_at),0)::text || ':' || (SELECT count(*) FROM frame_tags)::text || ':' || (SELECT count(*) FROM location_points)::text FROM frames WHERE kind = 'photo'"); err == nil && len(rows.Vals) == 1 && rows.Vals[0][0] != nil {
		sig = *rows.Vals[0][0]
	}
	if rows, err := db.Query("SELECT value FROM settings WHERE key = 'synthd_outings_sig'"); err == nil && len(rows.Vals) == 1 && rows.Vals[0][0] != nil && *rows.Vals[0][0] == sig && sig != "" {
		lastOutingPass = time.Now()
		return 0, nil
	}

	// the frames
	rows, err := db.Query("SELECT hash, taken_at, lat, lon, has_gps, place FROM frames WHERE kind = 'photo' AND taken_at > 0 ORDER BY taken_at")
	if err != nil {
		return 0, err
	}
	frames := make([]outings.Frame, 0, len(rows.Vals))
	byHash := map[string]int{}
	for _, v := range rows.Vals {
		if len(v) < 6 || v[0] == nil || v[1] == nil {
			continue
		}
		f := outings.Frame{Hash: *v[0]}
		f.TakenAt, _ = strconv.ParseInt(*v[1], 10, 64)
		if v[2] != nil {
			f.Lat, _ = strconv.ParseFloat(*v[2], 64)
		}
		if v[3] != nil {
			f.Lon, _ = strconv.ParseFloat(*v[3], 64)
		}
		f.HasGPS = v[4] != nil && *v[4] == "t"
		if v[5] != nil {
			f.Place = *v[5]
		}
		byHash[f.Hash] = len(frames)
		frames = append(frames, f)
	}
	// the tags, in one pass: a quarter million rows on a big archive, once per change
	rows, err = db.Query("SELECT hash, tag, category FROM frame_tags WHERE source <> 'user_removed'")
	if err != nil {
		return 0, err
	}
	for _, v := range rows.Vals {
		if len(v) < 3 || v[0] == nil || v[1] == nil {
			continue
		}
		i, ok := byHash[*v[0]]
		if !ok {
			continue
		}
		cat := ""
		if v[2] != nil {
			cat = *v[2]
		}
		frames[i].Tags = append(frames[i].Tags, outings.Tag{Name: *v[1], Category: cat})
	}

	outs, home := outings.Build(frames)
	written := 0
	keep := make([]string, 0, len(outs))
	now := time.Now().UnixMilli()
	for _, o := range outs {
		o.DistanceM = outingDistance(db, o.Start, o.End)
		keep = append(keep, o.Ref)
		meta, _ := json.Marshal(o)
		title, body := o.Title(), o.Body()
		ex, qerr := db.Query("SELECT id, user_edited, tombstoned, body FROM memories WHERE kind = 'outing' AND source_ref = $1", o.Ref)
		if qerr != nil {
			return written, qerr
		}
		if len(ex.Vals) > 0 {
			v := ex.Vals[0]
			if (len(v) > 1 && v[1] != nil && *v[1] == "t") || (len(v) > 2 && v[2] != nil && *v[2] == "t") {
				continue // the person's version outranks the machine's, forever
			}
			if len(v) > 3 && v[3] != nil && *v[3] == body {
				// unchanged prose; refresh the meta only (covers, tags) without churning updated_at
				if err := db.Exec("UPDATE memories SET meta = $1::jsonb WHERE id = $2", string(meta), *v[0]); err != nil {
					return written, err
				}
				continue
			}
			if err := db.Exec("UPDATE memories SET title = $1, body = $2, meta = $3::jsonb, updated_at = $4 WHERE id = $5",
				title, body, string(meta), now, *v[0]); err != nil {
				return written, err
			}
		} else {
			if err := db.Exec(
				"INSERT INTO memories (title, body, kind, source_ref, meta, created_at, updated_at) VALUES ($1,$2,'outing',$3,$4::jsonb,$5,$6)",
				title, body, o.Ref, string(meta), o.End*1000, now); err != nil {
				return written, err
			}
		}
		written++
	}
	// outings that dissolved on re-clustering go, unless the person touched them
	if len(keep) > 0 {
		if err := db.Exec("DELETE FROM memories WHERE kind = 'outing' AND NOT user_edited AND NOT tombstoned AND NOT (source_ref = ANY($1))",
			"{"+strings.Join(keep, ",")+"}"); err != nil {
			lg.Warn("stale outings not removed", "fn", "outingPass", "err", err)
		}
	}
	if err := tastePass(db, lg); err != nil {
		lg.Warn("taste not written", "fn", "outingPass", "err", err)
	}
	_ = db.Exec("INSERT INTO settings (key, value) VALUES ('synthd_outings_sig',$1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", sig)
	lastOutingPass = time.Now()
	if home.Known {
		lg.Info("outings built", "fn", "outingPass", "outings", len(outs), "written", written, "homeDays", home.Days)
	} else {
		lg.Info("outings built", "fn", "outingPass", "outings", len(outs), "written", written, "home", "unknown")
	}
	return written, nil
}

// outingDistance is how far the trail says the person moved between the outing's first and last
// photo (plus an hour either side), over the cleaned points, jitter excluded , the same rule the
// day paths use.
func outingDistance(db *poltergres.ReadWrite, start, end int64) float64 {
	rows, err := db.Query("SELECT ts, lat, lon FROM location_points WHERE ts >= $1 AND ts <= $2 ORDER BY ts", start-3600, end+3600)
	if err != nil || len(rows.Vals) < 2 {
		return 0
	}
	pts := make([]framed.TrackPoint, 0, len(rows.Vals))
	for _, v := range rows.Vals {
		if len(v) < 3 || v[0] == nil || v[1] == nil || v[2] == nil {
			continue
		}
		var p framed.TrackPoint
		p.TS, _ = strconv.ParseInt(*v[0], 10, 64)
		p.Lat, _ = strconv.ParseFloat(*v[1], 64)
		p.Lon, _ = strconv.ParseFloat(*v[2], 64)
		pts = append(pts, p)
	}
	cleaned, _ := framed.CleanTrack(pts)
	return framed.TrackDistanceM(cleaned)
}

// tastePass writes what the archive says the person likes to settings 'synthd_taste'.
func tastePass(db *poltergres.ReadWrite, lg *slog.Logger) error {
	var totalDays, totalPhotos int
	if rows, err := db.Query("SELECT count(DISTINCT taken_at / 86400)::text, count(*)::text FROM frames WHERE kind = 'photo' AND taken_at > 0"); err == nil && len(rows.Vals) == 1 && len(rows.Vals[0]) == 2 {
		if rows.Vals[0][0] != nil {
			totalDays, _ = strconv.Atoi(*rows.Vals[0][0])
		}
		if rows.Vals[0][1] != nil {
			totalPhotos, _ = strconv.Atoi(*rows.Vals[0][1])
		}
	} else if err != nil {
		return err
	}
	rows, err := db.Query(`
		SELECT t.tag, t.category, count(DISTINCT f.taken_at / 86400)::text, count(*)::text
		FROM frame_tags t JOIN frames f ON f.hash = t.hash
		WHERE t.source <> 'user_removed' AND f.kind = 'photo' AND f.taken_at > 0
		GROUP BY t.tag, t.category HAVING count(*) >= 2
		ORDER BY 3 DESC, 4 DESC LIMIT 400`)
	if err != nil {
		return err
	}
	var trs []outings.TasteRow
	for _, v := range rows.Vals {
		if len(v) < 4 || v[0] == nil {
			continue
		}
		r := outings.TasteRow{Tag: *v[0]}
		if v[1] != nil {
			r.Category = *v[1]
		}
		if v[2] != nil {
			r.Days, _ = strconv.Atoi(*v[2])
		}
		if v[3] != nil {
			r.Photos, _ = strconv.Atoi(*v[3])
		}
		trs = append(trs, r)
	}
	taste := outings.BuildTaste(trs, totalDays, totalPhotos, time.Now().Unix())
	b, _ := json.Marshal(taste)
	if err := db.Exec("INSERT INTO settings (key, value) VALUES ('synthd_taste',$1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", string(b)); err != nil {
		return err
	}
	lg.Info("taste written", "fn", "tastePass", "likes", len(taste.Likes), "interests", len(taste.Interests), "days", totalDays)
	return nil
}

// outingsSummary is the ctlsock view: counts and the taste's one line, for health and gpu-free debugging.
func outingsSummary(db *poltergres.ReadWrite) map[string]any {
	out := map[string]any{}
	if rows, err := db.Query("SELECT count(*)::text, count(*) FILTER (WHERE (meta->>'away')::boolean)::text FROM memories WHERE kind = 'outing' AND NOT tombstoned"); err == nil && len(rows.Vals) == 1 && len(rows.Vals[0]) == 2 {
		if rows.Vals[0][0] != nil {
			out["outings"], _ = strconv.Atoi(*rows.Vals[0][0])
		}
		if rows.Vals[0][1] != nil {
			out["trips"], _ = strconv.Atoi(*rows.Vals[0][1])
		}
	}
	if rows, err := db.Query("SELECT value FROM settings WHERE key = 'synthd_taste'"); err == nil && len(rows.Vals) == 1 && rows.Vals[0][0] != nil {
		var t outings.Taste
		if json.Unmarshal([]byte(*rows.Vals[0][0]), &t) == nil {
			out["taste"] = t.Summary
			out["tasteBuiltAt"] = t.BuiltAt
		}
	}
	if len(out) == 0 {
		out["note"] = fmt.Sprintf("no outings yet; the pass runs every %s once the archive changes", outingMinInterval)
	}
	return out
}
