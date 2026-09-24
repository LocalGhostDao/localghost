// Package gpu is the one place the box asks nvidia-smi how the card is doing. Two things asked
// before, each on its own clock: watchd's stats sampler every ten seconds, and secd's /v1/status on
// every Box Status poll. On a healthy card that is cheap. On a card the driver cannot bring up it
// is not: every nvidia-smi opens /dev/nvidia0, the driver tries RmInitAdapter again, fails again,
// and writes two lines to the kernel log , every ten seconds, for days , and a recovery attempt
// (a reset, a rebind) races a sampler that opens the device mid-sequence. So: one answer, reused
// for a few seconds by whoever asks; after a failure, no attempt at all for a while (a minute,
// doubling to a quarter hour); and no exec whatsoever when the driver's own list of cards is empty
// (/proc/driver/nvidia/gpus , a read that touches no hardware), because a card the driver does not
// have, nvidia-smi will not find either.
package gpu

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Stats is what nvidia-smi says about the first card.
type Stats struct {
	UsedMiB  float64
	TotalMiB float64
	Util     float64 // percent
	At       time.Time
}

const (
	okTTL      = 5 * time.Second  // a fresh answer serves every asker this long
	backoffMin = time.Minute      // the first wait after a failure
	backoffMax = 15 * time.Minute // and the longest, doubling up to it
	execBound  = 2 * time.Second  // never wait longer for nvidia-smi, whatever the caller allows
)

var (
	command   = "nvidia-smi"               // replaced in tests
	driverDir = "/proc/driver/nvidia/gpus" // the driver's own list; absent = module not loaded

	mu       sync.Mutex
	last     Stats
	lastErr  error
	failures int
	nextTry  time.Time
)

// Known is the cards the driver has, by PCI address, from its own /proc list. Empty when the
// module is not loaded or it knows no card; it says nothing about whether a card works.
func Known() []string {
	es, err := os.ReadDir(driverDir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// Query returns the card's memory and utilization, from a recent answer when there is one, from
// nvidia-smi when there is not, and from memory of the last failure when the card is being left
// alone. The error says which.
func Query(ctx context.Context) (Stats, error) {
	mu.Lock()
	defer mu.Unlock()
	now := time.Now()
	if lastErr == nil && !last.At.IsZero() && now.Sub(last.At) < okTTL {
		return last, nil
	}
	if now.Before(nextTry) {
		return Stats{}, fmt.Errorf("%w (next look in %s)", lastErr, nextTry.Sub(now).Round(time.Second))
	}
	if len(Known()) == 0 {
		return fail(now, errors.New("the driver lists no GPU"))
	}
	cctx, cancel := context.WithTimeout(ctx, execBound)
	defer cancel()
	cmd := exec.CommandContext(cctx, command,
		"--query-gpu=memory.used,memory.total,utilization.gpu", "--format=csv,noheader,nounits")
	// An nvidia-smi stuck inside a wedged driver does not die on SIGKILL either (it sits in D);
	// without a WaitDelay, Output would then wait for its pipe forever, and the sampler with it.
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if ee := (*exec.ExitError)(nil); errors.As(err, &ee) && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		if cctx.Err() != nil {
			msg = "nvidia-smi did not answer in time (wedged driver?)"
		}
		if msg == "" {
			msg = err.Error()
		}
		return fail(now, errors.New(msg))
	}
	f := strings.Split(strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]), ",")
	if len(f) != 3 {
		return fail(now, fmt.Errorf("unparseable nvidia-smi line %q", strings.TrimSpace(string(out))))
	}
	used, e1 := strconv.ParseFloat(strings.TrimSpace(f[0]), 64)
	total, e2 := strconv.ParseFloat(strings.TrimSpace(f[1]), 64)
	util, e3 := strconv.ParseFloat(strings.TrimSpace(f[2]), 64)
	if e1 != nil || e2 != nil || e3 != nil {
		return fail(now, fmt.Errorf("unparseable nvidia-smi line %q", strings.TrimSpace(string(out))))
	}
	last = Stats{UsedMiB: used, TotalMiB: total, Util: util, At: now}
	lastErr, failures, nextTry = nil, 0, time.Time{}
	return last, nil
}

// fail remembers the failure and how long to leave the card alone: a minute, then two, four ...
// up to a quarter hour. The first failure after a success is looked at again soonest.
func fail(now time.Time, err error) (Stats, error) {
	lastErr = err
	wait := backoffMin << uint(failures)
	if wait > backoffMax || wait <= 0 {
		wait = backoffMax
	}
	if failures < 10 {
		failures++
	}
	nextTry = now.Add(wait)
	last = Stats{}
	return Stats{}, err
}

// Reset forgets the last answer and any backoff (tests; and a lock/unlock that wants a fresh look).
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	last, lastErr, failures, nextTry = Stats{}, nil, 0, time.Time{}
}
