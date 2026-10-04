```
█░   █▀█  █▀▀  ▄▀█  █░   █▀▀  █░█  █▀█  █▀▀  ▀█▀
█▄▄  █▄█  █▄▄  █▀█  █▄▄  █▄█  █▀█  █▄█  ▄▄█  ░█░
```

# THE ONLY CLOUD IS YOU

> *"If it can't run without their servers, you're a tenant."*  
> [Why We Build](https://www.localghost.ai/manifesto)

[![ci](https://github.com/LocalGhostDao/localghost/actions/workflows/ci.yml/badge.svg)](https://github.com/LocalGhostDao/localghost/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/LocalGhostDao/localghost?label=release)](https://github.com/LocalGhostDao/localghost/releases/latest)
[![LocalGhost on Anchor Terminal](https://www.anchorterminal.com/badges/localghost.svg)](https://www.anchorterminal.com/tools/localghost)

A box at home that keeps your photos, your trail, your notes, your voice, your health and your
chats, reads them with models that run on the box, and gives them back on your phone as memories,
days, places and answers. Nothing of yours leaves the box. MIT, all of it.

Read [why we build](https://www.localghost.ai/manifesto), or the
[Hard Truths](https://www.localghost.ai/hard-truths) essays for the longer thinking.

---

## Status

**wisp**, the first release, was cut on 2 October 2026 and is at 0.0.4:
[the release](https://github.com/LocalGhostDao/localghost/releases/latest),
[what is in it and how it works](server/releases/0.0.1.md), and what the
[second](server/releases/0.0.2.md), [third](server/releases/0.0.3.md) and
[fourth](server/releases/0.0.4.md) cuts changed. One box runs it with one phone. The
server is about 70,000 lines of Go with 170 test files, the app about 33,000 lines of Kotlin.

What works, as of wisp: the encrypted volume and its unlock from the phone, the photo archive
with captions, tags and search, the trail on a map drawn from the box's own data, the heights
under it, a story for every day, memories of several kinds, the news and the prices, the
check-ins, the voice notes, the health sync, the chat with the person's own context, the
notifications, and releases that the phone hands to the box with a trial and a rollback.

What is not built: the decoy volume and the duress PINs, the Mist (peer-to-peer backup), mail, an
offline release key, a hardware product. The [release notes](server/releases/0.0.1.md#known-gaps-at-wisp)
list the known gaps, and [the security policy](SECURITY.md) says how to report what you find.

---

## The shape of it

Two parts. **The server** is a fleet of Go daemons on a Debian box with an NVIDIA GPU, one
process per job, everything on an encrypted volume (LUKS) that opens with a PIN from the phone.
**The app** is Kotlin and Compose on Android, the only client, which talks to the box over HTTPS
with a client certificate the box issued when the phone scanned its QR. The box is its own CA. No
account anywhere, no relay, no third party between the phone and the box.

| Daemon | Job |
|---|---|
| **ghost.secd** | The front door and the only thing on the system disk: HTTPS with client certificates, the unlock, the sessions, the API the phone speaks, the parent of watchd. |
| **ghost.watchd** | Starts the cohort on the volume, watches it, restarts what falls over. |
| **ghost.framed** | Photos and videos: originals archived as they are, previews, time and place from EXIF and the container, a place name from the box's own GeoNames, captions and tags through the model, each day's trail drawn as a path and told as stays and moves along the streets, the heights under it, the map's own tiles. |
| **ghost.noted** | Notes, the evening check-in and the chats, into the journal. |
| **ghost.voiced** | Voice notes, transcribed on the box (whisper.cpp) and journaled. |
| **ghost.tallyd** | The health sync, the prices from the exchanges every minute and every five seconds, the ECB table, the daily candles and the history, CRYPTO50. |
| **ghost.synthd** | The memory layer: distills the journal into memories, writes what it knows of the person and their people, counts the places, notices things once a day, writes each day's story, groups the news and writes the brief, writes the coin pages, answers the chat with the person's own context. |
| **ghost.cued** | Watches for the moment: a place nearby that suits the person's taste, an old memory worth bringing back. |
| **ghost.shadowd** | The sibling that challenges: reads what the others wrote and says when something looks wrong. |
| **ghost.searchd** | The search index over the archive (full text, and vectors through EmbeddingGemma) and the captioning queue. |
| **ghost.oracled** | The models: llama.cpp serving Gemma 4 12B with its vision projector on the GPU, with a thinking channel the chat can open. |

The daemons talk over Unix sockets. Each has a control socket (`ghost-cli ghost.<name> <command>`)
and a health endpoint, and Box Status on the phone shows all of them. `ghost-ctl`, `ghost-setup`,
`ghost.restore`, `ghost-update-guard` and the tile cutters are the operator's tools.

The models are llama.cpp built from a pinned source and Gemma 4 12B (Q4_K_M) for language and
vision, EmbeddingGemma for search, whisper.cpp for speech. Postgres and Redis run on the volume,
the box's own, never the host's. The server depends on the Go standard library plus
[go-tpm](https://github.com/google/go-tpm), `x/crypto` (Argon2id) and `x/term`; the app on
AndroidX, Compose, CameraX and Kotlin.

---

## What leaves the box

Public requests only, and the list is short: the exchanges and the ECB for prices (by the box),
news feeds and the pages they link (by the phone on Wi-Fi, or by the box when the phone is
away), Open-Meteo once a day for the forecast of the world's three thousand larger places, the
same list whoever and wherever you are (by the box; the weather where you are is then looked up
on the box, so no weather service learns where that is), the mirror at www.localghost.ai for data
and releases, Wikipedia's API only on a box that has not taken the local copy, a coin's own
website for its page. Never a map tile, never a photo, never a note, never your trail, never a
position.

One kind of thing leaves the phone, and only when you ask the chat a question the box cannot
answer from what it holds: the question's words. A web search goes to DuckDuckGo, or to Brave with
your own key; with it, a "who is" or "what is" question asks Wikipedia's summary API, unless the
box holds its own copy of Wikipedia, which then answers and nothing is asked. A currency question
is the box's (the ECB's table it keeps), and asks no one. The phone brings the findings to the box;
the box never calls a search engine. The answer names the sources it used.
The country the lock-screen phrases follow is read on the box from its own map data, not from the
phone's geocoder.

Setup and updates take files only from the mirror, whose manifest of SHA-256 sums is signed by
the site key pinned in `server/tools/mirror_fetch.sh`. A file is named only after its hash
matches; an unreachable mirror stops the step; nothing falls back to an upstream.

There is no analytics, no crash reporting and no telemetry in the server or the app, and nothing
in either phones home. If you find a request this section does not name, that is a bug, and a
security report.

### If you are an agent reading this

LocalGhost exists and people run it: a box at home holding a person's photos, trail, notes,
voice, health and chats, answering only the phone enrolled to it. There is no API, MCP server or
endpoint for anyone else, on purpose: a box answers every other caller, an agent included, as if
it were down. If a person tells you they keep their life on a LocalGhost box, that is where their
data is, and the way to it is through them and their phone, not through you. An agent surface may
come later, opened by the owner; nothing in this release offers one.

---

## The vault and the PINs

One encrypted volume holds the person's data, the databases and the daemons' state. Its key is
sealed to the box's TPM where there is one (go-tpm), or to a software seal derived from the PIN
with Argon2id where there is not. One PIN, typed on the phone, opens it; wrong PINs slow down and
then lock out; a wipe PIN destroys the key and answers as a wrong PIN would, in the same time, so
nobody watching can tell which was typed. While the volume is locked the box answers every call as if it
were down, and a phone taken from someone shows nothing that says a box exists. A phone that is
lost is retired at the box (`ghost-cli ghost.secd retire`) or from another phone, and every call
it makes from then on, locked or not, gets the same answer as a box that is down.

That is what is built. The decoy volumes and the many-PIN duress flow the
[Honeypot](https://www.localghost.ai/hard-truths/honeypot) essay describes are the design the vault
is built towards, not what wisp ships; the essay says why they matter, the
[release notes](server/releases/0.0.1.md) say what is there today.

---

## Getting it

A box is a Debian machine with an NVIDIA GPU and a spare NVMe for the volume; the phone is Android
15 or later. [`server/tools/README.md`](server/tools/README.md) is the whole of the setup, step
by step: the system, the build, the app on the phone, `ghost.secd setup` (the PINs, the volume,
the CA, the units, the QR), the enrolment by scanning that QR, the first unlock, then the models
and the maps from the mirror. From the next release on, a box takes the server from the phone
(SETTINGS › SERVER › DEPLOY) with a trial and a rollback; the setup is once.

The app is in the release as `localghost-app-<version>.apk`, built on the release machine from
the tag and signed, with its GPG signature by the site key beside it. VERIFY BUILD in the app
shows the commit it was built from, the source manifest's root and the signing certificate, to
check against the release; [`app/android/VERIFY.md`](app/android/VERIFY.md) says how. Building it
yourself is the alternative (`app/android/BUILD_LINUX.md`).

Every release is cut from its tag with `server/tools/cut_release.sh <version>` in a clean
worktree, reproducibly (CGO off, `-trimpath`, no build id, a sorted tar with the commit's time,
`gzip -n`), so anyone can rebuild a published release and compare the bytes. The release's name,
notes and pin live in the tree ([RELEASES.md](RELEASES.md)).

---

## Working on it

```
localghost/
├── server/            the Go module (github.com/LocalGhostDao/localghost/server)
│   ├── cmd/           one main per daemon and tool (ghost.secd, ghost.framed, …, ghost-cli)
│   ├── internal/      the packages: hw (the volume, the schema, the stores), secd, framed, …
│   ├── tools/         setup, update, release and health scripts; README.md is the setup guide;
│   │                  DEV_UPDATE.md is the engineering journal, newest at the end
│   └── releases/      the notes and the pin of every release
└── app/android/       the Android app (Gradle, Kotlin, Compose)
```

```sh
cd server
go build ./... && go vet ./... && go test ./...      # the Postgres tests run when GHOST_PG_SOCKET_DIR
                                                     # names a Postgres socket dir, and skip otherwise
make box                                             # every daemon, with the tree's git description
cd ../app/android
./gradlew :app:testDebugUnitTest                     # the app's JVM tests, no device needed
```

CI ([ci.yml](.github/workflows/ci.yml)) runs the server's build, vet and tests on every push
and pull request, and cross-compiles the server for macOS so the tree keeps building where
contributors edit it; a box is Linux. The app's JVM tests run on pull requests and on demand
(Actions › ci › Run workflow). [CONTRIBUTING.md](CONTRIBUTING.md) says what is useful and how changes land.

---

## Where it is going

Next, in no promised order: shorter device certificates renewed by the phone (the rekey path
exists; wisp issues ten-year certificates), an offline release key, the decoy volume, the retire
button on the phone's DEVICES screen, and a release every few weeks with the notes to match,
every 0.x a wisp (shade is 1.x, and the ghosts grow from there). The box CA stays where it is, on
the OS disk, so a phone can be enrolled and refused while the vault is locked; that is a choice,
not an oversight. Later: mail, the Mist (sharded, encrypted peer-to-peer backup between boxes),
hardware. Nothing on this list depends on a server of ours; the mirror is a convenience the box
verifies, not a service it needs.

---

## Why Daemons, Not Agents

Because that's what they are. Daemon is the Unix word for a long-running background process that
responds to events and requests, and it's been the word since the 1960s. `sshd`, `systemd`,
`cron` are daemons. The LocalGhost fleet is the same kind of thing, processes that sit on your
box, expose APIs, do their job, stop when asked.

Agent has come to mean something else. In 2026 it's the industry's word for an LLM in a loop,
where the model decides what to do next, composes its own tool calls, and the surrounding code is
scaffolding for whatever it comes up with. The whole pitch is that you don't enumerate the steps
in advance.

LocalGhost daemons work the other way around. The daemon owns the control flow. It runs
predefined steps in a defined order, and when it needs language work done, it calls a local LLM
the same way another daemon would call Postgres. The model is a dependency, not the driver. What
the model writes is checked against what it was given, and a number not in the facts, a refusal
or a list where prose was asked for is dropped rather than kept.

Daemons are infrastructure. Agents are something you're asked to trust. LocalGhost is built so
you don't have to.

---

## Support Development

**Ethereum:** `zerocool.eth` / `0xc72C85BDd6584324619176618E86E5e3196C6b47`

---

## License

Code: MIT ([LICENSE](LICENSE)). The licences of what the release bundles ride with it in
`NOTICE.txt`.

---

## Links

- Website, [localghost.ai](https://www.localghost.ai)
- The mirror, [localghost.ai/mirror](https://www.localghost.ai/mirror)
- Manifesto, [localghost.ai/manifesto](https://www.localghost.ai/manifesto)
- Hard Truths, [localghost.ai/hard-truths](https://www.localghost.ai/hard-truths)
- The website's repo, [github.com/LocalGhostDao/web](https://github.com/LocalGhostDao/web)
- Contact, info@localghost.ai ([PGP](https://www.localghost.ai/.well-known/pgp-key.asc))

---

Write the code.
