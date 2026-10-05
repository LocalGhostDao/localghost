package voiced

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeWav writes n frames of a 440 Hz tone at the given format; header can be left unfinished
// (sizes 0) the way a phone that died mid-note leaves it.
func writeWav(t *testing.T, path string, rate, ch, n int, unfinished bool) {
	t.Helper()
	data := make([]byte, n*ch*2)
	for i := 0; i < n; i++ {
		v := int16(8000 * math.Sin(2*math.Pi*440*float64(i)/float64(rate)))
		for c := 0; c < ch; c++ {
			binary.LittleEndian.PutUint16(data[(i*ch+c)*2:], uint16(v))
		}
	}
	dl := uint32(len(data))
	if unfinished {
		dl = 0
	}
	h := WavHeader(rate, ch, 16, dl)
	if unfinished {
		binary.LittleEndian.PutUint32(h[4:8], 0)
	}
	if err := os.WriteFile(path, append(h, data...), 0o600); err != nil {
		t.Fatal(err)
	}
}

// PhoneHeaderHex is one second of the phone's format (16 kHz mono 16-bit) as a 44-byte header. The
// app's VoiceWavTest asserts the same bytes from its own writer: the two sides agree on the file.
const PhoneHeaderHex = "52494646247d000057415645666d74201000000001000100803e0000007d00000200100064617461007d0000"

func TestWavHeaderMatchesPhone(t *testing.T) {
	if got := hex.EncodeToString(WavHeader(16000, 1, 16, 32000)); got != PhoneHeaderHex {
		t.Fatalf("header %s", got)
	}
}

func TestWavInfoAndDuration(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.wav")
	writeWav(t, p, 16000, 1, 16000*3, false)
	f, _ := os.Open(p)
	defer f.Close()
	w, err := ReadWavInfo(f)
	if err != nil {
		t.Fatal(err)
	}
	if w.Rate != 16000 || w.Channels != 1 || w.Bits != 16 || w.DataOff != 44 || w.Duration() != 3*time.Second || !w.IsSpeechFormat() {
		t.Fatalf("%+v %s", w, w.Duration())
	}
	// the phone died before patching the header: the samples still read to the end
	q := filepath.Join(dir, "b.wav")
	writeWav(t, q, 16000, 1, 8000, true)
	g, _ := os.Open(q)
	defer g.Close()
	w2, err := ReadWavInfo(g)
	if err != nil || w2.Duration() != 500*time.Millisecond {
		t.Fatalf("unfinished header: %+v %v", w2, err)
	}
	// not a WAV
	r := filepath.Join(dir, "c.wav")
	_ = os.WriteFile(r, []byte("ID3\x03hello this is an mp3 honest"), 0o600)
	h, _ := os.Open(r)
	defer h.Close()
	if _, err := ReadWavInfo(h); err == nil {
		t.Fatal("an mp3 read as a WAV")
	}
}

func TestToSpeechFormat(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "stereo48k.wav")
	writeWav(t, p, 48000, 2, 48000*2, false) // 2 s of stereo at 48 kHz
	f, _ := os.Open(p)
	defer f.Close()
	w, _ := ReadWavInfo(f)
	out := filepath.Join(dir, "out.wav")
	if err := ToSpeechFormat(f, w, out); err != nil {
		t.Fatal(err)
	}
	g, _ := os.Open(out)
	defer g.Close()
	w2, err := ReadWavInfo(g)
	if err != nil || !w2.IsSpeechFormat() || w2.Duration() != 2*time.Second {
		t.Fatalf("converted: %+v %v %s", w2, err, w2.Duration())
	}
	// the tone survives: the peak is near the source's 8000/32768
	raw := make([]byte, w2.DataLen)
	_, _ = g.ReadAt(raw, w2.DataOff)
	peak := 0
	for i := 0; i+1 < len(raw); i += 2 {
		v := int(int16(binary.LittleEndian.Uint16(raw[i:])))
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	if peak < 7000 || peak > 8100 {
		t.Fatalf("peak after conversion %d", peak)
	}
}

