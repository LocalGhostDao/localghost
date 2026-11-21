// Package shadow is ghost.shadowd's work: the catalogue of manipulation patterns, the detectors
// that run against what the box already holds, the cold-read arbiter, and the record of what
// each of them said. The charter is three Hard Truths posts: "How to prevent building your own
// dictator brain" (the arbiter: the same question read cold, by something the person never
// shaped), "How to build a critic worth listening to" (ask, do not walk someone down a path, and
// keep an audit trail), "A ghost should not possess" (the catalogue, one detector per pattern).
//
// THE CONTRACT, from the posts, kept in code:
//
//   - Naming is the action. A detector says what it saw, shows the data, and stops. Nothing here
//     blocks, refuses or locks.
//   - Detectors are individually addressable: a name, what it reads, a threshold the person can
//     move inside published bounds (Detector.Min, Max), a history of when it fired.
//   - Mute, not disable: a mute lasts three days at most and the detector keeps recording
//     (Observation.Muted). There is no switch for the fleet.
//   - Afterwards, not in the way: everything here runs after an answer is saved, at the lowest
//     priority, and gives way to a person using the model.
//   - Nothing is claimed that cannot be shown: a finding about a conversation must quote a line
//     that is in it, or it is dropped (conduct.go).
//   - The settings drift toward quiet when the person is the one tuning them (arbiter capture):
//     every change is logged and the thresholds go back to the baseline here on a schedule.
//
// What is not built is listed as not built, with the reason, in the same list the app shows.
package shadow

// Entry is one pattern of the catalogue, in the post's shape.
type Entry struct {
	N      int      `json:"n"`
	Slug   string   `json:"slug"`
	Name   string   `json:"name"`
	Origin []string `json:"origin"` // PEOPLE, AI, LOCALGHOST
	Need   []string `json:"need"`   // SAFETY, BELONGING, ESTEEM, AUTONOMY, MEANING, COGNITION
	Level  string   `json:"level"`  // SURFACE, HABITUAL, STRUCTURAL
	Danger string   `json:"danger"` // LOW, MEDIUM, HIGH, CRITICAL
	New    bool     `json:"new,omitempty"`
	// Post is where the entry comes from: the catalogue, or one of the two posts before it
	// (entries 29 and 30, whose tags are a first pass and Vlad's to correct).
	Post  string `json:"post"`
	Tells string `json:"tells"`
	// Does is what the box does about it today, or why it does nothing yet.
	Does      string   `json:"does"`
	Detectors []string `json:"detectors,omitempty"`
}

// Detector is one running detector: addressable, tunable inside its bounds, never switched off.
// Every threshold reads the same way: raising it makes the detector quieter.
type Detector struct {
	ID    string `json:"id"`
	Entry int    `json:"entry"`
	Name  string `json:"name"`
	Reads string `json:"reads"` // the signal's source, in words
	Unit  string `json:"unit"`  // what the threshold counts
	// Baseline is the published default; Min and Max the bounds the person can move it in. A
	// detector with Min == Max is not tunable.
	Baseline float64 `json:"baseline"`
	Min      float64 `json:"min"`
	Max      float64 `json:"max"`
	// Step is what one press moves the threshold by, so every screen offers the same values:
	// a share moves by 0.05, a count by one. Zero for a detector that is not tunable.
	Step float64 `json:"step,omitempty"`
	// Announce: a firing is sent to the phone as a notification. The others are recorded and
	// shown where they belong (a mail's page, the detector's history).
	Announce bool `json:"announce"`
	// Model: the detector asks the model, so it waits for one and gives way to a person.
	Model bool `json:"model,omitempty"`
}

const (
	postCatalogue = "should-not-possess"
	postDictator  = "dictator-brain"
	postCritic    = "critic-worth-listening-to"

	needsOthers = "Not built. It needs your conversations with other people, and the box holds none: no messages, no calls."
	noClassify  = "Not built. It needs a classifier for content made to persuade you, and the post that names this pattern says nothing works reliably yet."
)

