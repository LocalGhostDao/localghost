package main

import (
	"reflect"
	"testing"
	"time"
)

func TestCalendarQuestion(t *testing.T) {
	yes := []string{"what's on tomorrow", "Am I free on Friday?", "what meetings do I have this week", "when is my dentist appointment",
		"what's in my calendar next week", "what are my plans for the weekend", "anything booked for Saturday", "what do I have on today"}
	no := []string{"what's the weather tomorrow", "what is bitcoin", "the events of 1066", "who won the match", "tell me about Rome"}
	for _, q := range yes {
		if !calendarQuestion(q) {
			t.Errorf("a calendar question: %q", q)
		}
	}
	for _, q := range no {
		if calendarQuestion(q) {
			t.Errorf("not a calendar question: %q", q)
		}
	}
}

func TestCalendarWindow(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/London")
	now := time.Date(2026, 10, 16, 15, 0, 0, 0, loc) // a Friday
	cases := []struct {
		q, from, to string
		named       bool
	}{
		{"what's on tomorrow", "2026-10-17", "2026-10-18", true},
		{"what did I have yesterday", "2026-10-15", "2026-10-16", true},
		{"am I busy tonight", "2026-10-16", "2026-10-17", true},
		{"plans for the weekend", "2026-10-17", "2026-10-19", true},
		{"next weekend", "2026-10-24", "2026-10-26", true},
		{"meetings next week", "2026-10-19", "2026-10-26", true},
		{"meetings this week", "2026-10-16", "2026-10-19", true},
		{"am I free on Tuesday", "2026-10-20", "2026-10-21", true},
		{"what's on Friday", "2026-10-16", "2026-10-17", true},
		{"what's on next Friday", "2026-10-23", "2026-10-24", true},
		{"what did I have last Monday", "2026-10-12", "2026-10-13", true},
		{"what's in my calendar", "2026-10-16", "2026-10-23", false},
	}
	for _, c := range cases {
		from, to, _, named := calendarWindow(c.q, now, loc)
		if from.Format("2006-01-02") != c.from || to.Format("2006-01-02") != c.to || named != c.named {
			t.Errorf("%q: %s..%s named %v, want %s..%s %v", c.q, from.Format("2006-01-02"), to.Format("2006-01-02"), named, c.from, c.to, c.named)
		}
	}
	// a Sunday's weekend is the one under way, not the next
	sun := time.Date(2026, 10, 18, 11, 0, 0, 0, loc)
	if from, _, _, _ := calendarWindow("anything on this weekend", sun, loc); from.Format("2006-01-02") != "2026-10-17" {
		t.Errorf("weekend on a Sunday: %s", from)
	}
}

func TestCalendarTerms(t *testing.T) {
	cases := map[string][]string{
		"when is my dentist appointment":       {"dentist"},
		"when's my next meeting with Paul?":    {"paul"},
		"what's on tomorrow":                   nil,
		"when was the board meeting in March?": {"board", "march"},
	}
	for q, want := range cases {
		if got := calendarTerms(q); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %v, want %v", q, got, want)
		}
	}
}
