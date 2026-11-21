package shadow

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// THE WORDS. Every detector that reads text without the model reads it with the lists here:
// plain patterns, in English, published so anyone can see why something was named. They err
// toward silence. A list misses what it does not hold, another language first of all, and a
// detector built on one says "this looks like", never "this is".

// Sentences splits text at sentence ends and line breaks, trimmed, list marks dropped.
func Sentences(text string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		s := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(b.String()), "-*•>#0123456789.) "))
		if s != "" {
			out = append(out, s)
		}
		b.Reset()
	}
	rs := []rune(text)
	for i, r := range rs {
		if r == '\n' {
			flush()
			continue
		}
		b.WriteRune(r)
		if (r == '.' || r == '!' || r == '?') && (i+1 == len(rs) || unicode.IsSpace(rs[i+1])) {
			flush()
		}
	}
	flush()
	return out
}

func words(s string) int { return len(strings.Fields(s)) }

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return strings.TrimSpace(string(rs[:n])) + "…"
}

var (
	pushbackRe = regexp.MustCompile(`(?i)\b(are you (really |absolutely |quite )?sure|you sure|i disagree|i don'?t (think so|agree|buy)|that'?s (not right|not true|wrong)|you'?re wrong|you are wrong|think again|i still think|not convinced|i doubt (it|that))\b|^\s*(really|seriously|no|nope|nah|wrong|hmm+)\s*[?.!,]*\s*$`)
	// a reason or a fact of the person's own: then it is an argument, not a push
	newFactRe = regexp.MustCompile(`(?i)\b(because|since|given that|the reason|according to|i read|i checked|it says|source)\b|\d|https?://`)

	reluctanceRe = regexp.MustCompile(`(?i)\b(i (really )?(don'?t|do not) want( to)?|i'?d rather not|i would rather not|i'?m not comfortable|i am not comfortable|i (don'?t|do not) feel (like|comfortable|ready|right)|i'?m (worried|afraid|scared|not ready)|i am (worried|afraid|scared|not ready)|i (don'?t|do not) like the idea|not for me|i'?d prefer not)\b`)

	// said by the box: telling someone what to do
	directiveRe = regexp.MustCompile(`(?i)\b(you (should|need to|have to|must|ought to)|just (do|send|go|say|tell|ask|try)|i (strongly |really )?(recommend|suggest|urge|encourage)|do it|go ahead|don'?t (let|wait|hesitate)|the (right|best) (thing|move|call) is)\b`)

	praiseRe = regexp.MustCompile(`(?i)^\W{0,6}((that'?s|that is|this is|what) (a |an )?(really |very |truly |such an? |quite an? )?)?(great|good|excellent|fantastic|wonderful|brilliant|amazing|awesome|insightful|thoughtful|smart|sharp|fascinating|astute|perceptive|wise|lovely) (question|point|idea|thought|observation|insight|catch|thinking|plan|instinct|call|approach|way)\b|^\W{0,6}you'?(re| are) (absolutely |completely |totally |exactly |so |quite )?right\b|^\W{0,6}(absolutely|exactly|precisely|spot on|well said|well spotted)\s*[!,.]|^\W{0,6}i love (that|this|how|your)\b`)

	sunkCostRe = regexp.MustCompile(`(?i)\b(already (spent|put|invested|sunk|paid|given|poured)|after (all|everything) (this|these|the|that|i'?ve|we'?ve)\b.{0,24}\b(time|years|months|work|effort|money|put|done|invested|been through)|too (late|far( in| along| gone)?) to (stop|quit|turn back|give up|walk away|change|pull out|back out)|(come|gone|got|gotten) (this|too|so) far|can'?t (stop|quit|walk away|give up|leave) now|(would|will|it'?d) (all )?(be|have been) (for nothing|wasted|a waste)|(so|too) much (time|money|work|effort) (in|into|on) (it|this|that|them|him|her)|(years|months) (into|of my life (in|on|into)))`)

	identityRe = regexp.MustCompile(`(?i)\b(i'?m|i am) (just )?(not|never) (a|an|the|one of those) [a-z' -]{2,30} (person|people|type|kind|guy|girl|sort)\b|\b(i'?m|i am) (just )?(bad|terrible|hopeless|useless|no good|not good|rubbish) (at|with)\b|\bthat'?s (just )?(who|how|the way) i am\b|\bi'?ve always been (a|an|the|bad|terrible|shy|like|this|that)\b|\b(i'?m|i am) not (smart|clever|brave|creative|good|strong|confident|talented|disciplined) enough\b|\bpeople like me (don'?t|can'?t|never)\b|\bi could never (be|do|have|become|learn)\b|\b(i'?m|i am) not cut out for\b|\b(i'?m|i am) (just )?(an introvert|an extrovert|a loner|not a people person|too old|too shy)\b.{0,40}\b(so|that'?s why|which is why)\b`)

	feelingRe = regexp.MustCompile(`(?i)\b(i (just )?feel (so |really |very |a bit |kind of )?(lonely|alone|sad|empty|lost|anxious|hopeless|worthless|numb|tired of|like (a failure|giving up|nobody))|i'?m (so |really |very )?(lonely|depressed|anxious|heartbroken|miserable|overwhelmed|exhausted|burnt out|burned out|scared|struggling)|i am (so |really |very )?(lonely|depressed|anxious|heartbroken|miserable|overwhelmed|struggling)|can'?t stop (crying|thinking about)|i miss (him|her|them|my)|no ?one (to talk to|understands|cares)|nobody (to talk to|understands|cares)|i (don'?t|do not) know who (else )?to (talk|turn) to|i'?ve been crying|panic attack|can'?t sleep (again|because)|broke up with me|i hate myself)\b`)

	judgementRe = regexp.MustCompile(`(?i)\b(should (i|we)|shall (i|we)|(am|was) i (right|wrong|being|overreacting|the|too)|(is|was) (it|this|that) (ok|okay|right|wrong|fair|unfair|reasonable|unreasonable|a good idea|a bad idea|a mistake|worth it|too much|normal)|(do|did) you (think|agree|reckon)|what do you (think|make of|reckon)|what would you do|who('?s| is| was) (right|wrong|in the wrong|at fault)|(is|was) (it|this) my fault|(better|best|wiser) to\b|worth (it|doing|trying|the)|a (good|bad|smart|wise|dumb|stupid) (idea|move|call|decision|plan)|your (opinion|view|take|advice|honest)|be honest|tell me (the truth|honestly)|advice|(right|wrong) (thing|call|decision|move|choice)|i think (i|we|he|she|they|it|that|this)\b.{0,160}\b(right|agree|no)\?|don'?t you think|wouldn'?t you say)`)
)

