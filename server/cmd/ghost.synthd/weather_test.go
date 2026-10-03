package main

import (
	"testing"
	"time"
)

// A weather question is one by its words; the place it names comes after "in", "at", "for",
// "around" or "near", and a day is not a place.
func TestWeatherQuestionAndPlace(t *testing.T) {
	for q, place := range map[string]string{
		"what's the weather like today?":               "",
		"is it going to rain tomorrow":                 "",
		"weather for tomorrow":                         "",
		"do I need an umbrella this afternoon":         "",
		"what's the weather in Rome today":             "Rome",
		"forecast for Cluj-Napoca this weekend":        "Cluj-Napoca",
		"how hot is it in Buenos Aires right now?":     "Buenos Aires",
		"temperature at Lake Como?":                    "Lake Como",
		"will it snow near Innsbruck next week":        "Innsbruck",
		"100 dollars to euros and the weather in Rome": "Rome",
	} {
		if !weatherQuestion(q) {
			t.Errorf("%q is a weather question", q)
		}
		if got := placeOf(q); got != place {
			t.Errorf("%q: place %q, want %q", q, got, place)
		}
	}
	for _, q := range []string{"what's the btc price?", "who is the president of Romania", "the sky was red tonight", "windows update"} {
		if weatherQuestion(q) {
			t.Errorf("%q is not a weather question", q)
		}
	}
}

// The weather never goes to the web when the question names no place: the web would need the
// phone's position. A named place without a box: nothing to cover, the web may answer.
func TestWeatherCoversWithoutABox(t *testing.T) {
	if why, ok := weatherCovers("", "what's the weather like today?"); !ok || why != "the box's daily weather pull" {
		t.Fatal(why, ok)
	}
	if _, ok := weatherCovers("", "weather in Rome"); ok {
		t.Fatal("no box, no forecast for Rome: the web may answer")
	}
	if _, ok := weatherCovers("", "what's the btc price?"); ok {
		t.Fatal("not a weather question")
	}
	if items := weatherItems(nil, "what's the weather like today?", nil, time.Now()); items != nil {
		t.Fatal("no store, no items")
	}
}
