package main

import "strings"

// HOW MUCH ANSWER. The chat's switch beside incognito and web: short, medium or long (16 November
// 2026, Vlad: "if I ask about something or some place I want to choose if it's short answer,
// medium or long and on long I want research with internet and local sources").
//
//	short   the answer in one to three sentences; the context is the same, the words are few
//	medium  what the chat always did, no word added to the prompt
//	long    the story: the phone searches harder (all the plan's searches, more pages read), and
//	        the box brings more of its own: more of the person's memories, more of the box's
//	        Wikipedia article than its opening, and room for the answer when the model thinks
//
// The phone sends it; an older phone sends nothing, which is medium.
type chatLength string

const (
	lengthShort  chatLength = "short"
	lengthMedium chatLength = "medium"
	lengthLong   chatLength = "long"
)

// parseLength reads the phone's word; anything else is medium.
func parseLength(s string) chatLength {
	switch chatLength(strings.ToLower(strings.TrimSpace(s))) {
	case lengthShort:
		return lengthShort
	case lengthLong:
		return lengthLong
	default:
		return lengthMedium
	}
}

// instruction is what the prompt says about the answer's length, just before the question; ""
// for medium, which is the answer as it always was.
func (l chatLength) instruction() string {
	switch l {
	case lengthShort:
		return "Answer briefly: one to three sentences, the answer itself and nothing around it, " +
			"no headings and no lists. Name a source in a word when one matters."
	case lengthLong:
		return "Give the long answer, the whole story: open with the answer in a sentence or two, " +
			"then go through it properly, the background and the history, how it works or how it came " +
			"to be, the details worth knowing, and what the person's own memories and places add when " +
			"they bear on it. Short headings where they help. Cite the web findings by their numbers " +
			"[n], and say when something comes from the box's Wikipedia or from the person's memories. " +
			"Say where the sources disagree or are silent, and do not fill a gap with a guess."
	default:
		return ""
	}
}

// memories is how many of the person's memories a question brings (the best matches; the best
// one's heaviest neighbour comes on top).
func (l chatLength) memories() int {
	if l == lengthLong {
		return 5
	}
	return 2
}

// maxItems caps the box's own context items (the figures and the Wikipedia keep their places
// outside it, as always).
func (l chatLength) maxItems() int {
	if l == lengthLong {
		return 12
	}
	return 6
}

// wikiSections is how much of the box's Wikipedia article goes beyond its opening: the section
// the question points at (as for every length), and for a long answer the others after it.
func (l chatLength) wikiSections() (perSection, total int) {
	if l == lengthLong {
		return 1500, 6000
	}
	return wikiChatSection, wikiChatSection
}

// maxTokens is the answer's room when the model thinks: the reasoning is billed against the same
// cap, and a long answer after a brief think did not fit the 2,048 a brief think gets. 0 leaves
// oracled's budget for the think level as it is (and no cap at all when the model does not think).
func (l chatLength) maxTokens(think string) int {
	if l != lengthLong {
		return 0
	}
	switch think {
	case "brief":
		return 6144
	case "deep":
		return 12288
	default:
		return 0
	}
}

// lengthStep is the line the chat's trail shows for the length asked for; "" for medium.
func (l chatLength) step() string {
	switch l {
	case lengthShort:
		return "a short answer was asked for"
	case lengthLong:
		return "the long answer was asked for: more of your memories and of the box's Wikipedia, and the phone's deeper search"
	default:
		return ""
	}
}

// chatInput is the prompt the model gets for one question: the context block (the box's own
// items and the web findings) when there is one, the instruction on how to use it, the length's
// instruction, then the question. followUp says the conversation has turns before this one; need
// is what the plan said the question asks for.
func chatInput(block, prompt, need string, followUp bool, l chatLength) string {
	howLong := l.instruction()
	if block == "" {
		if howLong == "" {
			return prompt
		}
		return howLong + "\n" + prompt
	}
	ask := "answer:"
	if followUp {
		// the web findings are loud; the conversation is what says what a short follow-up
		// ("how about now?") is asking, so it is named here, with the need when there is one
		ask = "answer the next message of the conversation above, read as its next turn (a short one repeats or adjusts the last question)"
		if n := strings.TrimSpace(need); n != "" {
			ask += "; what it asks for: " + clip(n, 300)
		}
		ask += ":"
	}
	input := block + "\n\nUsing the context above only where it is actually relevant (say which source when you use one), " + ask + "\n"
	if howLong != "" {
		input += howLong + "\n"
	}
	return input + prompt
}
