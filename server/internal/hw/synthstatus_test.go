package hw

import "testing"

func TestSynthStatusWords(t *testing.T) {
	if got := synthDid(map[string]int{"distilled": 3, "trips": 1, "folded": 2, "nothing": 0}); got != "distilled 3 · people merged 2 · trips 1" {
		t.Fatalf("did %q", got)
	}
	if synthDid(nil) != "" {
		t.Fatal("an idle pass says nothing")
	}
	for n, want := range map[int64]string{19707096: "19.7 million", 1234: "1,234", 999: "999", 1234567: "1.2 million"} {
		if got := humanCount(n); got != want {
			t.Fatalf("%d: %q", n, got)
		}
	}
}
