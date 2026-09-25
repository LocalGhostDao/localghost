package search

// Ingest (spec 4, 9.1): tombstone check, dedup insert, chunk, enqueue. The write path never waits for
// a GPU , embedding and captioning are jobs. No cross-call transactions exist in poltergres (deliberate),
// so the original-then-chunks pair is made crash-safe by healing instead: a duplicate insert returns
// the existing id, and an original with zero chunks gets re-chunked. Same outcome as the spec's
// transaction, one mechanism instead of two.

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	_ "image/jpeg" // decoders for pHash
	_ "image/png"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Ingester struct {
	Store *Store
	Log   *slog.Logger
}

// IngestText ingests a text-bearing original (email/message/document/audio-transcript). body is the
// extracted text; header is the context header (spec 5). Returns the original id.
func (in *Ingester) IngestText(o Original, header, body string) (int64, error) {
	sha := sha256.Sum256([]byte(body))
	if o.SHA256 == nil {
		o.SHA256 = sha[:]
	}
	if dead, err := in.Store.Tombstoned(o.SHA256); err != nil {
		return 0, err
	} else if dead {
		return 0, fmt.Errorf("refused: content is tombstoned (deleted content never returns)")
	}
	id, existed, err := in.Store.InsertOriginal(o)
	if err != nil {
		return 0, err
	}
	if existed {
		if n, err := in.Store.ChunkCount(0, o.Source, id); err == nil && n > 0 {
			return id, nil // fully ingested before; dedup stop (spec 4 step 3)
		}
		// fall through: heal the half-done ingest
	}
	if o.Source == "email" {
		body = StripQuotedEmail(body)
	}
	chunks := ChunkText(header, body)
	if len(chunks) == maxChunks {
		in.Log.Warn("chunk cap hit, truncated", "fn", "IngestText", "source", o.Source, "id", id)
	}
	ids, err := in.Store.InsertChunksT0(o.Source, id, o.CapturedAt, chunks)
	if err != nil {
		return id, err
	}
	if err := in.enqueueEmbeds(ids); err != nil {
		return id, err
	}
	return id, nil
}

// IngestImage ingests an image (spec 9.1): pHash burst-collapse, meta, caption job. The caption
// arrives later and creates the chunks; the image is findable by filters immediately and by text once
// captioned.
func (in *Ingester) IngestImage(o Original, imageBytes []byte) (int64, error) {
	sha := sha256.Sum256(imageBytes)
	if o.SHA256 == nil {
		o.SHA256 = sha[:]
	}
	if dead, err := in.Store.Tombstoned(o.SHA256); err != nil {
		return 0, err
	} else if dead {
		return 0, fmt.Errorf("refused: content is tombstoned")
	}
	id, existed, err := in.Store.InsertOriginal(o)
	if err != nil {
		return 0, err
	}
	if existed {
		return id, nil // image dedup is exact-hash; near-dup handled below for new rows only
	}
	img, _, derr := image.Decode(bytes.NewReader(imageBytes))
	if derr != nil {
		in.Log.Warn("image undecodable, ingested without phash/caption", "fn", "IngestImage", "id", id, "err", derr)
		return id, nil
	}
	ph := DHash(img)
	if err := in.Store.SetPhash(id, ph); err != nil {
		return id, err
	}
	if repID, dist, found, err := in.Store.NearestPhash(ph, id); err == nil && found && dist <= 6 {
		// Burst sibling (spec 9.1 step 2): mark and skip captioning; reachable via its representative.
		in.Log.Info("burst sibling, caption skipped", "fn", "IngestImage", "id", id, "dupOf", repID, "hamming", dist)
		return id, in.Store.db.Exec(
			`UPDATE search.originals SET meta = meta || jsonb_build_object('dup_of', $1::bigint)
			 WHERE source = 'image' AND id = $2`, repID, id)
	}
	return id, in.Store.EnqueueJob("caption", map[string]any{"origId": id, "path": o.Path})
}

