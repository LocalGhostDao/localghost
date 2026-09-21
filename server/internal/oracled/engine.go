package oracled

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The engine's own account of itself: is the model on the GPU, and how fast does it go.
//
// "Is the GPU running" was answered by htop and guesswork; the answer is in llama-server's own
// startup lines, which stream past on stderr into oracled's log where nobody reads them:
//
//	ggml_cuda_init: found 1 CUDA devices:
//	  Device 0: NVIDIA GeForce RTX 4070, compute capability 8.9, VMM: yes
//	load_tensors: offloaded 49/49 layers to GPU
//	load_tensors:        CUDA0 model buffer size =  6942.35 MiB
//	load_tensors:   CPU_Mapped model buffer size =   525.00 MiB
//	llama_kv_cache_unified:      CUDA0 KV buffer size =   896.00 MiB
//
// and, when it is NOT:
//
//	warning: no usable GPU found, --gpu-layers option will be ignored
//	load_tensors: offloaded 0/49 layers to GPU
//
// [EngineWatch] sits on the child's stdout/stderr, passes every line through untouched, and keeps
// what those lines say. [EngineStats] keeps the timings llama-server returns with every answer
// (tokens generated, milliseconds), so the tokens-per-second figure is the model's own and not a
// guess from character counts. Both surface through oracled's `models` control command, which
// secd folds into the Box Status drill-in and tools/gpu.sh prints.

// EngineInfo is what the startup lines said.
type EngineInfo struct {
	Backend   string   `json:"backend"`             // cuda | cpu | unknown (nothing parsed yet)
	Devices   []string `json:"devices,omitempty"`   // "NVIDIA GeForce RTX 4070, compute capability 8.9"
	Offloaded string   `json:"offloaded,omitempty"` // "49/49"
	GPUMiB    float64  `json:"gpuMiB,omitempty"`    // model + KV buffers reported on CUDA devices
	CPUMiB    float64  `json:"cpuMiB,omitempty"`    // model buffers left on the CPU (mapped or not)
	Warnings  []string `json:"warnings,omitempty"`  // the lines that say something went wrong
	Lines     int      `json:"lines"`               // how much output was seen at all
}

// OnGPU is the verdict the numbers support: a CUDA device was found and at least one layer went
// to it. False with Backend "unknown" means llama-server never said, which happens with a binary
// built without CUDA (it prints no ggml_cuda lines at all).
func (e EngineInfo) OnGPU() bool {
	if e.Backend != "cuda" {
		return false
	}
	n, _, ok := strings.Cut(e.Offloaded, "/")
	if !ok {
		return true // a CUDA device and no layer line yet: assume the usual
	}
	k, _ := strconv.Atoi(n)
	return k > 0
}

// Verdict is the one line a person reads.
func (e EngineInfo) Verdict() string {
	switch {
	case e.OnGPU():
		dev := "CUDA"
		if len(e.Devices) > 0 {
			dev = e.Devices[0]
		}
		s := "on the GPU: " + dev
		if e.Offloaded != "" {
			s += " · " + e.Offloaded + " layers"
		}
		if e.GPUMiB > 0 {
			s += fmt.Sprintf(" · %.0f MiB VRAM", e.GPUMiB)
		}
		return s
	case e.Backend == "cuda":
		return "CUDA device found but NO layers offloaded , check -ngl and the VRAM warnings"
	case e.Backend == "cpu":
		return "on the CPU: llama-server found no usable GPU (driver, CUDA build, or the device is busy)"
	default:
		if e.Lines == 0 {
			return "unknown: llama-server has printed nothing yet"
		}
		return "unknown: no CUDA lines in llama-server's output , this llama-server is probably built without CUDA"
	}
}

