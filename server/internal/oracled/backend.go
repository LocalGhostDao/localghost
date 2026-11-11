package oracled

// A Backend is one concrete model oracled can route to. llamaBackend runs llama.cpp's llama-server as
// a PRIVATE child , bound to loopback on a port nobody else is told, weights loaded from the encrypted
// volume , so the model is "run directly" (no separate service, no exposure) without dragging GGML
// into this Go binary via cgo. A frontierBackend (an HTTPS call to a remote API) implements the same
// interface, so swapping local gemma for a frontier model is a conf change, invisible to the queue and
// the callers.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/procs"
)

// Backend serves one inference. The queue's single worker calls Infer one request at a time.
type Backend interface {
	Name() string
	Infer(ctx context.Context, req oracle.Request) (oracle.Response, error)
}

// LlamaConfig configures the llama-server child. Paths to the weights are on the ENCRYPTED VOLUME, so
// a locked box cannot read the model , consistent with everything else on the box.
type LlamaConfig struct {
	BinPath    string // llama-server binary, e.g. /usr/local/bin/llama-server (the binary is not secret)
	ModelPath  string // <mount>/ai-models/gemma-4-12b-it-Q4_K_M.gguf , weights ON the volume
	MmprojPath string // <mount>/ai-models/mmproj-F16.gguf (multimodal projector), optional
	Port       int    // loopback port oracled picks and tells no one
	ModelName  string // reported back in Response.Model, e.g. "gemma-4-12b"
	ExtraArgs  []string
	// LoadTimesPath keeps the last complete load's measured times (loadprog.go), the basis of the
	// next load's time-left estimate. Empty: estimates start from nothing every time.
	LoadTimesPath string
	// StrikesPath keeps the images the engine died on (strikes.go). Empty: in memory only.
	StrikesPath string
}

// projector is the mmproj path to start with, or "" and why not.
func (b *llamaBackend) projector() (string, string) {
	p := b.cfg.MmprojPath
	if p == "" {
		return "", "no projector configured (mmprojPath is empty in conf/ghost.oracled.conf)"
	}
	if _, err := os.Stat(p); err == nil {
		return p, ""
	}
	if _, err := os.Stat(p + ".off"); err == nil {
		return "", fmt.Sprintf("the projector is switched off by hand (%s.off): rename it back to %s and restart ghost.oracled (sudo ./tools/ns.sh ./bin/ghost-ctl restart-daemon ghost.oracled)", filepath.Base(p), filepath.Base(p))
	}
	return "", "no projector at " + p + " (tools/setup_llama.sh puts the pinned one there)"
}

// Vision is whether the running engine sees images, and why not.
func (b *llamaBackend) Vision() (bool, string) {
	b.visionMu.Lock()
	defer b.visionMu.Unlock()
	return b.vision, b.visionWhy
}

func (b *llamaBackend) setVision(on bool, why string) {
	b.visionMu.Lock()
	b.vision, b.visionWhy = on, why
	b.visionMu.Unlock()
}

// llamaBackend owns a llama-server subprocess.
type llamaBackend struct {
	visionMu  sync.Mutex
	vision    bool   // the running engine was started with its projector
	visionWhy string // why not
	cfg       LlamaConfig
	proc      *os.Process
	client    *http.Client
	// streamClient has NO overall timeout , a streamed deep-think runs for minutes by design, and
	// killing it at 120s would truncate answers mid-sentence. Cancellation comes from the request
	// context (the app hanging up propagates all the way here).
	streamClient *http.Client
	addr         string
	// What llama-server said about the hardware at startup, and how fast it has been answering ,
	// the two facts "is the GPU running" is made of. See engine.go.
	info  *engineInfoBox
	stats *EngineStats
	// exited is closed when the child is reaped (one waiter, started with it); exitState says how
	exited    chan struct{}
	exitState *os.ProcessState
	starts    int // how many times Start ran (the retry number on the unlock screen)
	strikes   *imageStrikes
}

