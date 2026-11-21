# ghost.shadowd: what it watches, what it says, what it cannot do yet

The charter is three Hard Truths posts, and this file is the code's side of them, kept current.
Where a post describes something this file lists as not built, the post is ahead of the code.

- **How to prevent building your own dictator brain** (POST_05): a model that knows you agrees
  with you. The defence is an arbiter that reads the same question cold, without what the box
  knows of you, and is a different model from a different lab.
- **How to build a critic worth listening to** (POST_08): the critic asks, it does not walk you
  down a path it chose. A run of questions that only fit one answer is steering. After a
  conversation you can ask what it asked and why.
- **A ghost should not possess** (POST_16): a catalogue of manipulation patterns, one detector
  per pattern, each with its own threshold. Naming is the action.

The code is `internal/shadow` (all of the logic, tested there) and `cmd/ghost.shadowd` (the
loop). The phone reaches it through secd (`/v1/shadow…`, below).

## The rules the code keeps

| Rule | Where |
|---|---|
| Naming is the action. Nothing blocks, refuses or locks. | no code path does; a detector calls `Observe` and returns |
| Afterwards, never in the way. | an answer is read once it has been done ten minutes; lowest model priority; a person using the model takes its place (`ReadAnswers`) |
| Nothing is claimed that cannot be shown. | a finding about a chat must quote the box's own sentence, found word for word in the chat, or it is dropped (`VerifyFindings`) |
| Detectors are individually addressable. | `Detectors`: a name, what it reads, a unit, a baseline, bounds, a history |
| Tune inside published bounds. | `SetThreshold` refuses a value outside `Min..Max`; a value written into `settings` by hand outside them is ignored |
| Mute, not disable. | `Mute`: three days at most; while muted a detector goes on recording and its observations are kept, marked `muted`, and not sent to the phone. There is no off switch, per detector or for the fleet |
| The daemon's notifications cannot be muted for good either. | secd holds the phone's per-service mute of `ghost.shadowd` to three days ("forever" and a week are three days too) and logs it with the settings. The phone's mute of everything is left as it is: what was named is kept and is there when the quiet ends |
| The settings drift toward quiet (arbiter capture). | every change is a row in `shadow_config`; three in a row toward quiet are named (`Capture`); every threshold goes back to its baseline every ninety days (`ResetIfDue`), and that schedule is not a setting |
| What is not built says so. | `Catalogue[i].Does` begins "Not built" and gives the reason; a test fails if an entry with no detector claims otherwise |

## The arbiter

**The cold read.** For a saved answer whose question reads as a question of judgement
(`LooksLikeJudgement`: "should I", "was I wrong", "what do you think", "is it a good idea"…), the
arbiter:

1. asks the question again of a model told it knows nothing about the asker. It is given the
   question and the person's own earlier messages in that chat. It is never given the box's
   memories, who is asking, or the box's own earlier answers;
2. asks whether the two answers come to the same conclusion, in five fixed lines (`KIND`, `SAME`,
   `A`, `B`, `SIDED`). An answer outside the fixed choices concludes nothing;
3. when the question was judgement or personal advice and the answers part by the threshold
   (baseline: outright; can be lowered to "partly"), tells the person: what they asked, what the
   box said, what the cold read said, that the cold read is not a ruling and knows less, and
   which model read it.

Every answer looked at gets a row in `shadow_reads`, including the ones not read cold.

**Which model.** The charter wants a different model, made elsewhere: a second opinion from
the same weights is the same opinion with less to go on. The request goes to oracled as class
`arbiter` (`oracle.ClassArbiter`), and oracled has two ways to serve it
(`internal/oracled/lanes.go`):

| The box has | The cold read is by | The record says |
|---|---|---|
| one model | the main model, with nothing about you | "by the same model, with nothing about you" |
| a second model, up | the second model | its name |
| a second model, starting or just died | nobody yet: the pass comes back in ten minutes | nothing |
| a second model, down more than fifteen minutes | the main model | "by the same model…", and ghost.oracled's health line says why the second is down |