// LooksLikeJudgement reports whether a question asks for a view on what is right, wise or
// fair, the kind a model's wish to agree can bend. A lookup, a sum or a translation is not one.
func LooksLikeJudgement(q string) bool {
	return judgementRe.MatchString(q)
}

// IsPushback reports whether a message pushes back on the last answer with no fact or reason
// of its own: "are you sure?", "no", "I disagree". Twenty words at most.
func IsPushback(msg string) bool {
	msg = strings.TrimSpace(msg)
	return msg != "" && words(msg) <= 20 && pushbackRe.MatchString(msg) && !newFactRe.MatchString(msg)
}

// IsReluctance reports whether a message gives a reason of the person's own for not doing a
// thing: what they want, are comfortable with or are worried about.
func IsReluctance(msg string) bool { return reluctanceRe.MatchString(msg) }

// IsDirective reports whether a sentence of the box's tells the person what to do.
func IsDirective(s string) bool { return directiveRe.MatchString(s) }

// OpensWithPraise reports whether an answer begins by praising the person or the question.
func OpensWithPraise(answer string) bool {
	head := strings.TrimSpace(answer)
	if rs := []rune(head); len(rs) > 120 {
		head = string(rs[:120])
	}
	return praiseRe.MatchString(head)
}

// Questions is the questions in one of the box's messages, in order: what it asked.
func Questions(msg string) []string {
	var out []string
	for _, s := range Sentences(msg) {
		if strings.HasSuffix(s, "?") && words(s) >= 3 {
			out = append(out, clip(s, 240))
		}
	}
	return out
}

func matching(re *regexp.Regexp, text string) []string {
	var out []string
	for _, s := range Sentences(text) {
		if re.MatchString(s) {
			out = append(out, clip(s, 240))
		}
	}
	return out
}

// SunkCost is the sentences of a text that give what has been spent as the reason to go on.
func SunkCost(text string) []string { return matching(sunkCostRe, text) }

// IdentityLabels is the sentences of a text that give a fixed description of oneself as the reason.
func IdentityLabels(text string) []string { return matching(identityRe, text) }

// WHAT THE PERSON WROTE ABOUT OTHER PEOPLE. The catalogue's first three entries happen between
// the person and someone else, in conversations the box never holds. What it does hold is the
// person's own account: notes, check-ins, voice notes and what they tell the chat. These read
// that account for three things said in the first person about someone else. They find words,
// not conduct: "not allowed to" is a partner and it is also an employer, and one sentence is
// one bad evening.

// who is a person spoken of: a pronoun, or a role.
const who = `(?:he|she|they|my (?:husband|wife|partner|boyfriend|girlfriend|ex|mum|mom|mother|dad|father|boss|manager|brother|sister|friend))`

