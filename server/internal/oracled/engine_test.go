package oracled

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// The startup lines of a llama-server on an RTX 4070, as llama.cpp prints them, fed through the
// watcher in the odd-sized chunks a pipe delivers: the verdict is the GPU, with layers and VRAM.
func TestEngineWatchReadsACudaStart(t *testing.T) {
	box := newEngineInfoBox()
	var passthrough bytes.Buffer
	w := box.watcher(&passthrough)
	lines := "ggml_cuda_init: GGML_CUDA_FORCE_MMQ:    no\n" +
		"ggml_cuda_init: found 1 CUDA devices:\n" +
		"  Device 0: NVIDIA GeForce RTX 4070, compute capability 8.9, VMM: yes\n" +
		"load_tensors: loading model tensors, this can take a while... (mmap = true)\n" +
		"load_tensors: offloading 48 repeating layers to GPU\n" +
		"load_tensors: offloading output layer to GPU\n" +
		"load_tensors: offloaded 49/49 layers to GPU\n" +
		"load_tensors:        CUDA0 model buffer size =  6942.35 MiB\n" +
		"load_tensors:   CPU_Mapped model buffer size =   525.00 MiB\n" +
		"llama_kv_cache_unified:      CUDA0 KV buffer size =   896.00 MiB\n" +
		"llama_context:      CUDA0 compute buffer size =   307.00 MiB\n" +
		"srv    init: initializing slots, n_slots = 1\n"
	// Deliver in awkward pieces: the parser must reassemble lines across writes.
	for i := 0; i < len(lines); i += 37 {
		end := i + 37
		if end > len(lines) {
			end = len(lines)
		}
		if _, err := w.Write([]byte(lines[i:end])); err != nil {
			t.Fatal(err)
		}
	}
	if passthrough.String() != lines {
		t.Fatal("the watcher must pass every byte through to the log")
	}
	info := box.get()
	if info.Backend != "cuda" || !info.OnGPU() {
		t.Fatalf("backend %q onGPU %v", info.Backend, info.OnGPU())
	}
	if len(info.Devices) != 1 || info.Devices[0] != "NVIDIA GeForce RTX 4070, compute capability 8.9" {
		t.Fatalf("devices = %v", info.Devices)
	}
	if info.Offloaded != "49/49" {
		t.Fatalf("offloaded = %q", info.Offloaded)
	}
	if int(info.GPUMiB) != 6942+896+307 || int(info.CPUMiB) != 525 {
		t.Fatalf("gpu %.0f cpu %.0f MiB", info.GPUMiB, info.CPUMiB)
	}
	if v := info.Verdict(); !strings.HasPrefix(v, "on the GPU: NVIDIA GeForce RTX 4070") || !strings.Contains(v, "49/49 layers") || !strings.Contains(v, "8145 MiB VRAM") {
		t.Fatalf("verdict: %s", v)
	}
}

// The same server with no GPU to be had: the warning is kept, the verdict says CPU, and a
// binary built without CUDA (no cuda lines at all) is told apart from one that looked and failed.
func TestEngineWatchReadsACpuStart(t *testing.T) {
	box := newEngineInfoBox()
	w := box.watcher(nil)
	_, _ = w.Write([]byte("warning: no usable GPU found, --gpu-layers option will be ignored\n" +
		"warning: one possible reason is that llama.cpp was compiled without GPU support\n" +
		"load_tensors: offloaded 0/49 layers to GPU\n" +
		"load_tensors:   CPU_Mapped model buffer size =  7467.35 MiB\n"))
	info := box.get()
	if info.Backend != "cpu" || info.OnGPU() || info.Offloaded != "0/49" || len(info.Warnings) != 1 {
		t.Fatalf("%+v", info)
	}
	if !strings.HasPrefix(info.Verdict(), "on the CPU") {
		t.Fatalf("verdict: %s", info.Verdict())
	}
	silent := newEngineInfoBox()
	_, _ = silent.watcher(nil).Write([]byte("srv    init: initializing slots, n_slots = 1\nmain: model loaded\n"))
	if v := silent.get().Verdict(); !strings.Contains(v, "built without CUDA") {
		t.Fatalf("a server that never mentions CUDA: %s", v)
	}
	if v := newEngineInfoBox().get().Verdict(); !strings.Contains(v, "printed nothing yet") {
		t.Fatalf("before any output: %s", v)
	}
	// A CUDA device that then failed to take layers , the middle case.
	half := newEngineInfoBox()
	_, _ = half.watcher(nil).Write([]byte("ggml_cuda_init: found 1 CUDA devices:\n  Device 0: NVIDIA GeForce RTX 4070, compute capability 8.9, VMM: yes\nload_tensors: offloaded 0/49 layers to GPU\n"))
	if h := half.get(); h.OnGPU() || !strings.Contains(h.Verdict(), "NO layers offloaded") {
		t.Fatalf("%+v: %s", h, h.Verdict())
	}
}

// Timings: the model's own tokens and milliseconds become tok/s; the ring averages the last
// twenty, an estimate is marked, empty answers are ignored, and the speed verdict reads the rate.
func TestEngineStats(t *testing.T) {
	s := NewEngineStats()
	if sum := s.Summary(); sum.Inferences != 0 || sum.TokPerSecAvg != 0 {
		t.Fatalf("empty: %+v", sum)
	}
	if v := SpeedVerdict(0, EngineInfo{}); !strings.Contains(v, "no answers timed") {
		t.Fatal(v)
	}
	s.Record("text", Timings{PromptN: 100, PromptMS: 200, PredictedN: 40, PredictedMS: 1000})   // 40 tok/s
	s.Record("image", Timings{PromptN: 900, PromptMS: 1800, PredictedN: 60, PredictedMS: 2000}) // 30 tok/s
	s.Record("text", Timings{PredictedN: 0, PredictedMS: 5})                                    // ignored
	s.Estimate("chat", 50, 2500*time.Millisecond)                                               // 20 tok/s, marked
	sum := s.Summary()
	if sum.Inferences != 3 || sum.TokPerSecLast != 20 || sum.Kinds["chat~"] != 1 || sum.Kinds["text"] != 1 {
		t.Fatalf("%+v", sum)
	}
	// tokens-weighted: (40+60+50) / (1+2+2.5)s = 27.3
	if sum.TokPerSecAvg < 27.2 || sum.TokPerSecAvg > 27.4 {
		t.Fatalf("avg %.2f", sum.TokPerSecAvg)
	}
	if sum.PromptTokPerSec != 500 { // (100+900) / 2.0s
		t.Fatalf("prompt tok/s %.1f", sum.PromptTokPerSec)
	}
	for i := 0; i < 30; i++ {
		s.Record("text", Timings{PredictedN: 10, PredictedMS: 5000}) // 2 tok/s, floods the ring
	}
	sum = s.Summary()
	if sum.Inferences != 33 || sum.TokPerSecAvg != 2 {
		t.Fatalf("ring: %+v", sum)
	}
	gpu := EngineInfo{Backend: "cuda", Offloaded: "49/49"}
	if v := SpeedVerdict(2, gpu); !strings.Contains(v, "CPU speed although llama-server reports the GPU") {
		t.Fatal(v)
	}
	if v := SpeedVerdict(2, EngineInfo{Backend: "cpu"}); v != "2.0 tok/s: CPU speed" {
		t.Fatal(v)
	}
	if v := SpeedVerdict(35.5, gpu); v != "35.5 tok/s: GPU speed" {
		t.Fatal(v)
	}
}