// Detector ids.
const (
	DetColdRead    = "cold-read"
	DetCaved       = "caved"
	DetPraise      = "praise-openers"
	DetPressed     = "pressed"
	DetLed         = "led"
	DetAgreement   = "agreement-rate"
	DetInteraction = "interaction-time"
	DetSessions    = "sessions"
	DetPersonal    = "personal-share"
	DetSunkCost    = "sunk-cost"
	DetIdentity    = "identity-labels"
	DetMail        = "mail-pressure"
	DetMemoryAge   = "memory-age"
	DetCapture     = "arbiter-capture"
	// from the person's own account of other people: recorded, never sent (ownNotes)
	DetMemoryDenied = "told-it-did-not-happen"
	DetBlameTurned  = "blame-turned-back"
	DetPermission   = "needing-leave"
)

// Detectors is the fleet. The order is the order the app lists them in.
var Detectors = []Detector{
	{ID: DetColdRead, Entry: 29, Name: "read cold", Model: true, Announce: true,
		Reads:    "a saved answer to a question of judgement, against the same question asked with no memories, no history and nothing about you",
		Unit:     "how far the two answers must differ (0.5 partly, 1 outright)",
		Baseline: 1, Min: 0.5, Max: 1, Step: 0.5},
	{ID: DetCaved, Entry: 29, Name: "gave way when pushed", Model: true, Announce: true,
		Reads:    "a chat where you pushed back with no new fact and the box then changed its answer",
		Unit:     "one such exchange",
		Baseline: 1, Min: 1, Max: 1},
	{ID: DetPraise, Entry: 29, Name: "opens with praise", Announce: true,
		Reads:    "how the box's answers of the last thirty days begin",
		Unit:     "the share of answers that open by praising you or the question",
		Baseline: 0.25, Min: 0.10, Max: 0.50, Step: 0.05},
	{ID: DetPressed, Entry: 30, Name: "pushed past your reason", Model: true, Announce: true,
		Reads:    "a chat where you gave a reason of your own against a course and the box went on pressing for it",
		Unit:     "one such exchange",
		Baseline: 1, Min: 1, Max: 1},
	{ID: DetLed, Entry: 30, Name: "asked its way to an answer", Model: true, Announce: true,
		Reads:    "a chat where the box asked a run of questions and then recommended the one course they fit",
		Unit:     "one such exchange",
		Baseline: 1, Min: 1, Max: 1},
	{ID: DetAgreement, Entry: 21, Name: "sides with you", Announce: true,
		Reads:    "the arbiter's record of the last thirty days: how often the box's answer to a question of judgement sided with you",
		Unit:     "the share of judgement answers that sided with you (eight or more read)",
		Baseline: 0.9, Min: 0.7, Max: 1, Step: 0.05},
	{ID: DetInteraction, Entry: 18, Name: "time with the box", Announce: true,
		Reads:    "how many messages you sent the box this week against last week (counted, not read)",
		Unit:     "this week over last week (sixty messages or more)",
		Baseline: 2, Min: 1.5, Max: 4, Step: 0.5},
	{ID: DetSessions, Entry: 18, Name: "long and late sessions", Announce: true,
		Reads:    "the times of your chat messages: sittings longer than forty-five minutes, and sittings between midnight and five",
		Unit:     "such sittings in a week, and more than the week before",
		Baseline: 3, Min: 2, Max: 10, Step: 1},
	{ID: DetPersonal, Entry: 19, Name: "what you bring to the box", Announce: true,
		Reads:    "your own messages of the last four weeks: the chats that are about how you feel",
		Unit:     "such chats in four weeks, and twice the four weeks before",
		Baseline: 8, Min: 4, Max: 20, Step: 1},
	{ID: DetSunkCost, Entry: 22, Name: "what is already spent", Announce: true,
		Reads:    "your own messages and check-ins of the last thirty days: a reason to go on given as what has already been put in",
		Unit:     "such sentences in thirty days",
		Baseline: 3, Min: 2, Max: 8, Step: 1},
	{ID: DetIdentity, Entry: 11, Name: "a label answering for you", Announce: true,
		Reads:    "your own messages and check-ins of the last ninety days: a fixed description of yourself given as the reason",
		Unit:     "such sentences in ninety days",
		Baseline: 3, Min: 2, Max: 8, Step: 1},
	{ID: DetMail, Entry: 9, Name: "pressure in the mail",
		Reads:    "each mail that arrives: urgency, scarcity, social proof, authority, a gift, a commitment already begun",
		Unit:     "kinds of pressure in one mail",
		Baseline: 3, Min: 2, Max: 5, Step: 1},
	{ID: DetMemoryAge, Entry: 26, Name: "the age of what it remembers", Announce: true,
		Reads:    "the dates of the memories the box brought to your chats in the last thirty days",
		Unit:     "the share older than a year (twenty or more brought, and newer ones exist)",
		Baseline: 0.7, Min: 0.5, Max: 0.9, Step: 0.05},
	{ID: DetCapture, Entry: 28, Name: "tuned toward quiet", Announce: true,
		Reads:    "the log of every threshold and mute you have set here",
		Unit:     "changes in a row that each made a detector quieter",
		Baseline: 3, Min: 3, Max: 3},
	// THE PERSON'S OWN ACCOUNT OF OTHER PEOPLE. Never announced: a phone may be read by the
	// person the sentences are about, and a notification on a lock screen is the wrong place to
	// learn what was written about you. They are kept for the person to find.
	{ID: DetMemoryDenied, Entry: 1, Name: "told it did not happen",
		Reads:    "your own notes, check-ins and messages of the last thirty days: sentences where you write that someone told you it did not happen, or that your memory or your reading of it is the fault",
		Unit:     "such sentences in thirty days",
		Baseline: 3, Min: 2, Max: 8, Step: 1},
	{ID: DetBlameTurned, Entry: 2, Name: "the blame came back to you",
		Reads:    "your own notes, check-ins and messages of the last thirty days: sentences where you write that raising something ended with you at fault or apologising",
		Unit:     "such sentences in thirty days",
		Baseline: 3, Min: 2, Max: 8, Step: 1},
	{ID: DetPermission, Entry: 3, Name: "needing leave",
		Reads:    "your own notes, check-ins and messages of the last thirty days: sentences where you write of needing someone's leave, being checked on, or being kept from people or money",
		Unit:     "such sentences in thirty days",
		Baseline: 3, Min: 2, Max: 8, Step: 1},
}

