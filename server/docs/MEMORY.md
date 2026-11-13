# How the box keeps memories

What is stored, who writes it, and how it stands against what the Hard Truths posts say a memory
layer should do. The posts are the design: "How memory gets made" (POST_09) for the model,
"Before you ask" (POST_11) for surfacing, "What the box does now" (POST_18, section 9) for what
had shipped by then. This file is the code's side of that, kept current. Where a post describes
something this file lists as not built, the post is ahead of the code and this file says so.

The daemon is ghost.synthd (`cmd/ghost.synthd`, its README has the pass step by step). The tables
are declared in `internal/hw/schemadef.go`. `docs/DATA.md` has the rules every table keeps.

## The three layers

```
 the originals          photos, notes, voice notes, the trail, the health sync, chats
      │                 (each ingester's own tables and files; never moved, never rewritten)
      v
 the journal            journal_entries: one row per thing that happened, in the ingester's words
      │                 (immutable; UNIQUE(source, ref); synthd is the only reader that makes memories)
      v
 the memories           memories: what the box keeps, in words, with where each came from
                        memory_links: which of them belong together, and why
                        day_summaries: each day's fact sheet and its telling
```

The originals stay where they are whatever happens above them. Everything in the third layer
can be made again from the first two, except what the person wrote, edited, deleted or marked.

## A memory

One row in `memories`.

| Column | What it is |
|---|---|
| `title`, `body` | the memory in words |
| `kind` | what sort of thing it is (below) |
| `source_ref` | what it was made from: a journal entry's ref, `day:2026-10-03`, `trip:2026-09-12`, `person:cristina` |
| `source_chat` | the chat it was distilled from, when it was |
| `meta` | what was counted beside the words: an outing's places and covers, a person's facts each with its source, `part_of`, the read-again verdict. Written whole by the passes |
| `created_at` | for a day, an outing, a trip or a month, when it happened (its end); for the rest, when the box made it |
| `user_edited` | the person wrote or rewrote it: no pass changes its words again |
| `tombstoned` | the person deleted it: no pass makes it again |
| `anchored_at` | when the person marked it as an anchor; NULL for the rest. No pass writes it |
| `touched_at` | when the person last had it in front of them: its page opened, or a question matched it |
| `emb` | reserved. Empty: memories are matched by word, not by meaning |

## The kinds

| Kind | One per | Made by | Words from |
|---|---|---|---|
| `me` | fact about the person | the note about me, the check-ins | the model, read again against the record |
| `person` | person | the note, the chats, the check-ins | facts kept with their source; the model writes them up once a day, inside the facts |
| `distilled` | thing worth keeping from a note, a chat, a voice note | the distiller | the model, read again against the record |
| `user` | memory the person wrote by hand | the person | the person |
| `outing` | run of photos in one place | the photos | a template from counts; the model when it stays inside them |
| `trip` | chain of outings away from home with a night in it | the outings | the same |
| `day` | day with something in it | everything that day | a fact sheet, a template, then the model inside the sheet |
| `month` | month that ended six weeks ago | its days | counted only |
| `place` | place the person keeps going to | the trail and the photos | counted only |
| `insight` | thing the box noticed over the last weeks | a counted sheet | the model, every number checked against the sheet |

## Detail fades, the outline stays

POST_09, section 4: hold yesterday in detail, last week in summary, last year in chunks, and do
it without throwing the originals away.

