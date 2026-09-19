package framed

// Converge: the archive's own stock-take, run at every start and on demand.
//
// The question it answers is the one the operator kept answering by hand with ad-hoc SQL and a
// reprocess: "we have X photos and Y videos; are they ALL at the latest stage?" The stages a frame
// passes through, in order, and who owns each:
//
//	archived   bytes in the archive, a row in frames                       framed
//	derived    metadata read by the CURRENT pipeline (frames.pipe_ver)     framed
//	previewed  preview + thumb on disk (a frame grab for a video)          framed
//	described  frames.description, the caption's SCENE                     searchd (vision model)
//	titled     frames.display_name, date + first tags                      searchd (tag pass)
//	tagged     rows in frame_tags                                          searchd (tag pass)
//
// One query reads every row's facts (Store.Audit). For each row Converge does what framed owns
// (re-derive when the row predates PipelineVersion, previews when absent) and hands the rest to
// searchd as ENSURE notifies, which searchd answers by enqueueing exactly the missing work. A
// healthy archive costs one query and one log line: "N frames, all at the latest stage". Nothing
// here trusts a constant; the counts come from the rows.

import (
	"encoding/json"
	"fmt"
	"time"
)

// ConvergeReport is what a pass found and did. Printed at start, returned by `converge`, and the
// numbers the Box Status drill-in shows.
type ConvergeReport struct {
	Photos        int           `json:"photos"`
	Videos        int           `json:"videos"`
	Other         int           `json:"other"`
	AtLatest      int           `json:"atLatest"` // derived at PipelineVersion, previewed, described, titled, tagged
	Behind        int           `json:"behind"`   // pipe_ver < PipelineVersion at the start of the pass
	NoPreview     int           `json:"noPreview"`
	NoDescription int           `json:"noDescription"`
	NoTitle       int           `json:"noTitle"`
	NoTags        int           `json:"noTags"`
	Rederived     int           `json:"rederived"`    // rows re-read from their original this pass
	Previewed     int           `json:"previewed"`    // previews written this pass
	Notified      int           `json:"notified"`     // ensure notifies handed to searchd
	Unrenderable  int           `json:"unrenderable"` // videos with no frame grab: nothing for a vision model to see
	Took          time.Duration `json:"took"`         // nanoseconds, as Duration marshals
}

func (r ConvergeReport) String() string {
	total := r.Photos + r.Videos + r.Other
	if total == 0 {
		return "archive empty"
	}
	if r.AtLatest == total {
		return fmt.Sprintf("%d frames (%d photos, %d videos), all at the latest stage (pipeline v%d), %s",
			total, r.Photos, r.Videos, PipelineVersion, r.Took.Round(time.Millisecond))
	}
	return fmt.Sprintf("%d frames (%d photos, %d videos): %d at the latest stage; behind v%d: %d; no preview: %d; "+
		"undescribed: %d; untitled: %d; untagged: %d; unrenderable videos: %d , re-derived %d, previewed %d, asked searchd for %d, %s",
		total, r.Photos, r.Videos, r.AtLatest, PipelineVersion, r.Behind, r.NoPreview,
		r.NoDescription, r.NoTitle, r.NoTags, r.Unrenderable, r.Rederived, r.Previewed, r.Notified, r.Took.Round(time.Millisecond))
}

// ConvergeState is the row framed publishes in daemon_state under "converge": the live progress
// of a running pass, then its result. The phone's Box Status screen draws it; hw serves it.
// Rows counted at the START of the pass, repairs as they happen, one write per 200 repairs.
type ConvergeState struct {
	Running    bool           `json:"running"`
	StartedAt  int64          `json:"startedAt"`  // unix seconds
	FinishedAt int64          `json:"finishedAt"` // 0 while running
	ToDo       int            `json:"toDo"`       // rows needing anything, counted at the start
	Done       int            `json:"done"`       // rows handled so far (re-derived and/or notified)
	Report     ConvergeReport `json:"report"`
	Summary    string         `json:"summary"`
}

func (p *Pipeline) publish(st ConvergeState) {
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	if err := p.store.SetState("converge", b); err != nil {
		p.log.Warn("converge: state publish failed", "fn", "publish", "err", err)
	}
}