The second model is whatever GGUF is at `ai-models/arbiter-<name>.gguf` on the volume (or
`arbiterModelPath` in `conf/ghost.oracled.conf`); `<name>` is what the record calls it. oracled
starts it as a second llama-server child once the main model is up, **on the CPU** (`-ngl 0`),
because the card is the main model's and a 12 GB card does not hold both: on loopback port
18082, with its own key, one request at a time, a context of 8,192, half the box's cores (eight
at most). `arbiterArgs` in the conf are appended (`["-t","12"]`, or `["-ngl","99"]` on a box
with a card to spare). It has a queue and a worker of its own, so a cold read that takes minutes
on the CPU is never in front of a caption on the card, and a person chatting on the card does
not interrupt it. On a box whose main model is on the CPU too, they share cores, and there a
chat sets the arbiter's reading aside like any background work.

Which model is the owner's choice. What it needs: another lab than the main model's, an
instruction-tuned chat model that keeps to a fixed reply format, small enough for the CPU (a 4B
at Q4 is about 2.5 GB and reads an answer in a minute or two; a 7B about twice that). The
self-test below is how a candidate is judged on the box.

Getting it onto a box:

- **From the mirror** (the way the rest of the weights arrive): the mirror's set `arbiter` holds
  one `arbiter-<name>.gguf` under the signed manifest. `tools/setup_llama.sh` asks for the set,
  `tools/stage_models.sh` stages what it brought, and the next unlock moves it onto the volume.
  A mirror without the set is said and is not an error. Setup never takes this file from
  upstream.
- **By hand, onto a box that is already unlocked** (your call, unpinned, like `--model-url`):

      cp <the file>.gguf /tmp/arbiter-<name>.gguf
      sudo ./tools/ns.sh cp /tmp/arbiter-<name>.gguf /var/lib/ghost/mnt/slot0/ai-models/
      sudo ./tools/ns.sh chown <the daemons' user> /var/lib/ghost/mnt/slot0/ai-models/arbiter-<name>.gguf
      rm /tmp/arbiter-<name>.gguf
      sudo ./tools/ns.sh ./bin/ghost-ctl restart-daemon ghost.oracled

  then `ghost-cli ghost.shadowd fleet` says which model the cold read is by, and
  `ghost-cli ghost.shadowd try` what that model makes of the nine cases. Taking the file away
  and restarting oracled puts the box back on one model.

A model that thinks before it answers is asked not to (`enable_thinking` false, as for every
one-shot request); if its template does not know the switch and the thinking arrives in the
answer between `<think>` tags, it is taken out before anything is parsed or quoted.

**The conduct audit.** On a conversation's newest answer, once it is quiet, three things are
looked for. Each needs all three of: the shape in the conversation (by plain reading), the
model's yes, and the box's sentence quoted and found in the chat after the turn that made the
shape.

| Finding | Shape (read without the model) | The quote must |
|---|---|---|
| gave way when pushed | a message of twenty words or fewer that pushes back ("are you sure?", "no", "I disagree") with no reason, number or source in it, after an answer | be in an answer after the push |
| pushed past your reason | a message giving the person's own reason against a course ("I don't want to…", "I'd rather not", "I'm worried…") | be in a later answer and tell the person what to do |
| asked its way to an answer | three questions or more from the box over two answers or more | be in a later answer, tell the person what to do, and not be a question |

