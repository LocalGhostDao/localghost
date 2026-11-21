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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
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
of=""; f=""; m=""; lang=""; ng=0; th=""
while [ $# -gt 0 ]; do
  case "$1" in
    -of) of="$2"; shift 2 ;;
    -f) f="$2"; shift 2 ;;
    -m) m="$2"; shift 2 ;;
    -l) lang="$2"; shift 2 ;;
    -t) th="$2"; shift 2 ;;
    -ng) ng=1; shift ;;
    *) shift ;;
  esac
done
[ -s "$m" ] || { echo "no model $m" >&2; exit 2; }
[ "$lang" = auto ] || [ -n "${FAKE_WHISPER_HEARS:-}" ] || { echo "language not auto" >&2; exit 2; }
[ "$ng" = 1 ] || { echo "GPU not left alone" >&2; exit 2; }
# what it was run with, one line a run, for the tests that ask: a question's file is under voiced/ask
case "$f" in */voiced/ask/*) what=ask ;; *) what=note ;; esac
[ -z "${FAKE_WHISPER_RUNS:-}" ] || echo "$what lang=$lang threads=$th" >> "$FAKE_WHISPER_RUNS"
# a voice note takes its time (a question does not): the test that sets one aside for a question
[ "$what" = ask ] || [ -z "${FAKE_WHISPER_NOTE_SLEEP:-}" ] || sleep "$FAKE_WHISPER_NOTE_SLEEP" >/dev/null 2>&1
# bytes 22-27: channels (1) and rate (16000 = 80 3e)
hdr=$(od -An -tx1 -j22 -N6 "$f" | tr -d ' \n')
[ "$hdr" = "0100803e0000" ] || { echo "not 16 kHz mono: $hdr" >&2; exit 3; }
# FAKE_WHISPER_HEARS=xx: left to itself it hears language xx; told a language, it hears that one
if [ -n "${FAKE_WHISPER_HEARS:-}" ]; then
  if [ "$lang" = auto ]; then printf '{"result":{"language":"%s"},"transcription":[{"text":" Beth yw r tywydd."}]}' "$FAKE_WHISPER_HEARS" > "$of.json"
  else printf '{"result":{"language":"%s"},"transcription":[{"text":" What is the weather?"}]}' "$lang" > "$of.json"; fi
  exit 0
fi
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
	r, err := d.Hear(context.Background(), wav, nil)
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
	if _, err := d.Hear(context.Background(), other, nil); err == nil || !strings.Contains(err.Error(), "not a file of") {
		t.Fatalf("a file outside ask: %v", err)
	}
	if _, err := d.Hear(context.Background(), filepath.Join(ask, "..", "inbox", "x.wav"), nil); err == nil {
		t.Fatal("a path climbing out of ask")
	}
}

// runs reads what the fake whisper was run with, one line a run.
func runs(t *testing.T, path string) []string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(b)), " ", "|"))
}

// A question with nobody in it: a covered microphone, a tap by mistake. A speech model handed
// silence writes words of its own ("Thank you."); the box does not hand it over.
func TestAQuestionOfSilenceIsNotGivenToWhisper(t *testing.T) {
	_, mount := fakeEngine(t)
	ran := filepath.Join(t.TempDir(), "runs")
	t.Setenv("FAKE_WHISPER_RUNS", ran)
	d := &Daemon{Mount: mount, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Find: FindEngine}
	ask := AskDir(mount)
	_ = os.MkdirAll(ask, 0o750)
	quiet := filepath.Join(ask, "quiet.wav")
	// two seconds of a room: a few parts in ten thousand of full scale
	data := make([]byte, 2*16000*2)
	for i := 0; i < 2*16000; i++ {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(int16(6*(i%5-2))))
	}
	if err := os.WriteFile(quiet, append(WavHeader(16000, 1, 16, uint32(len(data))), data...), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := d.Hear(context.Background(), quiet, []string{"en"})
	if err != nil || r.Text != "" {
		t.Fatalf("silence: %+v %v", r, err)
	}
	if got := runs(t, ran); len(got) != 0 {
		t.Fatalf("whisper was run on silence: %v", got)
	}
	if _, err := os.Stat(quiet); err == nil {
		t.Fatal("the question's file was kept")
	}
	// the measure itself: a tone at a quarter scale, and the room above
	loud := filepath.Join(ask, "loud.wav")
	writeWav(t, loud, 44100, 2, 44100, false)
	f, _ := os.Open(loud)
	info, _ := ReadWavInfo(f)
	l, err := LoudestTenth(f, info)
	f.Close()
	if err != nil || math.Abs(l-8000.0/32768/math.Sqrt2) > 0.003 {
		t.Fatalf("a tone of 8000: %v %v", l, err)
	}
	if err := os.WriteFile(quiet, append(WavHeader(16000, 1, 16, uint32(len(data))), data...), 0o600); err != nil {
		t.Fatal(err)
	}
	f, _ = os.Open(quiet)
	info, _ = ReadWavInfo(f)
	l, _ = LoudestTenth(f, info)
	f.Close()
	if l <= 0 || l >= SilenceFloor {
		t.Fatalf("a room: %v", l)
	}
}

// The language of a few seconds of speech is a guess. Heard as one this person has never spoken
// to the box, nor set their phone to, a short question is heard again as the one they speak
// most; heard as one of theirs, it is left alone; a long one is trusted.
func TestAQuestionHeardInTheWrongLanguageIsHeardAgain(t *testing.T) {
	_, mount := fakeEngine(t)
	ran := filepath.Join(t.TempDir(), "runs")
	t.Setenv("FAKE_WHISPER_RUNS", ran)
	t.Setenv("FAKE_WHISPER_HEARS", "cy")
	d := &Daemon{Mount: mount, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Find: FindEngine}
	d.langs = []string{"en", "ro"} // what the box has heard in voice notes, most first
	ask := AskDir(mount)
	_ = os.MkdirAll(ask, 0o750)
	q := func(seconds int) string {
		p := filepath.Join(ask, "q.wav")
		writeWav(t, p, 16000, 1, seconds*16000, false)
		return p
	}
	r, err := d.Hear(context.Background(), q(3), []string{"ro-RO", "en-GB"})
	if err != nil || r.Text != "What is the weather?" || r.Lang != "en" {
		t.Fatalf("heard again: %+v %v", r, err)
	}
	want := []string{"ask|lang=auto|threads=" + itoa(UrgentThreads()), "ask|lang=en|threads=" + itoa(UrgentThreads())}
	if got := runs(t, ran); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("runs: %v, want %v", got, want)
	}
	// a box that has heard nothing yet goes by the phone's languages, the first of them
	_ = os.Remove(ran)
	d.langs = nil
	if r, _ := d.Hear(context.Background(), q(3), []string{"ro-RO", "en-GB"}); r.Lang != "ro" {
		t.Fatalf("by the phone's languages: %+v", r)
	}
	// heard as one of theirs: once, and as heard
	_ = os.Remove(ran)
	t.Setenv("FAKE_WHISPER_HEARS", "ro")
	if r, _ := d.Hear(context.Background(), q(3), []string{"ro-RO", "en-GB"}); r.Lang != "ro" || len(runs(t, ran)) != 1 {
		t.Fatalf("one of theirs: %+v, runs %v", r, runs(t, ran))
	}
	// nothing known of what they speak: whisper's word stands
	_ = os.Remove(ran)
	t.Setenv("FAKE_WHISPER_HEARS", "cy")
	if r, _ := d.Hear(context.Background(), q(3), nil); r.Lang != "cy" || len(runs(t, ran)) != 1 {
		t.Fatalf("nothing known: %+v, runs %v", r, runs(t, ran))
	}
	// forty seconds is enough to tell a language by: trusted
	_ = os.Remove(ran)
	if r, _ := d.Hear(context.Background(), q(40), []string{"en"}); r.Lang != "cy" || len(runs(t, ran)) != 1 {
		t.Fatalf("a long question: %+v, runs %v", r, runs(t, ran))
	}
	if got := strings.Join(CleanLangs([]string{"en-GB", " RO ", "en", "../etc", "x", "fil_PH", "12", "toolong"}), ","); got != "en,ro,fil" {
		t.Fatalf("clean: %s", got)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// noteDB is the two questions TranscribeNext asks of the database, answered for one pending note.
type noteDB struct {
	mu     sync.Mutex
	path   string
	done   bool
	tries  int
	lang   string
	result string
}

func (n *noteDB) Exec(sql string, args ...any) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if strings.Contains(sql, "tries = tries + 1") {
		n.tries++
	}
	return nil
}

func (n *noteDB) Query(sql string, args ...any) (*poltergres.Rows, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	s := func(v string) *string { return &v }
	switch {
	case strings.Contains(sql, "WHERE status = 'pending' ORDER BY taken_at"):
		if n.done {
			return &poltergres.Rows{}, nil
		}
		return &poltergres.Rows{Vals: [][]*string{{s("0123456789abcdef0123456789abcdef"), s(n.path), s("1000")}}}, nil
	case strings.Contains(sql, "SET status = 'done'"):
		n.done = true
		n.result, _ = args[1].(string)
		n.lang, _ = args[2].(string)
		return &poltergres.Rows{Vals: [][]*string{{s("journal"), s("2026-11-21"), s("1795000000000"), s("1000")}}}, nil
	}
	return &poltergres.Rows{}, nil
}

// A QUESTION GOES FIRST. The queue is three seconds into a voice note when someone asks aloud:
// the note's whisper is stopped, the question is heard at once, and the note is taken up again
// afterwards and finished, with no attempt counted against it.
func TestAQuestionSetsTheNoteQueueAside(t *testing.T) {
	eng, mount := fakeEngine(t)
	ran := filepath.Join(t.TempDir(), "runs")
	t.Setenv("FAKE_WHISPER_RUNS", ran)
	t.Setenv("FAKE_WHISPER_NOTE_SLEEP", "3")
	t.Setenv("FAKE_WHISPER_MODE", "json")
	d := &Daemon{Mount: mount, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Find: FindEngine}
	_ = os.MkdirAll(filepath.Join(mount, "voiced", "archive"), 0o750)
	writeWav(t, filepath.Join(mount, "voiced", "archive", "note.wav"), 16000, 1, 16000, false)
	db := &noteDB{path: filepath.Join("voiced", "archive", "note.wav")}
	ask := AskDir(mount)
	_ = os.MkdirAll(ask, 0o750)
	q := filepath.Join(ask, "q.wav")
	writeWav(t, q, 16000, 1, 16000, false)

	queue := make(chan error, 1)
	go func() { _, err := d.TranscribeNext(context.Background(), db, eng); queue <- err }()
	// wait until the note's whisper is running
	for end := time.Now().Add(5 * time.Second); len(runs(t, ran)) == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the queue never started on the note")
		}
	}
	t0 := time.Now()
	r, err := d.Hear(context.Background(), q, nil)
	if err != nil || r.Text == "" {
		t.Fatalf("the question: %+v %v", r, err)
	}
	if took := time.Since(t0); took > 2*time.Second {
		t.Fatalf("the question waited %s behind a three-second note", took.Round(100*time.Millisecond))
	}
	if err := <-queue; err != nil {
		t.Fatalf("the note: %v", err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if !db.done || db.tries != 0 || db.result != "Walked to the harbour. Tired but good." {
		t.Fatalf("the note afterwards: done %v, tries %d, %q", db.done, db.tries, db.result)
	}
	// the note twice (stopped, then whole), the question once between them, on more cores
	got := runs(t, ran)
	if len(got) != 3 || !strings.HasPrefix(got[1], "ask|") || !strings.HasPrefix(got[0], "note|") || !strings.HasPrefix(got[2], "note|") {
		t.Fatalf("runs: %v", got)
	}
	if !strings.HasSuffix(got[1], "threads="+itoa(UrgentThreads())) || !strings.HasSuffix(got[0], "threads="+itoa(Threads())) {
		t.Fatalf("threads: %v", got)
	}
}
