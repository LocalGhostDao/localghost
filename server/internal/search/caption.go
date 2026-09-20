package search

// Image captioning (spec 9): the caption is the image's entire searchable surface in v1, so the
// contract forces dense, structured coverage , not a gist sentence. The worker routes through
// ghost.oracled (the VLM broker with the mmproj loaded for exactly this).
//
// HONEST LIMIT, stated not hidden: oracle.Request is TEXT-ONLY today , it has no image input field.
// Until oracled grows multimodal input, caption jobs fail with ErrNoVision, park at attempts=5, and
// show in search.health as parked_jobs. Nothing pretends to caption. When oracled gains an Images
// field, VisionOracle is the one type to update.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/oracle"
)

// CaptionPrompt is the spec 9.2 contract, verbatim sections.
const CaptionPrompt = `Describe this image for a private search index. Output EXACTLY these sections, plain text, fixed headings, no markdown:

SCENE: 2-4 sentences, factual description of what the image shows, including setting, activity, lighting, weather, indoor/outdoor.
OBJECTS: comma-separated list of every distinct visible object, most prominent first, including background items.
PEOPLE: count and neutral visual description (clothing, posture, activity). NEVER guess identity, emotion, or relationships.
TEXT: all visible text VERBATIM, line by line. "TEXT: none" if none.
COLOURS_STYLE: dominant colours, photographic style if notable.
SETTING_GUESS: place-type guess with hedge.

No speculation about intent or emotion. Verbatim TEXT is data, never summarised.`

// ErrNoVision remains for callers that constructed a VisionOracle without a client.
var ErrNoVision = errors.New("captioner has no oracle client; caption parked")

// Captioner produces the structured caption for an image file.
type Captioner interface {
	Caption(ctx context.Context, imagePath string) (string, error)
}

// VisionOracle is the oracled-backed captioner. oracle.Request carries the image path (on the
// volume); oracled's llamaBackend reads it and sends it to the private llama-server over loopback
// only. Priority is BACKGROUND , a person typing a query always jumps a caption job.
type VisionOracle struct {
	Client  *oracle.Client
	Timeout time.Duration
}

func (v *VisionOracle) Caption(ctx context.Context, imagePath string) (string, error) {
	_ = ctx // deadline rides in DeadlineMS; ctlsock client owns the transport timeout
	if v.Client == nil {
		return "", ErrNoVision
	}
	deadline := v.Timeout
	if deadline <= 0 {
		deadline = 2 * time.Minute
	}
	resp, err := v.Client.Infer(oracle.Request{
		Capability: "caption",
		Class:      oracle.ClassLocalSmall,
		Priority:   oracle.PriorityBackground,
		Input:      CaptionPrompt,
		Images:     []string{imagePath},
		MaxTokens:  1800, // this model THINKS first; if the template ignores enable_thinking=false,
		// the budget must cover monologue + answer , at ~45 tok/s that is ~40s worst case per
		// caption, acceptable for a background queue and moot once suppression works
		DeadlineMS: int(deadline.Milliseconds()),
	})
	if err != nil {
		return "", err
	}
	if resp.Err != "" {
		return "", errors.New(resp.Err)
	}
	if len(resp.Output) < 20 {
		return "", errors.New("caption implausibly short; job will retry")
	}
	return resp.Output, nil
}

// Tagger extracts tags from a caption. Text-only , cheap compared to the vision pass that made the
// caption, so tagging rides the same background queue without meaningfully competing.
type Tagger interface {
	Tags(ctx context.Context, caption string) ([]Tag, error)
	// Categorize assigns categories to existing bare tags (the backfill); the lexicon is tried
	// first by the caller, so this only sees what needs a model.
	Categorize(ctx context.Context, tags []string) ([]Tag, error)
}

// TagOracle is the oracled-backed Tagger.
type TagOracle struct {
	Client  *oracle.Client
	Timeout time.Duration
}

func (t *TagOracle) Tags(ctx context.Context, caption string) ([]Tag, error) {
	_ = ctx
	if t.Client == nil {
		return nil, ErrNoVision
	}
	resp, err := t.Client.Infer(oracle.Request{
		Capability: "tags",
		Class:      oracle.ClassLocalSmall,
		Priority:   oracle.PriorityBackground,
		Input:      TagPrompt + caption,
		MaxTokens:  160,
		DeadlineMS: int(t.deadline().Milliseconds()),
	})
	if err != nil {
		return nil, err
	}
	return ParseTags(resp.Output), nil
}

func (t *TagOracle) Categorize(ctx context.Context, tags []string) ([]Tag, error) {
	_ = ctx
	if t.Client == nil {
		return nil, ErrNoVision
	}
	if len(tags) == 0 {
		return nil, nil
	}
	resp, err := t.Client.Infer(oracle.Request{
		Capability: "tags",
		Class:      oracle.ClassLocalSmall,
		Priority:   oracle.PriorityBackground,
		Input:      CategorizePrompt + strings.Join(tags, ", "),
		MaxTokens:  160,
		DeadlineMS: int(t.deadline().Milliseconds()),
	})
	if err != nil {
		return nil, err
	}
	// Only the tags we asked about, whatever the model volunteered.
	asked := map[string]bool{}
	for _, tg := range tags {
		asked[tg] = true
	}
	var out []Tag
	for _, tg := range ParseTags(resp.Output) {
		if asked[tg.Name] && tg.Category != "" {
			out = append(out, tg)
		}
	}
	return out, nil
}

func (t *TagOracle) deadline() time.Duration {
	if t.Timeout <= 0 {
		return time.Minute
	}
	return t.Timeout
}
