# Contributing to LocalGhost

Thanks for stopping by. Here's an honest state of things so you know what's useful right now.

## What This Project Is

LocalGhost is a local-first, privacy-focused AI platform. Everything runs on hardware you own. Nine daemons, each with one job, all open-source. The [manifesto](https://www.localghost.ai/manifesto) is the long version.

## Where We Are

Phase 0. Website, architecture, and [ten essays](https://www.localghost.ai/hard-truths) documenting the thinking. First commit incoming. That means some things are useful to contribute now, some things aren't useful yet, and a few things will never be useful.

## Useful Right Now

**Read the Hard Truths and tell me where the reasoning is weak.** Especially [The Honeypot Under Your Desk](https://www.localghost.ai/hard-truths/honeypot) (the threat model), [Dictator Brain](https://www.localghost.ai/hard-truths/dictator-brain) (the adversarial mirror architecture), and [The Model Trap](https://www.localghost.ai/hard-truths/model-trap) (the behavioural test suite approach). Open an issue with "Feedback: [post title]" if you spot holes.

**Pressure-test the architecture docs.** `docs/SECURITY.md` covers the multi-PIN model. If you've broken a similar system before, or know a case the current design doesn't handle, tell me.

**Build something in the [ecosystem roadmap](https://www.localghost.ai/build).** Data liberation tools, cross-device sync without the cloud, a local photo library that doesn't suck. LocalGhost can't build everything and shouldn't. If you build one of these, add it to the [Freehold Directory](https://www.localghost.ai/directory) when it's ready.

**Write `ghost.tallyd` plugin specs.** When `ghost.tallyd` exists, it'll need data parsers for bank exports, health apps, fitness trackers, anything that exports structured data. The plugin architecture will be modular. Sketches and proposed schemas for these parsers are useful now so the plugin API gets designed around real inputs rather than imagined ones.

## Not Useful Yet

**Code contributions.** No code to contribute to. When `wisp` (v0.1) ships, this changes. Watch the repo.

**Bug reports.** Same reason.

**Feature requests for unshipped daemons.** If you have ideas about what `ghost.shadowd` should do, read the [Dictator Brain](https://www.localghost.ai/hard-truths/dictator-brain) post first and open a discussion rather than an issue.

## Never Useful

**"Add crypto integration" / "Add blockchain to X".** We use Ethereum for donations. The project isn't crypto-adjacent beyond that, and the `.ai` domain is a coincidence, not a strategy.

**"Centralise X for convenience".** The whole project exists to avoid this. If a feature requires a cloud dependency, it's either doing something wrong or it's not a LocalGhost feature.

**"Monetise X".** The [economics](https://www.localghost.ai/manifesto#economics) are documented. Hardware margin and optional software packages. No subscription.

## How to Reach Me

Issues for public discussion. PGP-encrypted email to `info@localghost.ai` for anything sensitive. Security issues have their own policy in [SECURITY.md](./SECURITY.md).

## A Note on Scope

This project is going to be built slowly and deliberately by someone who has built things before and doesn't need another startup to burn out on. Phase 0 means the foundation. The writing, the architecture, the design decisions that get locked in before the first line of code ships, those are the things that matter now, and those are the things worth contributing to.

When the code lands, the contribution surface widens. Until then, the best contribution is an argument that makes the design better before it's written.
