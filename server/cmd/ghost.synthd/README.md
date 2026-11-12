# ghost.synthd

The memory daemon. It is the only thing on the box that makes memories, it keeps them in order,
and it brings them to a question. It also runs the chat (the question, what the box holds on it,
the model, the answer) and keeps the day's news, the box's Wikipedia and the rates the chat
quotes, which are not this file's subject.

The model it works to is in the Hard Truths posts ("How memory gets made" first). What of that
model is built, what is not, and where each piece lives is in [`docs/MEMORY.md`](../../docs/MEMORY.md).
The design written in April 2026, before any of this existed, is kept beside this file as
[`DESIGN_APRIL_2026.md`](DESIGN_APRIL_2026.md); it is a record, not a description.

Runs only while the box is unlocked. Spawned by ghost.watchd from `<mount>/bin`; logs to
`<mount>/logs/ghost.synthd-YYYY-MM-DD.log`.

## What it reads and what it writes

It reads the **journal** (`journal_entries`), the one seam every ingester writes to in its own
words: framed (a photo, where), noted (a note, a chat once it has gone quiet, the evening
check-in), voiced (what a voice note said), tallyd (the day's health line). It reads the day
routes framed tells, the photos and their tags, the health sync, the phone's calendar and
contacts. It writes to no other daemon's table.

It writes `memories`, `memory_links`, `day_summaries`, the On This Day reports, and a handful of
settings (today's front page, the owner's name, how far each pass has got). The person's own
acts on a memory (write one, edit, delete, anchor, open) come through ghost.secd from the phone
and are written by the store (`internal/hw/notifstore.go`), not by this daemon.

## The pass

Every ten minutes while unlocked (`distillLoop` in `main.go`), in this order. Everything that
needs the model waits for the GPU; everything else is plain SQL and Go and runs every time.

| Step | File | What it does | Model |
|---|---|---|---|
| the note | `aboutme.go` | the note about me and my people, read once when it changes: facts about me, one line per person | yes |
| the check-ins | `aboutme.go` | what the evening check-ins say about me and my people | yes |
| names | `aboutme.go` | older memories written again with the owner's name | yes |
| distilling | `main.go` | each journal entry a person wrote, read once: what is worth keeping, about me or one of my people | yes |
| outings | `outings.go` | the photos clustered into outings, the taste they add up to | no |
| consolidation | `consolidate.go` | two rows for one person merged, memories named for a person folded into theirs, outings chained into trips, days and outings folded under them; once a day the people written up from their facts | once a day |
| places | `places.go` | the places the person keeps going to, counted from the trail and the photos | no |
| what it noticed | `places.go` | up to three things a day, each number checked against a counted sheet | yes |
| the telling | `prose.go` | outings and trips written from their fact sheets, kept only when they stay inside them | yes |
| read again | `verify.go` | distilled memories and people's facts checked against the record they came from; what the record does not say is put in doubt | yes |
| the days | `days.go` | one fact sheet and one summary per day, backfilled through the archive | template, then yes |
| the months | `months.go` | a month that ended six weeks ago becomes one memory, its days and outings under it | no |
| the neighbourhoods | `links.go` | which memories belong together and why, the table made equal to what the memories imply | no |

What the last pass did is on Box Status (`hw.SaveSynthStatus`).

## The rules every pass keeps

- **A deletion is never undone.** A memory the person deleted is a tombstone: no pass makes it
  again, and its links and its place on the front page go with it at once.
- **An edit is never written over.** A memory the person wrote or rewrote stands as written; the
  box may add facts beside it, it does not change the words.
- **An anchor is the person's alone.** No pass sets one, lifts one, deletes a memory that carries
  one or folds it into another. A merge of two rows for one person carries the mark to the row
  that stays. One exception, on purpose: a day emptied because the person deleted everything it
  was told from goes even if anchored, because their delete outranks their mark.
  `TestNoPassSetsAnAnchor` reads every daemon's source for this.
- **What the model writes is checked against what was counted.** A telling that says a number or
  a name the fact sheet does not hold is dropped and the template stands.
- **Nothing is invented to fill a gap.** A question that matches nothing is given nothing.

## The chat's side

`chatIdentity` puts the front page first (`frontpage.go`: the opening of the person's note, the
anchors, the heaviest few memories, chosen once a day so the model's cache of the prompt's
beginning holds). `gatherContext` then adds what the question's words find: the memories
(`memoriesSource`, the heavier of two equal matches first, and the best match's neighbour), the
archive through ghost.searchd, the box's Wikipedia, the weather, the calendar, the contacts, the
numbers. `grounding.go` cuts the lot to what the model can read in about twenty seconds.

## Control commands

`ghost-cli ghost.synthd <command>`: the base ones (`ping`, `status`, `reload`, `log-level`) and
`chat`, `onthisday`, `outings`, `days`, `consolidate`, `read-again`, `news`, `wiki`, `rates`,
`index-stats`, `ready`, `prime`.

`prime`, `ready` and `index-stats` are ghost.cued's side of "Before You Ask". The index behind
them is the empty one: `prime` returns nothing. That is the largest piece of the model still
missing here, and `docs/MEMORY.md` says what it needs.