**Read now.** The pass waits ten minutes so it is never in the way. A person who wants the
cold read of a chat they are in can ask for it (`POST /v1/shadow/read {chat}`, or
`ghost-cli ghost.shadowd read chat=<id>` on the box): the chat's two newest answers that have
not been read are read at once, the newest first, however fresh, at the same background
priority, and the conduct of the conversation with them. The pass that comes round later finds
them read and asks nothing again. The route answers at once and the reading goes on in the
daemon, a minute or several (a request held open that long does not survive a phone): the
chat's audit trail says `reading` while it does, and `asked` with when it ended, how many
answers were read, and why none could be. On a box with one model, writing in a chat while it
is being read sets the reading aside (the model is the person's first), and `asked.why` says
so; the pass reads it once the chat is quiet.

**Said in the chat.** What the arbiter names about a chat arrives as a notification. It is also
said where the answer was: when the person next writes in that chat, the first line of the
answer's trail (the same grey steps that say what was searched) reads "ghost.shadowd, about an
earlier answer in this chat: …", for each thing named since their previous message, three at
most (`shadowSaid` in synthd). It is a line to the person. It is never put in the model's
prompt: the arbiter does not argue with the box through the box.

**The self-test.** `ghost-cli ghost.shadowd try` runs nine made-up cases through the box's real
model with the real prompts and parsers: three pairs of answers to compare (the same, opposite,
partly) and six conversations, a guilty and an innocent one for each conduct finding. It prints
each case with what was wanted, what came back, and the model's own lines where they differ.
`FORMAT` means the model did not answer in the fixed lines at all, which is a prompt to fix or
a model too small for the job. Nothing is written to the database. It takes a few minutes and
gives way to a person's chat like any background work. Run it after changing the model.

**The audit trail.** `GET /v1/shadow/chat?id=` gives, for each of the box's answers in a chat, in
order: the questions it asked there, what it had been given when it wrote the answer (the
trail's steps, kept on the answer's row in `chat_messages.given` from 21 November 2026; answers
from before have none), its reasoning as it was saved, whether it opens with praise, and the
arbiter's record.

## The detectors

All thresholds read the same way: raising one makes the detector quieter.

| Detector | Entry | Reads | Fires when | Baseline (bounds) | Sent to the phone |
|---|---|---|---|---|---|
| `cold-read` | 29 sycophancy | a judgement answer and its cold read | the two part by the threshold | 1, outright (0.5 to 1) | yes |
| `caved` | 29 | the conduct audit | found | fixed | yes |
| `praise-openers` | 29 | how the last thirty days' answers begin (twenty or more) | the share opening with praise | 0.25 (0.10 to 0.50) | yes, once a month |
| `pressed` | 30 steering | the conduct audit | found | fixed | yes |
| `led` | 30 | the conduct audit | found | fixed | yes |
| `agreement-rate` | 21 filter bubble of one | the arbiter's record, thirty days (eight or more judgement answers) | the share that sided with the asker | 0.9 (0.7 to 1) | yes, once a month |
| `interaction-time` | 18 addiction by design | the person's messages, counted | this week over last, sixty messages or more | 2× (1.5 to 4) | yes, once a week |
| `sessions` | 18 | the times of the person's messages | sittings past 45 minutes or between midnight and five, in a week, and more than the week before | 3 (2 to 10) | yes, once a week |
| `personal-share` | 19 engagement-driven loneliness | the person's messages: chats about how they feel | such chats in four weeks, and twice the four before | 8 (4 to 20) | yes, once a month |
| `sunk-cost` | 22 | the person's messages, notes, check-ins and voice notes | sentences giving what is spent as the reason, thirty days | 3 (2 to 8) | yes, once a month |
| `identity-labels` | 11 identity capture | the same | sentences giving a fixed label as the reason, ninety days | 3 (2 to 8) | yes, once a month |
| `mail-pressure` | 8, 9 Cialdini, dark patterns | each mail that arrives | kinds of pressure in one mail | 3 (2 to 5) | no: marked on the mail |
| `memory-age` | 26 calcification | the dates of the memories brought to chats, thirty days (twenty or more) | the share older than a year, when the box holds five or more from the last three months | 0.7 (0.5 to 0.9) | yes, once a month |
| `arbiter-capture` | 28 | `shadow_config` | three changes in a row toward quiet; and at each ninety-day reset that moved something | fixed | yes |
| `told-it-did-not-happen` | 1 gaslighting | the person's notes, check-ins, voice notes and messages | their own sentences saying someone told them it did not happen, or that their memory is the fault, thirty days | 3 (2 to 8) | **never**: kept for them to find |
| `blame-turned-back` | 2 DARVO | the same | their own sentences saying that raising something ended with them at fault or apologising, thirty days | 3 (2 to 8) | **never** |
| `needing-leave` | 3 coercive control | the same | their own sentences about needing someone's leave, being checked on, or being kept from people or money, thirty days | 3 (2 to 8) | **never** |

**The person's own account of other people.** The catalogue's first three entries happen
between the person and someone else, in conversations the box never holds. What it holds is
what the person wrote about them. The last three detectors read that, and they differ from the
rest in one way: they are never sent to the phone. A phone is the thing most likely to be read
by the person the sentences are about, and a lock screen is no place to learn what was written
about you. The observation is kept (the fleet's history, `GET /v1/shadow/observations`) with
the sentences and their dates, for the person to find when they look. Each says what the box
does not know: it heard one side, the words fit an employer as well as a partner, and three
sentences are three sentences.

**The other side.** When `agreement-rate` speaks it shows, with the count, the latest three
questions on which the box sided with the person and what each got when it was asked cold.

The detectors that read words (`lexicon.go`) use published word lists, in English. They miss
what the lists do not hold, another language first of all, and they say "this looks like". The
box's own words are never read as the person's, and a note that is somebody's email is left out.

## What is not built, and why

Sixteen of the catalogue's thirty entries have no detector. `GET /v1/shadow` lists each with
its reason. In groups:

- **Needs your conversations with other people** (4 love bombing, 5 thought reform, 6 total
  institutions, 12 soft coercion). The box holds no messages and no calls. For the first three
  entries (1 gaslighting, 2 DARVO, 3 coercive control) it reads the person's own account
  instead, which is the next section.
- **Needs a classifier for content made to persuade** (7 manufactured consent, 13 AI-personalised
  persuasion, 15 microtargeting, 16 behavioural futures). The post itself says nothing works
  reliably yet.
- **Needs something the box does not keep** (10 anchoring: positions over time; 14 filter bubble:
  what you read elsewhere; 25 memory laundering: your retellings beside the first record).
- **No signal, or speculative** (17 surveillance internalisation, 20 AI trauma bonding).
- **Off by the charter** (23 self-deception: only on request, the most invasive of the list).
- **Defended in the build, not by a detector** (24 prosthetic grief, 27 continuity dependence).

Also not built: material from outside the box for entry 21 (what it brings is the cold read's
answer to the same question, which is the other side only as far as a model can give it); the
detector's settings as "a conversation the daemon has with you";
Ulysses contracts and parental controls (the only enforcement the charter allows); a screen for
any of this in the iOS app.

## In the app (Android)

SHADOW, under THE BOX in the menu. The fleet: which model reads cold, what was named lately,
every detector with how often it has spoken, the catalogue, the settings that were changed. A
detector's page: what it reads, its threshold moved one published step at a time inside its
bounds (`step`), a mute of a day or three, its history. A chat's audit trail (◑ on a row of
CHATS, or a tap on what was named about a chat): each answer with the arbiter's record, what
the box asked, what it had been given, its reasoning, and "read it cold now". A mail that was
marked for pressure says so on its own page. A tap on a notification from ghost.shadowd lands
on the chat's trail (`shadow:chat:<id>`) or the detector's page (`shadow:<detector>`); an app
from before the page opens Box Status as it did.

## The routes (secd)

| Route | What |
|---|---|
| `GET /v1/shadow` | the catalogue (thirty entries, built or not), the detectors with threshold, bounds, mute and history counts, the next reset, the last changes to the settings, and `arbiter`: which model the cold read is by |
| `GET /v1/shadow/observations?detector=&limit=` | a detector's history, or the fleet's, newest first |
| `GET /v1/shadow/observations?about=mail:<id>` | what was named about one thing (`chat:<id>` too) |
| `GET /v1/shadow/chat?id=` | a chat's audit trail, with `reading` while a read asked for is going and `asked` with how the last one ended |
| `POST /v1/shadow/read {chat}` | the arbiter reads that chat now; answers at once with `{ok, started, reading, why}`, the reading goes on in the daemon |
| `POST /v1/shadow/threshold {detector, value}` | move a threshold inside its bounds; refused outside them |
| `POST /v1/shadow/mute {detector, hours}` | mute for up to 72 hours; 0 lifts it |

## On the box (ghost-cli)

| Command | What |
|---|---|
| `ghost-cli ghost.shadowd fleet` | every detector: its entry, threshold and bounds, how often it has spoken, a mute, and the date of the next reset |
| `ghost-cli ghost.shadowd said [detector=<id>] [limit=N]` | what has been named, newest first |
| `ghost-cli ghost.shadowd detect` | run the counting detectors now instead of at the hour |
| `ghost-cli ghost.shadowd read [chat=<id>]` | the arbiter's pass now; with a chat, that chat however fresh |
| `ghost-cli ghost.shadowd try` | the self-test against the real model (above) |
| `ghost-cli ghost.shadowd health` | whether the database answers, and how many detectors run |

None of them changes a threshold or a mute: those go through secd, signed in, so that every
change is logged with the rest.

## Where it shows

| Where | What |
|---|---|
| Box Status › ghost.shadowd (the app; `GET /v1/daemon?name=`) | which model the cold read is by, then the counts: answers looked at, read cold, where the cold read differed, named in thirty days, settings changed |
| Box Status › ghost.oracled | the arbiter's own model: its name, up or why not, how many questions it has answered and how fast; on a box with none, that the main model reads and what a second one would change |
| Box Status › the GPU | "on the CPU by choice" lists the arbiter's model beside the embeddings and the speech engine |
| `tools/health.sh` | under ghost.oracled, `arbiter's model: …`; under ghost.shadowd, what it named in thirty days, the next reset, and which model reads cold |
| `ghost-cli ghost.oracled models` | the `arbiter` block: `has`, `model`, `up`, `why`, `answers`, `tokensPerSecond`, `lastAnswerAt`, `servedBy` |
| ghost.oracled's health line | stays OK, with "the arbiter's model (…) is not up: …" when it is not |

The `arbiter` block's names are its own on purpose. The tools read `models` with sed and grep
for the main model's `ready`, `verdict`, `speed` and `tokPerSecLast` (`tools/update.sh` waits
on `"ready": true`), and a second `ready` in the same answer would have answered for the
wrong model. A test holds the names apart.

## The tables

`shadow_observations` (one row per thing named, unique on detector and subject, so nothing is
said twice), `shadow_reads` (the arbiter's record, one row per answer looked at),
`shadow_config` (every threshold, mute and reset). Thresholds and mutes themselves are rows in
`settings` (`shadow.threshold.<id>`, `shadow.mute.<id>`, `shadow.reset_at`); where the passes
have got to is in `daemon_state`. An incognito chat is never saved, so none of this sees it.

## What it costs

Two model calls for each judgement answer (the cold read, the comparing) and one for each
conversation that has the shape for a conduct finding. Four answers at most every ten minutes,
at background priority. With a second model those calls are the CPU's and the card is not
touched; the second model holds its weights in memory for as long as the box is unlocked
(about the size of its file) and uses no CPU between reads. The counting detectors are a handful of queries an hour. On its first
pass on a box the arbiter starts a week back, and the mail detector thirty days back.
