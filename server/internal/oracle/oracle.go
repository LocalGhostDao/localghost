// Package oracle is the client contract for ghost.oracled, the box's inference broker. Callers speak
// in CAPABILITIES ("classify", "summarize", "chat") and model CLASSES ("local-small", "frontier"),
// never a concrete model. oracled's conf maps a capability+class to an actual backend , gemma-4-12b
// via a private llama-server child today, a frontier HTTPS endpoint tomorrow , so swapping the model
// is a conf change in oracled, invisible to every caller here.
//
// A single local model is a serial resource: it does one inference at a time. So oracled puts a
// priority queue in front. Interactive work (a person waiting) jumps background work (watchd's log
// triage, cued's evaluation), and a request with a deadline is dropped from the queue if it ages out
// before it runs , which is what lets watchd say "answer in 2s or I fall back to the safe default".
package oracle

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
)

// Class is the model tier. oracled resolves a class to a concrete backend from its conf.
type Class string

const (
	ClassLocalSmall Class = "local-small" // gemma-4-12b on the box today
	ClassFrontier   Class = "frontier"    // a remote model, only if the owner enabled one in conf
)

// Priority orders the queue.
type Priority int

const (
	PriorityBackground  Priority = iota // watchd log analysis, cued evaluation , can wait, can be dropped
	PriorityInteractive                 // a person is waiting on this reply , jumps the queue
)

// Request is one inference ask.
type Request struct {
	Capability  string   `json:"capability"`  // what to do: classify | summarize | chat
	Class       Class    `json:"class"`       // which tier; oracled resolves to a concrete model
	Priority    Priority `json:"priority"`    // background vs interactive
	Input       string   `json:"input"`       // the prompt/content
	Images      []string `json:"images,omitempty"` // volume paths of images for multimodal requests (captioning)
	// Think is PROMPTED deliberation depth: "" (answer directly), "brief" (think, then answer), or
	// "deep" (reason at length before answering). HONEST MECHANICS: gemma has no native reasoning
	// API, so oracled implements this as an instruction prefix plus a larger token budget , the model
	// is asked to show its working, not switched into a different mode.
	Think       string   `json:"think,omitempty"`
	MaxTokens   int      `json:"maxTokens"`   // 0 = backend default
	Temperature float64  `json:"temperature"` // 0 = backend default
	DeadlineMS  int      `json:"deadlineMS"`  // 0 = no deadline; else drop if not STARTED within this
}

// Response is the result. Model reports which concrete model actually served it, for the caller's
// logs , not for routing (callers must stay model-agnostic).
type Response struct {
	Output string `json:"output"`
	Model  string `json:"model"`
	Err    string `json:"err,omitempty"`
}

// Client submits requests to ghost.oracled over its control socket. It is a thin wrapper over ctlsock
// so oracled speaks the same protocol as every other service (ghost-cli can hit it too).
type Client struct {
	c *ctlsock.Client
}

// NewClient targets ghost.oracled's socket under runDir (<mount>/run). timeout should exceed a
// realistic inference time for interactive use; watchd passes a short one for background triage and
// treats a timeout as "no guidance, fall back".
func NewClient(runDir string, timeout time.Duration) *Client {
	return &Client{c: ctlsock.NewClientTimeout("ghost.oracled", runDir, timeout)}
}

// ErrPreempted is the Err of a background request oracled cancelled because a person started using
// the model (a streamed chat). The caller did nothing wrong: it retries later, and a job queue must not
// count it against the job's attempts.
const ErrPreempted = "preempted: a person is using the model, retry later"

// OnGPU asks oracled where the model runs now (its `models` answer): on the GPU an answer takes
// seconds, on the CPU minutes, and background callers size their deadlines from it.
func (c *Client) OnGPU() (bool, error) {
	resp, err := c.c.Call("models", nil)
	if err != nil {
		return false, err
	}
	if !resp.OK {
		return false, errors.New(resp.Err)
	}
	var m struct {
		OnGPU bool `json:"onGPU"`
		Ready bool `json:"ready"`
	}
	if err := json.Unmarshal(resp.Data, &m); err != nil {
		return false, err
	}
	if !m.Ready {
		return false, errors.New("the model is not loaded yet")
	}
	return m.OnGPU, nil
}

// Infer submits a request and blocks for the response (or the client timeout).
func (c *Client) Infer(req Request) (Response, error) {
	resp, err := c.c.Call("infer", req)
	if err != nil {
		return Response{}, err
	}
	var out Response
	if len(resp.Data) > 0 {
		if e := json.Unmarshal(resp.Data, &out); e != nil {
			return Response{}, e
		}
	}
	return out, nil
}