var (
	reCudaFound   = regexp.MustCompile(`ggml_cuda_init: found (\d+) CUDA devices?`)
	reDevice      = regexp.MustCompile(`^\s*Device \d+: (.+?)(?:, VMM: .*)?$`)
	reOffloaded   = regexp.MustCompile(`offloaded (\d+/\d+) layers to GPU`)
	reGPUBuffer   = regexp.MustCompile(`(CUDA\d+)[^=]*buffer size\s*=\s*([\d.]+) MiB`)
	reCPUBuffer   = regexp.MustCompile(`CPU(?:_Mapped)?[^=]*model buffer size\s*=\s*([\d.]+) MiB`)
	reNoGPU       = regexp.MustCompile(`no usable GPU found|failed to initialize CUDA|cudaMalloc failed|CUDA error|out of memory|ggml_cuda_init: failed`)
	reCPUBackend  = regexp.MustCompile(`using device CPU|load_tensors: offloading 0 repeating layers`)
	maxWarnings   = 8
	maxDeviceList = 4
)

// EngineWatch tees a child's output to [dst] and parses it. One per stream; both share [info].
type EngineWatch struct {
	dst  io.Writer
	info *engineInfoBox
	buf  bytes.Buffer
}

// engineInfoBox is the shared, locked EngineInfo behind the watchers.
type engineInfoBox struct {
	mu   sync.Mutex
	info EngineInfo
}

func newEngineInfoBox() *engineInfoBox { return &engineInfoBox{info: EngineInfo{Backend: "unknown"}} }

func (b *engineInfoBox) get() EngineInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.info
	out.Devices = append([]string(nil), b.info.Devices...)
	out.Warnings = append([]string(nil), b.info.Warnings...)
	return out
}

func (b *engineInfoBox) watcher(dst io.Writer) *EngineWatch { return &EngineWatch{dst: dst, info: b} }

// Write passes the bytes through, then feeds each complete line to the parser.
func (w *EngineWatch) Write(p []byte) (int, error) {
	if w.dst != nil {
		_, _ = w.dst.Write(p)
	}
	w.buf.Write(p)
	for {
		i := bytes.IndexByte(w.buf.Bytes(), '\n')
		if i < 0 {
			break
		}
		line := string(w.buf.Next(i + 1))
		w.info.line(strings.TrimRight(line, "\r\n"))
	}
	if w.buf.Len() > 64<<10 { // a line that never ends is not one we parse
		w.buf.Reset()
	}
	return len(p), nil
}

func (b *engineInfoBox) line(l string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := &b.info
	e.Lines++
	if m := reCudaFound.FindStringSubmatch(l); m != nil {
		if n, _ := strconv.Atoi(m[1]); n > 0 {
			e.Backend = "cuda"
		} else {
			e.Backend = "cpu"
		}
		return
	}
	if m := reDevice.FindStringSubmatch(l); m != nil && e.Backend == "cuda" {
		if len(e.Devices) < maxDeviceList {
			e.Devices = append(e.Devices, m[1])
		}
		return
	}
	if m := reOffloaded.FindStringSubmatch(l); m != nil {
		e.Offloaded = m[1]
		if strings.HasPrefix(m[1], "0/") && e.Backend == "unknown" {
			e.Backend = "cpu"
		}
		return
	}
	if m := reGPUBuffer.FindStringSubmatch(l); m != nil {
		if v, err := strconv.ParseFloat(m[2], 64); err == nil {
			e.GPUMiB += v
		}
		return
	}
	if m := reCPUBuffer.FindStringSubmatch(l); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			e.CPUMiB += v
		}
		return
	}
	if reNoGPU.MatchString(l) {
		if e.Backend == "unknown" {
			e.Backend = "cpu"
		}
		if len(e.Warnings) < maxWarnings {
			e.Warnings = append(e.Warnings, strings.TrimSpace(l))
		}
		return
	}
	if reCPUBackend.MatchString(l) && e.Backend == "unknown" {
		e.Backend = "cpu"
	}
}