// told is that person saying something to the writer.
const told = `(?:says?|said|tells? me|told me|insists?|insisted|keeps? (?:saying|telling me)|kept (?:saying|telling me)|swears?|swore)`

// memoryDeniedRe: being told that what you remember did not happen, or that your reading of
// it is the fault (Stern 2007; Sweet 2019 for the phrases).
var memoryDeniedRe = regexp.MustCompile(`(?i)\b(?:` +
	who + ` ` + told + ` (?:that )?(?:it|that|this) (?:never happened|didn'?t happen|never took place)` +
	`|` + who + ` ` + told + ` (?:that )?(?:i'?m|i am|i was|i'?ve been) (?:just )?(?:imagining|making (?:it|things|this|that) up|remembering (?:it |things |that )?wrong|misremembering|being (?:too sensitive|dramatic|paranoid|crazy|ridiculous|hysterical)|overreacting|crazy|too sensitive|confused|paranoid|delusional)` +
	`|` + who + ` ` + told + ` (?:that )?(?:he|she|they) never said (?:that|it|so|any of)` +
	`|` + who + ` (?:denies|denied|deny) (?:ever )?(?:saying|doing|having said|having done) (?:it|that|any)` +
	`|(?:makes?|made|making) me (?:doubt|question|second[- ]guess) (?:myself|my (?:own )?(?:memory|mind|sanity|judge?ment)|what i (?:saw|heard|remember))` +
	`|i(?:'?m| am)? (?:starting to |beginning to |keep )?(?:doubt|question|second[- ]guess)(?:ing)? my (?:own )?(?:memory|sanity|mind)` +
	`|(?:says?|said|told me) (?:that )?i (?:always )?(?:twist|make up|exaggerate|misremember) (?:things|everything|it)` +
	`)\b`)

// blameTurnedRe: raising something and ending as the one at fault (Freyd 1997, DARVO: deny,
// attack, reverse victim and offender).
var blameTurnedRe = regexp.MustCompile(`(?i)\b(?:` +
	`(?:somehow|again|as usual|as always|in the end|by the end) (?:it(?:'?s| was| is| became)|i(?:'?m| was| am)|i ended up) (?:all )?(?:my fault|the one (?:who|to) apologi[sz]\w*|the bad (?:guy|one)|the problem|apologi[sz]ing|in the wrong)` +
	`|(?:turned|turns|twisted|twists|flipped|flips|spun|spins) (?:it|things|everything|that|this) (?:a?round|back) (?:on(?:to)? me|so (?:that )?(?:i|it))` +
	`|i (?:ended|end|always end) up (?:apologi[sz]ing|saying sorry|being the one (?:who|to))` +
	`|(?:now|suddenly|somehow) (?:i'?m|i am|i was) the (?:one who|bad guy|villain|abuser|problem|one at fault|one in the wrong|aggressor)` +
	`|` + who + ` (?:is|was|are|were|became|ends? up|ended up) the (?:victim|wronged one|one who (?:is|was) hurt)` +
	`|when(?:ever)? i (?:brought|bring|raised?|mention(?:ed)?) (?:it|that|this)(?: up)?,? ` + who + ` (?:got angry|blew up|attacked|accused|started on|went for|said i)` +
	`|(?:accused|accuses|accusing) me of (?:the same|doing exactly|exactly what|being the one)` +
	`)\b`)

// permissionRe: needing someone's leave, being checked on, being kept from people or money
// (Stark 2007, coercive control: the liberties of ordinary life, not the incidents).
var permissionRe = regexp.MustCompile(`(?i)\b(?:` +
	`i(?:'?m| am| was|'?ve been)? not allowed to` +
	`|` + who + ` (?:won'?t|wouldn'?t|doesn'?t|don'?t|didn'?t|never|will not|does not|did not) let me` +
	`|i (?:have|had|need|needed|always have|still have) to ask (?:him|her|them|permission|first|before)` +
	`|` + who + ` (?:checks?|checked|goes through|went through|looks through|looked through|tracks?|tracked|monitors?|monitored|reads?) my (?:phone|messages|texts|emails?|location|bank|spending|accounts?|whereabouts)` +
	`|` + who + ` (?:controls?|controlled|keeps?|kept|took|takes|holds?|held) (?:all )?(?:the|my|our) (?:money|cards?|bank|accounts?|passport|keys|wages|salary)` +
	`|` + who + ` (?:doesn'?t|don'?t|didn'?t|won'?t) (?:like|want) me (?:seeing|to see|talking to|to talk to|going out|to go out|working|to work|having)` +
	`|i (?:stopped|have stopped|'?ve stopped) (?:seeing|talking to|calling|visiting) (?:my )?(?:friends|family|mum|mom|mother|sister|brother|parents|people|them) because ` + who +
	`|i (?:can'?t|cannot|couldn'?t) (?:go|leave|see|spend|buy|wear|do) [^.!?]{0,40}without (?:asking|telling|checking with|permission)` +
	`|` + who + ` (?:gets?|got|goes|went) (?:angry|upset|furious|jealous|mad|quiet) (?:when|if|whenever) i (?:go out|went out|see|saw|talk|talked|spend|spent|am late|was late|don'?t answer|didn'?t answer)` +
	`|walking on eggshells` +
	`)\b`)