// NewLlamaBackend prepares (does not start) the backend.
func NewLlamaBackend(cfg LlamaConfig) *llamaBackend {
	info := newEngineInfoBox()
	info.load = newLoadTracker(cfg.LoadTimesPath)
	return &llamaBackend{
		cfg:          cfg,
		client:       &http.Client{Timeout: 120 * time.Second}, // a 12B generation can be slow
		streamClient: &http.Client{},                           // streaming: context-cancelled, never clock-killed
		addr:         "127.0.0.1:" + strconv.Itoa(cfg.Port),
		info:         info,
		stats:        NewEngineStats(),
		strikes:      newImageStrikes(cfg.StrikesPath),
	}
}

// Load is how far the current start has got: phase, percent, time left (loadprog.go).
func (b *llamaBackend) Load() LoadProgress { return b.info.load.snapshot() }

// Died reports the running child's end: a channel closed when it exits, and a func that says how
// (its exit state and its last lines). Taken right after a Start that succeeded, so oracled notices
// a llama-server that dies LATER (a crash on a request, the kernel's OOM killer) instead of
// reporting "ok" over a dead port. nil when no child is running.
func (b *llamaBackend) Died() (<-chan struct{}, func() string) {
	ex := b.exited
	if ex == nil {
		return nil, nil
	}
	return ex, func() string {
		return "llama-server exited (" + stateString(b.exitState) + "); it said: " + b.info.Why()
	}
}

// Engine is what the child said about the hardware; Stats how fast it has been answering.
func (b *llamaBackend) Engine() EngineInfo                  { return b.info.get() }
func (b *llamaBackend) Stats() StatsSummary                 { return b.stats.Summary() }
func (b *llamaBackend) RecordStream(kind string, t Timings) { b.stats.Record(kind, t) }
func (b *llamaBackend) EstimateStream(kind string, tokens int, took time.Duration) {
	b.stats.Estimate(kind, tokens, took)
}

func (b *llamaBackend) Name() string { return b.cfg.ModelName }

