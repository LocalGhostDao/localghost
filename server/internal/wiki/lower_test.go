package wiki

import (
	"strings"
	"testing"
)

// A page whose letters change length when lowered (U+023A, U+023E), or whose bytes are not
// UTF-8 at all, is read like any other: the indices come from a copy of the same length.
func TestPagesThatChangeLengthWhenLowered(t *testing.T) {
	body := " is how the name is written in capitals, as the orthography has it.</p><TABLE><tr><td>x</td></tr></TABLE><p>After the table.</p></BODY></html>"
	pages := map[string]string{
		"longer when lowered":  "<html><BODY><p>" + strings.Repeat("ȺȾ", 30) + body,
		"not UTF-8":            "<html><BODY><p>" + strings.Repeat("\xe9\xff", 40) + body,
		"shorter when lowered": "<html><BODY><p>" + strings.Repeat("K", 40) + body, // the Kelvin sign
	}
	for name, page := range pages {
		if got := dropElement(page, "table"); strings.Contains(strings.ToLower(got), "<table") || !strings.Contains(got, "After the table.") {
			t.Fatalf("%s: dropElement: %q", name, got)
		}
		if lead := Lead(page, 600); !strings.Contains(lead, "orthography") {
			t.Fatalf("%s: Lead: %q", name, lead)
		}
		_ = Body(page, 4000)
		_ = Sections(page, 0)
	}
	if lowerASCII("AbC<Ⱥ>") != "abc<Ⱥ>" || len(lowerASCII("\xffA")) != 2 {
		t.Fatal("lowerASCII changes more than A-Z")
	}
}
