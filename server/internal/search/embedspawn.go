package search

// EmbedServer owns the embeddings llama-server child (spec 13.1: a separate llama.cpp instance, may
// be CPU-only). Mirrors oracled's llamaBackend pattern: weights live on the ENCRYPTED VOLUME, the
// child binds loopback only, inherits our stdout/stderr so its logs land in searchd's log, and dies
// with the daemon , which dies with the mount. -ngl 0 keeps it off the GPU so gemma's interactive
// inference keeps priority (the spec's embed_max_concurrent_batches=1 plus CPU-only is the
// conservative default; flip in conf if the GPU is idle).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/procs"
)

type EmbedServerConfig struct {
	BinPath   string // llama-server binary
	ModelPath string // EmbeddingGemma gguf on the volume
	Port      int    // loopback port, e.g. 18081
	Threads   int    // 0 = llama default
}

type EmbedServer struct {
	cfg EmbedServerConfig
	// key is what the child asks of every request but /health (LLAMA_API_KEY in its
	// environment, never an argument): loopback is every user's on the box, and the texts sent
	// here to be embedded are the person's own. Made once, for this daemon's run.
	key    string
	cmd    *exec.Cmd
	exited chan struct{} // closed when the child is reaped (one waiter, in Start)
	mu     sync.Mutex
	stop   bool
}

func NewEmbedServer(cfg EmbedServerConfig) *EmbedServer {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("no random bytes for the embedding server's key: " + err.Error())
	}
	return &EmbedServer{cfg: cfg, key: hex.EncodeToString(b[:])}
}

// Key is what the Embedder sends with every request to this child.
func (e *EmbedServer) Key() string { return e.key }

// Start launches the child and waits for /health. A missing weights file is a clean, nameable error ,
// the caller degrades to FTS-only and says so in health, per spec 4's failure semantics.
func (e *EmbedServer) Start(within time.Duration) error {
	if _, err := os.Stat(e.cfg.ModelPath); err != nil {
		return fmt.Errorf("embedding weights not on volume: %w", err)
	}
	args := []string{
		"--model", e.cfg.ModelPath,
		"--host", "127.0.0.1", "--port", strconv.Itoa(e.cfg.Port),
		"--embedding",
		// nomic-style embedders 500 on /v1/embeddings without an explicit pooling mode , mean
		// pooling is what these models were trained for. The batch/ctx sizes cover our chunk
		// lengths with headroom.
		"--pooling", "mean",
		// An embedding model reads an input whole, in ONE physical batch: an input longer than
		// -ub is refused with a 500 ("input is too large to process. increase the physical batch
		// size"). -ub was 1024 under a 2048 context, and a chunk of numbers, links or text without
		// spaces is far more tokens than its words suggest (chunker.go estimates from words), so
		// such chunks never got a vector. The batch now spans the whole context; what is longer
		// still is cut to fit by the worker (Embedder.EmbedFitting).
		"-c", "2048",
		"-b", "2048",
		"-ub", "2048",
		"-ngl", "0",
	}
	if e.cfg.Threads > 0 {
		args = append(args, "--threads", strconv.Itoa(e.cfg.Threads))
	}
	cmd := exec.Command(e.cfg.BinPath, args...)
	// Same orphan lesson as oracled: the kernel reaps the embed child if searchd dies hard.
	cmd.SysProcAttr = procs.ChildAttr()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if e.key != "" {
		cmd.Env = append(os.Environ(), "LLAMA_API_KEY="+e.key)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start embed llama-server: %w", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	e.mu.Lock()
	stopped := e.stop
	e.cmd, e.exited = cmd, exited
	e.mu.Unlock()
	if stopped { // Stop came while this was starting: nothing may outlive it
		e.end(false)
		return fmt.Errorf("embed llama-server stopped while starting")
	}
	deadline := time.Now().Add(within)
	url := fmt.Sprintf("http://127.0.0.1:%d/health", e.cfg.Port)
	for time.Now().Before(deadline) {
		select {
		case <-exited: // died (or was stopped) before it was ready: say so now, not in two minutes
			e.end(false)
			return fmt.Errorf("embed llama-server exited before it was healthy")
		default:
		}
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	e.end(false)
	return fmt.Errorf("embed llama-server not healthy within %s", within)
}

// BaseURL for the Embedder.
func (e *EmbedServer) BaseURL() string { return fmt.Sprintf("http://127.0.0.1:%d", e.cfg.Port) }

// Stop TERMs then KILLs the child, and ends Watch.
func (e *EmbedServer) Stop() { e.end(true) }

// end stops the child; for good (Watch ends too) when final.
func (e *EmbedServer) end(final bool) {
	e.mu.Lock()
	if final {
		e.stop = true
	}
	cmd, exited := e.cmd, e.exited
	e.cmd = nil
	e.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-exited
	}
}

// embedBackoff is how long Watch waits before each start after a death (tests shorten it).
var embedBackoff = []time.Duration{10 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute}

// Watch starts the child again whenever it dies (killed from outside, out of memory): searchd
// started it once and, when it went, embedded nothing until the next unlock, every embed job
// failing its way to parked. 10 s after a death, then longer while it keeps dying soon after
// starting. Returns when ctx ends or Stop is called.
func (e *EmbedServer) Watch(ctx context.Context, logf func(msg string, args ...any)) {
	backoff := embedBackoff
	fails := 0
	for {
		e.mu.Lock()
		exited, stopped := e.exited, e.stop
		e.mu.Unlock()
		if stopped || exited == nil {
			return
		}
		upAt := time.Now()
		select {
		case <-ctx.Done():
			return
		case <-exited:
		}
		e.mu.Lock()
		stopped = e.stop
		e.mu.Unlock()
		if stopped {
			return
		}
		if time.Since(upAt) > 10*time.Minute {
			fails = 0
		}
		wait := backoff[min(fails, len(backoff)-1)]
		fails++
		logf("embedding server died , starting it again", "up", time.Since(upAt).Round(time.Second).String(), "in", wait.String())
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if err := e.Start(2 * time.Minute); err == nil {
				logf("embedding server back")
				break
			} else {
				logf("embedding server did not come back", "err", err.Error())
				wait = backoff[min(fails, len(backoff)-1)]
				fails++
			}
			e.mu.Lock()
			stopped = e.stop
			e.mu.Unlock()
			if stopped {
				return
			}
		}
	}
}
