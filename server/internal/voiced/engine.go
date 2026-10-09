package voiced

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Engine is whisper.cpp's command-line program and the speech model it runs. Both come from the
// mirror through tools/setup_whisper.sh (sets whisper and speech), land on the volume like the chat
// model's engine and weights do, and are looked for again on every pass, so a model that arrives
// later (update.sh speech) is used without a restart.
type Engine struct {
	Bin   string
	Model string
	// Prompt is whisper's initial prompt, the people's names as the box spells them (names.go);
	// "" asks for nothing.
	Prompt string
}

// Name is the model's file name without its extension, as recorded beside each transcript.
func (e Engine) Name() string {
	return strings.TrimSuffix(filepath.Base(e.Model), filepath.Ext(e.Model))
}

// FindEngine looks for whisper-cli and a ggml speech model. GHOST_WHISPER_BIN and GHOST_WHISPER_MODEL
// override the search. The reason says what is missing when ok is false.
func FindEngine(mount string) (e Engine, reason string, ok bool) {
	bins := []string{os.Getenv("GHOST_WHISPER_BIN"),
		filepath.Join(mount, "bin", "whisper-cli"),
		"/opt/localghost/whisper.cpp/build/bin/whisper-cli",
		"/opt/localghost/bin/whisper-cli"}
	for _, b := range bins {
		if b == "" {
			continue
		}
		if st, err := os.Stat(b); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			e.Bin = b
			break
		}
	}
	if m := os.Getenv("GHOST_WHISPER_MODEL"); m != "" {
		if st, err := os.Stat(m); err == nil && st.Size() > 0 {
			e.Model = m
		}
	}
	if e.Model == "" {
		e.Model = pickModel([]string{filepath.Join(mount, "ai-models"), "/opt/localghost/models/speech"})
	}
	switch {
	case e.Bin == "" && e.Model == "":
		return e, "no speech engine on this box (whisper-cli and a ggml model: sudo ./tools/update.sh speech)", false
	case e.Bin == "":
		return e, "no whisper-cli on this box (sudo ./tools/update.sh speech)", false
	case e.Model == "":
		return e, "no speech model on this box (ai-models/ggml-*.bin: sudo ./tools/update.sh speech)", false
	}
	return e, "", true
}

// pickModel: the largest ggml-*.bin in the first folder that has one (the largest is the best of
// whisper's sizes), never a VAD model or one of whisper.cpp's for-tests stubs.
func pickModel(dirs []string) string {
	for _, d := range dirs {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		type cand struct {
			path string
			size int64
		}
		var cs []cand
		for _, en := range ents {
			n := en.Name()
			if en.IsDir() || !strings.HasPrefix(n, "ggml-") || !strings.HasSuffix(n, ".bin") ||
				strings.Contains(n, "vad") || strings.Contains(n, "silero") || strings.Contains(n, "for-tests") {
				continue
			}
			if st, err := en.Info(); err == nil && st.Size() > 1<<20 {
				cs = append(cs, cand{filepath.Join(d, n), st.Size()})
			}
		}
		if len(cs) > 0 {
			sort.Slice(cs, func(i, j int) bool { return cs[i].size > cs[j].size })
			return cs[0].path
		}
	}
	return ""
}

// Result is one transcription.
type Result struct {
	Text string
	Lang string
	Took time.Duration
}

// Threads for whisper: half the cores, at most four. The box's CPU also runs the photo pipeline
// and, some nights, the captions; a voice note is not urgent.
func Threads() int {
	n := runtime.NumCPU() / 2
	if n > 4 {
		n = 4
	}
	if n < 1 {
		n = 1
	}
	return n
}