// Stages is the read-only half of Converge: the same stock-take, no repairs. Cheap enough for a
// status screen.
func (p *Pipeline) Stages() (ConvergeReport, error) {
	t0 := time.Now()
	rows, err := p.store.Audit()
	if err != nil {
		return ConvergeReport{}, err
	}
	r := tally(rows)
	r.Took = time.Since(t0)
	return r, nil
}

func tally(rows []Audit) ConvergeReport {
	var r ConvergeReport
	for _, a := range rows {
		switch a.Kind {
		case "photo":
			r.Photos++
		case "video":
			r.Videos++
		default:
			r.Other++
			continue // an unknown blob has no stages to be at; it is counted and left alone
		}
		behind := a.PipeVer < PipelineVersion
		noPrev := a.PreviewPath == "" || a.ThumbPath == ""
		if behind {
			r.Behind++
		}
		if noPrev {
			r.NoPreview++
		}
		if !a.Described {
			r.NoDescription++
		}
		if !a.Titled {
			r.NoTitle++
		}
		if !a.Tagged {
			r.NoTags++
		}
		if !behind && !noPrev && a.Described && a.Titled && a.Tagged {
			r.AtLatest++
		}
	}
	return r
}

// Converge runs the stock-take and repairs. Under the drain mutex, like Reprocess: a start-up pass
// racing the resume drain over the same rows is noise. Progress is logged every 200 repairs so a
// big first pass (the one after a pipeline bump) is visible while it runs.
func (p *Pipeline) Converge() ConvergeReport {
	p.mu.Lock()
	defer p.mu.Unlock()
	t0 := time.Now()
	rows, err := p.store.Audit()
	if err != nil {
		p.log.Error("converge: audit query failed", "fn", "Converge", "err", err)
		return ConvergeReport{Took: time.Since(t0)}
	}
	r := tally(rows)
	gpsDays := map[string]bool{}
	work, logged := 0, 0
	st := ConvergeState{Running: true, StartedAt: t0.Unix(), ToDo: r.Photos + r.Videos - r.AtLatest, Report: r}
	p.publish(st)
	for _, a := range rows {
		if a.Kind != "photo" && a.Kind != "video" {
			continue
		}
		behind := a.PipeVer < PipelineVersion
		noPrev := a.PreviewPath == "" || a.ThumbPath == ""
		render := renderFor(a.Kind, a.ArchivePath, a.PreviewPath)
		takenAt := a.TakenAt
		touched := behind || noPrev || !a.Described || !a.Titled || !a.Tagged
		if behind || noPrev {
			// framed's own repairs: read the original again with today's pipeline. This also
			// stamps pipe_ver, so the row is not re-read at the next start.
			f, prev, derr := p.derive(a.ArchivePath, false)
			if derr != nil {
				p.log.Warn("converge: re-derive failed", "fn", "Converge", "hash", a.Hash, "err", derr)
			} else {
				r.Rederived++
				if prev {
					r.Previewed++
				}
				if f.HasGPS {
					gpsDays[time.Unix(f.TakenAt, 0).UTC().Format("2006-01-02")] = true
				}
				render = renderFor(f.Kind, f.ArchivePath, f.PreviewPath)
				takenAt = f.TakenAt
			}
			work++
		}
		if !a.Described || !a.Titled || !a.Tagged {
			// searchd's stages. It answers an ensure by enqueueing exactly what is missing for a
			// frame it already knows, or ingesting one it never saw. A video with no grab has
			// nothing a vision model can look at; that is counted, not hidden.
			if render == "" {
				r.Unrenderable++
			} else if p.notifySearch != nil {
				p.notifySearch(a.ArchivePath, render, takenAt, true)
				r.Notified++
			}
			work++
		}
		if touched {
			st.Done++
		}
		if work/200 > logged {
			logged = work / 200
			p.log.Info("converge progress", "fn", "Converge", "repairs", work, "rederived", r.Rederived, "notified", r.Notified)
			st.Report = r
			p.publish(st)
		}
	}
	for day := range gpsDays {
		p.RebuildDay(day)
	}
	r.Took = time.Since(t0)
	p.log.Info("converge: "+r.String(), "fn", "Converge")
	st.Running, st.FinishedAt, st.Report, st.Summary = false, time.Now().Unix(), r, r.String()
	p.publish(st)
	return r
}