// IngestImageFile is the frame path framed uses: identity is the sha256 of the ARCHIVED bytes,
// streamed (a video is hundreds of MB and never belongs in memory), while the picture the model
// looks at is the RENDER (framed's upright 1600px preview, or the frame grab of a clip). Unlike
// IngestImage, an original that already exists is not a full stop: with ensure set, the stages
// it is missing are queued, which is how a caption that parked in a bad week, or a video from
// before videos were captioned, gets its description without anyone running a script.
func (in *Ingester) IngestImageFile(o Original, identityPath, renderPath string, ensure bool) (int64, error) {
	if renderPath == "" {
		renderPath = identityPath
	}
	if ensure && o.SHA256 == nil {
		// A stock-take asks about frames searchd mostly already knows: find them by the frame
		// hash in the archive filename (a sha256 prefix) before paying to hash the bytes.
		if id, err := in.Store.OriginalIDByFrameHash(frameHashFromPath(identityPath)); err == nil && id != 0 {
			return id, in.ensureCaptioned(id, renderPath)
		}
	}
	if o.SHA256 == nil {
		f, err := os.Open(identityPath)
		if err != nil {
			return 0, err
		}
		h := sha256.New()
		_, cerr := io.Copy(h, f)
		_ = f.Close()
		if cerr != nil {
			return 0, cerr
		}
		o.SHA256 = h.Sum(nil)
	}
	if dead, err := in.Store.Tombstoned(o.SHA256); err != nil {
		return 0, err
	} else if dead {
		return 0, fmt.Errorf("refused: content is tombstoned")
	}
	id, existed, err := in.Store.InsertOriginal(o)
	if err != nil {
		return 0, err
	}
	if existed {
		if !ensure {
			return id, nil
		}
		return id, in.ensureCaptioned(id, renderPath)
	}
	img, derr := decodeFile(renderPath)
	if derr != nil {
		in.Log.Warn("render undecodable, ingested without phash; caption still queued", "fn", "IngestImageFile", "id", id, "render", renderPath, "err", derr)
		return id, in.Store.EnqueueJob("caption", map[string]any{"origId": id, "path": renderPath})
	}
	ph := DHash(img)
	if err := in.Store.SetPhash(id, ph); err != nil {
		return id, err
	}
	if repID, dist, found, err := in.Store.NearestPhash(ph, id); err == nil && found && dist <= 6 {
		// Burst sibling (spec 9.1 step 2): mark, and take the representative's caption so this
		// frame is described and titled too; it is the same picture to a person.
		in.Log.Info("burst sibling, caption copied from representative", "fn", "IngestImageFile", "id", id, "dupOf", repID, "hamming", dist)
		if err := in.Store.db.Exec(
			`UPDATE search.originals SET meta = meta || jsonb_build_object('dup_of', $1::bigint)
			 WHERE source = 'image' AND id = $2`, repID, id); err != nil {
			return id, err
		}
		return id, in.ensureCaptioned(id, renderPath)
	}
	return id, in.Store.EnqueueJob("caption", map[string]any{"origId": id, "path": renderPath})
}

// ensureCaptioned queues or completes exactly the missing stage for a known original:
//   - a caption already in meta: re-apply it (description, title, tags) where the frame lacks them;
//   - a burst sibling: copy the representative's caption and apply it;
//   - no caption, no live job: queue one against the render (a parked job is replaced);
//   - a runnable job already queued: nothing to do, it is on its way.
func (in *Ingester) ensureCaptioned(id int64, render string) error {
	st, err := in.Store.CaptionStateOf(id)
	if err != nil {
		return err
	}
	if st.Caption == "" && st.DupOf != 0 {
		rep, rerr := in.Store.CaptionStateOf(st.DupOf)
		if rerr == nil && rep.Caption != "" {
			if err := in.Store.SetCaption(id, rep.Caption); err != nil {
				return err
			}
			st.Caption = rep.Caption
		}
	}
	if st.Caption != "" {
		_, _, _, captured, oerr := in.Store.OriginalByID("image", id)
		if oerr != nil {
			return oerr
		}
		return in.ApplyCaption(id, render, st.Caption, captured)
	}
	if st.JobQueued && !st.JobParked {
		return nil
	}
	return in.Store.RequeueCaption(id, render)
}

