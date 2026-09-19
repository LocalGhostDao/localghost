package pair

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPayload(n int) []byte {
	// Deterministic, link-shaped: ASCII with the characters a real link uses, so the fixture the
	// app test reads is the same bytes on both sides.
	r := rand.New(rand.NewSource(42))
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.~%&=?:/"
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[r.Intn(len(alphabet))]
	}
	copy(b, "localghost://enroll?")
	return b
}

func TestStreamAnyKFramesRebuild(t *testing.T) {
	payload := testPayload(1243)
	s, err := NewStream(payload, 113, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if s.K != 11 || s.M != 6 {
		t.Fatalf("K=%d M=%d, want 11 and 6 for 1243 bytes at 113 per block", s.K, s.M)
	}
	frames := s.Frames()
	if len(frames) != s.K+s.M {
		t.Fatalf("%d frames", len(frames))
	}
	// Every frame fits the v8 budget.
	for i, f := range frames {
		if len(f) > versionM[8][0]-3 {
			t.Fatalf("frame %d is %d bytes, over v8's %d", i, len(f), versionM[8][0]-3)
		}
		if v, verr := chooseVersion(len(f)); verr != nil || v > 8 {
			t.Fatalf("frame %d needs v%d", i, v)
		}
	}
	// Any K distinct frames: 200 random subsets, including ones with no data frames at all.
	r := rand.New(rand.NewSource(7))
	for trial := 0; trial < 200; trial++ {
		perm := r.Perm(len(frames))
		pick := make([]string, 0, s.K+3)
		for _, i := range perm[:s.K] {
			pick = append(pick, frames[i])
		}
		pick = append(pick, frames[perm[0]], frames[perm[1]]) // duplicates are harmless
		got, err := DecodeStream(pick)
		if err != nil {
			t.Fatalf("trial %d: %v", trial, err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("trial %d: payload mismatch", trial)
		}
	}
	// Parity frames alone rebuild the payload when there are K of them.
	p2 := testPayload(300)
	s2, _ := NewStream(p2, 100, 1.0)
	if s2.K != 3 || s2.M != 3 {
		t.Fatalf("K=%d M=%d", s2.K, s2.M)
	}
	if got, err := DecodeStream(s2.Frames()[s2.K:]); err != nil || !bytes.Equal(got, p2) {
		t.Fatalf("parity-only decode: %v", err)
	}
	// K-1 frames are not enough.
	if _, err := DecodeStream(frames[:s.K-1]); err == nil {
		t.Fatal("K-1 frames decoded")
	}
}

func TestStreamRejectsCorruptAndForeignFrames(t *testing.T) {
	payload := testPayload(500)
	s, _ := NewStream(payload, 64, 0.5)
	frames := s.Frames()
	// A flipped byte in a body fails the part crc.
	bad := []byte(frames[2])
	bad[len(bad)-1] ^= 0x55
	if _, err := ParseStreamFrame(string(bad)); err == nil || !strings.Contains(err.Error(), "part crc") {
		t.Fatalf("corrupt body accepted: %v", err)
	}
	// A frame from another set is refused.
	other, _ := NewStream(testPayload(501), 64, 0.5)
	mixed := append(append([]string(nil), frames[:s.K-1]...), other.Frame(0))
	if _, err := DecodeStream(mixed); err == nil || !strings.Contains(err.Error(), "different") {
		t.Fatalf("mixed set accepted: %v", err)
	}
	// Small payloads and odd block sizes round-trip too.
	for _, n := range []int{1, 7, 63, 64, 65, 129} {
		for _, bs := range []int{8, 13, 64} {
			p := testPayload(n)
			st, err := NewStream(p, bs, 0.5)
			if err != nil {
				t.Fatal(err)
			}
			fr := st.Frames()
			got, err := DecodeStream(fr[st.M:]) // drop the first M frames: exactly K remain
			if err != nil || !bytes.Equal(got, p) {
				t.Fatalf("n=%d bs=%d: %v", n, bs, err)
			}
		}
	}
}

// The same frames, checked into the app's test resources, so the Kotlin decoder is proven against
// what the box actually emits. Regenerate with UPDATE_FIXTURES=1.
func TestStreamFixtureForApp(t *testing.T) {
	payload := testPayload(1243)
	s, _ := NewStream(payload, 113, 0.5)
	var b strings.Builder
	fmt.Fprintf(&b, "payload %s\n", base64.StdEncoding.EncodeToString(payload))
	for _, f := range s.Frames() {
		fmt.Fprintf(&b, "frame %s\n", base64.StdEncoding.EncodeToString([]byte(f)))
	}
	want := b.String()
	path := filepath.Join("testdata", "lgqr2_fixture.txt")
	if os.Getenv("UPDATE_FIXTURES") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fixture missing (run with UPDATE_FIXTURES=1): %v", err)
	}
	if string(got) != want {
		t.Fatal("fixture is stale: the frame format or the test payload changed; regenerate on both sides")
	}
}
