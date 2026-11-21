# ghost.shadowd

The ANTI-POSSESSION daemon (charter: hard-truths/should-not-possess, /dictator-brain,
/critic-worth-listening-to). Not the memory layer , that is ghost.synthd.

A fleet of individually-tunable manipulation-pattern DETECTORS (the catalogue) plus the
COLD-READ ARBITER, which asks a saved question again with nothing about the person and compares.

The contract: NAMING IS THE ACTION. Detectors name the pattern, point at the data, and stop.
No blocking, no refusing, mute-not-disable, enforcement only for rules the user wrote themselves
(Ulysses contracts, parental controls , neither built).

All of the logic is `internal/shadow`; this directory is the loop. `docs/SHADOW.md` says the
whole of it: every detector with what it reads and its bounds, the arbiter step by step, the
routes, and what is not built with the reason.

## Status

Seventeen detectors run, over fourteen of the catalogue's thirty entries (the post's twenty-eight
and two from the posts before it: sycophancy, steering by questions).

- The arbiter, afterwards: the cold read of a judgement answer, and the conduct audit of a
  conversation (gave way when pushed, pushed past your reason, asked its way to an answer). A
  conduct finding must quote the box's own sentence from the chat or it is dropped.
- Counted from what the box holds, hourly: messages week on week, long and late sittings, the
  person's own sentences (what is spent, a fixed label, how they feel), pressure in the mail
  that arrives, the age of the memories brought to chats, how often the box sides with the
  person, how often it opens with praise.
- The settings' own guard: every change logged, three in a row toward quiet named, thresholds
  back to the baseline every ninety days, a mute three days at most.

- On the box: `ghost-cli ghost.shadowd fleet | said | detect | read [chat=<id>] | try`. `try` is
  the self-test: nine made-up cases through the real model, printed with what was wanted and
  what came back. Run it after changing the model.
- What the arbiter names about a chat is also said in that chat: the next answer's trail opens
  with it.

- The arbiter's own model: a GGUF at `ai-models/arbiter-<name>.gguf` is run by ghost.oracled as
  a second model on the CPU, and the cold read is asked of it. A box without one reads with its
  main model and says so on every record.

Not built: the sixteen entries that need conversations with other people or a classifier nobody
has; a screen in the app. v1 does not ship without this daemon running.

The post "How memory gets made" gives this daemon one question for the queue ("whether a pattern
across five notes is a cluster worth naming or a coincidence"). There is no queue yet and this
daemon asks nothing; docs/MEMORY.md keeps the list of what the memory model has and has not.