// Start launches llama-server on loopback and waits for /health to go green before returning, so the
// queue never dispatches at a model still loading its weights. weights-load for a 12B Q4 model takes
// seconds to tens of seconds, so the wait is generous.
func (b *llamaBackend) Start(ctx context.Context) error {
	// Pre-flight the paths. Without this, a missing model means llama-server starts, fails its load,
	// dies, and oracled burns the full 90s health wait before reporting a vague "not healthy" , when
	// the real answer was knowable in a millisecond with the exact path named.
	if fi, err := os.Stat(b.cfg.ModelPath); err != nil {
		return fmt.Errorf("model weights not found at %s , place the gguf under <mount>/ai-models/ or set modelPath in conf/ghost.oracled.conf: %w", b.cfg.ModelPath, err)
	} else if fi.Size() < 1<<20 {
		return fmt.Errorf("model file at %s is %d bytes , that is not a model (interrupted download?)", b.cfg.ModelPath, fi.Size())
	}
	if bi, err := os.Stat(b.cfg.BinPath); err != nil {
		return fmt.Errorf("llama-server binary not found at %s , install it or set llamaBin in conf: %w", b.cfg.BinPath, err)
	} else if bi.Mode().Perm()&0o111 == 0 {
		// Not just existence: no exec bit at all is a guaranteed fork/exec failure , name the fix.
		return fmt.Errorf("llama-server at %s is mode %v , not executable (fix: chmod 755 %s)", b.cfg.BinPath, bi.Mode().Perm(), b.cfg.BinPath)
	} else if bi.Mode().Perm()&0o001 == 0 {
		// Some exec bits but not world-exec: fine when the owner/group matches this process, fatal
		// when the binary was installed by a DIFFERENT user (the observed case: installed by the
		// operator's account, run as the service user , stats fine, fork/exec dies with a bare
		// "permission denied"). Warn with the likely fix and let fork/exec be the final judge.
		slog.Warn("llama-server has no world-exec bit , if start fails with permission denied, chmod 755 it", "fn", "Start", "path", b.cfg.BinPath, "mode", bi.Mode().Perm().String())
	}
	args := []string{
		"-m", b.cfg.ModelPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(b.cfg.Port),
		// The embedded web chat UI is OFF unconditionally , hardcoded, not conf. This box's only
		// chat surface is the app through secd's authenticated edge; a browser UI on the loopback
		// would be an unauthenticated second door for anything that can reach localhost.
		"--no-webui",
		// GPU OFFLOAD , the htop confession: llama.cpp with a CUDA build STILL runs pure-CPU
		// unless told to offload; -ngl is opt-in per run, not baked in at compile. Eleven
		// CPU-hours ground through four threads while an RTX 4070 watched. All layers to the
		// GPU; conf can override through ExtraArgs (appended last wins in llama's parser) for
		// a box that genuinely lacks one.
		"-ngl", "99",
	}
	// The projector, looked for at EVERY start: a configured-but-missing one degrades to TEXT-ONLY
	// instead of handing llama-server a dead path and dying entirely (captions need it, nothing
	// else does). It used to be dropped from the config for good at the first miss, so a projector
	// put back was not used until oracled itself restarted; and the warning went to a stderr
	// nobody reads. Now the state is kept (Vision), said in the log, on the health line and in
	// `models`, and a start after the file is back uses it.
	if mm, note := b.projector(); mm != "" {
		args = append(args, "--mmproj", mm)
		b.setVision(true, "")
	} else {
		b.setVision(false, note)
		if note != "" {
			slog.Warn("starting TEXT-ONLY: no photo will be described until the projector is back", "fn", "Start", "why", note)
		}
	}
	args = append(args, b.cfg.ExtraArgs...)

	// A llama-server already running from this binary on this port is a predecessor's orphan (Pdeathsig only
	// covers children of THIS build's oracled; one from before it has been seen alive for sixty
	// days, holding the port and the VRAM, so the next child bound nothing and ran on the CPU).
	// It has no owner left to stop it: end it here, and say so.
	// Only one on OUR port: searchd's embedder runs from the same binary on its own port, alive
	// and owned, and ending it here took search's vectors down at every model start.
	port := strconv.Itoa(b.cfg.Port)
	ours := func(args []string) bool {
		return procs.HasFlag(args, "--port", port) && !hasArg(args, "--embedding")
	}
	if strays := procs.KillStraysMatching(b.cfg.BinPath, 2*time.Second, ours); len(strays) > 0 {
		slog.Warn("killed a stray llama-server before starting ours , it was holding the port and the GPU", "fn", "Start", "strays", strings.Join(strays, ", "))
	}
	b.info.resetTail()
	b.starts++
	b.info.load.begin(b.starts)
	cmd := exec.Command(b.cfg.BinPath, args...)
	// own process group so oracled can signal the whole group on stop, and inherit oracled's env
	// (GHOST_LOG_LEVEL etc.). stdout/stderr pass through to oracled's log THROUGH the engine
	// watcher, which keeps what the startup lines say about the GPU (engine.go).
	// Setpgid so oracled can signal the whole group on a graceful stop, AND Pdeathsig so the
	// KERNEL kills llama-server the moment its parent dies , the orphan systemd caught
	// ("left-over process llama-server in control group while starting unit") held port 18080
	// and 9GB of VRAM, so the next oracled's child could not bind and every caption timed out
	// for an hour. A SIGKILLed parent cannot clean up after itself; the kernel can.
	cmd.SysProcAttr = procs.ChildAttr()
	cmd.Stdout = b.info.watcher(os.Stdout)
	cmd.Stderr = b.info.watcher(os.Stderr)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start llama-server: %w", err)
	}
	b.proc = cmd.Process
	// ONE waiter reaps the child and closes exited: waitHealthy returns the moment llama-server dies
	// (a flag this build does not know, a model or projector it cannot read, the GPU full) with its
	// own last lines, instead of polling a dead port for minutes and saying "not healthy".
	exited := make(chan struct{})
	b.exited = exited
	go func(c *exec.Cmd) {
		_ = c.Wait() // reaps it AND waits for its last lines to reach the watcher
		b.exitState = c.ProcessState
		close(exited)
	}(cmd)
	if err := b.waitHealthy(ctx, llamaReadyWithin); err != nil {
		b.info.load.fail()
		return err
	}
	b.info.load.ready()
	// A log with nothing to say about the GPU (the mirror's v0.5.0): ask the driver instead.
	if !b.info.get().OnGPU() && b.proc != nil {
		if mib, ok := gpuMiBOfPID(b.proc.Pid); ok && mib > 0 {
			b.info.seenOnGPU(mib)
		}
	}
	// The verdict, once, where a person looks: on the GPU with how many layers and how much VRAM,
	// or a warning naming why not. tools/gpu.sh and the Box Status drill-in read the same facts.
	info := b.info.get()
	if info.OnGPU() {
		slog.Info("llama-server "+info.Verdict(), "fn", "Start", "devices", strings.Join(info.Devices, "; "), "offloaded", info.Offloaded, "gpuMiB", int(info.GPUMiB), "cpuMiB", int(info.CPUMiB))
	} else {
		slog.Warn("llama-server "+info.Verdict(), "fn", "Start", "warnings", strings.Join(info.Warnings, " | "), "offloaded", info.Offloaded, "cpuMiB", int(info.CPUMiB))
	}
	return nil
}