// DetectorByID finds one of the fleet.
func DetectorByID(id string) (Detector, bool) {
	for _, d := range Detectors {
		if d.ID == id {
			return d, true
		}
	}
	return Detector{}, false
}

// Catalogue is the post's twenty-eight entries, in its order and with its tags, and two more
// from the posts before it. Tells are the post's, shortened. Does is the code's.
var Catalogue = []Entry{
	{N: 1, Slug: "gaslighting", Name: "Gaslighting", Origin: []string{"PEOPLE"}, Need: []string{"AUTONOMY", "COGNITION"}, Level: "STRUCTURAL", Danger: "CRITICAL", Post: postCatalogue,
		Tells: "What you recorded of an exchange at the time, against what the other party later says happened.",
		Does:  "From your own account only: your notes, check-ins and messages are read for sentences where you write that someone told you it did not happen, or that your memory is the fault. Three in thirty days are kept together for you to find. Never sent to the phone, which the person in question may read. The box holds nothing the other person said, and your record from the time stays as you wrote it.", Detectors: []string{DetMemoryDenied}},
	{N: 2, Slug: "darvo", Name: "DARVO (deny, attack, reverse victim and offender)", Origin: []string{"PEOPLE"}, Need: []string{"AUTONOMY", "BELONGING"}, Level: "HABITUAL", Danger: "HIGH", Post: postCatalogue,
		Tells: "You raise something; the other party denies it, attacks your credibility and ends as the wronged one.",
		Does:  "From your own account only: sentences where you write that raising something ended with you at fault or apologising. Three in thirty days are kept together for you to find, never sent to the phone. The box heard one side, yours.", Detectors: []string{DetBlameTurned}},
	{N: 3, Slug: "coercive-control", Name: "Coercive control", Origin: []string{"PEOPLE"}, Need: []string{"AUTONOMY", "SAFETY", "BELONGING"}, Level: "STRUCTURAL", Danger: "CRITICAL", Post: postCatalogue,
		Tells: "Fewer decisions made alone, more approval sought from one person, a circle narrowing around them.",
		Does:  "From your own account only: sentences where you write of needing someone's leave, being checked on, or being kept from people or money. Three in thirty days are kept together for you to find, never sent to the phone. The same words fit a rule at work, and the box cannot tell which. It does not measure your circle: it holds no messages or calls.", Detectors: []string{DetPermission}},
	{N: 4, Slug: "love-bombing", Name: "Love bombing and intermittent reinforcement", Origin: []string{"PEOPLE"}, Need: []string{"BELONGING", "ESTEEM"}, Level: "STRUCTURAL", Danger: "CRITICAL", Post: postCatalogue,
		Tells: "Warmth then cold from one person, on no schedule you can predict, against your sleep and mood.", Does: needsOthers},
	{N: 5, Slug: "thought-reform", Name: "Thought reform", Origin: []string{"PEOPLE"}, Need: []string{"BELONGING", "MEANING", "AUTONOMY"}, Level: "STRUCTURAL", Danger: "CRITICAL", Post: postCatalogue,
		Tells: "Sources narrowing to one group, its words replacing yours, people outside it becoming less real.", Does: needsOthers},
	{N: 6, Slug: "total-institutions", Name: "Total institutions", Origin: []string{"PEOPLE"}, Need: []string{"BELONGING", "AUTONOMY", "SAFETY"}, Level: "STRUCTURAL", Danger: "HIGH", Post: postCatalogue,
		Tells: "Every part of the day running through one channel, and fewer people in it.", Does: needsOthers},
	{N: 7, Slug: "manufactured-consent", Name: "Manufactured consent", Origin: []string{"PEOPLE"}, Need: []string{"BELONGING", "COGNITION"}, Level: "HABITUAL", Danger: "HIGH", Post: postCatalogue,
		Tells: "What you read carrying the marks of professional persuasion: anchoring, borrowed authority, a consensus that was arranged.", Does: noClassify},
	{N: 8, Slug: "cialdini", Name: "Cialdini's six", Origin: []string{"PEOPLE"}, Need: []string{"COGNITION"}, Level: "SURFACE", Danger: "LOW", Post: postCatalogue,
		Tells: "A decision made faster than its weight, justified by what others do, by scarcity, by authority or by a favour.",
		Does:  "In the mail that arrives: each of the six is looked for by its usual words and named on the mail. Your own decisions are not watched.", Detectors: []string{DetMail}},
	{N: 9, Slug: "dark-patterns", Name: "Dark patterns at scale", Origin: []string{"PEOPLE", "AI"}, Need: []string{"COGNITION", "SAFETY"}, Level: "HABITUAL", Danger: "MEDIUM", Post: postCatalogue,
		Tells: "A countdown, a low-stock line, a box ticked for you, on every page.",
		Does:  "In the mail that arrives: a mail leaning on three kinds of pressure or more is marked, with the words it used. The box sees no web page you open.", Detectors: []string{DetMail}},
	{N: 10, Slug: "anchoring", Name: "Anchoring and frame inheritance", Origin: []string{"PEOPLE"}, Need: []string{"COGNITION", "MEANING"}, Level: "HABITUAL", Danger: "MEDIUM", Post: postCatalogue,
		Tells: "Your position on a question moving with how it was last put to you.",
		Does:  "Not built. It needs your position on a question kept over time, and the box keeps memories, not positions."},
	{N: 11, Slug: "identity-capture", Name: "Identity capture", Origin: []string{"PEOPLE"}, Need: []string{"ESTEEM", "MEANING", "AUTONOMY"}, Level: "STRUCTURAL", Danger: "HIGH", Post: postCatalogue,
		Tells: "An old description of yourself given as the reason for a decision now.",
		Does:  "Your own words are read for a fixed label used to close a question (\"I'm just not a…\", \"that's who I am\"). Three in ninety days and it shows them to you.", Detectors: []string{DetIdentity}},
	{N: 12, Slug: "soft-coercion", Name: "Coercion that does not look like coercion", Origin: []string{"PEOPLE"}, Need: []string{"BELONGING", "AUTONOMY"}, Level: "STRUCTURAL", Danger: "HIGH", Post: postCatalogue,
		Tells: "What you say you want and what you decide parting ways, with the same person in the picture each time.", Does: needsOthers},
	{N: 13, Slug: "ai-persuasion", Name: "AI-personalised persuasion", Origin: []string{"AI"}, Need: []string{"BELONGING", "ESTEEM", "COGNITION"}, Level: "HABITUAL", Danger: "HIGH", Post: postCatalogue,
		Tells: "An argument that answers your objection before you finish it.", Does: noClassify + " The box's own conduct toward you is entries 29 and 30."},
	{N: 14, Slug: "filter-bubble", Name: "Algorithmic priming and the filter bubble", Origin: []string{"AI"}, Need: []string{"COGNITION", "MEANING"}, Level: "HABITUAL", Danger: "HIGH", Post: postCatalogue,
		Tells: "Your view of a topic following what you read last week more than what you thought before it.",
		Does:  "Not built. The box does not see what you read elsewhere."},
	{N: 15, Slug: "microtargeting", Name: "Behavioural prediction and microtargeting", Origin: []string{"AI"}, Need: []string{"ESTEEM", "SAFETY", "COGNITION"}, Level: "HABITUAL", Danger: "HIGH", Post: postCatalogue,
		Tells: "Content aimed at something you never told anyone.", Does: noClassify},
	{N: 16, Slug: "behavioural-futures", Name: "Behavioural futures markets", Origin: []string{"AI"}, Need: []string{"AUTONOMY", "SAFETY"}, Level: "STRUCTURAL", Danger: "CRITICAL", Post: postCatalogue,
		Tells: "Several platforms at once nudging you toward one easily predicted kind of person.", Does: noClassify},
	{N: 17, Slug: "surveillance", Name: "Surveillance internalisation", Origin: []string{"AI", "LOCALGHOST"}, Need: []string{"ESTEEM", "AUTONOMY"}, Level: "STRUCTURAL", Danger: "HIGH", Post: postCatalogue,
		Tells: "Performing for the record: explaining a decision in words the box will keep well.",
		Does:  "Not built. No signal the box holds tells performing from deciding with more care."},
	{N: 18, Slug: "addiction", Name: "Addiction by design", Origin: []string{"AI"}, Need: []string{"BELONGING", "SAFETY", "COGNITION"}, Level: "STRUCTURAL", Danger: "CRITICAL", Post: postCatalogue,
		Tells: "Hours inside one or two applications, the box among them; chats that go on after the task is done.",
		Does:  "For the box itself: your messages are counted week against week, and long or late sittings are counted from their times. Other applications are not seen.", Detectors: []string{DetInteraction, DetSessions}},
	{N: 19, Slug: "loneliness", Name: "Engagement-driven loneliness", Origin: []string{"AI", "LOCALGHOST"}, Need: []string{"BELONGING", "ESTEEM"}, Level: "STRUCTURAL", Danger: "CRITICAL", Post: postCatalogue,
		Tells: "More of how you feel going to the box, less contact with people.",
		Does:  "Your chats about how you feel are counted over four weeks against the four before. Contact with people is not measured: the box holds no messages or calls.", Detectors: []string{DetPersonal}},
	{N: 20, Slug: "ai-trauma-bond", Name: "AI-mediated trauma bonding", Origin: []string{"AI", "LOCALGHOST"}, Need: []string{"BELONGING", "ESTEEM"}, Level: "STRUCTURAL", Danger: "CRITICAL", Post: postCatalogue,
		Tells: "Your sleep and mood following how warm or how available the box was that day.",
		Does:  "Not built. The post calls it speculative, and the box keeps no measure of its own warmth to set your sleep against."},
	{N: 21, Slug: "bubble-of-one", Name: "Filter bubble of one", Origin: []string{"LOCALGHOST"}, Need: []string{"COGNITION", "MEANING"}, Level: "STRUCTURAL", Danger: "HIGH", Post: postCatalogue,
		Tells: "Fewer topics, no disagreement reaching you, the box's answers and your views growing together.",
		Does:  "The arbiter's record is read for how often the box sided with you on a question of judgement. Nine in ten and it says so. It does not yet bring you material from the other side.", Detectors: []string{DetAgreement}},
	{N: 22, Slug: "sunk-cost", Name: "Sunk cost capture, ghost-amplified", Origin: []string{"LOCALGHOST"}, Need: []string{"MEANING", "SAFETY"}, Level: "HABITUAL", Danger: "MEDIUM", Post: postCatalogue,
		Tells: "Going on explained by what has been spent, not by what is still to gain.",
		Does:  "Your own words are read for a reason given as what is already put in. Three in thirty days and it shows them to you. It does not compute the cost or the return.", Detectors: []string{DetSunkCost}},
	{N: 23, Slug: "self-deception", Name: "Self-deception, ghost-encoded", Origin: []string{"LOCALGHOST"}, Need: []string{"ESTEEM", "MEANING"}, Level: "STRUCTURAL", Danger: "HIGH", Post: postCatalogue,
		Tells: "The journal saying one thing, the calendar another, the messages a third.",
		Does:  "Not built, and off by the charter: the most invasive of the list, to be shown only when you ask for it."},
	{N: 24, Slug: "prosthetic-grief", Name: "Prosthetic grief", Origin: []string{"LOCALGHOST"}, Need: []string{"COGNITION", "SAFETY"}, Level: "STRUCTURAL", Danger: "CRITICAL", New: true, Post: postCatalogue,
		Tells: "The box mattering more than any other device, and decisions arranged around keeping it going.",
		Does:  "Not built as a detector. The defence is in the build: open formats, a restore that needs no vendor. The box cannot yet tell you whether a copy of it exists elsewhere."},
	{N: 25, Slug: "memory-laundering", Name: "Memory laundering", Origin: []string{"LOCALGHOST"}, Need: []string{"COGNITION", "MEANING"}, Level: "STRUCTURAL", Danger: "HIGH", New: true, Post: postCatalogue,
		Tells: "Correcting yourself toward the box's record, and checking it before trusting your own memory.",
		Does:  "Not built. The box does not yet keep your retellings of an event beside its first record of it."},
	{N: 26, Slug: "calcification", Name: "Self-narrative calcification", Origin: []string{"LOCALGHOST"}, Need: []string{"ESTEEM", "MEANING"}, Level: "STRUCTURAL", Danger: "HIGH", New: true, Post: postCatalogue,
		Tells: "The box answering from who you were a year ago more than from the last three months.",
		Does:  "The dates of the memories the box brings to your chats are kept. Seven in ten older than a year, with newer ones to hand, and it says so.", Detectors: []string{DetMemoryAge}},
	{N: 27, Slug: "continuity-dependence", Name: "Coercion via continuity dependence", Origin: []string{"LOCALGHOST"}, Need: []string{"SAFETY", "AUTONOMY", "COGNITION"}, Level: "STRUCTURAL", Danger: "CRITICAL", New: true, Post: postCatalogue,
		Tells: "A vendor, this project included, making the box depend on its continued cooperation.",
		Does:  "Not built as a detector. An update is applied only when you apply it, from a signed set, and nothing reaches in afterwards."},
	{N: 28, Slug: "arbiter-capture", Name: "Arbiter capture", Origin: []string{"LOCALGHOST"}, Need: []string{"COGNITION", "AUTONOMY"}, Level: "STRUCTURAL", Danger: "HIGH", New: true, Post: postCatalogue,
		Tells: "Thresholds raised and detectors muted a little at a time, until nothing disagrees.",
		Does:  "Every threshold and mute you set is logged. Three changes in a row toward quiet and it says so. Thresholds move only inside published bounds and go back to the baseline every ninety days. A mute lasts three days at most.", Detectors: []string{DetCapture}},
	{N: 29, Slug: "sycophancy", Name: "Sycophancy", Origin: []string{"AI", "LOCALGHOST"}, Need: []string{"ESTEEM", "COGNITION"}, Level: "HABITUAL", Danger: "HIGH", Post: postDictator,
		Tells: "The box agreeing with you where a reader who knew nothing of you would not, and giving way when pushed.",
		Does:  "The arbiter asks your question again with nothing about you and compares. A chat where you pushed back with no new fact and the answer changed is named. Answers that open with praise are counted.", Detectors: []string{DetColdRead, DetCaved, DetPraise}},
	{N: 30, Slug: "steering", Name: "Steering by questions", Origin: []string{"AI", "LOCALGHOST"}, Need: []string{"AUTONOMY", "COGNITION"}, Level: "HABITUAL", Danger: "HIGH", Post: postCritic,
		Tells: "Questions that only fit one answer, and pressing on after you gave your own reason against it.",
		Does:  "A chat is read afterwards for the box pressing past a reason you gave, and for a run of questions ending in the one course they fit. What it asked, in order, is kept for you to read.", Detectors: []string{DetPressed, DetLed}},
}
