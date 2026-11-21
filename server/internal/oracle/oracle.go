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
	// ClassArbiter is ghost.shadowd's cold reader: by the charter a different model from the one
	// that talks to the person, made by other people. A box with one model has no backend for it,
	// and oracled then serves it from local-small and says so (Response.Served), so the record
	// reads "read cold by the same model" and never claims a second opinion it did not have.
	ClassArbiter Class = "arbiter"
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
	// Served is the class whose backend answered: the class asked for, or local-small when an
	// arbiter was asked for and the box has none of its own.
	Served Class  `json:"served,omitempty"`
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

// ErrArbiterDown is the Err of an arbiter request on a box that has a second model for the
// arbiter while that model's process is not up (starting after an unlock, or started again
// after it died). The caller retries later. A box whose second model stays down is served from
// local-small after a while, and the answer says so (Response.Served).
const ErrArbiterDown = "the arbiter's model is not up yet, retry later"

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

// ArbiterInfo is what oracled says of the arbiter's own model, in its `models` answer: whether
// the box has a second model for ghost.shadowd's cold read, whether its process is up, and
// which model a cold read asked for now would be read by.
//
// The field names are its own on purpose. This sits inside oracled's `models` answer, which
// the tools read with sed and grep for the MAIN model's "ready", "verdict", "speed" and
// "tokPerSecLast" (tools/update.sh waits on "ready": true; tools/gpu.sh times tokPerSecLast).
// A second "ready" here, sorted ahead of the first, would answer for the wrong model.
type ArbiterInfo struct {
	Has   bool   `json:"has"`             // the box has a second model for the arbiter
	Model string `json:"model,omitempty"` // its name
	Up    bool   `json:"up"`              // its process is serving
	Why   string `json:"why,omitempty"`   // why it is not up
	// What it has done since it started: how many questions it answered, how fast it
	// writes, and when it last did.
	Answers         int     `json:"answers,omitempty"`
	TokensPerSecond float64 `json:"tokensPerSecond,omitempty"`
	LastAnswerAt    string  `json:"lastAnswerAt,omitempty"`
	// ServedBy is which model reads now: the second model's name, "the main model", or
	// "nothing yet: waiting for <name>" while a second model starts.
	ServedBy string `json:"servedBy"`
	Main     string `json:"main,omitempty"` // the main model's name
}

// Arbiter asks oracled which model the arbiter's cold read is read by.
func (c *Client) Arbiter() (ArbiterInfo, error) {
	resp, err := c.c.Call("models", nil)
	if err != nil {
		return ArbiterInfo{}, err
	}
	if !resp.OK {
		return ArbiterInfo{}, errors.New(resp.Err)
	}
	var m struct {
		Main    string       `json:"local-small"`
		Arbiter *ArbiterInfo `json:"arbiter"`
	}
	if err := json.Unmarshal(resp.Data, &m); err != nil {
		return ArbiterInfo{}, err
	}
	if m.Arbiter == nil { // an oracled from before it had lanes: one model
		return ArbiterInfo{ServedBy: "the main model", Main: m.Main}, nil
	}
	m.Arbiter.Main = m.Main
	return *m.Arbiter, nil
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
