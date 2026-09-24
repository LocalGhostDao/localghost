package gpu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSMI writes a script that counts its calls in a file and either answers a line or fails.
func fakeSMI(t *testing.T, dir string, body string) (cmd string, calls func() int) {
	t.Helper()
	counter := filepath.Join(dir, "calls")
	script := filepath.Join(dir, "nvidia-smi")
	sh := "#!/bin/sh\necho x >> " + counter + "\n" + body + "\n"
	if err := os.WriteFile(script, []byte(sh), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, func() int {
		b, _ := os.ReadFile(counter)
		return strings.Count(string(b), "x")
	}
}

func TestNoExecWhenTheDriverListsNoCard(t *testing.T) {
	dir := t.TempDir()
	Reset()
	driverDir = filepath.Join(dir, "gpus-absent")
	var calls func() int
	command, calls = fakeSMI(t, dir, "echo '100, 12282, 7'")
	if _, err := Query(context.Background()); err == nil || !strings.Contains(err.Error(), "lists no GPU") {
		t.Fatalf("err = %v", err)
	}
	if calls() != 0 {
		t.Fatal("nvidia-smi was run although the driver has no card")
	}
	// and the backoff holds: the next asker within the minute gets the memory, not an exec
	if _, err := Query(context.Background()); err == nil || !strings.Contains(err.Error(), "next look in") {
		t.Fatalf("second err = %v", err)
	}
}

func TestFailureBacksOffAndSuccessIsReused(t *testing.T) {
	dir := t.TempDir()
	Reset()
	driverDir = filepath.Join(dir, "gpus")
	if err := os.MkdirAll(filepath.Join(driverDir, "0000:2e:00.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if k := Known(); len(k) != 1 || k[0] != "0000:2e:00.0" {
		t.Fatalf("known = %v", k)
	}
	var calls func() int
	command, calls = fakeSMI(t, dir, "echo 'No devices were found' >&2; exit 6")
	if _, err := Query(context.Background()); err == nil || !strings.Contains(err.Error(), "No devices were found") {
		t.Fatalf("err = %v", err)
	}
	if calls() != 1 {
		t.Fatalf("calls = %d", calls())
	}
	for i := 0; i < 5; i++ { // the sampler's next five ticks
		if _, err := Query(context.Background()); err == nil || !strings.Contains(err.Error(), "next look in") {
			t.Fatalf("tick %d: err = %v", i, err)
		}
	}
	if calls() != 1 {
		t.Fatalf("a failing card was poked again: calls = %d", calls())
	}
	if failures != 1 || nextTry.Sub(time.Now()) > backoffMin || nextTry.Sub(time.Now()) < backoffMin-5*time.Second {
		t.Fatalf("first backoff = %s (failures %d), want ~1m", nextTry.Sub(time.Now()), failures)
	}
	// Doubling: two more failures and the wait is four minutes; ten failures cap at the max.
	nextTry = time.Time{}
	Query(context.Background())
	nextTry = time.Time{}
	Query(context.Background())
	if w := nextTry.Sub(time.Now()); w < 4*time.Minute-5*time.Second || w > 4*time.Minute {
		t.Fatalf("third backoff = %s, want ~4m", w)
	}
	failures = 10
	nextTry = time.Time{}
	Query(context.Background())
	if w := nextTry.Sub(time.Now()); w > backoffMax || w < backoffMax-5*time.Second {
		t.Fatalf("capped backoff = %s, want ~15m", w)
	}

	// The card comes back: one exec answers everyone for the next few seconds.
	Reset()
	command, calls = fakeSMI(t, dir, "echo '3100, 12282, 42'")
	before := calls()
	g, err := Query(context.Background())
	if err != nil || g.UsedMiB != 3100 || g.TotalMiB != 12282 || g.Util != 42 {
		t.Fatalf("g = %+v err = %v", g, err)
	}
	for i := 0; i < 3; i++ {
		if g2, err := Query(context.Background()); err != nil || g2 != g {
			t.Fatalf("reuse: %+v %v", g2, err)
		}
	}
	if calls()-before != 1 {
		t.Fatalf("a fresh answer was not reused: calls = %d", calls()-before)
	}
	// Past the TTL it asks again.
	last.At = time.Now().Add(-okTTL - time.Second)
	if _, err := Query(context.Background()); err != nil || calls()-before != 2 {
		t.Fatalf("after the TTL: calls = %d err = %v", calls()-before, err)
	}
}

func TestHangIsBoundedAndNamed(t *testing.T) {
	dir := t.TempDir()
	Reset()
	driverDir = filepath.Join(dir, "gpus")
	os.MkdirAll(filepath.Join(driverDir, "0000:2e:00.0"), 0o755)
	command, _ = fakeSMI(t, dir, "sleep 5")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	_, err := Query(ctx)
	if err == nil || !strings.Contains(err.Error(), "did not answer in time") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(t0) > 2*time.Second {
		t.Fatal("the caller's bound was not honoured")
	}
}
