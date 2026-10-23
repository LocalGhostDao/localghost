package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
)

func TestContactQuestion(t *testing.T) {
	yes := []string{"what's Ana's number", "Paul's email?", "phone number for the plumber", "when is Ana's birthday",
		"whose birthday is this week", "any birthdays coming up", "who is +44 7700 900123", "email address for Paul", "contact details for Acme"}
	no := []string{"what is the birthday paradox", "what's the weather tomorrow", "what's on tomorrow", "the number of planets", "who is the prime minister"}
	for _, q := range yes {
		if !contactQuestion(q) {
			t.Errorf("a contacts question: %q", q)
		}
	}
	for _, q := range no {
		if contactQuestion(q) {
			t.Errorf("not a contacts question: %q", q)
		}
	}
}

func TestContactTerms(t *testing.T) {
	cases := map[string][]string{
		"what's Ana Pop's number":      {"ana", "pop"},
		"Paul's email?":                {"paul"},
		"whose birthday is this week":  nil,
		"phone number for the plumber": {"plumber"},
	}
	for q, want := range cases {
		if got := contactTerms(q); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %v, want %v", q, got, want)
		}
	}
}

func TestNextBirthday(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 16, 15, 0, 0, 0, loc)
	next, age := nextBirthday(hw.Contact{Birthday: "1986-10-17"}, now, loc)
	if next.Format("2006-01-02") != "2026-10-17" || age != 40 {
		t.Fatalf("%s %d", next, age)
	}
	next, age = nextBirthday(hw.Contact{Birthday: "--10-16"}, now, loc)
	if next.Format("2006-01-02") != "2026-10-16" || age != 0 {
		t.Fatalf("today's is today's: %s %d", next, age)
	}
	next, _ = nextBirthday(hw.Contact{Birthday: "--03-01"}, now, loc)
	if next.Format("2006-01-02") != "2027-03-01" {
		t.Fatalf("passed this year: %s", next)
	}
	next, _ = nextBirthday(hw.Contact{Birthday: "2000-02-29"}, now, loc)
	if next.Format("2006-01-02") != "2028-02-29" {
		t.Fatalf("leap day: %s", next)
	}
}
