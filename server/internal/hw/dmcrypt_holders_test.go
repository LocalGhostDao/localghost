package hw

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// holdersOf must find this very process when it holds a file under the "mount" by descriptor,
// and by memory mapping alone , the case a descriptor scan misses and a dying llama-server is.
func TestHoldersOfFindsDescriptorsAndMappings(t *testing.T) {
	dir := t.TempDir()
	if got := holdersOf(dir); !strings.HasPrefix(got, "none found") {
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
	if got := holdersOf(dir); !strings.Contains(got, want) {
		t.Fatalf("open descriptor not seen: %q, want %q", got, want)
	}
	// Now by mapping only: close the descriptor, keep the mmap.
	m, err := syscall.Mmap(int(f.Fd()), 0, 4096, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if got := holdersOf(dir); !strings.Contains(got, want) || !strings.HasSuffix(got, " R") && !strings.HasSuffix(got, " S") {
		t.Fatalf("mapping not seen, or state unparsed: %q", got)
	}
	_ = syscall.Munmap(m)
	if got := holdersOf(dir); !strings.HasPrefix(got, "none found") {
		t.Fatalf("after munmap still held: %s", got)
	}
}