// Timings is what llama-server reports with every answer (its "timings" object).
type Timings struct {
	PromptN     int     `json:"prompt_n"`
	PromptMS    float64 `json:"prompt_ms"`
	PredictedN  int     `json:"predicted_n"`
	PredictedMS float64 `json:"predicted_ms"`
}

// EngineStats keeps the last few answers' timings and the totals since start.
type EngineStats struct {
	mu    sync.Mutex
	ring  []Timings
	next  int
	count int
	last  time.Time
	kinds map[string]int
}

const statsRing = 20

func NewEngineStats() *EngineStats {
	return &EngineStats{ring: make([]Timings, 0, statsRing), kinds: map[string]int{}}
}

// Record keeps one answer's timings. Zero predicted tokens (an empty answer) is skipped: it
// would drag the average without meaning anything.
func (s *EngineStats) Record(kind string, t Timings) {
	if t.PredictedN <= 0 || t.PredictedMS <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ring) < statsRing {
		s.ring = append(s.ring, t)
	} else {
		s.ring[s.next] = t
	}
	s.next = (s.next + 1) % statsRing
	s.count++
	s.last = time.Now()
	s.kinds[kind]++
}

// Estimate records an answer without llama's timings (a stream that ended without them): the
// token count and the wall time the caller measured. Marked so the summary can say "estimated".
func (s *EngineStats) Estimate(kind string, tokens int, took time.Duration) {
	s.Record(kind+"~", Timings{PredictedN: tokens, PredictedMS: float64(took.Milliseconds())})
}

// StatsSummary is the figure a person reads.
type StatsSummary struct {
	Inferences      int            `json:"inferences"`
	TokPerSecLast   float64        `json:"tokPerSecLast"`
	TokPerSecAvg    float64        `json:"tokPerSecAvg"` // over the last few answers, tokens-weighted
	PromptTokPerSec float64        `json:"promptTokPerSec"`
	LastAt          string         `json:"lastAt,omitempty"`
	Kinds           map[string]int `json:"kinds,omitempty"`
}

func (s *EngineStats) Summary() StatsSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := StatsSummary{Inferences: s.count, Kinds: map[string]int{}}
	for k, v := range s.kinds {
		out.Kinds[k] = v
	}
	if len(s.ring) == 0 {
		return out
	}
	lastIdx := (s.next - 1 + statsRing) % statsRing
	if lastIdx >= len(s.ring) {
		lastIdx = len(s.ring) - 1
	}
	l := s.ring[lastIdx]
	out.TokPerSecLast = round1(float64(l.PredictedN) / l.PredictedMS * 1000)
	var n, ms, pn, pms float64
	for _, t := range s.ring {
		n += float64(t.PredictedN)
		ms += t.PredictedMS
		pn += float64(t.PromptN)
		pms += t.PromptMS
	}
	if ms > 0 {
		out.TokPerSecAvg = round1(n / ms * 1000)
	}
	if pms > 0 {
		out.PromptTokPerSec = round1(pn / pms * 1000)
	}
	out.LastAt = s.last.UTC().Format("2006-01-02 15:04:05 UTC")
	return out
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

// SpeedVerdict reads the generation rate against what the hardware should do: a 12B Q4 on a
// consumer GPU generates tens of tokens a second; on four CPU threads, low single digits.
func SpeedVerdict(avg float64, info EngineInfo) string {
	switch {
	case avg == 0:
		return "no answers timed yet , ask the box something, or run tools/gpu.sh"
	case avg < 6 && info.OnGPU():
		return fmt.Sprintf("%.1f tok/s is CPU speed although llama-server reports the GPU , VRAM contention, thermal throttle, or a huge context; look at nvidia-smi while it answers", avg)
	case avg < 6:
		return fmt.Sprintf("%.1f tok/s: CPU speed", avg)
	case avg < 15:
		return fmt.Sprintf("%.1f tok/s: slow for a GPU , partial offload or a shared card", avg)
	default:
		return fmt.Sprintf("%.1f tok/s: GPU speed", avg)
	}
}