func (b *llamaBackend) waitHealthy(ctx context.Context, within time.Duration) error {
	deadline := time.Now().Add(within)
	url := "http://" + b.addr + "/health"
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-b.exited:
			return fmt.Errorf("llama-server exited (%s) before it was ready; it said: %s", stateString(b.exitState), b.info.Why())
		default:
		}
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("llama-server not healthy within %s (still running); its last lines: %s", within, b.info.Why())
}

// reInvalidArg: llama-server refusing an argument it does not know ("error: invalid argument: --mlock").
var reInvalidArg = regexp.MustCompile(`invalid argument: (\S+)`)

// DropRejectedArg: the child died on an argument it does not know, and that argument came from
// the conf's extraArgs (tuning, not something oracled needs): it is dropped for the next start and
// named. True when something was dropped, so the caller starts again at once. A llama.cpp update
// that retires a flag (v0.5.0 no longer knows --mlock, 29 Sep 2026) then costs one failed start,
// not a box without a model. oracled's own arguments are never dropped; conf/ghost.oracled.conf
// keeps the flag until the person takes it out.
func (b *llamaBackend) DropRejectedArg(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	m := reInvalidArg.FindStringSubmatch(err.Error())
	if m == nil {
		return "", false
	}
	flag := strings.TrimRight(m[1], ".,;:'\"")
	for i, a := range b.cfg.ExtraArgs {
		if a != flag {
			continue
		}
		n := 1
		// its value too, when the next element is one ("-fa on", "--cache-type-k q8_0")
		if i+1 < len(b.cfg.ExtraArgs) && !strings.HasPrefix(b.cfg.ExtraArgs[i+1], "-") {
			n = 2
		}
		dropped := strings.Join(b.cfg.ExtraArgs[i:i+n], " ")
		b.cfg.ExtraArgs = append(append([]string(nil), b.cfg.ExtraArgs[:i]...), b.cfg.ExtraArgs[i+n:]...)
		return dropped, true
	}
	return "", false
}

// llamaReadyWithin: how long a start may take before oracled gives up on it. A 12B from a cold
// encrypted volume, right after a seven-minute build has pushed it out of the page cache, is not a
// ten-second load; a child that dies is noticed at once anyway.
const llamaReadyWithin = 5 * time.Minute