func TestParseWhisperJSONAndClean(t *testing.T) {
	js := `{"systeminfo":"AVX2 = 1","model":{"type":"large"},"params":{"language":"auto"},
	"result":{"language":"ro"},
	"transcription":[
	 {"timestamps":{"from":"00:00:00,000","to":"00:00:04,000"},"offsets":{"from":0,"to":4000},"text":" Azi am mers pe jos până la port."},
	 {"timestamps":{"from":"00:00:04,000","to":"00:00:06,000"},"offsets":{"from":4000,"to":6000},"text":" [BLANK_AUDIO]"},
	 {"timestamps":{"from":"00:00:06,000","to":"00:00:09,000"},"offsets":{"from":6000,"to":9000},"text":"  Mă simt bine. "},
	 {"timestamps":{"from":"00:00:09,000","to":"00:00:12,000"},"offsets":{"from":9000,"to":12000},"text":" Mă simt bine."},
	 {"timestamps":{"from":"00:00:12,000","to":"00:00:14,000"},"offsets":{"from":12000,"to":14000},"text":" (silence)"}
	]}`
	text, lang, err := ParseWhisperJSON([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	if lang != "ro" || text != "Azi am mers pe jos până la port. Mă simt bine." {
		t.Fatalf("%q %q", text, lang)
	}
	if _, _, err := ParseWhisperJSON([]byte(`{"result":{}}`)); err == nil {
		t.Fatal("JSON with no transcription accepted")
	}
	if got := FirstWords("one two three four five six seven eight nine ten eleven", 30); got != "one two three four five six…" {
		t.Fatalf("%q", got)
	}
}

// fakeWhisper is a stand-in whisper-cli: it checks the arguments voiced passes, checks the input is
// a 16 kHz mono WAV, and writes the JSON the real one writes (or only the .txt, or fails).
const fakeWhisper = `#!/bin/sh
of=""; f=""; m=""; lang=""; ng=0
while [ $# -gt 0 ]; do
  case "$1" in
    -of) of="$2"; shift 2 ;;
    -f) f="$2"; shift 2 ;;
    -m) m="$2"; shift 2 ;;
    -l) lang="$2"; shift 2 ;;
    -ng) ng=1; shift ;;
    *) shift ;;
  esac
done
[ -s "$m" ] || { echo "no model $m" >&2; exit 2; }
[ "$lang" = auto ] || { echo "language not auto" >&2; exit 2; }
[ "$ng" = 1 ] || { echo "GPU not left alone" >&2; exit 2; }
# bytes 22-27: channels (1) and rate (16000 = 80 3e)
hdr=$(od -An -tx1 -j22 -N6 "$f" | tr -d ' \n')
[ "$hdr" = "0100803e0000" ] || { echo "not 16 kHz mono: $hdr" >&2; exit 3; }
case "${FAKE_WHISPER_MODE:-json}" in
  json) printf '{"result":{"language":"en"},"transcription":[{"text":" Walked to the harbour."},{"text":" [BLANK_AUDIO]"},{"text":" Tired but good."}]}' > "$of.json" ;;
  txt) printf ' Walked to the harbour.\n Tired but good.\n' > "$of.txt"; printf '{"transcription":[{"text":"bad \001 control"}' > "$of.json" ;;
  fail) echo "whisper_init_from_file: failed to load model" >&2; exit 1 ;;
esac
exit 0
`

func fakeEngine(t *testing.T) (Engine, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin", "whisper-cli")
	_ = os.MkdirAll(filepath.Dir(bin), 0o755)
	if err := os.WriteFile(bin, []byte(fakeWhisper), 0o755); err != nil {
		t.Fatal(err)
	}
	models := filepath.Join(dir, "ai-models")
	_ = os.MkdirAll(models, 0o755)
	big := make([]byte, 3<<20)
	_ = os.WriteFile(filepath.Join(models, "ggml-small.bin"), big[:2<<20], 0o644)
	_ = os.WriteFile(filepath.Join(models, "ggml-large-v3-turbo-q5_0.bin"), big, 0o644)
	_ = os.WriteFile(filepath.Join(models, "ggml-silero-v5.1.2.bin"), append(big, big...), 0o644) // a VAD model, bigger, never picked
	_ = os.WriteFile(filepath.Join(models, "embeddinggemma-300m-qat-Q8_0.gguf"), big, 0o644)
	e, why, ok := FindEngine(dir)
	if !ok {
		t.Fatalf("engine not found: %s", why)
	}
	return e, dir
}

func TestFindEngine(t *testing.T) {
	e, _ := fakeEngine(t)
	if e.Name() != "ggml-large-v3-turbo-q5_0" {
		t.Fatalf("picked %s", e.Model)
	}
	t.Setenv("GHOST_WHISPER_BIN", "")
	empty := t.TempDir()
	if _, why, ok := FindEngine(empty); ok || !strings.Contains(why, "update.sh speech") {
		// /opt/localghost may hold a real whisper on a dev box; only a bare one must say so
		if _, err := os.Stat("/opt/localghost/bin/whisper-cli"); err != nil {
			t.Fatalf("empty mount: ok=%v why=%q", ok, why)
		}
	}
}

func TestTranscribeWithFakeWhisper(t *testing.T) {
	e, dir := fakeEngine(t)
	wav := filepath.Join(dir, "n.wav")
	writeWav(t, wav, 16000, 1, 16000, false)
	work := filepath.Join(dir, "work")
	for _, mode := range []string{"json", "txt"} {
		t.Setenv("FAKE_WHISPER_MODE", mode)
		r, err := e.Transcribe(context.Background(), wav, work, time.Second)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if r.Text != "Walked to the harbour. Tired but good." {
			t.Fatalf("%s: %q", mode, r.Text)
		}
		if mode == "json" && r.Lang != "en" {
			t.Fatalf("lang %q", r.Lang)
		}
	}
	ents, _ := os.ReadDir(work)
	if len(ents) != 0 {
		t.Fatalf("whisper's output left on disk: %v", ents)
	}
	t.Setenv("FAKE_WHISPER_MODE", "fail")
	if _, err := e.Transcribe(context.Background(), wav, work, time.Second); err == nil || !strings.Contains(err.Error(), "failed to load model") {
		t.Fatalf("failure not reported with whisper's words: %v", err)
	}
}

func TestStateFile(t *testing.T) {
	mount := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mount, "voiced"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadState(mount); ok {
		t.Fatal("a state with no file")
	}
	d := &Daemon{Mount: mount}
	d.stat.Why = "no speech engine on this box"
	d.stat.Pending = 1
	d.setWorking("")
	st, ok := ReadState(mount)
	if !ok || st.Why != "no speech engine on this box" || st.Pending != 1 || st.Working != "" || time.Since(time.UnixMilli(st.At)) > time.Minute {
		t.Fatalf("%+v %v", st, ok)
	}
	d.setWorking("0123456789abcdef0123456789abcdef")
	if st, _ = ReadState(mount); st.Working != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("working %+v", st)
	}
	if _, err := os.Stat(StatePath(mount) + ".tmp"); err == nil {
		t.Fatal("tmp file left behind")
	}
}

// A question asked aloud: heard from under voiced/ask, the file gone after, nothing else written;
// a file anywhere else is refused (and removed, since the caller meant it to go).
func TestHearAQuestion(t *testing.T) {
	_, mount := fakeEngine(t)
	t.Setenv("FAKE_WHISPER_MODE", "json")
	d := &Daemon{Mount: mount, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Find: FindEngine}
	ask := AskDir(mount)
	_ = os.MkdirAll(ask, 0o750)
	wav := filepath.Join(ask, "q1.wav")
	writeWav(t, wav, 16000, 1, 16000, false)
	r, err := d.Hear(context.Background(), wav)
	if err != nil || r.Text != "Walked to the harbour. Tired but good." || r.Lang != "en" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := os.Stat(wav); err == nil {
		t.Fatal("the question's file was kept")
	}
	for _, e := range func() []os.DirEntry { ents, _ := os.ReadDir(filepath.Join(mount, "voiced")); return ents }() {
		if e.Name() != "ask" && e.Name() != "work" {
			t.Fatalf("something else was written: %s", e.Name())
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(mount, "voiced", "work")); len(ents) != 0 {
		t.Fatalf("scratch left behind: %v", ents)
	}
	// not under ask: refused
	other := filepath.Join(mount, "voiced", "inbox", "x.wav")
	_ = os.MkdirAll(filepath.Dir(other), 0o750)
	writeWav(t, other, 16000, 1, 16000, false)
	if _, err := d.Hear(context.Background(), other); err == nil || !strings.Contains(err.Error(), "not a file of") {
		t.Fatalf("a file outside ask: %v", err)
	}
	if _, err := d.Hear(context.Background(), filepath.Join(ask, "..", "inbox", "x.wav")); err == nil {
		t.Fatal("a path climbing out of ask")
	}
}
