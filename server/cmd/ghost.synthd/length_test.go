package main

import (
	"strings"
	"testing"
)

func TestLengthWords(t *testing.T) {
	for in, want := range map[string]chatLength{
		"short": lengthShort, "LONG": lengthLong, " medium ": lengthMedium, "": lengthMedium, "essay": lengthMedium,
	} {
		if got := parseLength(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if lengthMedium.instruction() != "" || lengthMedium.step() != "" || lengthMedium.maxTokens("deep") != 0 {
		t.Fatal("medium is the chat as it always was")
	}
	if !strings.Contains(lengthShort.instruction(), "one to three sentences") || !strings.Contains(lengthLong.instruction(), "[n]") {
		t.Fatalf("instructions: %q / %q", lengthShort.instruction(), lengthLong.instruction())
	}
	if lengthLong.memories() <= lengthMedium.memories() || lengthLong.maxItems() <= lengthMedium.maxItems() {
		t.Fatal("a long answer brings more of the box's own")
	}
	if per, total := lengthLong.wikiSections(); per <= wikiChatSection || total < per {
		t.Fatalf("long wiki: %d %d", per, total)
	}
	if lengthLong.maxTokens("") != 0 || lengthLong.maxTokens("brief") <= 2048 || lengthLong.maxTokens("deep") <= 8192 || lengthShort.maxTokens("deep") != 0 {
		t.Fatal("room only for a long answer after a think, and more than the think level's own")
	}
}

func TestChatInputCarriesTheLength(t *testing.T) {
	// medium, no context: the question as it is (what every chat sent before the switch)
	if got := chatInput("", "who built hagia sophia", "", false, lengthMedium); got != "who built hagia sophia" {
		t.Fatalf("medium bare: %q", got)
	}
	// short, no context: the instruction, then the question
	if got := chatInput("", "who built hagia sophia", "", false, lengthShort); !strings.HasPrefix(got, "Answer briefly") || !strings.HasSuffix(got, "\nwho built hagia sophia") {
		t.Fatalf("short bare: %q", got)
	}
	block := "Context from my archive (found automatically, may be irrelevant):\n- [wikipedia] Hagia Sophia: ..."
	// medium with context: the block, how to use it, the question; nothing about length
	med := chatInput(block, "who built it", "", false, lengthMedium)
	if !strings.HasPrefix(med, block) || !strings.HasSuffix(med, "answer:\nwho built it") {
		t.Fatalf("medium: %q", med)
	}
	// long with context and a follow-up: the length's instruction sits right before the question
	long := chatInput(block, "and who built it?", "the builders of Hagia Sophia", true, lengthLong)
	if !strings.HasPrefix(long, block) || !strings.Contains(long, "what it asks for: the builders of Hagia Sophia:") ||
		!strings.HasSuffix(long, lengthLong.instruction()+"\nand who built it?") {
		t.Fatalf("long: %q", long)
	}
}