// Infer sends one completion request to the private llama-server. This is the ONLY place the model's
// address is used. Text-only requests keep the native /completion path unchanged; requests carrying
// images go through /v1/chat/completions with data-URI content parts, which is the multimodal path
// the current llama-server (libmtmd, the mmproj we load) supports. Image paths must be ON THE VOLUME
// , this reads them and never sends bytes anywhere but loopback.
func (b *llamaBackend) Infer(ctx context.Context, req oracle.Request) (oracle.Response, error) {
	if len(req.Images) > 0 {
		return b.inferMultimodal(ctx, req)
	}
	prompt, budget, think := applyThink(req.Think, req.Input, req.MaxTokens)
	// CHAT COMPLETIONS, not raw /completion , even for plain text. The raw endpoint sends the bare
	// prompt with NO chat template, and an instruction-tuned model without its turn structure leaks
	// template tokens into the output and rambles to the token cap (observed on device: garbage
	// prefixes, minutes-long answers). /v1/chat/completions applies the template from the GGUF.
	payload := map[string]any{
		"messages": []map[string]any{{"role": "user", "content": prompt}},
		// Same disease, second organ: the TEXT one-shot path (tags, distillation) was still
		// letting this natively-thinking gemma burn its whole budget on reasoning and return
		// empty content , the caption fix only covered the multimodal path. One-shot tasks do
		// not want a monologue unless the caller asked for a think level.
		"chat_template_kwargs": map[string]any{"enable_thinking": think},
	}
	if budget > 0 {
		payload["max_tokens"] = budget
	}
	if req.Temperature > 0 {
		payload["temperature"] = req.Temperature
	}
	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+b.addr+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return oracle.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return oracle.Response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return oracle.Response{}, refusalError("chat/completions", resp)
	}
	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
		Timings *Timings `json:"timings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return oracle.Response{}, err
	}
	if out.Timings != nil {
		b.stats.Record("text", *out.Timings)
	}
	if len(out.Choices) == 0 {
		return oracle.Response{}, fmt.Errorf("chat/completions: empty choices")
	}
	ch := out.Choices[0]
	if ch.Message.Content == "" && ch.Message.Reasoning != "" {
		// The model reasoned the budget away and never answered , name it precisely so the log
		// reads "reasoning consumed the budget", not "the model said something short".
		return oracle.Response{}, fmt.Errorf("reasoning consumed the token budget (finish=%s, %d reasoning chars, no content) , raise MaxTokens or suppress thinking", ch.FinishReason, len(ch.Message.Reasoning))
	}
	return oracle.Response{Output: ch.Message.Content, Model: b.cfg.ModelName}, nil
}

// inferMultimodal builds an OpenAI-style chat completion with image content parts.
func (b *llamaBackend) inferMultimodal(ctx context.Context, req oracle.Request) (_ oracle.Response, err error) {
	promptText, mmBudget, _ := applyThink(req.Think, req.Input, req.MaxTokens)
	req.MaxTokens = mmBudget
	content := []map[string]any{{"type": "text", "text": promptText}}
	var keys []string
	sent := false
	// the engine died with these images in flight: a strike against each (strikes.go)
	defer func() {
		if err == nil || !sent || ctx.Err() != nil || !b.diedDuring(3*time.Second) {
			return
		}
		n := 0
		for _, k := range keys {
			if c := b.strikes.strike(k); c > n {
				n = c
			}
		}
		err = fmt.Errorf("llama-server died with this image in flight (strike %d; at %d it is not sent again): %w", n, strikesToRefuse, err)
	}()
	for _, imgPath := range req.Images {
		uri, err := imageForModel(ctx, imgPath)
		if err != nil {
			return oracle.Response{}, err
		}
		k := imageKey(uri)
		if b.strikes.refused(k) {
			return oracle.Response{}, fmt.Errorf("image %s: llama-server died on it %d times, it is not sent again (forgive it: delete ghost.oracled.image-strikes beside the conf)", filepath.Base(imgPath), strikesToRefuse)
		}
		keys = append(keys, k)
		content = append(content, map[string]any{
			"type":      "image_url",
			"image_url": map[string]string{"url": uri},
		})
	}
	payload := map[string]any{
		"messages": []map[string]any{{"role": "user", "content": content}},
		// This gemma THINKS NATIVELY and llama-server parses the thinking into reasoning_content ,
		// caption jobs were burning their whole budget on an internal monologue and returning
		// EMPTY content ("implausibly short", literally). Ask the template to skip thinking for
		// these one-shot vision tasks; templates that ignore the kwarg are covered by the budget
		// and the reasoning-aware parse below.
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		payload["temperature"] = req.Temperature
	}
	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+b.addr+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return oracle.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	sent = true
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return oracle.Response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return oracle.Response{}, refusalError("chat/completions", resp)
	}
	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
		Timings *Timings `json:"timings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return oracle.Response{}, err
	}
	if out.Timings != nil {
		b.stats.Record("image", *out.Timings)
	}
	if len(out.Choices) == 0 {
		return oracle.Response{}, fmt.Errorf("chat/completions: empty choices")
	}
	ch := out.Choices[0]
	if ch.Message.Content == "" && ch.Message.Reasoning != "" {
		// The model reasoned the budget away and never answered , name it precisely so the log
		// reads "reasoning consumed the budget", not "the model said something short".
		return oracle.Response{}, fmt.Errorf("reasoning consumed the token budget (finish=%s, %d reasoning chars, no content) , raise MaxTokens or suppress thinking", ch.FinishReason, len(ch.Message.Reasoning))
	}
	return oracle.Response{Output: ch.Message.Content, Model: b.cfg.ModelName}, nil
}