// ApplyCaption is everything that follows a caption, idempotent, so the ensure path and the
// worker share one truth: the SCENE section onto frames.description (only where empty), the
// caption as searchable chunks (only when the original has none), and the tag pass queued (which
// writes tags and the title, both only where missing).
func (in *Ingester) ApplyCaption(origID int64, path, caption string, captured time.Time) error {
	if scene := captionSection(caption, "SCENE:"); scene != "" {
		if hash := frameHashFromPath(path); hash != "" {
			if err := in.Store.db.Exec(
				`UPDATE frames SET description = $1, described_at = $3 WHERE hash = $2 AND (description IS NULL OR description = '')`,
				scene, hash, time.Now().UTC().Unix()); err != nil {
				in.Log.Warn("description write failed", "fn", "ApplyCaption", "hash", hash, "err", err)
			}
		}
	}
	if n, cerr := in.Store.ChunkCount(0, "image", origID); cerr == nil && n == 0 {
		_, _, meta, _, oerr := in.Store.OriginalByID("image", origID)
		if oerr != nil {
			return oerr
		}
		header := ContextHeader("photo", captured.Format("2006-01-02"), metaCamera(meta))
		ids, err := in.Store.InsertChunksT0("image", origID, captured, ChunkText(header, caption))
		if err != nil {
			return err
		}
		if err := in.enqueueEmbeds(ids); err != nil {
			return err
		}
	}
	// The tag pass is a model call: run it only for a frame that still lacks a title or tags,
	// and only when one is not already queued (a parked one is replaced, like a caption).
	if hash := frameHashFromPath(path); hash != "" {
		if need, nerr := in.Store.FrameNeedsTagPass(hash); nerr == nil && !need {
			return nil
		}
	}
	queued, parked, jerr := in.Store.JobState("tag", origID)
	if jerr != nil {
		return jerr
	}
	if queued && !parked {
		return nil
	}
	if parked {
		if err := in.Store.db.Exec(`DELETE FROM search.jobs WHERE kind = 'tag' AND (payload->>'origId')::bigint = $1`, origID); err != nil {
			return err
		}
	}
	return in.Store.EnqueueJob("tag", map[string]any{
		"origId": origID, "path": path, "caption": caption, "captured": captured.Unix(),
	})
}

// decodeFile decodes an image file for the perceptual hash without holding more than that file.
func decodeFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, derr := image.Decode(f)
	if derr != nil && isWebP(path) {
		// framed's previews are WebP wherever cwebp is installed, and Go decodes no WebP: every
		// such frame was ingested without a perceptual hash, so bursts of them were never folded
		// and each sibling was captioned on its own. dwebp comes with cwebp.
		if img2, err2 := decodeWebP(path); err2 == nil {
			return img2, nil
		}
	}
	return img, derr
}

func isWebP(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var h [12]byte
	if _, err := io.ReadFull(f, h[:]); err != nil {
		return false
	}
	return string(h[0:4]) == "RIFF" && string(h[8:12]) == "WEBP"
}

// decodeWebP decodes through dwebp (the webp package) into a temporary PNG.
func decodeWebP(path string) (image.Image, error) {
	bin, err := exec.LookPath("dwebp")
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp("", "lg-phash-*.png")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	cmd := exec.Command(bin, "-quiet", path, "-o", tmp.Name())
	cmd.WaitDelay = 5 * time.Second
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("dwebp: %v %s", err, strings.TrimSpace(string(out)))
	}
	f, err := os.Open(tmp.Name())
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

func (in *Ingester) enqueueEmbeds(chunkIDs []int64) error {
	const batch = 64 // spec 4: claim up to 64 chunk ids per embed call
	for start := 0; start < len(chunkIDs); start += batch {
		end := start + batch
		if end > len(chunkIDs) {
			end = len(chunkIDs)
		}
		if err := in.Store.EnqueueJob("embed_text", map[string]any{"chunkIds": chunkIDs[start:end]}); err != nil {
			return err
		}
	}
	return nil
}

// Rebuild (spec 13.3): truncate derived chunks, re-chunk every original from its source-appropriate
// text, re-enqueue embeds. Images re-chunk from meta.caption , no re-captioning (that is a model
// migration, a different, expensive operation). Proof that indexes are derived state.
func (in *Ingester) Rebuild() (int, error) {
	if err := in.Store.RebuildTruncate(); err != nil {
		return 0, err
	}
	origs, err := in.Store.AllOriginals()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range origs {
		var body string
		switch o.Source {
		case "image":
			if c, ok := o.Meta["caption"].(string); ok {
				body = c
			}
		default:
			b, rerr := os.ReadFile(o.Path)
			if rerr != nil {
				in.Log.Warn("rebuild: original unreadable, skipped", "fn", "Rebuild",
					"source", o.Source, "id", o.ID, "err", rerr)
				continue
			}
			body = string(b)
		}
		if body == "" {
			continue
		}
		header := ContextHeader(o.Source, o.CapturedAt.Format("2006-01-02"))
		chunks := ChunkText(header, body)
		ids, cerr := in.Store.InsertChunksT0(o.Source, o.ID, o.CapturedAt, chunks)
		if cerr != nil {
			return n, cerr
		}
		if err := in.enqueueEmbeds(ids); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
