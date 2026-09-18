package poltergres

import "testing"

// TestArgTextByteSlice pins the bytea encoding. A []byte bound through the default fmt.Sprint branch
// became "[12 34 56]" , matched nothing, raised nothing , which is how a caption's description
// never reached a single frame.
func TestArgTextByteSlice(t *testing.T) {
	if got := argText([]byte{0x12, 0x34, 0xab}); got != `\x1234ab` {
		t.Fatalf("[]byte must bind as bytea hex, got %q", got)
	}
	if got := argText([]byte{}); got != `\x` {
		t.Fatalf("empty []byte must bind as empty bytea, got %q", got)
	}
	// The scalar cases must not have moved.
	for _, c := range []struct {
		in   any
		want string
	}{{"s", "s"}, {7, "7"}, {int64(-1), "-1"}, {1.5, "1.5"}, {true, "t"}, {false, "f"}} {
		if got := argText(c.in); got != c.want {
			t.Fatalf("argText(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