// Transcribe runs whisper-cli on a 16 kHz mono WAV and reads what it wrote. The output files go to
// workDir, which is on the encrypted volume: a transcript is never written to the OS disk. The
// language is detected per note (Vlad talks in English and Romanian). The GPU is left to the chat
// model (-ng); the process runs at nice 10 for the same reason.
func (e Engine) Transcribe(ctx context.Context, wav, workDir string, audio time.Duration) (Result, error) {
	var r Result
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return r, err
	}
	base := filepath.Join(workDir, strings.TrimSuffix(filepath.Base(wav), filepath.Ext(wav)))
	defer os.Remove(base + ".json")
	defer os.Remove(base + ".txt")
	limit := 10*time.Minute + 4*audio
	cctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	args := []string{"-m", e.Model, "-f", wav, "-l", "auto", "-t", fmt.Sprint(Threads()), "-oj", "-otxt", "-of", base, "-np"}
	if filepath.Base(e.Bin) == "whisper-cli" && os.Getenv("GHOST_WHISPER_GPU") != "1" {
		args = append(args, "-ng")
	}
	if e.Prompt != "" {
		args = append(args, "--prompt", e.Prompt)
	}
	cmd := exec.CommandContext(cctx, e.Bin, args...)
	var stderr tailBuffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	t0 := time.Now()
	if err := cmd.Start(); err != nil {
		return r, err
	}
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 10)
	err := cmd.Wait()
	r.Took = time.Since(t0)
	if cctx.Err() == context.DeadlineExceeded {
		return r, fmt.Errorf("whisper-cli ran past %s on a %s note, stopped", limit.Round(time.Second), audio.Round(time.Second))
	}
	if err != nil {
		return r, fmt.Errorf("whisper-cli: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	if b, jerr := os.ReadFile(base + ".json"); jerr == nil {
		if text, lang, perr := ParseWhisperJSON(b); perr == nil {
			r.Text, r.Lang = text, lang
			return r, nil
		}
	}
	// the JSON writer of some whisper.cpp versions leaves control characters unescaped; the plain
	// text file says the same words
	b, terr := os.ReadFile(base + ".txt")
	if terr != nil {
		return r, fmt.Errorf("whisper-cli wrote neither %s.json nor .txt: %s", filepath.Base(base), strings.TrimSpace(stderr.String()))
	}
	r.Text = CleanTranscript(strings.Split(string(b), "\n"))
	return r, nil
}

// ParseWhisperJSON reads whisper-cli's -oj output: the segments' text and the detected language.
func ParseWhisperJSON(b []byte) (text, lang string, err error) {
	var j struct {
		Result struct {
			Language string `json:"language"`
		} `json:"result"`
		Transcription []struct {
			Text string `json:"text"`
		} `json:"transcription"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		return "", "", err
	}
	if j.Transcription == nil {
		return "", "", errors.New("no transcription in whisper's JSON")
	}
	segs := make([]string, 0, len(j.Transcription))
	for _, s := range j.Transcription {
		segs = append(segs, s.Text)
	}
	return CleanTranscript(segs), j.Result.Language, nil
}

// markerRE: whisper's non-speech markers, a whole segment of them ([BLANK_AUDIO], (silence), [Music]).
var markerRE = regexp.MustCompile(`^\s*(\[[^\]]*\]|\([^)]*\)|\*[^*]*\*)\s*$`)

// CleanTranscript joins the segments into prose: markers dropped, whitespace collapsed, and a
// segment identical to the one before it dropped (whisper looping on a long silence says the same
// line again and again; a person rarely does, segment for segment).
func CleanTranscript(segs []string) string {
	var out []string
	for _, s := range segs {
		s = strings.Join(strings.Fields(s), " ")
		if s == "" || markerRE.MatchString(s) {
			continue
		}
		if len(out) > 0 && strings.EqualFold(out[len(out)-1], s) {
			continue
		}
		out = append(out, s)
	}
	return strings.TrimSpace(strings.Join(out, " "))
}

// tailBuffer keeps the last 2 KB written to it (whisper's stderr, for the error line).
type tailBuffer struct{ b bytes.Buffer }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b.Write(p)
	if t.b.Len() > 4096 {
		keep := t.b.Bytes()[t.b.Len()-2048:]
		nb := append([]byte(nil), keep...)
		t.b.Reset()
		t.b.Write(nb)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	s := t.b.String()
	if len(s) > 600 {
		s = s[len(s)-600:]
	}
	return s
}
