# Contributing to LocalGhost

Thanks for stopping by. Here is an honest state of things, so you know what is useful right now.

## What this is

A box at home that keeps a person's photos, trail, notes, voice, health and chats, reads them with
models that run on the box, and gives them back on the phone. A fleet of Go daemons on a Debian
box with a GPU (`server/`), and an Android app that is the only client (`app/android/`). The
[README](README.md) is the shape of it, [the release notes](server/releases/0.0.1.md) are the
whole of what wisp 0.0.1 does and how it works, and `server/tools/DEV_UPDATE.md` is the
engineering journal, newest at the end: what changed, why, and what was tested.

## Where we are

One release, wisp 0.0.1, cut on 2 October 2026. One box runs it with one phone, every day. The
code is the truth; where an essay on the website and the code disagree, the code is what ships
and the essay is where it is going.

## Useful right now

**Run it and say what broke.** A box is a Debian machine with an NVIDIA GPU and a spare NVMe;
`server/tools/README.md` is the setup. An issue with the daemon's log lines
(`journalctl -u ghost.<name>`) and the Box Status screen is a good issue. A photo format the box
got wrong, a trail the map drew badly, a story that said something the facts did not, are all bugs.

**Fix a bug with a test.** Every package has tests beside it (`go test ./...`; the Postgres ones
run when `GHOST_PG_SOCKET_DIR` names a Postgres socket directory and skip otherwise). A pull
request that adds the failing case first is easy to say yes to.

**Read the security model and tell me where it is weak.** [SECURITY.md](SECURITY.md) says how to
report; the [release notes' known gaps](server/releases/0.0.1.md#known-gaps-at-wisp) say what is
already known. If you have broken a similar system, or know a case the design does not handle,
say so.

**A format, a feed, a source.** The box reads EXIF and ISO-BMFF, GeoTIFF, ZIM, OpenStreetMap's
land and roads, Health Connect's days and samples, the exchanges' order books. A phone or a camera
that writes something the box misreads is worth a fixture and a test.

## How changes land

- One commit, one change, with the reason in the message. The tree is `gofmt`-clean in every
  file a change touches.
- A test for what changed, in the package that changed. No network in tests; a fake mirror, a
  fake exchange, a fixture file.
- Nothing is dropped from the schema automatically. Adding is fine (`internal/hw/schemadef.go`
  converges at every start); dropping is a named, dated migration a person decides on.
- Nothing fetches from an upstream. The mirror, checked against its signed manifest, is the only
  place setup and updates take files from, and an unreachable mirror stops the step rather than
  falling back. If something needs a new public dataset, it goes onto the mirror first.
- Nothing leaves the box but what the [README](README.md#what-leaves-the-box) names. A feature
  that needs a request beyond that list needs a conversation first, in an issue.
- The standard library first. The server's dependencies are go-tpm, `x/crypto` and `x/term`, and
  that list grows for a reason, not for convenience. The decoders (zstd, ZIM, GeoTIFF, EXIF,
  ISO-BMFF) are written in the tree for that reason.
- A note at the end of `server/tools/DEV_UPDATE.md`: what changed, why, what was tested.
- CI ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)) runs `go vet`, every Go test and a
  macOS cross-compile on every push and pull request, and the app's JVM tests on pull requests
  and on demand (Actions › ci › Run workflow). A box is Linux;
  the tree still builds on a Mac so it can be edited there (Linux-only calls live in
  `_linux.go` files with a counterpart beside them).

## Not useful

**"Add crypto integration" / "Add blockchain to X".** Ethereum takes donations. The project is
not crypto-adjacent beyond that, and the `.ai` domain is a coincidence, not a strategy.

**"Centralise X for convenience".** The whole project exists to avoid this. If a feature needs a
cloud dependency, it is either doing something wrong or it is not a LocalGhost feature.

**"Monetise X".** The [economics](https://www.localghost.ai/manifesto#economics) are documented.
Hardware margin and optional software packages. No subscription.

**A dependency for something the standard library does.**

## How to reach me

Issues for anything public. PGP-encrypted email to `info@localghost.ai` for anything sensitive
([the key](https://www.localghost.ai/.well-known/pgp-key.asc), fingerprint in
[SECURITY.md](SECURITY.md)). Security issues have their own policy there.

## A note on pace

This is built slowly and deliberately by one person who has built things before and does not
need another startup to burn out on. Pull requests and issues get answered; it may take a week.