// dataURI wraps image bytes as a data URI, sniffing jpeg/png/webp by magic bytes (jpeg default).

// Stop signals llama-server (TERM, then KILL after a short grace) and reaps it. Called on oracled
// shutdown, which is the lock path, so the model process dies with the mount.
//
// Every step is logged with how long it took, because "llama-server took 44s to go" is the line
// a halt gets diagnosed from and the answer has two very different shapes: the process ignoring
// TERM (it is alive; the KILL settles it in a second) or the process ALREADY DEAD and the kernel
// still releasing what it held , gigabytes of VRAM and of host pages the CUDA driver pinned ,
// which no signal can hurry and which shows as state D with a wchan in the driver. The reap
// wait after KILL is therefore bounded too: oracled must not sit in Wait for a corpse until
// watchd's 5s grace kills oracled as well; a KILLed child that is still tearing down is left to
// init, and the log says so, with the pid, so the next `ps` on the box answers the question.
func (b *llamaBackend) Stop() {
	if b.proc == nil {
		return
	}
	if b.exited != nil {
		select {
		case <-b.exited: // died by itself and already reaped: nothing to signal
			b.proc, b.exited = nil, nil
			return
		default:
		}
	}
	pid := b.proc.Pid
	t0 := time.Now()
	_ = b.proc.Signal(syscall.SIGTERM)
	slog.Info("llama-server stop: SIGTERM sent", "fn", "Stop", "pid", pid)
	// the waiter started with the child reaps it; Stop only watches for that (two Waits on one pid
	// race, and the loser returns at once as if the child were gone)
	exited := b.exited
	if exited == nil {
		ch := make(chan struct{})
		go func(p *os.Process) { st, _ := p.Wait(); b.exitState = st; close(ch) }(b.proc)
		exited = ch
	}
	select {
	case <-exited:
		slog.Info("llama-server stop: exited on SIGTERM", "fn", "Stop", "pid", pid, "ms", time.Since(t0).Milliseconds(), "state", stateString(b.exitState))
	case <-time.After(llamaTermGrace):
		// A model server has nothing to flush; watchd gives the whole daemon 5s before it kills
		// us (and, through Pdeathsig, llama with us), so TERM gets one second, not a courtesy.
		_ = b.proc.Kill()
		slog.Warn("llama-server stop: no exit on SIGTERM, SIGKILL sent", "fn", "Stop", "pid", pid, "afterMs", time.Since(t0).Milliseconds())
		select {
		case <-exited:
			slog.Info("llama-server stop: reaped after SIGKILL", "fn", "Stop", "pid", pid, "ms", time.Since(t0).Milliseconds(), "state", stateString(b.exitState))
		case <-time.After(llamaReapWait):
			slog.Warn("llama-server stop: SIGKILLed but not reaped yet , the kernel is still tearing it down (VRAM, pinned pages); leaving it to init, oracled exits",
				"fn", "Stop", "pid", pid, "afterMs", time.Since(t0).Milliseconds())
		}
	}
	b.proc = nil
	b.exited = nil
}

const (
	llamaTermGrace = 1 * time.Second // TERM to KILL
	llamaReapWait  = 2 * time.Second // KILL to giving up on the reap; oracled has ~5s in all
)

func stateString(st *os.ProcessState) string {
	if st == nil {
		return "?"
	}
	return st.String()
}