A memory can be **part of** another (`meta.part_of` holds the whole's ref). A day or an outing
folds under its trip; a day under an outing that was a night or more away; and a day or an outing
older than six weeks, not already under a trip, under its **month** (`months.go`). MEMORIES shows
the whole where it would have shown the parts. The parts are one tap down, unchanged, and what
they were told from is untouched on disk. An anchored part still shows on its own.

What makes an outing and a trip (`internal/outings`, `consolidate.go`), since both decide what
the feed shows:

- An **outing** is three photos or more, close in time (a silence of 36 hours ends it) and in
  place (a photo 30 km from the outing's centre begins another). It is **away** when its centre
  is more than 25 km from home, and home is where the photos say it is: the 5 km cell
  photographed on the most days.
- A stop away from home with one or two photos is not a memory by itself. It joins the outing
  beside it (the one before, else the one after, within 36 hours), counted in its photos, places
  and dates, and leaving its centre where it was.
- Two outings can begin on one day. The first has the ref `outing:<day>`, the next
  `outing:<day>.2`.
- A **trip** is a chain of outings away, each beginning within three days of the last ending,
  that has a **night** in it: the clock where the photos were taken passed four in the morning
  between the first photo and the last, and those are eight hours apart or more. One outing can
  be a trip. A day out, however long, is not.
- A trip is titled by its countries, and by its places when it never left the country the
  person lives in (the one most photographed at home).

Both sets of rules are named (`outings.Rules`, `tripRules`), and a box that takes a build with
other rules groups its photos and chains its trips again at the first pass.

So the feed reads: the last six weeks day by day, before that month by month, with every trip a
whole of its own. Years are not built: a month from 2014 is still a month.

## The company a memory keeps

POST_09, section 2: a memory survives by the neighbourhood it is in, and touching one member
reinforces the rest.

`memory_links` holds one row per pair of memories that belong together, and why. The linking
pass (`links.go`) derives the whole set from the memories with no model, every pass, and makes
the table equal to it:

| Why | Between |
|---|---|
| `part` | a part and its whole |
| `names` | a person and a day, an outing, a trip, a note or a fact whose words name them |
| `place` | a place and the days, outings and trips that were there |
| `written` | a note or a fact and the day it was written on |
| `month` | a trip or an outing and the month it began in |

Every link is something the box can point at, and the memory's page says which. A person or a
place keeps at most its forty newest links of a kind. A name must stand as a word with its
capital: "Will" links, "it will rain" does not.

**Not built: the link the post opens with.** Four dinners in four cities that share no word and
are alike only in what they mean. That needs the meanings (`emb`), and with them the question
the post gives the queue: is this correlation meaningful.

## Anchors

POST_09, section 3: some memories carry their weight from the first instant, and "the user is the
only person who can mark them".

The person marks a memory on its page; `anchored_at` records when. Nothing else writes it
(`hw.MemoryAnchor` is the only setter; `TestNoPassSetsAnAnchor` reads every daemon's source).
An anchor comes first in MEMORIES past any limit, is always on the front page, and wins a tie in
what a question brings. No pass deletes an anchored memory or folds it into another; a merge of
two rows for one person carries the mark to the row that stays. It is still folded and linked
like anything else. One thing outranks it: the person's own delete.

## Weight

How much a memory counts when the box has to choose (`weight.go`). Worked out from the rows when
it is wanted, never stored:

- an anchor: high, and stays high however old and however alone;
- company: more neighbours, more weight, levelling off;
- warmth: a neighbourhood is as fresh as its newest moment, a member made or a member touched,
  and cools by half every ninety days nobody goes near it. "The cluster's age is the age of its
  most recent member, not the average age of its members";
- the person's own words count a little over the box's.

A memory is **touched** when the person opens its page or asks a question that matches it.

## The front page

What every chat starts from (`frontpage.go`): the opening of the note about me, the anchors,
then the heaviest of the rest, within 1,200 characters. Chosen once a day and kept, so the
prompt begins with the same bytes all day and the model's cache of it holds. Chosen again when
the day turns, the note changes, or the person sets an anchor, edits a memory or deletes one.
MEMORIES marks the memories on it.

The page is the head of every prompt, so what may be on it is narrower than what a question may
bring. A distilled memory is on it only once the read-again pass has confirmed it against its
record, or when the person anchored it. Each memory is one sentence on one line, clipped, so its
words cannot pass for a heading or for the person's note. And the box's lines are introduced to
the model as its own notes, which can be wrong. This matters because other people's text can
still reach a distilled memory (a web page quoted in a chat answer: audit run 2, open); the page
must not be where that text gains a voice.

## What a question brings

`memoriesSource`: the question's words that carry meaning are matched against every live memory
the box still stands behind. The two that match the most words go to the model, the heavier of
two equal ones first; the best match brings its heaviest neighbour, which the question did not
have to name. Then the archive, the box's Wikipedia and the rest (`gatherContext`). A question
that matches nothing is given nothing.

## The person's say

POST_09, section 6: any memory can be opened, seen for what the system made of it and why,
corrected, or deleted.

| The person can | And then |
|---|---|
| open a memory | its page says its kind, where it came from, what it was counted from, what the box found when it read the record again, what it is part of, and the company it keeps with the reason for each |
| edit it | the words are theirs for good |
| delete it | a tombstone. Its links go at once, the front page is chosen again, its month is counted again at the next pass, and no pass makes it again |
| mark it as an anchor | above |
| write one by hand | `kind = user`, theirs from the start |

**Read again** (`verify.go`): the box checks each distilled memory and each fact about a person
against the record it was made from. What the record does not say is put in doubt, shown to the
person with what the record says instead, and kept out of the chat until they decide.

**Not built: reclassify.** A memory's kind cannot be changed from the phone, and a link cannot be
cut by hand.

## Against the posts

| The posts say | The code |
|---|---|
| The originals do not move; the interpretation is what changes (09 §5) | Built. Three layers, and nothing in the third writes to the first two |
| Every modality writes into one memory layer (09 §5) | Built. Photos, notes, voice, health, chats all reach the journal |
| Neighbourhoods; touching one member reinforces the rest (09 §2) | Built for links the box can point at (who, where, when, part of). Not built for likeness in meaning |
| A cluster's age is its newest member's (09 §2) | Built, in the weight |
| Anchors, marked only by the person (09 §3) | Built |
| Detail fades, outline survives, nothing destroyed (09 §4) | Built to months. Years not built |
| The system decides alone most of the time, from touch, cluster and anchor (09 §6) | Partly. Those three are the weight, and the weight orders; nothing is let fade on it yet |
| The queue: yes, no, don't ask again; it waits (09 §6) | Not built. The box asks one thing a day (the check-in) and offers at most one place and one old day. There is no queue, and no third answer |
| Open any memory, see why, reclassify, edit, delete (09 §6) | Built, except reclassify |
| Deletion restructures what depended on it, slowly, and is not optional (09 §6) | Built for links, the front page, months and days emptied of their records. Open: a day's memory deleted on MEMORIES is still told on the DAY page |
| The right memory surfaces on a cue, unasked (09 §2, 11) | Not built. ghost.cued's engine is whole and wired, but nothing feeds it a context and the index it primes against is empty. What comes unasked is a day a year ago and something nearby |
| Rich about the user, thin about everyone else: a person is a reference that connects memories, never a model of them (13) | Close to the line, and worth a look. A person is one memory that connects the days and notes naming them, which is what the post asks for. It also keeps what the box's owner said of them as a list of facts, each with its source, and the model writes those up once a day, which is more than a pointer. Nothing is kept of what they themselves said or did |
| Outings, days, the taste (18 §9) | Built |

## What is missing, by what it needs

1. **Meanings.** Embeddings on memories (the embedder ghost.searchd runs would do). That gives
   likeness links, retrieval by meaning, and an index for ghost.cued to prime against.
2. **A queue.** One table, three answers, and "don't ask again" kept per kind of question. The
   first questions are the ones the linking pass cannot settle: are these two the same person,
   is this likeness meaningful, is this a cluster worth a name.
3. **Context for ghost.cued.** The place and the hour are on the box already; nothing hands
   them to the engine.
4. **Years**, and a rule for what is let fade from the feed.
5. **Reclassify** from the phone.

## The tests that hold this

`cmd/ghost.synthd/neighbours_test.go` (links, weight, ranking, the front page, months, no pass
sets an anchor, no placeholder memories), `neighbours_pg_test.go` (the same against Postgres:
links made and let go, a question's matches and its neighbour, the page kept for the day, months
folded and unfolded, anchors through a merge, a fold and a dissolved trip),
`internal/hw/memoryanchor_pg_test.go` (the person's acts at the store),
`internal/secd/memoryanchor_http_test.go` (the two endpoints refuse without a session).
