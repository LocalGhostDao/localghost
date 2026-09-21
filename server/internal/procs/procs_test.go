package procs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// holdersOf must find this very process when it holds a file under the "mount" by descriptor,
// and by memory mapping alone , the case a descriptor scan misses and a dying llama-server is.
func TestHoldersOfFindsDescriptorsAndMappings(t *testing.T) {
	dir := t.TempDir()
	if got := HoldersOf(dir); !strings.HasPrefix(got, "none found") {
		t.Fatalf("empty dir held by: %s", got)
	}
	f, err := os.Create(filepath.Join(dir, "model.gguf"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(make([]byte, 8192)); err != nil {
		t.Fatal(err)
	}
	self := strings.TrimSpace(func() string { b, _ := os.ReadFile("/proc/self/comm"); return string(b) }())
	want := self + "[" + strconv.Itoa(os.Getpid()) + "]"
	if got := HoldersOf(dir); !strings.Contains(got, want) {
		t.Fatalf("open descriptor not seen: %q, want %q", got, want)
	}
	// Now by mapping only: close the descriptor, keep the mmap.
	m, err := syscall.Mmap(int(f.Fd()), 0, 4096, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if got := HoldersOf(dir); !strings.Contains(got, want) || !strings.HasSuffix(got, " R") && !strings.HasSuffix(got, " S") {
		t.Fatalf("mapping not seen, or state unparsed: %q", got)
	}
	_ = syscall.Munmap(m)
	if got := HoldersOf(dir); !strings.HasPrefix(got, "none found") {
		t.Fatalf("after munmap still held: %s", got)
	}
}

// A process running a binary from under the prefix is found, named and ended; one running from
// elsewhere is left alone.
func TestKillStraysEndsWhatRunsFromThePrefix(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Skip("no /bin/sleep")
	}
	if err := os.WriteFile(filepath.Join(bin, "llama-server"), src, 0o755); err != nil {
		t.Fatal(err)
	}
	stray := exec.Command(filepath.Join(bin, "llama-server"), "300")
	if err := stray.Start(); err != nil {
		t.Fatal(err)
	}
	bystander := exec.Command("/bin/sleep", "300")
	if err := bystander.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bystander.Process.Kill(); _, _ = bystander.Process.Wait() }()
	names := KillStrays(bin+"/", 2*time.Second)
	if len(names) != 1 || !strings.HasPrefix(names[0], "llama-server[") {
		t.Fatalf("strays = %v, want the one llama-server", names)
	}
	done := make(chan error, 1)
	go func() { done <- stray.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stray still alive after KillStrays")
	}
	if _, err := os.Stat("/proc/" + strconv.Itoa(bystander.Process.Pid)); err != nil {
		t.Fatal("the bystander was killed")
	}
	if KillStrays(bin+"/", time.Second) != nil {
		t.Fatal("nothing left, nothing killed")
	}
}

// pendingSignals reads the kernel's own mask: this process has nothing pending; a SIGKILL sent to
// a stopped child is delivered-not-acted-on for the instant before it dies , the shape an
// unkillable process shows for weeks.
func TestPendingSignalsAndUnkillable(t *testing.T) {
	if got := pendingSignals(os.Getpid()); got != "none" {
		t.Fatalf("this process: %q", got)
	}
	if Unkillable(os.Getpid()) {
		t.Fatal("a live process with nothing pending is not unkillable")
	}
	if Unkillable(999999999) {
		t.Fatal("a pid that does not exist is not unkillable")
	}
	child := exec.Command("/bin/sleep", "300")
	if err := child.Start(); err != nil {
		t.Skip(err)
	}
	pid := child.Process.Pid
	_ = child.Process.Signal(syscall.SIGSTOP)
	time.Sleep(50 * time.Millisecond)
	_ = child.Process.Signal(syscall.SIGTERM) // a stopped process keeps TERM pending
	time.Sleep(50 * time.Millisecond)
	if got := pendingSignals(pid); !strings.Contains(got, "TERM") {
		t.Fatalf("stopped child with TERM sent: pending = %q", got)
	}
	_ = child.Process.Kill()
	_, _ = child.Process.Wait()
	if s := procState(pid); s != "" {
		t.Fatalf("after wait the process is gone, state %q", s)
	}
}