// applyThink turns the Think level into what the request needs: an instruction on how much to
// reason, a token budget, and whether the chat template's native thinking is on at all.
//
// This gemma thinks natively: with enable_thinking the template opens a reasoning channel and
// llama-server hands it back as reasoning_content, separate from the answer. So "off" turns that
// channel OFF (the answer comes straight, fastest), and "brief" and "deep" turn it on with a word
// on how long to go and a budget that leaves room for the answer after the reasoning. Before 1 Oct
// 2026 the streamed chat never set the kwarg, so the model reasoned at every level, "off"
// included, and the levels asked for <think> tags on top of the native channel; the switch in
// SETTINGS changed nothing a person could see. Budgets only apply when the caller left MaxTokens at
// the backend default , an explicit caller budget always wins.
func applyThink(level, input string, maxTokens int) (prompt string, budget int, think bool) {
	switch level {
	case "brief":
		if maxTokens == 0 {
			maxTokens = 2048 // reasoning is billed against max_tokens
		}
		return "Think briefly first, a few lines at most, then answer clearly.\n\n" + input, maxTokens, true
	case "deep":
		if maxTokens == 0 {
			// Reasoning tokens count INSIDE this cap: at 2048 a thorough think consumed the whole
			// budget and the visible answer was EMPTY. The 4070 makes 8192 cheap.
			maxTokens = 8192
		}
		return "Think carefully and at length first: work step by step and consider what could be wrong, then give your best answer.\n\n" + input, maxTokens, true
	default:
		return input, maxTokens, false
	}
}

// Turn is one prior exchange half, as the caller (synthd) reconstructs it from the persisted chat.
// Role is "user" or "assistant"; anything else is dropped rather than forwarded to the template.
type Turn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// StreamChat opens a streaming chat completion against the running llama-server and returns the raw
// SSE body for the caller to translate. The chat template applies exactly as in the one-shot path;
// applyThink shapes the prompt and budget the same way, so streamed and one-shot answers are the
// same answer delivered differently.
//
// history is the CONVERSATION SO FAR, oldest first. Without it every message was a fresh, amnesiac
// one-shot: the box persisted both halves of every exchange and then never showed the model any of
// them, so "and what about the second one?" was answered by a model that had never heard the first.
// The think instruction, when any, wraps only the CURRENT prompt; prior turns go in verbatim.
func (b *llamaBackend) StreamChat(ctx context.Context, history []Turn, prompt, think, imageB64 string) (io.ReadCloser, string, error) {
	// No separate ready gate: if llama-server is down or still loading, the POST below fails fast
	// (refused connection / non-200) and the caller reports it , one truth source, no stale flag.
	p, budget, thinking := applyThink(think, prompt, 0)
	// Text-only stays a plain string; with an image the content becomes OpenAI-style parts , the
	// SAME shape inferMultimodal uses for captions, so the projector path is already proven.
	var content any = p
	if imageB64 != "" {
		// A photo attached to a chat goes through the same preparation as every captioned
		// image: decoded here, turned upright, fitted to ImageMaxSide and re-encoded. It used
		// to reach llama-server as the phone sent it (audit run 2), and the pinned engine is on
		// record aborting on a full-size photo; what does not decode is refused, not passed on.
		uri, err := chatImage(imageB64)
		if err != nil {
			return nil, "", err
		}
		content = []map[string]any{
			{"type": "text", "text": p},
			{"type": "image_url", "image_url": map[string]string{"url": uri}},
		}
	}
	messages := make([]map[string]any, 0, len(history)+1)
	for _, t := range history {
		if (t.Role != "user" && t.Role != "assistant") || t.Content == "" {
			continue
		}
		messages = append(messages, map[string]any{"role": t.Role, "content": t.Content})
	}
	messages = append(messages, map[string]any{"role": "user", "content": content})
	payload := map[string]any{
		"messages": messages,
		"stream":   true,
		// the native reasoning channel, on only when the person's think level asks for it
		"chat_template_kwargs": map[string]any{"enable_thinking": thinking},
	}
	if budget > 0 {
		payload["max_tokens"] = budget
	}
	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+b.addr+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := b.streamClient.Do(httpReq)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		err := refusalError("chat stream", resp)
		_ = resp.Body.Close()
		return nil, "", err
	}
	return resp.Body, b.cfg.ModelName, nil
}

func hasArg(args []string, a string) bool {
	for _, x := range args {
		if x == a {
			return true
		}
	}
	return false
}
