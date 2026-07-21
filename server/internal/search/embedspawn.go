package search

// EmbedServer owns the embeddings llama-server child (spec 13.1: a separate llama.cpp instance, may
// be CPU-only). Mirrors oracled's llamaBackend pattern: weights live on the ENCRYPTED VOLUME, the
// child binds loopback only, inherits our stdout/stderr so its logs land in searchd's log, and dies
// with the daemon , which dies with the mount. -ngl 0 keeps it off the GPU so gemma's interactive
// inference keeps priority (the spec's embed_max_concurrent_batches=1 plus CPU-only is the
// conservative default; flip in conf if the GPU is idle).

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

type EmbedServerConfig struct {
	BinPath   string // llama-server binary
	ModelPath string // EmbeddingGemma gguf on the volume
	Port      int    // loopback port, e.g. 18081
	Threads   int    // 0 = llama default
}

type EmbedServer struct {
	cfg EmbedServerConfig
	cmd *exec.Cmd
}

func NewEmbedServer(cfg EmbedServerConfig) *EmbedServer { return &EmbedServer{cfg: cfg} }

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
		"-c", "2048",
		"-ub", "1024",
		"-ngl", "0",
	}
	if e.cfg.Threads > 0 {
		args = append(args, "--threads", strconv.Itoa(e.cfg.Threads))
	}
	cmd := exec.Command(e.cfg.BinPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start embed llama-server: %w", err)
	}
	e.cmd = cmd
	deadline := time.Now().Add(within)
	url := fmt.Sprintf("http://127.0.0.1:%d/health", e.cfg.Port)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	e.Stop()
	return fmt.Errorf("embed llama-server not healthy within %s", within)
}

// BaseURL for the Embedder.
func (e *EmbedServer) BaseURL() string { return fmt.Sprintf("http://127.0.0.1:%d", e.cfg.Port) }

// Stop TERMs then KILLs the child.
func (e *EmbedServer) Stop() {
	if e.cmd == nil || e.cmd.Process == nil {
		return
	}
	_ = e.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = e.cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = e.cmd.Process.Kill()
		<-done
	}
	e.cmd = nil
}
