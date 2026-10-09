package voiced

import "testing"

// A name said another way comes out as the box spells it; a name that is right, a word that is
// not a name, and a near name of another person are left alone.
func TestFixNames(t *testing.T) {
	names := []string{"Vlad", "Cristina Cealicu", "Cristina", "Toby", "James", "Ștefan"}
	for in, want := range map[string]string{
		"I went swimming with Christina and Kristina's sister.": "I went swimming with Cristina and Cristina's sister.",
		"Tobi and Jaymes came over; Tony did not.":              "Toby and James came over; Tony did not.",
		"cristina was there too.":                               "cristina was there too.", // not capitalised: whisper did not take it for a name
		"CRISTINA shouted.":                                     "Cristina shouted.",
		"A Christian festival in Cealicu's village.":            "A Christian festival in Cealicu's village.",
		"Stefan and Vlad, then Wlad again.":                     "Ștefan and Vlad, then Vlad again.",
		"Cristine said no.":                                     "Cristina said no.",
		"James, Toby.":                                          "James, Toby.",
	} {
		got, _ := FixNames(in, names)
		if got != want {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
	if s, n := FixNames("anything", nil); s != "anything" || n != 0 {
		t.Fatal(s, n)
	}
	if SoundsLike("Christina") != SoundsLike("Cristina") || SoundsLike("Christina") == SoundsLike("Christian") {
		t.Fatal(SoundsLike("Christina"), SoundsLike("Cristina"), SoundsLike("Christian"))
	}
	if p := Prompt([]string{"Vlad", "Cristina"}); p != "People: Vlad, Cristina." {
		t.Fatal(p)
	}
	if Prompt(nil) != "" {
		t.Fatal("a prompt from nothing")
	}
}