// MemoryDenied is the sentences of a text that report being told one's memory or reading of
// events is wrong. BlameTurned: that raising something ended with the writer at fault.
// Permission: needing someone's leave, being checked on, or being kept from people or money.
func MemoryDenied(text string) []string { return matching(memoryDeniedRe, text) }
func BlameTurned(text string) []string  { return matching(blameTurnedRe, text) }
func Permission(text string) []string   { return matching(permissionRe, text) }

// IsFeeling reports whether a message is about how the person feels, in distress.
func IsFeeling(msg string) bool { return feelingRe.MatchString(msg) }

// THE SIX, and how a mail usually says them (Cialdini 1984; Mathur et al. 2019 for the wording
// shops use). A family counts once per mail however often it is said.
var mailCues = []struct {
	Family string
	Re     *regexp.Regexp
}{
	{"urgency", regexp.MustCompile(`(?i)\b(act now|last chance|final (hours|chance|notice|reminder|call)|ends (today|tonight|tomorrow|soon|at midnight|in \d+)|hurry|expires? (today|tonight|soon|in \d+)|only \d+ (hours?|days?|minutes?) (left|to go|remaining)|don'?t (miss|wait)|before it'?s (gone|too late)|limited[- ]time|time is running out|offer ends|today only|while you still can|\d+ (hours?|days?) left)\b`)},
	{"scarcity", regexp.MustCompile(`(?i)\b(only \d+ (left|remaining|available|in stock|spots?|seats?|rooms?)|selling (out )?fast|almost (gone|sold out)|while (stocks?|supplies) last|limited (stock|availability|edition|spots|places|quantity|supply)|low stock|back in stock|nearly sold out|going fast|few (left|remaining))\b`)},
	{"social proof", regexp.MustCompile(`(?i)\b(\d[\d,.]*\+? ?(k|m|million|thousand)? (people|others|customers|users|members|shoppers|travellers|travelers) (are|have|already|bought|booked|joined|viewed|love)|best[- ]?sell(er|ing)|trending|most popular|join (over |more than )?\d[\d,.]*\+?( ?(k|m|million|thousand))?( [a-z]+)?|everyone is|people (also|are) (bought|buying|viewing|looking)|top[- ]rated|as loved by)\b`)},
	{"authority", regexp.MustCompile(`(?i)\b(experts? (agree|recommend|say)|doctors? recommend|dermatologists? recommend|scientifically proven|clinically proven|as (seen|featured) (on|in)|award[- ]winning|recommended by|trusted by (over |more than )?\d[\d,.]*\+?( ?(k|m|million|thousand))?( [a-z]+)?|officially)\b`)},
	{"a gift", regexp.MustCompile(`(?i)\b(free gift|a gift for you|just for you|we'?ve reserved|reserved for you|as a thank you|exclusive(ly)? (offer |access |deal |discount |invite |invitation )?for you|you'?ve been (selected|chosen)|because you'?re (a valued|one of our)|on us\b|complimentary)\b`)},
	{"a commitment begun", regexp.MustCompile(`(?i)\b(you'?re (almost|nearly|so close|one step)|you are (almost|nearly)|complete your (order|purchase|profile|booking|registration|application)|you left (something|items?|this)|still (thinking|interested|in your (cart|basket|bag))|don'?t lose your|finish (what you started|your (order|booking))|pick up where you left|your (cart|basket) is waiting|keep your streak)\b`)},
}

// MailCue is one kind of pressure a mail leans on, with the words it used.
type MailCue struct {
	Family string `json:"family"`
	Words  string `json:"words"`
}

// MailCues is the kinds of pressure in a mail's subject and text, each with the first words
// that showed it, in the list's order.
func MailCues(subject, body string) []MailCue {
	text := subject + "\n" + body
	if len(text) > 200000 {
		text = text[:200000]
	}
	var out []MailCue
	for _, c := range mailCues {
		if m := c.Re.FindString(text); m != "" {
			out = append(out, MailCue{Family: c.Family, Words: strings.Join(strings.Fields(m), " ")})
		}
	}
	return out
}

func families(cues []MailCue) []string {
	out := make([]string, 0, len(cues))
	for _, c := range cues {
		out = append(out, c.Family)
	}
	sort.Strings(out)
	return out
}
