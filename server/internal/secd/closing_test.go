package secd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// An upload streaming into the volume when the box starts locking lets go of its file: the
// handler answers appears-down and no .part stays behind to keep umount busy. Covers both ways an
// upload can be caught: bytes still arriving (the gate), and the phone stalled mid-body (the read
// deadline). On 29 Sep 2026 a phone re-sending its camera roll held the volume through every
// secd restart for 75 s, and the lock gave up with LUKS still open.
func TestCloseDoorsCutsUploads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stalled bool
	}{{"streaming", false}, {"stalled", true}} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			s := &Server{cfg: Config{StateDir: state}, session: newSessionManager(time.Hour), mounted: 0}
			tok, err := s.session.Issue()
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(s.handleFrameUpload))
			defer srv.Close()

			pr, pw := io.Pipe()
			req, _ := http.NewRequest(http.MethodPost, srv.URL, pr)
			req.Header.Set("X-Ghost-Token", tok)
			type answer struct {
				code int
				err  error
			}
			got := make(chan answer, 1)
			go func() {
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					got <- answer{err: err}
					return
				}
				resp.Body.Close()
				got <- answer{code: resp.StatusCode}
			}()
			chunk := make([]byte, 32<<10)
			stopWriting := make(chan struct{})
			var once sync.Once
			finish := func() { once.Do(func() { close(stopWriting); _ = pw.Close() }) }
			defer finish() // before srv.Close, which waits for the handler
			go func() {
				for {
					select {
					case <-stopWriting:
						return
					default:
					}
					if _, err := pw.Write(chunk); err != nil {
						return
					}
					if tc.stalled {
						<-stopWriting // one chunk, then the phone goes quiet with the body unfinished
						return
					}
					time.Sleep(5 * time.Millisecond)
				}
			}()
			incoming := filepath.Join(state, "mnt", "slot0", "frames", "incoming")
			waitFor(t, func() bool { m, _ := filepath.Glob(filepath.Join(incoming, "*.part")); return len(m) == 1 })
			if n := s.uploadsNow(); n != 1 {
				t.Fatalf("%d uploads registered", n)
			}

			t0 := time.Now()
			s.closeDoors()
			var a answer
			select {
			case a = <-got:
			case <-time.After(5 * time.Second):
				t.Fatal("the upload still holds the volume 5 s after the doors closed")
			}
			finish()
			if a.err == nil && a.code != http.StatusServiceUnavailable {
				t.Fatalf("answered %d, want appears-down", a.code)
			}
			if d := time.Since(t0); d > 2*time.Second {
				t.Fatalf("took %s to let go", d)
			}
			waitFor(t, func() bool { m, _ := filepath.Glob(filepath.Join(incoming, "*")); return len(m) == 0 })
			if n := s.uploadsNow(); n != 0 {
				t.Fatalf("%d uploads still registered", n)
			}
		})
	}
}

// While the doors are closed a new upload is refused before a file is made; once they open again
// (a halt that left the volume mounted) uploads are taken as before.
func TestClosedDoorsRefuseNewUploads(t *testing.T) {
	state := t.TempDir()
	s := &Server{cfg: Config{StateDir: state}, session: newSessionManager(time.Hour), mounted: 0}
	s.closing.Store(true)
	tok, _ := s.session.Issue()
	post := func() int {
		r := httptest.NewRequest(http.MethodPost, "/v1/frames", strings.NewReader("jpegbytes"))
		r.Header.Set("X-Ghost-Token", tok)
		w := httptest.NewRecorder()
		s.handleFrameUpload(w, r)
		return w.Code
	}
	if c := post(); c != http.StatusServiceUnavailable {
		t.Fatalf("closing: %d", c)
	}
	if _, err := os.Stat(filepath.Join(state, "mnt", "slot0", "frames", "incoming")); err == nil {
		m, _ := filepath.Glob(filepath.Join(state, "mnt", "slot0", "frames", "incoming", "*"))
		if len(m) != 0 {
			t.Fatalf("a refused upload left %v", m)
		}
	}
	s.closing.Store(false)
	if c := post(); c != http.StatusAccepted {
		t.Fatalf("open again: %d", c)
	}
}

func (s *Server) uploadsNow() int {
	s.upMu.Lock()
	defer s.upMu.Unlock()
	return len(s.uploads)
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached in 5 s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
