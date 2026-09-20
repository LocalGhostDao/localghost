package search

import "testing"

func TestParseTagsWithCategories(t *testing.T) {
	got := ParseTags("place:beach, People:child, food: ice cream, swimming, weird:thing, x, sunset, beach")
	want := []Tag{{"beach", "place"}, {"child", "people"}, {"ice cream", "food"}, {"swimming", "activity"},
		{"weird:thing", ""}, {"sunset", "nature"}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tag %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestLexiconCompoundsAndPlurals(t *testing.T) {
	cases := map[string]string{
		"dog": "animal", "dogs": "animal", "wooden table": "object", "red car": "vehicle",
		"fishing boat": "vehicle", "birthday cake": "food", "mountains": "nature", "pastel de nata": "food",
		"quantum foam": "", "beach": "place", "picnic": "activity",
	}
	for tag, want := range cases {
		if got := Lexicon(tag); got != want {
			t.Errorf("Lexicon(%q) = %q, want %q", tag, got, want)
		}
	}
	for _, c := range Categories {
		if !IsCategory(c) {
			t.Fatal(c)
		}
	}
	if IsCategory("other") {
		t.Fatal("'other' is a display bucket, not a category")
	}
}

func TestSortedCategories(t *testing.T) {
	m := map[string][]string{"other": {"x"}, "food": {"a"}, "people": {"b"}, "place": {"c"}}
	got := SortedCategories(m)
	if got[0] != "people" || got[1] != "place" || got[2] != "food" || got[3] != "other" {
		t.Fatalf("%v", got)
	}
}
