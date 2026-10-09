# Compiling, testing, and updating a box that's already set up

The architecture that makes deploys clean: ghost.secd is the ONLY process on the unencrypted system
disk. Everything else , the ghost.*d daemons, their data, logs, and binaries , lives on the encrypted
volume and is owned by ghost.watchd. secd owns exactly one child: watchd. watchd supervises the rest.

So there are two deploy paths, and neither uses pkill:

## Compile + test (safe any time, mounted or not)

    go build ./...
    go test ./...
    # the properties worth re-checking after a change near the lifecycle:
    go test ./internal/watchd/ -run 'Teardown|Critical'   # anti-wedge: cohort dead before unmount
    go test ./internal/hw/     -run 'ReWrap|SelectSealer|Software'
    go test ./internal/secd/   -run 'Unlock'

Build + test never touch the box.

## Deploy a DAEMON (ghost.*d) , box stays unlocked, no re-unlock

The daemon binaries live at <mount>/bin. watchd execs them from there. To deploy one:

    ./tools/release.sh --daemon ghost.synthd

That builds it, installs it to /var/lib/ghost/mnt/slot0/bin, and runs `ghost-ctl restart-daemon
ghost.synthd`, which asks watchd (over its control socket on the volume) to kill the old process and
start the new one from the same path. secd is untouched, the mount is untouched, the box stays
unlocked. This is the loop you'll run most often for daemon work.

    # inspect the cohort any time:
    ghost-ctl daemon-status

## Deploy ghost.secd , clean lock, then re-unlock

secd has ONE shutdown behaviour: SIGTERM does a full clean lock (stop watchd -> cohort torn down and
confirmed dead -> stop DBs -> unmount -> close LUKS). So a secd deploy re-locks the box:

    ./tools/release.sh --secd
    # then RE-UNLOCK from the app.

This is deliberate. A stopped front door must not leave the volume mounted with a running cohort
behind it. The cost is one re-unlock per secd deploy , the trade for secd being freely restartable
with nothing ever orphaned. In a controlled deploy you can script the re-unlock after the restart.

## Deploy everything

    ./tools/release.sh --all

Builds all, installs the daemons to the volume (if unlocked), and restarts secd ONLY if its binary
actually changed (a re-lock is disruptive, so it is not forced needlessly).

## The rule, in one line

Daemon change -> restart via watchd, box stays up. secd change -> clean re-lock, re-unlock after.

## Why this is better than the old model

Previously secd owned the daemons, so restarting secd (the thing you deploy most) orphaned them. Now
watchd owns them and secd owns only watchd, so: a daemon deploy never touches secd, and a secd deploy
cleanly brings the whole stack down and back , no orphans, no pkill, no wedged mount. The box
self-heals (watchd restarts a crashed daemon with backoff) and deploys are non-destructive.

## Logs

Everything on the volume logs to <mount>/logs (default /var/lib/ghost/mnt/slot0/logs):

    ghost.secd    -> journald (it is a systemd unit on the system disk)
    ghost.watchd  -> logs/watchd-YYYY-MM-DD.log
    ghost.<x>d    -> logs/<name>-YYYY-MM-DD.log

Each daemon (and watchd) writes through a self-rotating writer: it holds one file open all day and
opens a new dated file at the first write past midnight , no restart, so a daemon running for years
rolls a file per day on its own. At midnight watchd's janitor gzips each completed day into
logs/archive/<name>-YYYY-MM-DD.log.gz and keeps the last 7 days.

Lines are structured key=value, greppable. The service and date are in the filename, so the line
carries only the intraday clock and its fields:

    15:04:05.123456789 level=INFO fn=Produce msg="stored notification" id=4127

    # examples:
    grep 'level=ERROR' logs/ghost.synthd-*.log
    grep 'fn=Produce'  logs/ghost.cued-*.log
    zgrep 'level=ERROR' logs/archive/*.log.gz     # search the archived days too

## Services and the service console (ghost-cli)

Every ghost.*d daemon exposes a control socket at <mount>/run/<service>.sock and answers a BASE command
set; some add their own. Talk to them with ghost-cli (the redis-cli of the box), on the box, unlocked:

    ghost-cli <service> <command> [key=value ...]

    # works on every service:
    ghost-cli ghost.noted ping
    ghost-cli ghost.noted status
    ghost-cli ghost.noted log-level level=debug     # change log level LIVE, no restart
    ghost-cli ghost.noted reload                     # re-read <service>.conf; reports applied vs needs-restart
    ghost-cli ghost.noted commands                   # discover what this service accepts

    # ghost.oracled (the inference broker) adds:
    ghost-cli ghost.oracled models
    ghost-cli ghost.oracled infer capability=chat input="hello"

Per-service config lives at <mount>/conf/<service>.conf (JSON). Absent file = defaults. Base keys:
logLevel, logSoftCapMB, logHardCapMB, retentionDays. reload applies the hot keys and tells you which
need a restart.

## ghost.oracled , the inference broker

Model-agnostic front for anything that talks to a model. Callers ask for a CAPABILITY + CLASS
(local-small | frontier), never a concrete model; oracled queues (interactive beats background,
deadlines drop stale work), routes to a backend, returns the result. The local backend runs
llama.cpp's llama-server as a PRIVATE loopback child, weights loaded from <mount>/ai-models on the
encrypted volume , exposed to nothing, dies with the mount. Swapping gemma for a frontier model is a
conf change in ghost.oracled.conf, invisible to callers.

## Log disk-guard (watchd)

watchd measures <mount>/logs every 15 min. Under logSoftCapMB: normal. Over soft: it samples recent
logs and asks ghost.oracled whether a service is over-logging, then raises that service's log
threshold (debug->info) over its control socket , it never deletes recent logs on the model's say-so.
Over logHardCapMB: the dumb backstop , delete oldest ARCHIVES first; today/yesterday plain logs are
protected even then, and if clearing all archives still leaves it over cap, watchd shouts (a runaway
logger the operator must see) rather than eating recent logs. If oracled is slow or gone, the guard
falls back to the safe default: hold, protect recent logs, log loudly.

## ghost.synthd , the memory-surfacing daemon (retrieval side of the cueing loop)

synthd owns retrieval: given a context, it ranks memories from its index and returns candidates for
ghost.cued to gate. synthd decides WHAT is a candidate; cued decides WHEN and WHETHER.

HONEST STATE: the index (embeddings, vector store, corpus) is the next few months of work and does not
exist. synthd runs the real query pipeline over an EMPTY index, so `prime` returns nothing and cued
stays silent. Running-but-blind, one layer under cued. When the corpus is built behind the Index
interface, synthd returns candidates with no change to cued.

    ghost-cli ghost.synthd index-stats     # {ready:false, size:0} today
    ghost-cli ghost.synthd ready
    ghost-cli ghost.synthd prime summary="at the kitchen table, morning"   # [] today

cued talks to synthd over the SAME control socket (synth.SocketClient calls synthd's `prime`), so
there is one protocol for operator commands and inter-daemon calls alike. cued->synthd is not a bespoke
channel.

## Every service on ghost-cli

All services answer the base set (ping | status | reload | log-level | commands). Those with logic add
their own:

    ghost.secd       status | off pin=<mainPIN>    (on the unencrypted disk; works when LOCKED)
    ghost.watchd     cohort
    ghost.synthd     prime | ready | index-stats
    ghost.cued       nominate | queue
    ghost.oracled    infer | models

Each daemon has its own health port (9110-9118) and its own <service>.sock under <mount>/run
(ghost.secd's socket is under the state dir so it is reachable when locked).


## The `off` command (border-crossing "make appears-down true")

`ghost-cli ghost.secd off pin=<mainPIN>` locks the box NOW from the local socket, authorized by the
main PIN (not an app session). It is the same teardown as /v1/lock , stop cohort, stop DBs, unmount,
luksClose , reachable without opening the app.

Option A by design: off is a LOCK, never a wipe. It can only tear the box down to the cold state the
main PIN reverses; it cannot destroy data. So you can state it plainly: off cannot erase anything, and
therefore cannot be coerced into erasing anything. The wipe stays entirely separate under its own
armed-PIN flow.

The reply is opaque: right PIN, wrong PIN, wipe PIN, and already-locked all return the same "ok". The
only thing an observer learns is that the box is down , which is the point. off never touches the
rate-limit gate and never arms or confirms a wipe (checked side-effect-free), so it is not an unlock
oracle and cannot disturb a pending wipe.

## ghost.framed , the photo pipeline (real daemon)

Phone -> POST /v1/frames/upload (raw image bytes, session bearer) and POST /v1/locations (JSON
{"source":"watch","points":[{"ts":..,"lat":..,"lon":..}]}). secd stays thin: it streams bytes to
<mount>/frames/incoming* via .part-then-rename and never decodes anything , no image parser in the
root, network-facing process. A locked box rejects uploads (appears-down); the app queues and syncs
after unlock.

framed drains the intake one file at a time: sha256 hash (dedupe, idempotent), pure-Go EXIF (time +
GPS, no third-party lib), atomic MOVE of the untouched original to archive/YYYY/MM/DD/<hash>, derived
1600px preview + 320px thumb (pure-Go area-average downscale), record in Postgres (psql shell-out,
same pattern as the rest). Crash mid-photo loses work, never a photo.

Watch points + photo GPS become one GeoJSON per day at <mount>/frames/paths/YYYY-MM-DD.geojson ,
LineString track (Douglas-Peucker, ~11m tolerance) plus Point markers carrying frame hashes. The box
NEVER fetches map tiles or contacts a map service; requesting tiles ships your coordinate history to a
third party. The phone renders the GeoJSON over OpenStreetMap client-side.

    ghost-cli ghost.framed queue                    # intake backlog
    ghost-cli ghost.framed drain                    # force a pass now (after a bulk sync)
    ghost-cli ghost.framed rebuild-day day=2026-07-04

Not built yet, named honestly: journal TEXT (gemma captioning via ghost.oracled , the mmproj is
already loaded for exactly this) and feeding day summaries to cued/synthd as memories. The day's
frames, track, and GeoJSON are the raw material; the prose comes when the oracled hook is wired.

## Native Postgres + Redis clients (internal/pgwire, internal/redisc)

The box now talks to Postgres and Redis over native Go clients instead of shelling out to psql/redis-cli
in the daemon data path. Two reasons that matter: parameterized queries (pgwire extended protocol) mean
values travel out-of-band and SQL injection is structurally impossible , the sqlQuote/esc/sqlEscape
string-builders are being deleted as call sites move over , and there is no password on an argv anymore.

Role split, enforced three ways. Provisioning creates ghost_ro (SELECT only) and ghost_rw
(SELECT/INSERT/UPDATE/DELETE) in Postgres, and ghost_ro (+@read) / ghost_rw (+@read +@write) as Redis
ACL users. pg_hba switches from trust to scram-sha-256 so the password is actually verified. And the Go
clients are role-typed: redisc.ReadOnly / pgwire.ReadOnly expose no write method, so a read-only daemon
cannot even compile a write. Server GRANT + ACL is the wall; the type system makes the mistake
uncompilable; scram makes the password real.

SCRAM-SHA-256 is implemented in pgwire (pbkdf2 from golang.org/x/crypto, already a dependency , no new
module) and tested against the RFC 7677 published vector. No TLS (volume-local socket), no binary
format, no pooling , the box does not need them.

Ported so far: framed.Store (fully parameterized), NotifStore and MuteStore (MuteStore parameterized;
NotifStore helpers routed through the native clients, call-site parameterization is a follow-up). The
only remaining psql/redis-cli shell-outs are in datastore.go's DB bring-up, which runs as the owner
before pg_hba is hardened , correct to keep.

## ghost.searchd , the search layer (SPEC v1.1, steps 1-7)

New daemon on health port 9119, in the registry, supervised, seeded. One index over three tiers ,
originals, journal entries, memories , with two entry points: interactive search (FTS + pgvector legs
run concurrently on separate connections, fused with RRF k=60, grouped under parents, ranked with the
spec's adjustments) and ambient retrieval for ghost.cued (vector only, tiers 1+2). searchd answers
prime/ready wire-compatibly with ghost.synthd, so pointing cued here is a socket change; retiring
synthd is a decision for Vlad, not taken unilaterally.

Schema applies at unlock as the OWNER from hw/datastore, before pg_hba hardening (the owner
authenticates by trust only , order matters). pgvector and the embedding weights are both optional at
runtime: either missing degrades searchd to FTS-only, recorded in search.meta and reported in health.
The embeddings model runs as searchd's private loopback llama-server child (CPU by default), oracled's
pattern, dies with the daemon, which dies with the mount.

Honest deviations and additions, each commented at the code site:
- The spec's fts GENERATED column cannot compile as written , unaccent() is STABLE, not IMMUTABLE.
  search.immutable_unaccent wraps the dictionary form; declared IMMUTABLE, safe on this box.
- search.citations is an addition: deletion phase 1 must find citing T1/T2 rows, and the entry/memory
  tables live outside this spec and do not exist yet. Producers record citations at write time, so
  invariant I4 is honorable from day one.
- Captions are parked, loudly: oracle.Request is text-only today, so caption jobs fail with ErrNoVision
  and sit at attempts=5 in parked_jobs until oracled gains image input. Nothing pretends to see.
- Reconsolidation phase 2 without a consolidation daemon: zero-survivor rows are deleted; rows with
  survivors stay stale forever rather than being un-staled while still citing deleted material.
  Stale-forever is the safe failure.
- No LISTEN/NOTIFY (workers poll, conf interval) and no COPY (batched multi-row INSERT, the spec's own
  fallback) , the in-house pg client omits both deliberately.
- Anchor boost and cluster warmth run through interfaces with a Zero implementation until the
  consolidation daemon exists , the ranking pipeline is real, the T2 signals honestly contribute zero.
- framed thumbnails stay JPEG, not the spec's 256px WebP: no pure-Go WebP encoder, and framed already
  ships 320px JPEG thumbs. Named, not hidden.

framed now hands every archived photo to searchd over ctlsock, best-effort: the archive is the source
of truth, rebuild re-covers anything missed. pgwire gained QuerySimple for the two SIMPLE-INLINE
cases: leg B (SET LOCAL must share the SELECT's implicit transaction) and deletion phase 1 (one
multi-statement implicit transaction); every inlined value is a validated enum, an integer, or hex.

Eval harness and regression tests are skeletons that skip without a live corpus, with the scoring and
the invariant assertions already written , they become real the day the first corpus exists.

## oracled sees images; captions drain

oracle.Request gained Images (volume paths). Text requests keep the native /completion path byte-for-
byte unchanged; requests with images go through llama-server's /v1/chat/completions with base64 data
URIs, loopback only, the multimodal path the loaded mmproj serves. search's VisionOracle is now a real
captioner (background priority , a person's query always jumps a caption job), so parked caption jobs
drain on their next retry once this deploys.

cued's ambient provider is now a conf key (synthService, default ghost.synthd). ghost.searchd answers
prime/ready wire-compatibly over the real corpus; flipping is one conf edit, retiring synthd stays an
operator decision.

## searchd correctness pass

Self-review caught a real spec violation in the fresh code: leg B compared the query vector against
EVERY stored vector, including rows embedded by a different model. Spec 14.3's model-mismatch
invariant now holds structurally , LegB filters emb_model = the configured model id (charset-validated
before inlining), so mid-migration old-model rows simply sit out until the background re-embed
replaces them.

Two usability gaps closed with it: ingest-t1/ingest-t2 ctlsock commands now exist (summary body +
cited original ids), without which the interpretation tiers could never populate and ambient ready
would honestly stay false forever; and the search command takes CSV filters that survive ghost-cli's
scalar coercion (tiers=0 arrives as a JSON number , the handler decodes flexibly instead of erroring).

## seance and whisper , the DB libs, renamed, voiced, and audited

pgwire is now internal/seance (a seance being a structured protocol for questioning what's buried ,
you ask precisely, you get back exactly what was stored, you do not improvise the ritual) and redisc
is internal/whisper (quick, small, ephemeral , the things a ghost mutters rather than commits to the
record). Type names are unchanged; call sites changed only their qualifier. Both doc headers now carry
the covenant in writing: NOT an ORM, never an ORM , query in, rows out.

Injection audit, with evidence instead of vibes. Every value from outside the binary travels as an
extended-protocol parameter , NotifStore, MuteStore, framed, and the search store have zero SQL
string-building for values. The two SIMPLE-INLINE exceptions (leg B's SET LOCAL bundle, deletion
phase 1's multi-statement transaction) admit only validator-gated values, and guard_test.go now
proves the gates: validModelID rejects quotes/spaces/semicolons/unicode, Filters.validate rejects
non-enum sources and out-of-range tiers, VecText's output alphabet is checked character-by-character.
whisper's injection story is structural , RESP frames every argument as a length-prefixed bulk
string, so encodeRESP was extracted and whisper_test.go feeds it CRLF, smuggled commands, and whole
framed RESP as values, asserting they stay inert bytes.

One real hardening from the audit: provisioning inlined role names and passwords from services.conf
into owner-privilege SQL. The passwords are randHex by construction, but services.conf is data on
disk , so identifiers are now validated against [a-z_][a-z0-9_]* (refusal on tamper, loud not quiet)
and password literals go through quote-doubling. A hand-edited conf can now break provisioning, but
it can no longer BE provisioning.

## Final names: poltergres and apparedis

One more rename, this time to names that explain themselves: internal/poltergres (the resident
poltergeist's line to Postgres , unseen, permanent, moves things when asked correctly) and
internal/apparedis (the apparition layer over Redis , data that appears just long enough to be useful
and is gone on restart). Earlier entries in this log reference pgwire/redisc and briefly
seance/whisper; those names were true when written. Types and behaviour unchanged throughout , only
the qualifier at call sites moved.

## Deployment refresh for first hardware bring-up

server_setup_root.sh now installs pgvector (postgresql-<detected-ver>-pgvector, package-based check
since pgvector ships no binary; failure degrades search to FTS-only rather than blocking setup).
tools/README.md gained: the llama-server prerequisite (oracled and searchd both spawn it),
the split model-homes step (7a app catalog on the unencrypted disk vs 7b inference weights on the
ENCRYPTED volume , gemma + mmproj + embeddinggemma into <mount>/ai-models/ after first unlock), and
a step 8 first-unlock checklist that starts with the PTT cold power cycle and ends with the one-photo
test that exercises the whole spine , upload, EXIF, archive, ingest, queue, vision, chunking,
embedding , in a single tap.

## The sim is dead; the docs now know it

Confirmed against the tree: one build, no tags, both seal tiers compiled in, tier chosen at runtime
from GHOST_SEAL_MODE in seal.env, never a silent downgrade. Encryption is always software (LUKS); the
tier is key custody , PTT-sealed on real hardware, Argon2id-wrapped for TPM-less dev boxes. The
README's step 2 and both setup scripts' printed hints still said `make box TAGS=tpm` with the old
sim warning; corrected. Also removed internal/secd/simkey_test.go, a compile bomb pinning a
derivation the runtime-tier rework deleted (it called simDiskKey and debian.SimDiskKey, neither of
which exists) , it only survived because the targeted test list never included ./internal/secd/.
Coverage is better than what it pinned: hw/seal_software_test.go holds five software-tier tests
(roundtrip, wrong PIN, rekey, cross-slot rejection, destroy). The first-unlock checklist now starts
by reading GHOST_SEAL_MODE and stopping if real hardware says software.

## Multi-frame enrolment QR

A real device identity (P256 cert + key, PEM-wrapped, base64url'd into the enrol link) is ~1.4 KB ,
too much for one comfortably-scannable QR (it would need a dense ~v31 symbol). Instead of one big
code, the box now splits the link into several small frames the app scans in sequence , "Scan 1 of N"
, each fitting an easy ~v12 (65x65) QR.

Wire format, one line per frame: `LGQR1 <seq> <total> <sha8> <chunk>`. sha8 is the first 8 hex of
SHA-256 over the full reassembled link; the app uses it to know all frames belong to one enrolment and
to verify the join before parsing. internal/pair/qrchunk.go (ChunkLink/JoinFrames) and the app's
qr/FrameAssembler.kt are the two ends of this contract , tests on both sides build byte-identical
frames, so a format drift on one side fails the other. Order-independent and duplicate-safe: scan the
frames in any order, rescanning is idempotent, a frame from a different box resets the set. A small
link yields a single frame, so single-QR and multi-frame share one code path with no mode switch.

Two real bugs fixed alongside, both caught by the tests: the QR encoder never placed the v>=7
version-information modules (a latent break for any symbol >= v7), now added and the regions reserved;
and TestEnrollLinkCarriesVersion was self-contradictory (asserted CurrentVersion==1 AND v=2) , fixed
to ==2 to match the constant. The setup unit test wrongly required a literal /dev/tpmrm0 line; the
unit grants TPM access via PrivateDevices=no by design (a DeviceAllow whitelist would deny /dev/mapper
that cryptsetup needs), so the assertion now checks for that instead.

## Rotating enrolment QR , automation without a feedback channel

The box cannot know a frame was scanned: pre-enrolment the phone has no client cert (the cert is IN
the QR), so nginx's mTLS edge rejects it, and an unauthenticated "advance" endpoint would puncture
appears-down. So no feedback , by design. But none is needed: FrameAssembler was built order-
independent and duplicate-safe, so pair.Run now ANIMATES on an interactive tty , each frame shows
for ~2.2s, the screen clears, the next appears, looping until the operator presses Enter. The person
just holds the phone up; the app catches frames across a loop or two and its "scanned N of M" counter
says when it is done. Same trick air-gapped hardware wallets use for large payloads. Pure display:
zero network, zero new surface. Non-tty output (pipes, logs) keeps the static sequential rendering.
Both ghost-setup and ghost-qr detect the tty via x/term (already a dependency).

## Ten easy frames instead of seven dense ones

Field observation: of the seven frames a real identity link split into at v10, the LAST one , the
short remainder, a v7-8 symbol , scanned first try every time, while the six v10 frames each
sometimes needed another lap of the rotation. So the rotating view now caps at v8
(pair.maxAnimatedVersion): a 1.25KB link becomes ten frames of 49 modules a side, every one the
size of the frame that always worked, one lap of ~32s and no waiting for the next. Nothing on the
app side changes; the assembler never cared how many frames there are. framebudget_test.go pins
the cap and the ten-frame split.

## The archive's own stock-take (pipeline versioning)

frames.pipe_ver records which framed pipeline last derived a row (framed.PipelineVersion, now 2:
truncation-tolerant EXIF, video moov metadata, previews for clips). Every InsertFrame stamps it and
the ON CONFLICT clause converges the row column by column (a better taken_at source wins, a
missing preview is filled, never the reverse). At start, after the resume drain, framed waits for
searchd to answer a ping and runs Converge: one Audit query over frames (kind, paths, pipe_ver,
description set, title set, tags exist), then re-derives rows behind the version or without
previews, and sends an ENSURE ingest for rows missing a description, title or tags. searchd
answers an ensure by doing exactly the missing stage , re-applying a caption it already has,
copying a burst representative's, requeueing a parked caption job against the frame's render (a
video is captioned from its frame grab), or ingesting a frame it never saw. Every step is
idempotent: the description writes only where empty, chunks only when the original has none, the
tag pass only where a title or tag is missing, the tags chunk once.

A healthy box logs one line: "N frames (P photos, V videos), all at the latest stage (pipeline
v2)". Anything else is itemised (behind, no preview, undescribed, untitled, untagged, unrenderable
videos) and repaired, with progress every 200 repairs. `ghost-cli ghost.framed stages` is the
read-only version; `ghost-cli ghost.framed converge` runs a pass now. Bumping PipelineVersion IS
the migration: the next start re-derives every row below it.

## The phone's own location trail

POST /v1/locations always accepted watch points; nothing on the phone ever sent any. Now the app
keeps a trail itself (sync/LocationLog.kt): a WorkManager run every quarter hour takes one fix
through the framework LocationManager (fused, else network, else GPS , no Play Services), keeps it
when the phone moved 25m or an hour passed, and appends "ts lat lon" to a capped spool in the
app's private files. The spool is flushed to the box as {"source":"phone","points":[...]} whenever
there is an enrolled box with a live session (each worker run, and at unlock) , so a trail begun
before any box exists catches up the day one is enrolled, and a phone that never enrols keeps a
trail that never leaves it. The fix is also geocoded on the phone (OS Geocoder, only when it moved
20km or half a day passed) and that country outranks the mobile network for the lock-screen
phrase, so hotel Wi-Fi with a foreign SIM no longer says the wrong language.

All of it is asked for on the new welcome screen, before any QR is scanned: notifications,
location, background location, photos and videos, camera , one chain of system dialogs with the
reason next to each line, then two switches (the lock-screen phrase, the trail) that default on.
The welcome shows once per install, upgraded installs included, and the phrases and the trail run
from that moment with or without a box.

## The archive's progress, on the phone (/v1/pipeline)

Box Status now opens with the archive pipeline panel: every stage as a bar , at the latest stage,
derived (v2), previewed, described, titled, tagged , with done / total / percent / left, the pace
(descriptions landed in the last hour and today), the time the rest will take at that pace, the
searchd queue (captions, tags, embeds; parked counts), and framed's own stock-take line ("checking
now · 340 of 2,100 rows handled" while it runs; "last check 4 min ago: N frames, all at the latest
stage" after). Polled every 5s while the screen is open; nothing is estimated on the phone.

Server side: frames.described_at (stamped by ApplyCaption, indexed where > 0) is the rate's only
source , no counter in memory; daemon_state (daemon, key, value, updated_at) is where a daemon
publishes work in flight, single writer per daemon, and framed writes its ConvergeState there at
the start of a pass, every 200 repairs and at the end. hw.PipelineProgressFrom reads frames,
search.jobs and daemon_state in three flat SELECTs; secd serves it at GET /v1/pipeline (session).
internal/pgtest runs all of it , the schema registry incl. the upgrade path (drop the new columns
and table, converge again), InsertFrame convergence, Audit, every ensure-path query, ApplyCaption,
the progress feed, location points , against a REAL Postgres when GHOST_PG_SOCKET_DIR is set;
skipped otherwise. First blind SQL on this project that was proven before it shipped.

## Enrolment QR: any K of K+M frames (LGQR2), a smaller link (v3), a hardened decoder

The stock-take of the scanner (the phone's from-scratch QrSampler/QrMatrixDecode/ReedSolomon
against ZXing, ZXing-cpp, BoofCV, quirc and the BC-UR animated-QR standard hardware wallets use)
found the shape of the user's wait: LGQR1 needed EVERY one of N specific frames, so one missed
frame cost a whole lap of the rotation. Fixed at the root:

- internal/pair/qrstream.go + app qr/StreamAssembler.kt: the link is split into K data blocks
  and M = K/2 parity blocks from a Cauchy matrix over GF(256) (an MDS code, the QR symbol's own
  Reed-Solomon one level up). ANY K distinct frames rebuild the link; a miss costs one more frame,
  never a lap. Frame: `LGQR2 idx K M len crc32 pcrc body`; the per-frame 16-bit CRC drops a
  garbled read instead of letting it poison the set, the payload CRC-32 verifies the join. The
  Go test writes testdata/lgqr2_fixture.txt and the app's StreamAssemblerTest decodes it (200
  random K-subsets, parity-only, duplicates, corruption, a foreign frame), so both ends are
  proven against the same bytes. The old LGQR1 path stays in the app for older boxes.
- Link v3: certder/keyder carry base64url(DER) instead of base64url(PEM(DER)): 858 bytes for a
  real identity instead of 1243. With v8 frames that is 8 data + 4 parity frames; the phone is
  done after 8 catches, typically 16-20s at the new 2s hold (was 3.2s: the long hold was
  insurance against missing a frame, which no longer costs anything). The app wraps the DER back
  into PEM for its keystore; v2 links still parse; a v4 link tells the app to update.
- Terminal rendering: on a terminal tall enough (57+ rows for v8) the frames are drawn with one
  full background-coloured cell pair per module (RenderTerminalCells) instead of half-block
  glyphs , the likeliest cause of "dense frames only scan from far away" is the hairline most
  fonts leave through every second module row; background colour paints the whole cell. Smaller
  terminals keep the half-block form at v8.
- Decoder hardening (qr/ReedSolomon.kt, QrMatrixDecode.kt): the errors-only path now has the
  capacity check and the syndrome re-check the erasure path always had; erasures are capped at
  nsym-4 (ERASE_MARGIN) because at e = nsym every input "decodes" , pure interpolation, no check
  left, and that fabricated block used to reach the frame assembler. QrSampler's binariser probes
  negative biases too (-4, -8): a bright monitor blooms light into dark modules and only a
  threshold that GROWS dark regions recovers them. Auto-zoom 2x in the scanner after a sustained
  no-decode streak on a code under ~5 px/module (720p analysis frames are pixel-starved on a v8
  symbol at arm's length); back to 1x when the code grows past 9.5 px/module.
- Known and left: QrSamplerTest.recoversRotated10Degrees fails on the untouched tree too (a v2
  symbol at 10 degrees is sampled as 37 modules , the timing-line version estimate), and the
  sampler still uses one alignment pattern of the six a v8 symbol has and no temporal fusion
  across attempts. Both are the next levers if ten easy frames still stall; the review's full
  ranked list is in the project notes.

## The lock-screen card: answered from a snapshot, promoted to a live update

The pause between SAY and NEXT on the lock screen was a cold process: a tap woke the app (killed
hours earlier), which then parsed eighteen packs, resolved the country and rebuilt the slot's
list before the card could change. PhraseSurface now keeps a Snapshot in prefs at every refresh ,
the slot's ordered phrases flattened to strings, the headline, the voice tag , and NEXT redraws
the card and widget from that in a few milliseconds, then re-syncs from the packs in the
background. SAY speaks from the same snapshot; the voice engine's own warm-up on a cold process
(~half a second) is the part that remains, and goAsync keeps the process alive six seconds after
a SAY so the NEXT that follows is warm.

"More than a notification": on Android 16+ the same card asks to be a LIVE UPDATE
(NotificationCompat setRequestPromotedOngoing + setShortCriticalText, POST_PROMOTED_NOTIFICATIONS
in the manifest): the top of the lock screen, a status-bar chip with the phrase, the always-on
display, and Samsung's Now Bar on One UI 8. The OS grants it per app; PHRASES shows the switch
and links to the settings page when it is not yet allowed (canPostPromotedNotifications). The
widget was already lock-screen capable (Android 16 QPR2+ needs no opt-in; One UI 8 lists
third-party widgets under Settings › Lock screen › Widgets); its SAY/NEXT are broadcasts, so they
work without unlocking, and a RemoteViews update lands faster than a notification re-render.

## Halt timings, and why the cohort took 31s

The redeploy's graceful halt reported "cohort down after 31s, still stopping" without naming a
daemon. Two causes, both fixed:

- ghost.oracled stopped the BROKER before the MODEL: the broker's Stop waits for its worker, the
  worker was mid-caption (captions run all day now), and llama does not answer until the caption
  is done , longer than watchd's 5s grace, every halt, so oracled was SIGKILLed each time (llama
  dies with it through Pdeathsig, so nothing leaked, but 5s were paid). Now llama is killed
  first (2s grace, down from 10), the in-flight request fails at once, and the broker stop is
  bounded at 2s.
- watchd tore the cohort down IN SERIES, each daemon with its own 5s grace: three slow exits were
  15s before anything else stopped, and the script's 30s patience expired on a cohort that was
  merely queueing. TeardownAll now signals every daemon at once and waits concurrently; the
  cohort is down in max(exit times), not the sum. Nothing depended on the order: the daemons
  talk only to each other and to the datastores, which secd still stops after this returns.

Timings, as asked: watchd logs "service stopped svc=… ms=… killed=…" per daemon and one "cohort
down ms=… services=ghost.oracled 0.3s, ghost.framed 0.1s, …" line; redeploy.sh names the
survivors every 5s while it waits and ends with "cohort down after Ns , slowest: ghost.x=6s …",
with a 45s budget instead of 30.

## The trail: a quarter hour, kept on the phone, synced without duplicates

Operator ruling: a point every fifteen minutes is the trail; no minute-by-minute service (it was
built and removed the same evening: a foreground service with its own notification and battery
contract, for a history the quarter-hour worker already gives). What stays is the spool and the
sync, and the sync is duplicate-free by construction, not by comparison:

- the phone keeps one point per 25 m or per hour, timestamps STRICTLY increasing (a cached fix
  older than the last point is not news and is dropped), in an append-only file;
- a point leaves the spool only when the box has answered 202 for the batch it was in, and the
  acknowledgement removes exactly those timestamps , never a range, never a position , so a
  point recorded while a batch was in flight is untouched;
- a lost reply or a 503 leaves the batch in place and the next flush re-sends it, oldest first,
  4000 points a batch; the box keys location_points on (ts, source) with ON CONFLICT DO NOTHING,
  so a re-sent batch is absorbed and the trail never holds a point twice;
- the source is per phone ("phone-" + 8 hex of the stable id), so two phones in one archive
  cannot collide on a second, and a phone's re-send lands only on its own rows.

Proven on the JVM against stubs (25 m rule, monotonic timestamps, exact ack, 9000 points in
batches with a failure in the middle re-sent once, no-session no-op) and, for the box side, in
internal/pgtest (InsertPoints twice = the same rows). Settings › LOCATION TRAIL shows points
today and points waiting for the box.

## Tags with categories, and the photo digest a prompt actually wants

frame_tags.category (people, place, object, activity, food, animal, vehicle, nature, event, text,
style; '' = not yet) turns a flat word list into a summary. The tag pass now asks the model for
category:tag pairs (search.TagPrompt); a bare tag is placed by search.Lexicon, a few hundred of
the tags an archive actually produces (last word of a compound decides, plurals fold), no model
needed; whatever is left goes to a categorize job (search.CategorizePrompt, one small call per
frame, background). The stock-take has a new stage, CATEGORISED (Audit, Converge, /v1/pipeline
"categorised" + a "categorize" queue), and after each pass framed asks searchd for
`categorize`, which queues the backfill in bulk , thousands of frames are one command, not
thousands of notifies. /v1/frames rows carry tagsByCategory next to tags.

synthd injects the matched photo SET as one line, after memories and before the caption
snippets: "8 photos match (2026-07-04 to 2026-07-06): people: child, two adults · place: beach,
harbour · food: pastel de nata · activity: sailing" , searchd `search` limited to images, then
`digest` over the frame hashes (Store.TagDigest groups by category, most frequent first). Six
snippets tell the model about six photos; the digest tells it what the whole set is made of.

## The phone searches the web; the box never does

New in /v1/chat (secd forwards, synthd formats): `web`, an array of results the PHONE fetched for
this question , title, url, snippet, an excerpt of the page cut to the paragraph that matches
the question, and when. synthd bounds it (6 hits, 1500 chars of excerpt each), adds each as a
"web" context item so the app's transparency panel shows exactly what the model saw, and puts a
labelled block after the archive context: "Web results the user's phone fetched … this box has
no internet … attribute by site name". The box still opens no socket to the outside.

App: net/WebSearch.kt , DuckDuckGo's HTML endpoint (built for script-less browsers; no key, no
library), the result anchors and snippets by regex, the top three pages fetched in parallel and
cut to the best-matching window; five results, bounded seconds, nothing rather than late. A
"web" line under the composer cycles off / auto / on (AppSettings.webMode, off by default): auto
searches only when the question mentions time, money, news, places or comparisons
(WebSearch.looksFresh); the chat shows "searching the web on this phone…" while it runs. The
result-page markup (result__a, result__snippet, the uddg= redirect) is what that endpoint has
served for years but could not be fetched from the build sandbox; if it changes, the parser finds
nothing and the question goes to the box without web context, never with garbage.

## The web search grows up: a plan, tools, a readability pass, numbered sources

net/WebSearch.kt is now a small pipeline rather than one request. PLAN: the question with the
chat filler cut off both ends ("hey, can you please tell me…", "…for me please") is the first
search; its bare keywords are a second, run only when the first came back thin; and when the
question is about now and names no year, the same again with the year, always. Results merge by
URL and the pages several queries agree on move up. TOOLS: a weather question goes to Open-Meteo
(geocoded by the place named, or the trail's last fix within six hours when it names none; "for
tomorrow" is a day, not a place), a currency question to the ECB's rates via Frankfurter
("100 euros in pounds", "gbp to ron", symbols too), a short "who is / what is X" to Wikipedia's
summary API; all keyless, one GET each, and each comes back as a hit with a kind of its own
(weather, rate, summary) so the box sees a dated figure, not somebody's prose. SEARCH: DuckDuckGo
HTML, then the lite endpoint when HTML answers with nothing or its bot check, both parsed by the
anchor's class whatever the attribute order. READ: the top three pages get a readability pass ,
the article/main element when there is one, comments and script/style/nav/header/footer/aside/
form/figure removed, paragraphs split at block ends, anything shorter than forty characters or
more link than text dropped (menus, "related" lists), the page's own title, description and
publish date kept (meta, JSON-LD datePublished, the first <time>) , and the excerpt is the
description plus the window around the paragraph with the most question terms. A Wikipedia
result is read through the summary API instead of scraped. Ten-minute cache per question.

The chat now shows the findings numbered under the reply ("5 from the web · searched on this
phone"), each row the title, site and date, a tap opening the page; synthd's block is numbered
the same way and asks the model to cite by number and site, to prefer a dated figure to an
undated page, and to say when the findings do not settle the question; the block carries the
fetched time once, so "today" in a page means the right day. synthd bounds eight hits now and
validates the kind. Verified on fixtures: both DuckDuckGo markups, the bot check, the readability
extract on a page with nav, sidebar, comment and link-menu traps, the tool selection, and the
three JSON formatters (Open-Meteo, Frankfurter, Wikipedia) on sample documents in the shape
those APIs return , none of the four hosts is reachable from the build sandbox, so the live
markup and JSON remain unverified until the APK runs on a phone; each path returns nothing on a
surprise, never garbage.

## The lock-screen card pulled open, GOT IT, and a Greek pack with levels

Pull the card down and it now shows the pronunciation and meaning, the aside, then "next" and
"then" (the two cards after this one, so a glance teaches three), and "36 of 178 known · level 2,
getting by · 4/41 morning". A third action, GOT IT, marks the phrase known: it leaves the walk at
once (the next card slides in from the snapshot, no pause), counts in the progress line, and comes
back only as a review. The cursor is pinned to the card just shown before the background re-sync
from the packs, and a redraw of the same card is skipped, so a tap never shows one card and then
another. Same on the widget: the header carries known/total.

Phrases carry a level (1 survival, 2 getting by, 3 conversation, 4 sounding local; packs without
the field are level 1) and the walk is gated by the person's band: level 1 until 60% of it is
known, then level 2 joins, and so on; a slot with fewer than five cards left borrows the next
level so the walk never runs dry. After every four unlearned cards one known card comes round
(each at most once a walk; which one shifts with the day), and when everything is known the walk
is the known cards, heaviest first. The greeting always leads. Known = drill score ≥ 3, now keyed
per language (drill.<lang>.<id>; the old drill.<id> keys are read as a fallback), set by GOT IT,
the ✓ on any row (slot list, phrasebook), three GOT ITs in the drill, or the LEVELS section's
"I know these" per level, which is how someone who already has the basics skips forty taps.

Greek is the first pack with the levels filled in: 181 phrases (39 survival as before, 79
getting by, 45 conversation, 18 sounding local) across five new chapters , numbers & time, out
and about, small talk, what you think, sounding local , with the coffee orders (freddo métrio,
ellinikó skéto), the kilo of house wine, sunbeds, the ferry, "siga siga", "éla", "ti léei", "mia
chará", "kalí synécheia", "filótimo", and speaker forms where the adjective changes
(kourasménos/-i, allergikós/-í, sígouros/-i). Written by me, not by a native speaker: the
accents and the stressed syllables were checked word by word, the cultural notes are the ones a
regular gets told, and any one that makes a taverna laugh is one edit in el.json. Verified with
the app's own parser and engine over all eighteen packs (the harness: band, the due walk, the
review sprinkle, progress) and with the surface itself compiled against Android stubs: draw,
the expanded text, NEXT, GOT IT on a card and on the greeting, the band climbing after
"I know these", lock screen off, SAY. JUnit: PhraseEngineTest, WebSearchTest.

## The clock on every line, and the 44-second llama-server

redeploy.sh now stamps EVERY line it prints with HH:MM:SS (bash's own printf %T through one
filter on stdout+stderr, the full date once at the top, the PIN prompt written to the terminal
directly so buffering cannot hold it): its own messages, make's output, systemctl's status, the
halt watch. The halt watch itself says more: each daemon is named the second it goes ("gone after
2s: ghost.searchd"), and every five seconds a survivor is printed with pid, parent, state, kernel
wait channel and age , the line that tells a process ignoring SIGTERM (state S, parent 1) from a
corpse the kernel is still clearing (state D or Z, wchan in exit_mmap or the GPU driver). health.sh
prints "health as of <date time>" at the top and the time on its verdict line.

The paste that prompted this: every ghost.*d gone inside a second, llama-server alone alive for
44s, then "still stopping after 45s" from a poll race. Two things can produce that and the new
output separates them; both are handled either way. (1) oracled's Stop now logs each step with
its time (SIGTERM sent; exited on SIGTERM after Nms; no exit, SIGKILL after 1s; reaped after Nms;
or SIGKILLed but not reaped in 2s "the kernel is still tearing it down, leaving it to init") and
never sits in Wait on a corpse long enough for watchd's 5s grace to kill oracled too. (2) secd's
unmount waits for a busy mount instead of failing on the first "target is busy": up to 75s, retry
every half second, and every five seconds it logs who holds the mount , a /proc scan of exe,
cwd, root, open descriptors AND memory mappings (a dying llama-server holds the volume through
its mmap of the model file, which a descriptor scan misses), as "comm[pid] state". The old
single-shot umount errored, the LUKS mapping stayed open with the key resident, and the next
unlock met the mounted-but-dead state it repairs; now the halt finishes what it started and the
log says what it waited for. Test: internal/hw TestHoldersOf, this process seen by descriptor,
then by mapping alone, then not at all.

## Twelve codes, any eight; one second each

The enrolment rotation is now a fixed set: the identity link is cut into exactly 8 data frames
plus 4 parity frames (pair.streamDataFrames/streamParityFrames), any 8 of which rebuild it , the
erasure code from the LGQR2 work, with the count pinned instead of derived, so the sentence on
the screen never changes and four misses a lap are free. Blocks are sized to the link (~108
bytes for today's DER identity), which only makes the symbols lighter than the v8 budget; a link
too long for eight budget-sized blocks (none today) grows the count with parity still at half.
Each frame now stays one second instead of two (pair.defaultHold; `ghost-qr --hold-ms` for a
monitor that needs longer): the phone samples every 100ms while assembling, a miss costs a frame
not a lap, so the second second was insurance the parity already gives. A lap is twelve seconds
and eight distinct frames is the floor, so a clean first connect is eight to twelve seconds
where it was sixteen to twenty-four. The caption prints the hold and the lap. On the phone, a
code in view is sampled every 150ms (was 250) so the FIRST frame , the one that starts the
assembly burst , gets ~6 attempts inside its second; assembly stays at 100ms. animateFrames
takes its stop channel from the caller (Enter on the terminal) instead of reading stdin itself,
which is what let it be tested: TestFrameSetIsTwelveAnyEight (four sizes, rebuild from frames
1-4 plus parity, the overlong fallback, the default hold) and TestAnimateFramesCaptionAndHold.

## The trail on the map: days, a clock along the line, and today before any sync

The trail was already reaching the map as the dim day lines (phone spool → /v1/locations →
framed's day GeoJSON → /v1/geo/tracks), but as an anonymous thread: no day, no time, nothing of
today until a sync. Now framed writes a `times` array parallel to the simplified LineString (the
second each kept vertex was recorded) and `distanceM` over the RAW points (haversine, hops under
15m not counted so a café afternoon does not walk a kilometre), and /v1/geo/tracks passes both
through (times only when they match the line; old day files simply have none). The app asks for
sixty days in the one round trip.

On the phone, LocationLog keeps a 48-hour ring of its own points that the box's ack never empties
(location-recent.log), so the map draws today from the phone alone , before a sync, and with no
box , and the part of a day the box has not seen yet is the dashed green continuation with a dot
per quarter-hour fix. The map gains a TRAIL line under the canvas ("today 3.2 km · last fix 12
min ago · 41 days"); open, a strip of days with their distance (a dot after the label means part
of it is still only on the phone), a tap lights the day in green, frames it, and shows a scrubber
that walks the line with the clock (HH:MM and the coordinates at that point, drawn on the map
as a ring with the time). The last fix is a ringed dot wherever the camera is. Settings ›
LOCATION TRAIL links straight to the map. Verified: BuildDayPath times/distance and
TrackDistanceM (jitter floor, a degree at the equator) in framed's tests; the tracks handler
passing times and distance, dropping mismatched times, and serving old files without them, in
secd's; the map itself is Compose and was desk-checked.

## Phrases, hidden until a trip asks for them

ghost.phrased is OFF on a fresh install and on upgrade: no drawer entry, no lock-screen card, no
alarm; the welcome screen's phrase switch is gone (a line says what will happen instead). Home is
the SIM's country, settled once at the welcome screen (Settings › PHRASES shows it, with "home is
here" when the phone thinks it is somewhere else). When the phone lands in a country that is not
home and has a pack, it asks ONCE for that country: a silent notification ("Γεια σας , you're in
Greece … TURN ON / NO THANKS", tap opens PHRASES) and the same question as a line at the top of
the app until one of them is answered. Yes turns the feature on (lock-screen card on, PHRASES in
the drawer); no is remembered for that country and never asked again for it. Landing is noticed
wherever the app already learns the country: the trail's geocoded fix in the background worker
(the offer arrives on the day you land, app closed), the boot and time-zone broadcasts, and the
app opening. Settings › PHRASES is the manual way in and out.

The learning tools are folded: the levels, the ✓ marks on rows and the drill sit under a
collapsed LEARNING section (one line of progress shows; tap for the rest), the hero card no longer
wears its level, and its "got it" appears only with the fold open. GOT IT on the lock screen
stays , that is the natural gesture. Verified with the phrases package compiled against the
Android stubs: home from the SIM, never offered at home or for a country without a pack, offered
once per country, decline remembered, accept turns everything on and draws the card, settings-off
re-arms offers for a new country.

Also in this drop: PhraseSurface.promotedSettingsIntent uses the action string
"android.settings.MANAGE_APP_PROMOTED_NOTIFICATIONS" (the Settings constant did not resolve
against the compile SDK) and falls back to the app's notification page when the phone has no
such screen.

## The 44-second llama-server was a 60-day-old orphan, and "is the GPU running" gets an answer

The stamped redeploy told the story the old one could not: `pid 2306644 · parent 1 · state R ·
up 5166560s`. The llama-server that outlived every halt was not a corpse being torn down; it was
ALIVE, parented to init, running for sixty days , a child of an oracled from before Pdeathsig
existed, which no code path has killed since, because after watchd confirms the cohort down
nothing looked for what was still running from the volume's bin. It ignored the halt, held the
volume open (the unmount), held the service's cgroup open (the three-minute `systemctl restart`:
systemd's stop timeout, then SIGKILL), and very probably held the port and the VRAM the next
llama-server needed , which is how a CUDA box ends up captioning on the CPU.

Three fixes, one for each place it hid. internal/procs (new): HoldersOf(mnt) (moved from hw) and
KillStrays(prefix, grace) , every process whose exe starts with the prefix gets SIGTERM, then
SIGKILL after the grace, each named in the log with its age; zombies count as gone. The lock path
calls it with `<mount>/bin/` right after watchd confirms the cohort down: nothing legitimate runs
from there at that moment, so what does is an orphan and it ends. oracled calls it with its own
llama-server path before spawning: a predecessor's child holding the port and the GPU dies before
ours starts, and the log says so. redeploy.sh, ten seconds into the halt watch, kills any
survivor with parent 1 and state R or S (an orphan ignoring the halt, not a teardown), so the
restart is never systemd's timeout again. Test: procs.KillStrays on a copied /bin/sleep under a
temp bin, the bystander untouched.

And the GPU question, answered from inside. oracled now tees llama-server's stdout/stderr through
an EngineWatch that keeps what the startup lines say , `ggml_cuda_init: found 1 CUDA devices`,
the device name, `offloaded 49/49 layers to GPU`, the CUDA and CPU buffer sizes, and the warnings
(`no usable GPU found`, cudaMalloc, out of memory) , and logs one verdict line once the model is
ready: "llama-server on the GPU: NVIDIA GeForce RTX 4070 · 49/49 layers · 8145 MiB VRAM", or a
WARN naming why not (CPU: no usable GPU; CUDA device found but no layers offloaded; no CUDA
lines at all = a llama-server built without CUDA). Every answer's `timings` (llama's own tokens
and milliseconds, on chat completions and on the last stream chunk; estimated from the deltas
and the clock when absent, and marked so) feed EngineStats: last and tokens-weighted average
tok/s over the last twenty, prompt tok/s, count. `ghost-cli ghost.oracled models` returns all of
it plus a speed verdict (under 6 tok/s is CPU speed; under 6 with the GPU claimed is contention
or throttling; 15+ is GPU speed); secd's daemon drill-in for ghost.oracled shows it on the Box
Status screen (model · runs · speed · generation · answers since start · what llama-server
said); health.sh prints the verdict and speed under oracled.

tools/gpu.sh is the debugging pass, five angles that must agree, stamped: nvidia-smi (card,
memory, WHICH processes hold it, orphans flagged), the llama-server processes (count, age, parent,
port, -ngl; more than one is the bug), the binary on the volume (linked against CUDA or not),
oracled's own account and its log's GPU lines, and a timed answer with the GPU's utilization
sampled while it runs , 0% throughout means the CPU did it whatever anything else says. Exit 0
when every angle says GPU. Tested with a fake ghost-cli in the sandbox (no card here); the
parser tests feed real llama.cpp startup lines through the watcher in pipe-sized pieces.

## The orphan survives SIGKILL: it is inside the kernel, and only a reboot ends it

Same pid, 2306644, every three seconds, SIGTERM then SIGKILL, and it kept running , state R,
wchan "-", now sixty days old. Nothing restarts it and there is only one: SIGKILL cannot be
ignored, it can only be outrun by a process that never comes back from the kernel to receive
it, and a llama-server spinning inside the GPU driver (a hung CUDA context after a fault, dmesg
shows an Xid) is exactly that. That is also why systemd's restart took three minutes and then
started the new secd beside it: systemd's final SIGKILL fares no better, it gives up. The card
that process holds stays held.

So the code now tells "unkillable" apart from "orphan". procs.KillStrays waits two seconds after
SIGKILL and, for anything still running, logs an ERROR with the state, the pending signals
(ShdPnd/SigPnd from /proc/<pid>/status , SIGKILL pending on a running process is the
signature) and the top of its kernel stack, and marks it "(unkillable)". hw.Unmount stops
waiting the moment a holder of the mount is unkillable (procs.UnkillableHolder) and names it:
"the volume cannot be fully locked until the box reboots" , the lock is partial, the log says
so, and nobody waits 75 seconds for a reboot that has not happened. redeploy.sh kills an orphan
once; a survivor of SIGKILL is reported once with the pending signals, the kernel stack and the
last Xid lines, the loop goes quiet, and the closing lines say what the restart will do (wait
out systemd's timeout, start beside it) and what to do (`sudo reboot`, unlock from the app).
gpu.sh checks each llama-server for a pending SIGKILL and says "sudo reboot" rather than "kill
-9", bounds nvidia-smi with a 15s timeout (a hung nvidia-smi is the same wedged driver) and
prints the last Xid lines. Test: pendingSignals on this process (none) and on a stopped child
with SIGTERM sent (TERM pending); Unkillable false for a live process and for no process.

## The widget gets a look of its own; and "sudo: ./tools/gpu.sh: command not found"

The widget now has settings: opacity of the void behind the words (0-100%, a lock screen shows
its photo through it), text size (small/normal/large), which lines show (the header, how to say
it, what it means, the buttons , the phrase itself always shows, and the widget closes up
around what is left so a two-line one fits the lock screen's smallest slot), and the tint
(phosphor, white, amber, ice). One look for every widget placed. It is edited from the widget
itself , the launcher opens PhraseWidgetConfigActivity at placement (configuration_optional lets
it skip that) and again from long-press › settings (reconfigurable) , and from PHRASES › widget
› "look", the same editor (ui/WidgetLook.kt): a preview of the current phrase drawn the way the
widget will, over a wallpaper-ish gradient so opacity means something, and every change saved
and pushed to the placed widgets at once. In the RemoteViews the background became an ImageView
under the words (an ImageView's alpha is settable through RemoteViews, a background's is not),
sizes go through setTextViewTextSize, lines through setViewVisibility(GONE), colours through
setTextColor; the widget also gains [ got it ]. Verified with the phrases harness: defaults,
opacity → alpha (85% = 216), sizes, lines, tint colours, the clamp, and the no-card case.

"sudo: ./tools/gpu.sh: command not found" with the file plainly there: the drop passed through
a Windows machine and arrived with CRLF line endings, so the shebang read "bash\r" and env found
no such interpreter (reproduced here: the same message from a two-line CRLF script; with LF and
+x it runs). redeploy.sh now strips CR from every tools/*.sh and makes them executable at the
start, so the one script that always runs repairs the others. By hand, once:
`sed -i 's/\r$//' tools/*.sh && chmod +x tools/*.sh`.

## The card fell off the bus; what root can and cannot do about pid 2306644

gpu.sh on the box said it in two lines: `Xid 79 (PCI:0000:2e:00) GPU has fallen off the bus`
and `nvidia-smi: No devices were found`. The card is not on the PCIe bus. Pid 2306644 (the
59-day-old llama-server, launched by the old oracled with -ngl 0) is spinning inside the nvidia
module reading 0xffffffff from a device that is not there, with SIGKILL pending, and it will
spin until the kernel it lives in goes away. Root cannot end it: a signal needs the task to leave
the kernel (it never does), ptrace needs it to stop (it cannot), a module cannot be unloaded
while a CPU is executing its code, nvidia-smi has no device to reset. Its /proc/pid/stack is
empty because it is ON a CPU right now , a running task cannot be unwound from /proc; only an
NMI backtrace (sysrq l) shows where. The "NVRM: … the NVIDIA kernel module is unloaded" lines the
redeploy quoted are the tail of the standard Xid message ("run nvidia-bug-report.sh … before the
module is unloaded"), not a statement that it was; the tail -2 cut the Xid line off.

The Xid was logged at uptime 4274592s, about ten days ago: since then every llama-server has
started on a box with no GPU and run on the CPU, which is the slowness of the last drops.

tools/unwedge.sh (new, root) is the whole picture on one screen: every process with SIGKILL
pending that is still running (a /proc scan of ShdPnd/SigPnd bit 9), the card (the nvidia-bound
PCI address, its config space , 0xffff means off the bus , link state, modules, nvidia-smi under
a 15s timeout), the kernel log (Xid codes with a plain-words table, the first fault turned into
a wall-clock time and an age, AER/PCIe errors, lockups), and inside the stuck process: what
/dev/nvidia* it holds, per thread the state, the CPU it is on, and how much of a core it burns
in the kernel over two seconds (99% stime = spinning), then an NMI backtrace of that CPU via
sysrq l (enabled for the one write, restored after) with the [nvidia] frames counted. The verdict
splits on whether the card answers. Off the bus: nothing ends the process; the clean exit is a
COLD reboot (poweroff, 30s, on , a warm reboot often leaves a dropped card dropped, the rail never
falls), and `--reset` offers the one gamble, a PCI remove + rescan of the slot, which sometimes
retrains the link and binds a fresh device beside the zombie. Wedged but present (Xid 119/120
GSP timeouts, 109, or nothing logged and nvidia-smi hanging): `--reset` stops ghost.secd (waits
for the service's other processes to leave the cgroup; systemd keeps waiting on the stuck one,
which is fine), stops nvidia-persistenced, refuses if anything else holds the card (--force),
then nvidia-smi -r, a sysfs function-level reset, remove + rescan, each written from a child with
a 30s bound (a sysfs write that never returns is itself stuck in the kernel, and the script says
so rather than joining it), each checked; when the process dies the modules are reloaded and the
card proven with nvidia-smi -L. With nothing stuck it is the after-the-reboot check: the card is
back, its link, and whether it fell off in this boot too. Exit 0 clear, 1 stuck/reboot, 2 unsure.

Xid 79 has causes, and the reboot does not fix them: power delivery (PSU, the 8-pin, a riser),
PCIe power management (`pcie_aspm=off` on the kernel command line or off in BIOS), heat, a card
on its way out. After the box is back: `sudo ./tools/unwedge.sh` (clear?), `sudo ./tools/gpu.sh`
(on the GPU?), `nvidia-smi -q -d TEMPERATURE,POWER`, and `journalctl -k -b -1 | grep -B5 'Xid'`
for what preceded the fall last time. redeploy.sh, gpu.sh, procs.KillStrays and the Unmount
error now all point at unwedge.sh instead of a bare "sudo reboot", and the redeploy's UNKILLABLE
block prints the Xid count and the first Xid line whole. Verified here: the scan (nothing stuck
on the sandbox), the --pid path against a dd burning kernel time (99% of a core INSIDE THE
KERNEL, on cpu 0, syscall running), the bounded write (returns 124 on a write that blocks, 0 on
one that lands, 1 on a bad path), missing lspci/nvidia-smi said plainly; the levers themselves,
sysrq and the systemctl dance need the box.

Then Vlad ran the gamble by hand: `nvidia-smi -r` (no devices), `echo 1 > …/0000:2e:00.0/remove`
(returned, did not hang), `echo 1 > /sys/bus/pci/rescan`, and lspci listed the card again with
its real IDs (10de:2786, RTX 4070): the link retrained, the silicon answers, the card is not
dead. nvidia-smi still said "No devices found": the fresh device at 2e:00.0 either was not bound
or the probe refused. unwedge.sh gained that third state. Section 2 now also prints the driver's
own list (/proc/driver/nvidia/gpus), whether nvidia is bound to the slot, the last NVRM lines
that are not Xid noise (a refused probe says so there) and any BAR/bridge-window trouble on the
slot; the verdict is "gone" (config space 0xffff / no device), "returned" (Xid 79 in this boot
but the slot answers now: bound? seen?) or "wedged". `--reset` on a returned card starts with
lever 0, a bind of nvidia to the fresh device (unbind first when bound but blind, modprobe if the
module is out), each write bounded; lever 3 (remove + rescan) now binds after a successful rescan
too; and the ending has a middle outcome: the card is back beside the zombie (nvidia-smi -L lists
it) → exit 0, start the stack, the zombie keeps one core until the next reboot and the lock path
reports it unkillable each time and carries on. If the probe refuses, the module's state is
poisoned by the task still executing in it and cannot be reloaded: cold reboot, knowing the card
is sound. Verified here with a faked kernel log (Xid 79 + a refused probe line): the returned
verdict, bound 0, seen 0.

Vlad's next paste: `Kernel driver in use: nvidia`, the driver's information file with the model
but `GPU UUID: GPU-????` and `Video BIOS: ??`, `RmInitAdapter failed! (0x23:0x65:1552)` every
10s, `LnkSta: Speed 2.5GT/s (downgraded), Width x2 (downgraded)`, and `ps` showing 2306644 in
state X. Read: the remove kicked the zombie out of its spin (X = dead, in its final teardown in
the driver's release path; its 99% is the lifetime average), the driver bound to the returned
card and cannot bring the chip up, and the link came back at x2 , speed drops at idle are normal,
width drops are physical (seating, riser, slot) or a hot rescan that never ran equalization.
unwedge.sh now prints LnkCap beside LnkSta and flags a narrowed width, names the upstream port
and its link, counts RmInitAdapter failures and says what they mean, calls out state X for what
it is, and `--reset` on a returned card that is bound but blind offers lever 0b: unbind, a
function-level reset, a link retrain from the upstream port (setpci, Link Control bit 5), bind
again, nvidia-smi -L. The closing reboot advice adds "reseat the card, check the 8-pin" when the
link was narrow, because a reboot alone does not widen a link.

## Trail glitches: the spike to the mainland and back

Vlad's map: a walk on an island, and one dashed line shooting 100 km to the mainland coast and
straight back. A fix is sometimes not where the phone is , a cell-tower position from the network
provider, a stale fix from another provider , and the trail drew it as a journey. Two fixes, at
the two ends of the pipe.

At the source (app sync/LocationLog.kt): a fix now carries its error radius (Location.accuracy,
the OS's 68% circle), a fourth field on the spool line ("ts lat lon acc"; old three-field lines
still parse; the box never sees it, the POST stays ts/lat/lon). record() reads it: a COARSE fix
(radius over 200 m) whose circle still contains the last point is not evidence of movement , it
confirms where we were , so within the hour it is not written, and past the hourly gap it is
written with the LAST point's coordinates and the new time ("still here, as far as the phone can
tell"), never its own, or a parked phone on a tower fix wanders two kilometres every hour. A
HOPELESS fix (radius over 5 km) is only ever such a confirmation, never a position. A coarse fix
whose circle does not hold the last point is taken: imprecise, but we moved. Fine fixes go through
the old 25 m / hour rules untouched.

At the view (framed/clean.go on the box, sync/TrailClean.kt on the phone , the same rules, the
same numbers, the same haversine on the same 6371 km sphere so a 299 m hop is 299 m on both):
CleanTrack judges shape and speed, never a coordinate on its own. An IMPOSSIBLE hop (over
350 m/s, faster than an airliner over the ground) is dropped alone. A FAST hop (over 90 m/s,
faster than road or rail) that the trail COMES BACK from , within 8 points and 90 minutes it is
once more within a third of the hop's length from where it left , is a spike: the points out
there are dropped; a flight is a fast hop that does not come back. A LONE point reached and left
at 12 m/s or better (43 km/h averaged over a leg, both legs) between walking-pace hops is a spike
too: nobody goes from a stroll to a forty-kilometre round trip with no fix at the far end and back
to a stroll inside two sampling gaps. Hops under 300 m are never judged. What is NOT caught, on
purpose: a slow zigzag between two providers 2 km apart (2 m/s , that is the record-time radius
rule's job), a real errand with a fix at the far end, a boat, a drive, a day of travel. The raw
points stay in location_points; the view can be wrong, the record should not be.

Where it runs: BuildDayPath cleans before it simplifies, computes distanceM over the cleaned
points (a spike is not a journey) and writes "glitches" (how many of the day's raw points fell)
beside "points"; rebuildDay now fetches two hours either side of the day so a spike at 00:05 is
judged by its neighbours across midnight, and BuildDayPath keeps only the day's own points.
/v1/geo/tracks passes glitches through; the phone's DayTrack carries it; the map's phoneTracks
cleans the whole 48 h ring before splitting it per day, counts the fallen per day; the TRAIL
panel's lit-day line says "· 2 glitches ignored". The old days on the box are rebuilt on their
next rebuild (a new point for the day, or a stock-take that touches it).

One fixture, two tests: framed/testdata/trail_glitches.txt (a copy in the app's test resources)
has eleven cases , island_spike, spike_repeated, flight, lone_errand_slow (8 km, 9 m/s: kept),
lone_spike_moderate (25 km, 28 m/s: dropped), real_drive, impossible, same_second, zigzag_slow
(kept), boat, trailing_unconfirmed (a fast hop with nothing after it is kept until the next fix
says) , each point marked x when it must fall. Go's TestCleanTrackAgainstTheSharedFixture and the
app's TrailCleanTest read the same file, so a number that drifts on one side fails a test.
Verified here: both pass the eleven, unsorted input is judged in time order, the midnight case
(points 4, glitches 1, distance ~290 m, the spike not drawn, yesterday's context not leaked),
the legacy BuildDayPath test moved into its day, and the phone's record() rules in the trail
harness against the real LocationLog.kt: a coarse consistent fix within the hour writes nothing,
after the hour writes the previous place with the new time, a hopeless fix writes nothing, a coarse
inconsistent one is taken, a fine one lands with its radius as the fourth field, an old three-field
line reads as radius 0, and the POST to the box has no acc.

Not done: the location_points table has no accuracy column, so the box cannot apply the radius
rule to Google Timeline imports (Records.json carries "accuracy"); a column plus the same rule in
timeline.go is the next step if the imported days show tower spikes the shape rules miss.

## The 10-second RmInitAdapter drumbeat was ours: one GPU probe for the whole box

"It would be something we did or ran, one of the services." It was: watchd's stats sampler ran
nvidia-smi every ten seconds (stats.go, the 10s ticker), and secd's /v1/status ran it again on
every Box Status poll. Cheap on a healthy card. On a card the driver cannot bring up, every
nvidia-smi opens /dev/nvidia0, the driver tries RmInitAdapter, fails, writes two lines to the
kernel log , 17k lines a day , and a recovery attempt (a reset, a rebind) races a sampler that
opens the device mid-sequence. internal/gpu (new) is now the one place the box asks: Query()
reuses an answer for 5 s for whoever asks next; after a failure it leaves the card alone for a
minute, doubling to fifteen, and answers from memory ("No devices were found (next look in 4m)");
it does not exec at all when the driver's own list (/proc/driver/nvidia/gpus, a read that touches
no hardware) is empty; the exec is bounded at 2 s under the caller's own bound with a WaitDelay,
because an nvidia-smi stuck in a wedged driver does not die on SIGKILL either and Output() would
otherwise wait on its pipe forever. Known() is the driver's list. watchd's host.gpu entry now
carries the reason when not visible; /v1/status keeps the GPU block absent when not visible and
adds host.gpuNote saying why. Tests: no exec and a backoff when the driver lists no card; a
failing card is asked once and then answered from memory across the next five ticks, the wait
doubling 1m → 4m and capping at 15m; a card that comes back is asked once and reused for the TTL,
asked again past it; a hung nvidia-smi returns "did not answer in time" inside the caller's bound.

Note for the recovery itself: the manual retrain block and unwedge.sh --reset should run with
the stack down (unwedge.sh stops ghost.secd first; the manual block does not), otherwise the
sampler , this build or the old one , can open the device between the unbind and the bind.

## The box froze under unwedge.sh --reset; the gate, the order, and the watchdog

Vlad ran `unwedge.sh --reset` on the returned-but-blind card (driver bound, RmInitAdapter failing
every open, the old process in state X inside the driver's release path, link x2) and the box
went dark , no network, no console , with nobody home for eight hours. The script never reboots
(every acting line grepped: reboot/poweroff appear only in printed advice); it was the unbind →
reset → bind of lever 0b, on the one driver state most likely to take the kernel with it, and I
had rated that freeze "rare". Owned in the chat; fixed in the tools:

- unwedge.sh --reset now refuses to touch the driver unless a hardware watchdog is armed (systemd's
  RuntimeWatchdogUSec non-zero and /dev/watchdog present , a hard lockup then resets the box on
  its own) or the person types `button` at a prompt (or passes --button) saying someone can
  power-cycle the box right now. No tty counts as no. The header says why, with the date.
- The levers are reordered: 0a retrains the PCIe link from the upstream port FIRST , the one lever
  that never touches the driver , and if the width is still downgraded after it, the sequence
  stops there ("physical: slot, riser, card; a reboot alone will not widen a link"), because no
  driver lever is worth the freeze against a fault that is not in the driver. Only a link back at
  full width goes on to bind (0), and 0b is labelled as the lever that froze the box.
- tools/watchdog.sh (new): status (devices, modules, wdctl, cpu vendor → which chip driver,
  RuntimeWatchdogSec/RebootWatchdogSec, armed or not) and --arm (load iTCO_wdt/sp5100_tco in the
  vendor's order, persist in /etc/modules-load.d, softdog as the honest fallback , catches a wedged
  userspace and most oopses, not a hard lockup with interrupts off , then a systemd drop-in
  RuntimeWatchdogSec=60 / RebootWatchdogSec=10min, daemon-reexec, verify). README 1b says to run
  it once before the box is ever left alone, and pairs it with a smart plug + "power on after AC
  loss" in the BIOS. Sandbox: status mode runs (no device here, says so); --arm needs the box.

For whoever gets home: hold the power button ten seconds (a frozen kernel ignores the ACPI tap),
wait thirty, press once; reseat the card and check the 8-pin if easy. Then from anywhere: unlock
from the app, `sudo ./tools/unwedge.sh` for the link width in the fresh boot, `sudo ./tools/gpu.sh`,
and `sudo ./tools/watchdog.sh --arm` before anything else is tried.

## Memories from the photos: outings, the taste, and what is near you

Vlad, waiting for someone to get home and press the button: "add a few more features, based on
location and a summary of the pictures I usually take: memories out of the pictures with things I
like, and in the future recommend things close by based on my memories." Built without the model
on purpose , the GPU is dead and a memory of a trip should exist the week it happened, not when a
card gets around to it. Three pieces, all on the box, nothing on the network.

OUTINGS (internal/outings, synthd's outingPass). Photos with a time are sorted and grouped into
outings: a new one starts after 36 h of silence, when a geotagged photo lands 30 km from the
group's running centre, or when a group would span more than ten days. Groups under three photos
are noise. HOME is the ~5 km cell with the most distinct photo days (five at least); an outing
farther than 25 km from it is a trip. Each outing gets: the place (the most photographed on-box
hierarchy's last name, and the country from the hierarchy's second part), the distinct places
(≤ 4), the tags across its photos with counts, a cover (the frame with the most tags) and six
covers spread across its days, the distance moved (the trail between first and last photo,
cleaned with the glitch rules, jitter excluded), how far from home. Title "Antipaxos · 19-23 Sep
2026"; body from a template over real numbers: "30 photos over 5 days around Antipaxos, Greece.
Mostly beach, boat, sea, sunset and taverna; also dog and harbour. Places: Antipaxos, Gaios.
27 km on the move. 2,300 km from home." (text and style tags never make the sentence). Written
to memories as kind='outing', source_ref='outing:<day>', created_at = the outing's end, plus the
new memories.meta JSONB column (the structured detail for the app's cards; schemadef + ALTER
converge it). The person's edits and tombstones outrank regeneration; outings that dissolve on a
re-clustering are deleted unless touched. The pass runs inside distillLoop after the episodes,
at most every 30 minutes and only when the archive's signature (frames, newest photo, tag count,
trail points) changed; `ghost-cli ghost.synthd outings` shows counts and the taste line,
`rebuild=true` forces the pass. health.sh prints them under synthd.

THE TASTE (outings.BuildTaste, tastePass → settings 'synthd_taste', GET /v1/taste). Every tag's
presence is counted in DISTINCT PHOTO DAYS, not photos, so five hundred beach photos on one day
weigh one day and a boat photographed on thirty separate days wins: the taste is what the person
keeps coming back to. Text and style tags out, two days minimum, six per category so the list
stays varied, thirty likes at most, share = days with the tag / days with any photo. The likes
fold onto thirteen fixed INTERESTS (beaches, harbours, islands and capes, peaks and trails, lakes
and waterfalls, parks and nature, castles and ruins, monasteries and shrines, museums, caves, hot
springs, food and markets, old towns and villages), each a list of the plain words a caption model
uses and the GeoNames feature codes that ARE that kind of place; an interest's weight is the
summed share of the likes that name it. One sentence for the person: "You photograph sea, beach
and boat most , sea on 40% of your days with a camera out , then street, coffee and dog. Places to
your taste: beaches, harbours and old towns and villages."

NEAR YOU (GET /v1/nearby?lat&lon&km, outings.Rank). geo-import now keeps a fourth kind, S, the
spots the interests name and the geocoder never needed: beaches, coves, harbours, marinas,
capes, cliffs, lighthouses, castles, ruins, forts, archaeological and historical sites,
amphitheatres, monuments, towers, palaces, temples, monasteries, mosques, shrines, churches,
museums, caves, spas, restaurants, markets, vineyards, gardens, zoos (outings.SpotCodes; a box
that predates this needs `ghost-cli ghost.framed geo-import` once to gain them; P/K/F rows are
untouched). The handler reads the taste, pulls the S/K/F points and the villages (P/PPL) in the
radius's bbox (≤ 3000), counts the person's own geotagged photos on a ~1 km grid over the same
bbox in one query, and ranks: interest weight × sqrt(1 − distance/radius), halved when the
person has photographed within a kilometre of it (a memory is not a discovery, but still a place
they liked), at most four per interest, twenty in all, each with the plain kind, distance,
compass bearing, and the why: "you photograph sea and beach (78% of your days) · new to you".
A box without geo data, or without a taste yet, answers an empty list with a note, never an
error the app reads as down.

THE APP (MemoriesScreen). Two folds above ON THIS DAY: "[ + what you photograph ]" (the sentence,
the likes by category with their shares, the interests) and "[ + near you ]" (5/15/40 km chips
over the phone's last fix; no fix → "turn on the location trail"; each row name · kind · distance
bearing · "new", then the why). Outing memories render with a strip of their cover thumbnails
(off /v1/frames/thumb) and a footer "from your photos · 30 photos · 5 days · 27 km · a trip".
BoxClient: MemRow gains meta (covers, outingLine), taste(), nearby().

Tests: outings (a London home of seventeen days plus a five-day island trip: home found, one trip,
photos/days/place/country/places/from-home, the cover with the most tags, the covers spread, the
title, the body with every clause; splits on distance and silence, no home under five days, a
title without a place, two photos are not a memory; DateRange's four shapes), taste (ranked by
days, style/text/one-day/empty tags out, the per-category cap, the interests' weights, the
sentence, the empty case), Rank (the near photographed beach halved below a farther new one, the
harbour and village placed, out-of-radius and unlisted codes dropped, the reason strings, the
bearing, the per-interest cap, no taste → nothing), SpotCodes and KindName, geoKind's S. The SQL
(frames + tags + trail in outingPass, the taste aggregate, the bbox spot and photo-cell queries)
is read, not run: no Postgres here.

## "failed at mounting store" after the power cycle: preen before mount, find the disk by its key, grow-to-fill never fatal

The box came back locked (as it always does), secd and nginx up, the phone's location worker
reaching secd every 15 minutes with a session that died with the reboot. The PIN got past
"unsealing key" and failed at "mounting store". The MOUNT stage is luksOpen + mount + resize2fs,
and a hard power-off can break each of them in its own way; the fixes make all three survivable:

- PREEN BEFORE MOUNT (hw.preen in ensureMounted, both the cold path and the already-open one).
  The OS disk gets an fsck at boot; this volume never did. `e2fsck -p` on the open mapping before
  mount: a clean ext4 answers in milliseconds, a journal to replay in a second, an error-flagged
  filesystem gets a forced check and is repaired (exit 1-3 are successes, logged WARN with
  e2fsck's last lines). Exit 4 and up fails the stage with the exact command: the volume is
  UNLOCKED BUT NOT MOUNTED, so `sudo e2fsck -f /dev/mapper/ghost-slot0` from the host needs no
  key (the mapping is in the kernel), then unlock again. Non-ext filesystems are left alone.
- GROW-TO-FILL NEVER FAILS AN UNLOCK (backend.Mount). resize2fs refuses a filesystem flagged with
  errors ("Please run 'e2fsck -f' first" , reproduced here on an ext4 image with state=2), and a
  refused resize used to fail the whole MOUNT stage with the volume already mounted behind it. A
  mounted volume that could not grow is a working box: WARN and carry on.
- THE DISK FOUND BY ITS KEY (MapWithKey). NVMe names follow probe order, which is not stable across
  boots. When --disk is not a LUKS container, every LUKS container blkid lists is tried with the
  key (a wrong key changes nothing; the AMK is random and unique, so the one that opens is the
  volume), and the journal names the stable /dev/disk/by-id path (whole disk, eui./wwn- preferred)
  to put in the unit. A configured disk that IS LUKS but refuses the key is not sprayed around.
- Tests: findByKey (stops at the first that opens, first-line errors, no candidates), stableName
  (whole-disk eui link over the model name, -part skipped, no link → the device), preenVerdict
  (0, 1-3 repaired, 4 → the command and "NOT mounted", 8), and preen against a real ext4 image in
  the sandbox: clean in 4 ms, error-flagged → forced check, repaired, exit 1 → success.

Setup still writes --disk as /dev/nvmeXn1; making it write the by-id name is the next small step.

It was the disk names. The journal: `luksOpen slot 0: Device /dev/nvme1n1 is not a valid LUKS
device`; lsblk: nvme0n1 7.3T crypto_LUKS (the volume), nvme1n1 7.3T xfs mounted at
/data/bitcoin-ssd. The power cycle swapped the probe order. Nothing was damaged (luksOpen on the
xfs disk only reads the header). The immediate fix on the box: point --disk in
/etc/systemd/system/ghost.secd.service at the /dev/disk/by-id name of nvme0n1, daemon-reload,
restart, unlock (redeploy does not rewrite the unit). The new secd would also have found it by the
key. Two more things so it cannot happen, or cost more, again:

- ghost-setup writes the STABLE name into the unit (hw.StableDiskName: the by-id link that resolves
  to the chosen disk, eui./wwn- preferred, -part skipped) and prints the translation.
- ghost-setup given --disk by FLAG now refuses a disk that is mounted or carries a filesystem or a
  partition table that is not a LUKS container, unless --erase-disk-with-data is added. The
  README's own example named /dev/nvme1n1: after this reboot, re-running it verbatim and typing
  "yes" would have luksFormatted the bitcoin SSD. The picker already warned; the flag path did not.
  README says to use /dev/disk/by-id and why.

Also seen, left alone: lsblk lists nvme0n1p1 (69.4G) and nvme0n1p2 (943.8G) under the whole-disk
LUKS container , a stale partition table (most likely the backup GPT at the end of the disk, which
luksFormat of the whole disk does not overwrite). Harmless while nothing touches those partition
nodes; removing it must be surgical (wipefs -o <offset of the backup GPT only>, after a
luksHeaderBackup), never sgdisk --zap-all or wipefs -a, which would take the LUKS header with it.

## Back up; host.gpu gets a drill-in that never touches the card

The box is up and unlocked (the by-id unit fix). Box Status: host.gpu "not visible", "no drill-in
for this daemon yet"; the pipeline describing at 24/h on the CPU, 3399 left, "about 6 days". So
after a COLD boot the card is still not usable, and the question is which layer , the answer to
which decides between the screwdriver and the PSU.

internal/gpu/diagnose.go: Cards() reads every NVIDIA display device (vendor 0x10de, class 0x03)
from sysfs , bound driver, current_link_width/max_link_width, current/max link speed, power
state, and whether config space answers (0xffff = off the bus) , which the kernel serves from PCI
config space without waking the nvidia driver: no /dev/nvidia0 open, no RmInitAdapter, safe to
ask on every tap. Diagnose() adds the driver's own list, the shared probe's last nvidia-smi answer
(never a fresh exec), and the kernel log read through syslog(2) (no exec; secd is root): Xid count
and last line, RmInitAdapter failure count. The verdict, first match wins: no card on the bus;
listed but off the bus; no driver bound; a link narrower than the card (physical: reseat, riser,
slot , plus the init failures it most likely causes); the driver bound but failing to bring the
chip up (power or a failing card); the driver listing nothing; working (with any faults logged).
secd's /v1/daemon/summary?name=host.gpu now answers with these rows (verdict first), so the phone's
drill-in is the diagnosis. Tests on fake sysfs trees (an Intel iGPU and an NVIDIA NIC ignored):
x2 of x16 with two init failures, no card, off the bus with Xid 79, unbound, working at x16.

## Chat end to end: search on the phone, the box adds you, the answer comes back , and says why it waits

Vlad: "can I have the chat work end to end: search on google and scrape a few websites on the app,
pass the context to the server, the server adds our own memories, we get the response." Most of
the road existed (WebSearch v2 on the phone: plan, DuckDuckGo, top three pages read with the
readability pass, weather/rates/Wikipedia tools; secd forwards web; synthd labels it, numbers it,
adds the archive; oracled streams). What was missing, and is now there:

GOOGLE, honestly: its results page forbids scripts and answers them with consent walls and
captchas; the Custom Search JSON API is closed to new customers and is discontinued on
1 January 2027 (developers.google.com/custom-search/v1/overview). So the phone gains BRAVE as a
second engine: the Brave Search API with the person's own key (X-Subscription-Token; JSON
web.results → title, url, description with its <strong> marks stripped, page_age as the page's
date), $5 per 1,000 searches after a $5 monthly credit, with DuckDuckGo behind it so a bad key or a
spent credit degrades to the keyless search, not to nothing. Settings › WEB SEARCH: engine chips,
the key field (masked, kept on the phone, the box never sees it). A page's own date now only
replaces the engine's when the page states one.

THE PERSON, better: memoriesSource matched on every word of three letters or more, so "the",
"what", "did" matched every memory the box had; memoryTerms drops a stopword list first (six
content terms, deduplicated). And when a question asks for a suggestion or a plan (wantsTaste:
recommend, should I, what to do, where can we, near here, a day out, swim, eat ...), synthd adds
the TASTE sentence from the outing pass and, when the phone sent where it is (`here`: its last fix
under six hours old, range-checked in secd), up to four places near it that fit, ranked by
internal/outings.Rank over the box's own GeoNames spots, each with its reason ("Voutoumi (beach,
1.8 km NE from where you are) , new to you"). The geo SQL is shared: hw.QuerySpotsNear /
QueryPhotoCellsNear over a small Querier interface, used by secd's /v1/nearby and synthd alike.

THE WAIT, explained and bounded: on a CPU-only box (as now) a 4B model reads a prompt at tens of
tokens a second, and eight web pages of 1,500 characters is two minutes of reading before the
first word , the chat looked dead. synthd now asks oracled `models` (cached a minute) for the
measured prefill speed (defaults: 40 t/s on CPU, 1,500 on GPU), budgets the context to about
twenty seconds of reading, and fitWeb trims the phone's findings to fit: the lowest hits lose their
page text first (title, URL and snippet stay, so they are still citable), the rest are shortened
evenly on rune boundaries, never below three hits or 1,800 characters. The first event of the
stream carries a `note` when the read is long or something was cut ("reading 1,300 words of
context on the CPU (no GPU right now) , about 35s before the first word · web pages shortened to
keep it there"). The app shows a live status line in the answer's place from the first tap:
"searching the web on this phone (Brave)…" → "5 found, 3 read on this phone , asking your box…" →
the box's note → the first reasoning or word replaces it; a stream that ends with nothing says so
instead of leaving the status standing. Message gains `status`, BoxClient.ChatChunk gains Status,
BoxClient.chat gains `here`.

Tests: Go , memoryTerms (the question's content words, the cap), wantsTaste (six that ask, three
that do not), the budget arithmetic and the note, fitWeb (generous budget cuts nothing; a CPU budget
keeps the top two's text, keeps every trimmed hit citable, lands under budget; a hopeless budget
keeps three; Greek cut on rune boundaries), here's validity. Kotlin , the real WebSearch.kt in the
harness: the Brave parser (tags stripped, entities decoded, empty url skipped, page_age → date, bad
JSON → nothing), Engine.brave, and every existing WebSearch check; a JUnit twin in WebSearchTest.
Not run here: Brave's live endpoint (no key, no egress), the phone UI, the box end to end.

## The map looks like a map: sea and land, and the coast at full detail when zoomed in

Vlad: "make the map look a bit more like a map, a different thing for water; on zoom in send the
points for the extra detailed map , for Paxos we get nothing special in shape; split the extra
high-resolution points and load them when we need them."

SEA AND LAND. The canvas is water now (a deep blue-black), the Natural Earth rings are FILLED as
land (a shade lighter and greener) and stroked as the coast and borders in the dim phosphor line;
the graticule sits under the land, so it reads as a grid on the sea. The dots and the trail stay
the bright things on the screen.

THE COAST AT FULL DETAIL. Natural Earth's 10m file (the finest cut on the box, 548k points for the
world) draws Paxos as a handful of vertices, and there is nothing finer in Natural Earth.
OpenStreetMap's land polygons (osmdata.openstreetmap.de, land-polygons-complete-4326, ODbL) draw
every cove , and the world at that resolution is tens of millions of points, exactly what must not
go to a phone whole. internal/landtiles (new, stdlib only) cuts it once, on the box:

- a hand-written shapefile reader (100-byte header, big-endian record headers, little-endian
  Polygon/PolygonZ/PolygonM bodies; everything else skipped; the .dbf and .shx are not needed);
- recursive bisection on WHOLE DEGREES (Sutherland–Hodgman against one line at a time, orientation
  kept), so a continent-sized ring is cut in O(n log cells) , a 3-million-point ring the size of
  a small continent cuts in about a second here , and every artificial edge the cutting makes lies
  exactly on a one-degree cell border;
- per one-degree cell of the 360×180 grid: no land → water, no file; land covering the cell →
  "land", no file; otherwise a COAST tile: the rings clipped to the cell, each vertex quantised to
  1/65535 of a degree (under two metres) as two uint16s, four bytes a vertex, repeated points
  dropped, slivers dropped; plus index.bin, one byte per cell (0 water, 1 coast, 2 land) behind a
  magic, 64,800 bytes. Written into a .tmp directory and swapped in whole.

ghost.framed: `ghost-cli ghost.framed geo-tiles` builds from land_polygons.shp under <mount>/geo
(or one level down, where the zip unpacks) into <mount>/landtiles, in the background, one build at a
time, state in daemon_state ("landtiles"); framed also builds by itself at start when the shapefile
is newer than the tiles. secd: GET /v1/geo/landtiles/index (204 when none) and
GET /v1/geo/landtile?x=&y= (range-checked), static files with an mtime+size ETag and 304s.
tools/fetch_geo.sh fetches and unpacks the OSM file at setup (GHOST_GEO_NO_OSM=1 skips it); README
1b' has the commands for a box already running.

The phone (ui/LandTileGeom.kt, pure; ui/LandTiles.kt, Paths and the cache): the index is fetched
with the world (ETag-cached; a new index drops the cached tiles, since it means a new cut). Zoomed in
past 150 screen px per map unit (about 1.5 degrees across a phone screen), every visible cell is drawn
from the index: sea left as sea, land filled solid, a coast cell from its tile , fetched then, four at
a time, from disk when the phone has it (cache trimmed at 200 MB, least recently looked-at first),
built off the main thread into three levels (tolerances ~280 m, ~28 m, every point, picked by zoom;
border vertices always kept) , and the base clipped to the cell while the tile is on its way. Fill:
all of a tile's rings in one Path, non-zero, so a lake stays water. Coast: the edges that run along
the cell border (both ends on the same side) are skipped, so tile seams never show. The tile origin is
kept in Double (a Float origin is pixels off at street zoom). The note line credits "coast ©
OpenStreetMap contributors" when the tiles are there.

Tests: Go , the L-shaped (concave) ring across three cells keeps its area and orientation and never
leaves its cells; a ring touching the next cell's edge stays home; quantise/encode/decode; a
synthetic shapefile (island, a 2×2-degree block of solid land, a square across four cells, a full cell
with a lake, a null and a point record skipped) → index states, tile contents, no file for land or
sea, the atomic swap; garbage rejected; the static serving's ETag/304. A golden tile written by the
encoder (internal/landtiles/testdata/tile_fixture.lgt, re-checked by its own test) is decoded by the
phone's code in the harness and in LandTileGeomTest: the island is one closed coast run, the mainland
corner's two border edges are skipped, simplify keeps border vertices, the cell window for Paxos is
one cell. Not run here: the real OSM file (no egress), the phone drawing.

## Setup pulls from localghost.ai/mirror, signed like the releases

Vlad: "can we just host everything on localghost.ai? i can run the scripts to host it there and we
can just pull it on setup from there" , then: "i can do it all on the server, i already sign the
releases with a key [a sha256sum deploy manifest, gpg --detach-sign --local-user info@localghost.ai];
can't i do the same under localghost.ai/mirror, with the signature and the terms there as well , for
the open map, for Gemma, for the other map with GPS I downloaded, and future models?"

Yes, and that is what it is now: shell, gpg and sha256sum, the release script's own pattern.

THE SERVER (the web repo, LocalGhostDao/web, mirror/, so the site and its data are deployed
together; data outside the repo and outside the deploy directory, never on GitHub). mirror/publish.sh
<data-dir> [set ...] reads mirror/mirror.conf (`set file terms source [check=godev]`), refuses a data
dir inside the repo, and writes:
  <root>/<build>/<set>/<file>             a directory per publish (20260924T120000Z), never changed after
  <root>/<build>/<set>/TERMS-<name>.txt   the terms each file travels under (mirror/terms/)
  <root>/<build>/<set>/NOTICE.txt         which file, which terms, where from
  <root>/MANIFEST.txt(.asc)               "# LocalGhost Mirror Manifest", "# Build:", "# Signed:", then
                                          sha256sum lines "/<build>/<set>/<file>", gpg --detach-sign
                                          --local-user info@localghost.ai , the release script's lines
Sources: an https URL (cached; `curl -z` fetches again only when upstream changed), a path on the
server (a model, a map), or landtiles:<zip> , the OSM land polygons cut on the server by
cmd/ghost-landtiles (new, 30 lines around internal/landtiles.Build) and packed with GNU tar
--sort=name --mtime --owner=0 | gzip -n, so an unchanged coastline packs to the same bytes (and is
cached by the zip's hash, never cut twice). check=godev: the Go tarball must match go.dev's checksum.
A refreshed set is rebuilt from the conf alone; the others are hard-linked from the previous build (no
copies, the old build untouched). Nothing changed = no new build. The manifest pair is the last thing
written; the last two builds are kept, so a box mid-download of the previous manifest still finishes.
One publish at a time (flock); a build directory is never reused (two publishes in one second wait);
any stop before the manifest moves removes the half-built directory. The first run exports the public
key to the web repo's mirror/mirror-key.asc; copied here as tools/mirror-key.asc and committed, it is
what boxes trust. cmd/ghost-landtiles (this repo) is the cutter the web server runs.

TERMS. mirror/terms/ in the web repo: geonames (CC BY 4.0 attribution), naturalearth (public domain),
osm-odbl (the ODbL notice + "© OpenStreetMap contributors"; the tiles are a derivative database,
offered under the ODbL, method = internal/landtiles), go-bsd, llama-mit, apache-2.0 (its first line
"#fetch https://www.apache.org/licenses/LICENSE-2.0.txt": the full text is fetched at publish),
gemma4 (Gemma 4 is Apache 2.0 since March 2026; the GGUFs are conversions, Q4_K_M modifies the weights,
so the file says who converted them , EDIT-ME until filled), gemma-terms (EmbeddingGemma is under the
Gemma Terms of Use, which must be passed on in full with its use restrictions , EDIT-ME until the
text is pasted in). A terms file that says EDIT-ME stops the publish of anything that uses it. A new
dataset or model: a line in mirror.conf (a set of its own) and a terms file.

THE BOX. tools/mirror_fetch.sh <set> <dir> [file]: fetches MANIFEST.txt(.asc), imports
tools/mirror-key.asc into a throwaway gpg home, requires a VALIDSIG (three tries three seconds apart:
a publish swaps the pair one after the other), requires the "# LocalGhost Mirror Manifest" header (a
release manifest signed by the same key is not accepted as a mirror one) and a build line; refuses a
build older than the one this box last used (/var/lib/ghost/mirror-build; a replayed manifest) unless
GHOST_MIRROR_ALLOW_OLD=1; takes only lines "/<build>/<set>/<name>" with names that cannot climb;
downloads each to a hidden .part (resumes with curl -C -, starts over when the server cannot resume,
gives up under 1 KB/s for a minute), checks the SHA-256, and only then gives it its name. Files
already there with the right hash are kept. Writes <dir>/.mirror-files (the set's names). Exit 0 all
here, 1 something failed (callers fall back), 3 nothing to offer (off, no key in the repo, no gpg, no
such set). https only, except loopback or GHOST_MIRROR_ALLOW_HTTP=1.

Callers: fetch_geo.sh (geo when something is missing or on refresh: verified files moved in,
allCountries.zip unzipped, the TERMS and NOTICE beside the data; landtiles when there is no index.bin:
unpacked beside the live tiles and swapped in whole; the upstream blocks fill whatever the mirror did
not deliver, and with tiles present the shapefile is not downloaded); setup.sh (the Go tarball,
before Go exists, gpg installed first if missing; no mirror → go.dev's checksum list as before);
setup_llama.sh (pinned llama.cpp source: a tarball with a new name replaces the folder whole, build
included, so it rebuilds; an existing git checkout keeps pulling unless --from-mirror; the weights
when neither --models nor --model-url, no Hugging Face token; gpg added to its apt line).
Provisioning chowns geo/ and landtiles/ to the service user after the fetch.

Replaced before it shipped: the first cut of this (an ed25519-signed JSON manifest, a Go client, a
content-addressed store, a pins file for Go) , one mechanism now, the one the releases already use.

Tested here with a real gpg key (ed25519, uid info@localghost.ai): publish against fake upstreams
(GeoNames, Natural Earth, a shapefile in a zip cut into tiles, Go checked against a fake go.dev list,
a local "map" with two terms files, one fetched), the layout and manifest above; fetch_geo.sh twice
(second: nothing fetched) and with GHOST_GEO_REFRESH=1; a tampered file on the host (that file
refused, the rest in, the rerun fetches only it); a repo holding a different key; a tampered
manifest; a release manifest signed by the same key; off / no key / no set / plain http to a LAN
address; a stale .part against a server without Range (starts over); a 404 (curl's reason shown);
a republish with nothing changed; a geo-only republish (the rest hard-linked); a replayed older
manifest refused, then allowed with the override; pruning to two builds; Go from the mirror and with
the mirror off; llama.cpp from the mirror, again (kept, build/ kept), and a new pin (replaced); an
EDIT-ME terms file stopping the publish; two publishes at once (the second refused); a failing
source leaving no directory behind. Not run: the real upstreams (no egress), the world-size cut on
the real server, nginx.

## The mirror as it went live (2026-09-25): www, the site key pinned, raw OSM cut on the box

The web side (LocalGhostDao/web, deploy/mirror/) is live at https://www.localghost.ai/mirror and
became a pure proxy: files exactly as upstream publishes them, signed by the SITE key (the one that
signs site deploys), refreshed by every site deploy. What changed here to match:

- tools/mirror_fetch.sh: default base https://www.localghost.ai/mirror (the bare domain answers 301
  to www; redirects are followed, never down to plain http: --proto-redir =https). The signer is
  PINNED: DCE9 A3D1 4EB4 6197 1DD5 F393 706E 4194 F08A 09A0 (GHOST_MIRROR_FPR overrides, for tests).
  tools/mirror-key.asc must hold that key (checked before anything is downloaded; "is not the
  LocalGhost site key" otherwise) and the manifest's VALIDSIG must name it, as the signing key or its
  primary; any other key in the file signs nothing that counts. The key is committed once, by hand,
  never fetched at verify time. The header check matters more now: a site deploy manifest is signed
  by the same key, and line 1 must be exactly "# LocalGhost Mirror Manifest".
- The mirror has no landtiles set. It carries OpenStreetMap's land-polygons-complete-4326.zip as set
  `landpolygons`; fetch_geo.sh takes it from the mirror (terms and notice beside the shapefile),
  falls back to osmdata.openstreetmap.de, and CUTS IT ON THE BOX with bin/ghost-landtiles (make box
  builds it; else ghost.framed cuts at its next start, or `ghost-cli ghost.framed geo-tiles`). Tiles
  present and no refresh asked: nothing downloaded.
- llama and models are not published yet: mirror_fetch.sh exits 3 ("build … has no set 'llama'"),
  setup_llama.sh clones llama.cpp from GitHub as before, and the weights need --models or
  --model-url as before (there is no unattended upstream for them: Hugging Face gates them).
- Docs: server/README.md and tools/README.md 0b link the mirror page and list what a box verifies.

Tested against a local https mock of the live contract (self-signed TLS, a bare-domain server that
301s to the www server, the manifest format and sets above, a test key pinned via GHOST_MIRROR_FPR):
go from www; geo through the 301; landpolygons; fetch_geo.sh fresh (geo from the mirror, polygons from
the mirror, cut into tiles with terms beside them) and again (nothing fetched); llama and models
missing → exit 3, llama falls to the git clone; the Go block of setup.sh; a 301 to plain http
refused; a tampered manifest; a key file that is not the pinned key (exit 3, named); a key file that
holds the pinned key AND another, with the manifest signed by the other (refused); a site deploy
manifest signed by the pinned key (refused on the header); a replayed older build; unreachable; off.
Not run here: the live mirror (no egress from this sandbox) , the commands are in tools/README.md 0b.

## The captions stopped: 3240 parked, every one refused by llama-server with an unexplained "http 400"

Vlad: "the server has stopped processing, we still have a lot of images to process". The box said:
searchd queue parkedJobs 3240, runnableJobs 0; searchd and oracled logs full of
`kind=caption err="chat/completions: http 400"`, the same ten jobs failing in 10-15 ms each, every few
minutes, until they too parked (last one 2026-09-24 20:14). My first guess , CPU captions dying at the
GPU-sized deadline , was wrong for THIS stall: a 400 in 10 ms is llama-server refusing the request
before doing any work. Why, it said in the response body, and oracled threw the body away.

What a 400 that fast can be (the body decides; the probe below asks llama-server directly):
- llama-server running without its projector: "image input is not supported … provide the mmproj";
- an image it cannot decode: it reads JPEG/PNG/GIF/BMP (stb_image), and framed converts previews to
  WebP whenever cwebp is on the box , a WebP preview is refused every time;
- the request exceeding the context size.

Fixed, all three ways:
- oracled: every non-200 from llama-server carries llama-server's own message into the error (the
  multimodal path, the text path , which used to decode an error body as an empty answer , and the
  chat stream). The searchd log will say WHY from now on.
- oracled: an image the model cannot read is converted before it is sent: WebP through dwebp (Debian's
  `webp` package, the one that brings cwebp), anything else through ffmpeg (HEIC/AVIF on builds that
  have it); a file nothing can convert fails with its format named. JPEG/PNG/GIF/BMP go as they are,
  with their real media type.
- A refusal that means "this server takes no images at all" comes back as "no vision: …", and
  searchd HOLDS the caption lane (5 minutes at a time, attempts refunded, one WARN per hold) instead of
  failing and parking every job: fixing the server resumes the queue.

And, for when captions run again on the CPU (still true, just not today's cause): search.Pace asks
oracled whether the model is on the GPU (`models` → onGPU, at most once a minute) and stretches the
deadlines on the CPU (caption 2 → 15 min, tag 1 → 8; transport 16 min); TagOracle returns resp.Err
instead of an empty tag list (a failed tag pass used to COMPLETE the job untagged); and a streamed chat
pauses the background lane (a running caption is cancelled with oracle.ErrPreempted and requeued
without losing an attempt; nothing background starts until the stream ends + 20 s).

Also found in the same health output: ghost.cued has not posted a reflection or a cue since it was
wired to the notification store , it built the store from `<mount>/mnt/slot0` as if -mount were the
state dir (secd's shape), so every post looked for /var/lib/ghost/mnt/slot0/mnt/slot0/services.conf.
It uses the volume it is given now.

After deploying oracled, searchd and cued:
    sudo ./tools/ns.sh ./bin/ghost-cli ghost.searchd unpark kind=caption
    sudo ./tools/ns.sh ./bin/ghost-cli ghost.searchd unpark kind=tag
    sudo ./tools/ns.sh ./bin/ghost-cli ghost.framed converge

Tests: image kinds by magic bytes; JPEG passed through untouched, WebP converted, HEIC with no
converter refused with its format named, a missing file; a fake llama-server answering 400 "image
input is not supported" → "no vision:", 400 context size → the reason in the error, 500 on the text
path, 400 on the stream; Pace and the deadlines; the broker's pause and preemption; the queue's hold.
Not run here: the real llama-server's answer , the probe in the reply asks it.

## After the redeploy: what the first health said, and three small things it showed

The box, 19:58 UTC 2026-09-25, all eleven up. oracled: "model ready" seven seconds after start ,
that is the GPU (the card is back on the bus; the caption probe answered "The square is red" at
58 tok/s). searchd: `render undecodable ... .webp ... image: unknown format` , the previews ARE
WebP (cwebp is installed), which is what llama-server was refusing with its 400s; oracled now
converts them through dwebp before sending. framed's converge: 32,856 frames, 3,388 undescribed,
asked searchd for 3,390 (the parked caption jobs are replaced by the ensure path, so converge alone
un-parks them), and 44 frames with NO preview: "invalid JPEG format: missing 0xff00 sequence" / "bad
Huffman code". cued: no more doubled-path errors.

Three fixes from that:
- framed: a JPEG Go's strict decoder refuses is re-encoded through ffmpeg once (`-err_detect
  ignore_err`, from a temp file: ffmpeg's stdin probe fails on a damaged file where the named-file
  path reads what is there), then previewed as usual; the 44 get previews, captions and a place in
  the gallery at the next converge (they are re-derived: "no preview" is a stage). Bundled ffmpeg
  first, PATH second, as for video frames.
- searchd: the perceptual hash of a WebP render is computed through dwebp (Go decodes no WebP), so
  bursts of WebP previews fold again and one caption serves the burst instead of one per sibling.
- searchd: the pace probe treats "model not loaded yet" as no answer and asks again after ten
  seconds instead of caching "CPU" for a minute (the first health showed exactly that line, five
  seconds after oracled started).

Tests: a JPEG with a damaged scan (Go refuses it) re-encoded through ffmpeg and decoded at its
size (skipped where there is no ffmpeg); the pace re-asking after the model loads. Not run here:
dwebp on a real WebP preview (no dwebp in this sandbox; the code path is the same as oracled's).

## The map's coast: the contract checked end to end, and a line on the screen that says what it is doing

Vlad: the tiles are cut (6,903 coast tiles, 313 MB, six seconds through the mirror) but "the new
maps stuff does not work on the mobile , make sure we're serving the right things from the right
places and the api definitions are the same".

Checked, both sides, byte by byte:
- Files: ghost-landtiles wrote <mount>/landtiles/{index.bin, XXX_YYY.lgt}; secd serves them from
  <state>/mnt/slot<N>/landtiles, the same place seen from inside its namespace, the same shape as
  the world cuts under geo/ that already draw.
- Routes: GET /v1/geo/landtiles/index and GET /v1/geo/landtile?x=&y= (secd server.go), bearer
  session like every other GET, nginx proxies all of /. 204 = no index yet; 404 = no tile for
  that cell; 304 on a matching ETag ("t-<mtime>-<size>").
- Index: 4-byte LE magic "LGI1" + 64,800 bytes (one per cell, y*360+x; 0 water, 1 coast, 2 land),
  64,804 bytes total; the phone refuses any other size. Tile: LE "LGT1", u16 x, u16 y, u32 rings,
  per ring u32 n and n×(u16, u16) at 1/65535° from the cell's SW corner. The phone's decoder was
  proven against a golden tile written by the Go encoder (LandTileGeomTest).
- Names: server TileName "%03d_%03d.lgt" (x, y); the phone asks ?x=key%360&y=key/360 and caches
  under the same name.
- Geometry: the phone projects each vertex with the same Mercator as the world (WORLD 1024 units
  = 360°), origin at the cell's NW corner in Double, positive down; drawn with translate(origin) +
  scale(pxz). The detail switches on at TILE_PXZ = 150 px per map unit = 427 px per degree, about
  2.5° across a 1080-px screen , at "Greece" zoom the base still draws, at "Paxos" zoom the tiles.
Nothing disagrees. What I cannot see from here is the phone.

So the map now SAYS what the coast is doing, in the note line under it: "coast: no tile index from
the box" (the index never arrived: server side, or an old build), "coast © OpenStreetMap
contributors (zoom in for detail)" (index here, not zoomed in past 427 px/°), or "tiles N wanted,
M here[, K failed: <reason>]" with the last failure's reason (not 200 from the box / bytes the
phone could not decode). logcat (tag LocalGhost) gets the index fetch's outcome (http code, wrong
size, 204) and every tile failure. And a cell that failed is asked again after a minute instead of
being dead for the session (a dropped connection is not a missing tile). health.sh prints the
server half under ghost.framed: "map base: <cuts>" and "map coast: N tiles, size, index ok" (or
the exact reason it is not).

Not run here: the app (no Android SDK in this sandbox; the structural check cannot see Compose
state, so it says nothing useful about MapScreen). If the note line says "no tile index" with
health.sh saying "index ok", the phone never asked or was refused: adb logcat -s LocalGhost while
opening the map has the http code.

## The weights are pinned: Unsloth's Gemma 4 build, by hash, from the mirror or Hugging Face or a USB stick

The web side published the `models` set: Unsloth's Gemma 4 12B GGUF build (Apache 2.0, no account),
gemma-4-12b-it-Q4_K_M.gguf (sha256 0a270ec9…e4c42, 7,121,861,440 bytes) and mmproj-F16.gguf
(91f08697…a219e, 175,115,840 bytes), and asked the box side to keep the same two pins, fall back to
Hugging Face with curl, accept copied files by pin, and keep Python, huggingface-cli and Hugging Face
accounts out of setup. Also: "one map" , the GPS map and the photo map are the same map (Natural
Earth + the OSM coast tiles + GeoNames, with location history and photos as layers), so there is no
second dataset and no new caller: that open item is closed.

- tools/model.pins: `name sha256 bytes upstream`, the two files above; the embedder
  (embeddinggemma-300m-q8.gguf) deliberately unpinned until its source is settled.
- tools/model_pins.sh (POSIX sh, sourced): pin_sha/pin_size/pin_url/pin_names and pin_check <path>
  , size first (cheap), then sha256sum; an mmproj of the right size with the wrong hash is named
  for what it is (Unsloth's earlier upload, before their F32 patch_embd fix).
- tools/setup_llama.sh: the default path fetches exactly the pinned names , the mirror first (its
  signed manifest AND the pin), else `curl -fL -C -` from the pin's upstream (resumable; a rerun
  continues; an already-downloaded file that matches is kept) , then the pin; a file that fails is
  deleted and setup stops with the copy-it-over hint. `--model/--mmproj/--embed <path>` take files
  you copied (a USB stick, scp), check them by the name they will have on the box, link them into
  the download dir, and fetch whatever pinned file was not given. `--models <dir>` checks every
  pinned name it holds. `--model-url` (other weights) stays, unpinned and said so. `--hf-token` is
  gone (a message says why). The embedder: pinned when the pins say so, else from the mirror
  unpinned, else a note (FTS-only until provided).
- tools/stage_models.sh: the same check before staging; a file under a pinned name that is not
  that file is not staged (GHOST_MODEL_PINS_SKIP=1 for a deliberate experiment).
- tools/models_check.sh [--fix], run through ns.sh on an unlocked box: every pinned name in
  <mount>/ai-models against the pin (unpinned files listed); --fix fetches what does not match
  (mirror, then upstream, checked), moves the old file to <name>.replaced, puts the new one in place
  with the volume's ownership and mode 600, and asks watchd to restart ghost.oracled so llama-server
  loads it (about ten seconds without a model on the GPU; searchd retries captions in flight).
- README 7b rewritten around the pins.

Tested in the sandbox with a fake Hugging Face and fake pins: pin_check on a matching file, a
same-size wrong-hash mmproj (named as the old upload), an unpinned file; stage_models.sh refusing
the wrong mmproj and staging the rest; setup_llama.sh's weights step: the default path with the
mirror off (both files by curl, pins checked), the rerun downloading nothing, Hugging Face serving
the old mmproj (refused, setup stops), --model/--mmproj/--embed from a "USB stick" (checked, the
missing pinned file fetched), --mmproj pointing at the old upload (exit 4), --models with the old
upload (exit 4); models_check.sh finding the old mmproj, --fix replacing it (old kept as .replaced,
new mode 600, hashes match), then all matching. Found on the way: the default branch ignored a
lone --mmproj (condition checked only --model); fixed. Not run: the real 7 GB download, the real
mirror's models set, the oracled restart through watchd.

## The app: the scanner breathing, the torch left on, the map's chatter, slow photos, trails, land over text

Vlad: "the app is weird: it zooms in and out randomly on the QR code, and it turns on the light but
does not turn it off after; the map has a lot of text , an option to enable debug on the app to
show all that; the images are slow to load when we select them; I want the trails by day drawn as
well; and the land overlaps the text."

- THE SCANNER BREATHED because its two zoom thresholds met across the 2x: a code at 4.9 px/module
  zoomed in (under 5), became 9.8 (over the 9.5 back-out line), zoomed out, became 4.9, zoomed in ,
  every half second. The back-out line is 14 now (a code that was 7 unzoomed), no zoom change
  follows another within two seconds, zoom-in needs a code actually in view, and the scanner backs
  out by itself when no code has been seen for four seconds so the next one starts wide.
- THE TORCH STAYED ON because bindToLifecycle follows the ACTIVITY, which outlives the scanner
  screen: a decode or back left the camera bound and the torch lit until the app was backgrounded.
  The screen's onDispose now turns the torch off, resets the zoom and unbinds the camera.
- THE MAP'S TEXT: the note line (photo count, detail level, landmass rings and points, the coast
  tiles' state) shows only in DEBUG MODE (settings › "set app in debug mode", the switch that
  already gates the tok/s); otherwise the map says nothing, except the one line that explains an
  empty map. The scanner's decoder commentary is behind the same switch.
- SLOW PHOTOS: ImageViewer fetched the ORIGINAL, the preview AND the thumb before showing anything
  (`listOf("original" to fetch(), ...)` evaluates every fetch first), then decoded the original at
  full resolution , a black screen for seconds per tap. Now the thumb is on screen at once, the
  1600px preview replaces it a moment later, and the original comes only when zoomed past 1.5x or
  on [ full ], decoded with inSampleSize to at most ~4000px on the long edge; decodes run off the
  main thread. The corner line says what is on screen.
- TRAILS: the other days' trails were drawn in the coast's own dim green over land the same
  shade; they vanished into the coastline. Amber now, every trail on a dark halo; the lit day and
  the phone's part stay green.
- LAND OVER TEXT: Compose does not clip a Canvas to its box, and a filled continent at street zoom
  is a rectangle the size of the screen , it painted over the title, the note and the trail panel
  below (strokes never showed this; the new fills did). clipToBounds on the map canvas.

Not run here: the app (no Android SDK in this sandbox).

## Names on the map: countries, regions, capitals, cities, towns, villages , from the GeoNames already on the box

Vlad: "for countries could I get cities and streets maybe? or is that too much? it's weird when I
zoom in on a location and I don't even know what it is, like the capital of Romania; on an island
it's ok."

Names first, because the box already has them: GeoNames' allCountries is on the volume and in
geo_points for the geocoder. What was missing was a way to say which name matters in a given view.

- geo_points grows `population` (GeoNames column 15) and `rank`, materialised at import by
  framed.labelRank: a country (PCL*, kind A, new) above everything in it; a capital (PPLC)
  8×population + 1M, so Ankara reads before Istanbul; a region (ADM1, kind A, new) 2×pop + 50k; a
  region's seat (PPLA) 3×pop + 100k; a plain place its population; a place with none known (most
  villages) 1, shown when the view is small enough to have room; sections of cities, abandoned
  and historical places 0, never shown. Partial index on (rank DESC) WHERE rank > 0.
- secd: GET /v1/geo/labels?minlat&maxlat&minlon&maxlon&n → {"labels":[{name,lat,lon,k,pop}]}, best
  first, n ≤ 200; k is C country, R region, X capital, P place. A world-sized window is a
  handful of index rows; a small one uses the lat/lon btrees; never a sort of millions.
- The phone fetches the names with the dots (same escape rule: a new request when the view
  leaves the fetched margin or the zoom moves ~1.6×): 40 at world span, 70 at country span, 120
  closer. Draw: ranked order, each label claims a rectangle, a lower-ranked label that would
  overlap is skipped , a town never sits on its country's name, a crowded coast shows the few
  that fit. Countries in capitals, wide and dim; capitals bright and bold; towns small; every
  name on a dark halo; a dot under a place name; all under the photo dots.

ON A BOX THAT IS ALREADY RUNNING the columns arrive with the schema (ALTER TABLE … IF NOT EXISTS
at the next secd start) but are zero until geo-import runs again:
    sudo ./tools/ns.sh ./bin/ghost-cli ghost.framed geo-import
Twelve million rows upserted, a while (watch framed's log: "geo import progress" every 100k rows);
the map shows names as soon as it finishes. Until then /v1/geo/labels answers an empty list and
the map draws none.

STREETS are a different order: OpenStreetMap's roads for the world are tens of gigabytes of
vectors, a planet PBF parser (protobuf + zlib, doable in the standard library, a few hundred lines),
a line cutter into the same one-degree tiles with three LODs (the polygon cutter is most of it),
and a phone that draws thousands of segments per tile. Per-country extracts (Geofabrik) rather
than the planet make it tractable , Romania is ~200 MB of PBF, Europe ~30 GB. A session or two of
work, not an evening; the mirror would carry the extracts and the box would cut them like the
coast. Not started.

Tests: labelRank's order (country > capital > region > seat > town > village = 1; a capital over a
bigger plain city; PPLX/PPLQ/PPLW/PPLH are 0; only PCL*/ADM1 in class A). Not run: the phone, and
the import on the real 12M rows.

## Streets: OpenStreetMap's roads for the whole world, cut on the box, fetched by the phone a cell at a time

Vlad: "let's do it all end to end, also give me the things I need to tell my mirror to get; we
have 8 TB, I'm quite sure we can hold the whole world and the phone can just get the granularity
when we move and the server pulls the right things."

The shape is the coast's, one level deeper. The box holds the continents' PBF extracts, cuts every
road into tiles once, serves a tile per HTTP request; the phone asks for the cells under its view
at the zoom it is at and keeps what it fetched. Nothing about a road ever reaches a phone unless
the phone is looking at that cell.

- internal/osmpbf , a PBF reader in the standard library: the protobuf wire format hand-decoded
  (varints, length-delimited fields, packed arrays), the blob framing (a big-endian u32, a
  BlobHeader, a Blob with raw or zlib data; other compressions are ErrFeature), the header's
  required features, then Nodes (plain and DenseNodes with their delta-coded ids, lat/lon and
  key/value runs) and Ways (keys, values, delta-coded refs) with the block's granularity and
  offsets applied. Scan(path, workers, fn, progress) reads blobs in order, inflates and parses
  them on N goroutines, and hands the blocks to fn IN FILE ORDER (an ordering channel per blob),
  so a caller that depends on id order (the node file below) sees it. Encode builds a small PBF
  for tests, so the reader's tests and the cutter's tests need no real extract in the repo.
- internal/roadtiles , the cutter. Two grids: level 1 is the coast's one-degree grid and holds the
  MAJOR roads (motorway, trunk, primary, secondary), what a screen 2.5° across should show; level 0
  is a tenth-of-a-degree grid (3600×1800) and holds EVERY road with its name. Three passes over
  each PBF: a bitmap over the node id space of the nodes the roads reference; those nodes' lat/lon
  to a flat file in id order (16 bytes a node, mmapped, binary search); then the ways, walked
  through the cells they cross, each crossing cut exactly on the cell border, each cell's piece
  appended to a per-cell buffer that flushes to disk past BufferMB. One last pass turns the
  buffers into tiles, writes index.bin (64,800 bytes for the major grid, an 810,000-byte bitmap
  for the fine grid) and swaps the directory in whole, so secd serves the old tiles or the new
  ones, never half. A tile ("GLR1") is pieces of {class, flags (one-way, tunnel, bridge, named),
  name, points quantised to 1/65535 of the cell , two metres in a 1° cell, twenty centimetres in
  a 0.1° cell}. A way with a missing node (an extract's edge) keeps the points it has; under two,
  it is dropped. cmd/ghost-roadtiles runs it by hand; framed runs it: `ghost-cli ghost.framed
  road-tiles`, and by itself at start when the PBFs under <mount>/geo/roads are newer than the
  tiles, one build at a time, state row "roadtiles".
- secd: GET /v1/geo/roadtiles/index (the index, ETag; 204 while no tiles exist) and
  GET /v1/geo/roadtile?l=<0|1>&x=&y= (one tile, ETag; 404 for a cell without one). Static files
  under <mount>/roadtiles, the same code path as the coast.
- fetch_geo.sh grew a `roads` set, OPT-IN: GHOST_GEO_ROADS=all (the eight continents) or a list of
  files. Mirror first (set `roads`, one file at a time, signed and resumable), Geofabrik itself as
  the fallback (https, resume, no pin , Geofabrik republishes every extract daily, so there is no
  fixed hash to pin against; the mirror's manifest is the pin). The PBFs stay under <geo>/roads;
  then bin/ghost-roadtiles cuts them, or on a running box framed does. health.sh prints a "map
  roads:" line.
- The phone: RoadTileGeom (the codec and the grid arithmetic, pure Kotlin, tested against the
  box's own fixture bytes), RoadTiles (a tile becomes one Path per class per detail level, plus
  the named roads' middle segments), RoadTileCache (an LRU of 60 built tiles, four fetches at a
  time, a failed cell retried after a minute), BoxClient.roadTileIndex/roadTile (index
  ETag-revalidated; tiles on disk up to 400 MB, trimmed to 300 by age). MapScreen asks for the
  major cells under the view from the coast's zoom and the street cells from ten times closer,
  draws the classes the zoom allows (major only, then tertiary and residential, then service,
  then tracks and paths), casing under fill, motorways amber down to paths grey, tracks dashed,
  over the land and under the trails; from thirty times the coast's zoom, street names rotated
  along their middle segment, biggest roads first, a claimed strip per name so none cross, at
  most 80 a frame, only where the road is longer than its name. The debug note says how many
  cells are wanted and here and the last failure.

WHAT IT COSTS. Geofabrik's continents today: Europe 32.6 GB, North America 18.1, Asia 15.2,
Africa 7.4, South America 3.8, Australia-Oceania 1.5, Central America 0.75, Antarctica 0.03 ,
about 79 GB of PBF. The cut: the node id bitmap is ~1.6 GB (OSM's ids are global, so a small
extract pays the same), the cell buffers 512 MB, and the node file wants the page cache (16 bytes
a road node; Europe's is on the order of 10 GB; a box with less RAM finishes, slower, because the
lookups become NVMe reads). Time: the synthetic benchmark (300k roads, 4.6M nodes, 28 MB) cuts in
3 s; the real thing is dominated by inflating 79 GB three times and by the node lookups , hours
for Europe, a day for the world is the honest guess, in the background, once. Output: on the
order of 15–25 GB of tiles for the world (four bytes a vertex, the major roads stored twice).
Do Europe first (GHOST_GEO_ROADS=europe-latest.osm.pbf), see the numbers framed logs, then the
rest. The PBFs stay on the volume beside the tiles: 80 GB of a box with 8 TB, and a re-cut when a
newer extract arrives needs no download.

A way that crosses a continent boundary is in both extracts (Geofabrik keeps it whole in each);
both copies are cut and the overlapping piece is drawn twice, on top of itself. Harmless, a few
bytes; noted so nobody hunts a "duplicate road" bug.

NOT DONE: no relations (route relations, turn restrictions , not drawn); no per-country extracts
in the fetch script (the mirror can carry them, GHOST_GEO_ROADS takes any file name the mirror
has); no house numbers, no POIs, no water, no rail; names in the tile are OSM's `name` only (no
name:en). The tile stores no elevation. Steps are drawn as paths. The phone's road paints are a
first pass at colours; the halo/casing widths are in screen pixels and will want a look on a
real screen at 3× density.

Tests: osmpbf (an Encode'd file , zlib blobs, DenseNodes with delta ids, negative coordinates,
ways with tags and refs , read back in file order on three workers under -race, fn's error stops
the scan, garbage is refused); roadtiles (tile and index round trip, the cell-border cut on a road across four
cells, a full Build from an Encode'd PBF with a missing node and a building, Stale, FindPBFs, the
bitmap and node file); the golden fixtures (road_fixture.lgr, road_index_fixture.bin) that both
the Go encoder test and the phone's RoadTileGeomTest (7 tests, JUnit, run here with kotlinc
against the fixture) hold to; secd's two routes (auth, 204 without tiles, bytes and 404 with,
ETag). Not run: the cut on a real extract (no Geofabrik in this sandbox; the synthetic 28 MB PBF
was the largest), the phone's drawing (no Android SDK here), the fetch from the mirror once the
web side publishes `roads`.

## The day as a route: stays, moves, on foot or by road, along the streets

Vlad: "the daily walking / moving route based on the data points." The box already had the points
(a fix a quarter hour, the photos' positions, the day's steps) and drew chords between them. Now it
tells the day.

- internal/roadtiles writes a ROUTING GRAPH beside the tiles (`graph/<x>_<y>.lgg`, the fine 0.1°
  grid): every road way split at the nodes it shares with another road (a second bitmap in pass 1:
  a node seen twice is a junction) and at the nodes the extract lacks, each stretch an edge with its
  ends' ids, class, flags, length and geometry, in the cell of BOTH ends (a reader dedupes). Tiles
  without a graph are stale, so a box that cut before this build cuts again (`-no-graph` for tiles
  only). The synthetic 300k-road bench went from 3 s to 5 s.
- internal/roadgraph walks it: loads the cells around two points, snaps each to the nearest road a
  person can walk (class ≥ primary, within 120 m), A* on edge lengths with the two snaps as virtual
  ends, the path along the roads' own geometry back. Beyond 8 km, off the roads (a beach, a boat),
  or no way between them: an error the caller draws a chord on. Tested on a synthetic town: the
  walk takes the streets and the diagonal footway, never the motorway; across a cell border; a
  gap is "no route" until a bridge way joins it.
- internal/dayroute tells the day: the fixes and photos in time order; a STAY is ten minutes or more
  within 100 m of the running centre (two quarter-hour fixes in one place; one fix is not); a MOVE is
  what lies between stays, WALKED when no hop was faster than 7 km/h, RIDDEN otherwise; each walked
  hop drawn along the streets when the router finds a way under 2.5× the chord + 100 m, else the
  chord. Totals on foot and by road; a note when the walk is longer than the steps allow; a one-line
  title: "near Strada A → Corner Café → Voutoumi · 1.4 km on foot, 10 km by road".
- framed: RebuildDay writes `<day>.route.json` beside the GeoJSON (the store names stays: an S
  spot within 300 m , beach, harbour, museum , else "near <place>" within 5 km; DaySteps from the
  health sync); the router is the road graph when the box has one; `ghost-cli ghost.framed
  day-routes [days=N]` rebuilds the last N; after a road-tiles build the last 60 days are told
  again on the new streets.
- secd: GET /v1/geo/route?d=YYYY-MM-DD (ETag, 404 untold, 400 for a day that is not a day);
  /v1/geo/tracks rows carry line, walkM, rideM, stays.
- The phone: the lit day fetches its route; walks in green along the streets, rides as dashed blue
  chords, each stay a ring with its name and hours (the raw line steps back to a thin thread while
  the route is up); under the day strip, the line, on foot / by road, the stays with their hours
  and photos. BoxClient.dayRoute, RouteStay/RouteMove/DayRoute.
- PASSIVE FIXES (PassiveFixReceiver): the OS hands the app a copy of every fix another app asks
  for , Maps open, the camera geotagging , delivered by PendingIntent so the process need not be
  alive, through LocationLog.record's own rules (25 m or an hour, coarse fixes confirm rather than
  move). No GPS of our own, no service, nothing new to a battery; the trail is denser exactly when
  the phone moves with someone else's GPS on. Registered with the worker, gone with it, back at
  boot. Settings says "(N from other apps' fixes)".

FOUND ON THE WAY: secd read the day paths from `<mount>/paths`; framed writes them to
`<mount>/frames/paths`. /v1/geo/tracks and /v1/geo/day answered an empty list on every box, and
the map drew the phone's own 48 hours and nothing older. secd reads frames/paths now (with the old
directory as a fallback where something wrote there).

Tests: roadtiles (graph cells, edges split at junctions and gaps, both-cell storage, Haversine);
roadgraph (the town, the same-street case, off-road, too far, no graph, across cells, a gap then a
bridge); dayroute (the told day with 46 fixes, 3 stays, a walk along the streets and a ride, the
step note, the JSON round trip; the edges: one fix, jitter, a stay with no move, no stays at all, a
ride by speed, photos alone); secd (the route body, ETag/304, 404, 400 on a path as a day, the
track row's line); framed's existing tests. Not run: the phone's drawing, the passive provider on
a real phone, a real extract.

## The web search, smarter: the model says what it needs, the box reads by meaning, one more round

Vlad: "improve the scraping to focus just on the data we need, make it smarter somehow." The phone
searched by the question's words and sent one keyword window per page. Three changes:

- THE NEED. Before the phone searches it asks the box (POST /v1/chat/plan → synthd /plan): the
  model, in one short call with the last four turns for what "it" means, states whether the question
  needs the web at all, the fact that would answer it in one sentence, its shape (number, date,
  name, list, howto, prose), whether it goes stale, and one to three searches, most specific first.
  Parsed tolerant of the model's wrapping, validated (dupes folded, three at most, none over 120
  chars). {"ok":false} and the phone plans by itself as before; in auto mode a "search: false" is
  final, in "on" mode the phone searches anyway. The first two searches run; the third is spare.
- THE PARAGRAPHS. The phone sends each read page's paragraphs (the twelve with the most question
  terms plus the first and the description, in page order, 6 KB a page) beside its old excerpt.
  synthd embeds the need and the paragraphs through searchd's new `embed` command (EmbeddingGemma,
  the child searchd already runs; 64 texts of 2000 chars) and keeps, per page, the three closest to
  the need in page order as the excerpt , the passage that answers, not the one that repeats the
  words. No embedder: a term-share pick stands in. The context event's note says how it read.
- ONE MORE ROUND. When the best paragraph is under 0.30 to the need (or nothing was found) and a
  spare search exists and this is round 1, the stream says `{"more":{"queries":[…]}}` and ends with
  `{"done":true,"more":true}` before the model speaks; the phone runs them all, merges by URL, and
  asks again as round 2. Never a third.

secd forwards need/round/spare and takes 256 KB of findings (was 32). Old phones and old boxes
interoperate: a phone without paragraphs gets the old excerpt path; a box without /plan gets the
phone's own plan.

Tests: parsePlan (fences, dupes, the cap, no-search plans, refusals), planPrompt (turns, date,
bounds), rankParagraphs without an embedder (the price paragraph wins, page order kept, a hit
without paragraphs keeps its excerpt), boundWeb, moreWeb's stream shape; WebSearchTest (the plan's
first/spare, worthSending, merge) , 8 JUnit tests run here. Not run: the model's actual plans (its
JSON discipline is the risk; the parser tolerates prose around it and refuses the rest), the
embedder over the socket, a phone.

## The memories, written: outings and days from a fact sheet, checked against it

Vlad picked "model-written text, grounded". prosePass (synthd, prose.go), only with the model on
the GPU (OnGPU; on the CPU the template stands):

- OUTINGS: three a pass, newest first, those without `meta.prose`. The sheet: the memory's own
  dates, days, photos, main place, country, places, trip/distance from home, distance moved, the
  top tags, the cover frames' SCENE captions (from search.originals; without access, without them),
  and the route lines of its days. The model writes two to four sentences in the second person,
  past tense, from the sheet only. groundedProse keeps it only when: 60–900 chars; no list, heading,
  refusal or markdown; and EVERY number in it is a number in the sheet (as written or its integer
  part: "2.3 km" allows "2.3" and "2", never "2.5"). Kept: body = prose, meta.prose and
  meta.template; outingPass carries the prose along while the template it was written from stands,
  and drops back to a fresh template (to be written again) when the facts change. Three failed
  tries and the template stays.
- DAYS (kind='day', source_ref='day:<date>'): a day with signal , two or more stays, a walk of
  3 km, five photos , from its route (stays with hours and names, moves with how far and how, the
  totals, the steps), its photos (count, places, tags, four captions spread across the day). Two a
  pass, the last 45 days, told once the day is over, told again when the day's line changes (new
  points), tries tracked in settings (no empty cards). Title "Friday 25 September 2026 · Corner
  Café, Voutoumi"; meta carries the line and up to five covers; the app shows a day's covers and
  "a day, from your trail and photos · <line>".

Tests: groundedProse (a good memory kept, an invented number refused, the integer-part rule, lists/
headings/refusals/markdown/short/preamble refused, cleanup of quotes, exclamation marks and
newlines), memoryPrompt, dayHasSignal, dayTitle, dayFacts from a route alone, kmText. Not run: the
model's prose (the check is what makes it safe to ship untried).

## The captions that closed without describing: why "1051 left" stood still with nothing queued

Vlad, with the status screen: 30336 photos, described 31858 of 32909, tagged 31893, "50/h · 2343
today · 1051 left, about 21 h", queue captions 0 · tags 0, last check a day ago. The queue was
EMPTY with 1051 frames undescribed: their jobs had run and completed without describing anything.
The one path that does that: a caption the model returned without the fixed `SCENE:` section ,
thinking that ran past the token budget, a refusal, prose with dressed-up headings ("**Scene:**")
the section reader did not read. Caption() stored anything over 20 characters; ApplyCaption wrote
the description only from a SCENE section; the job closed; every stock-take found the frame
undescribed, asked searchd, which found a caption in meta and re-applied nothing. And the
stock-take itself only ran at boot.

- search.NormalizeCaption puts the headings back into the contract's form (markdown, case, spacing,
  "Colors" → "COLOURS", each at the start of its line; text before the first heading dropped) and
  says whether a SCENE section exists. Caption() refuses a caption without one (the job retries with
  the backoff and parks visibly after five, where the queue line shows it) and stores the
  normalised text otherwise.
- ensureCaptioned (the stock-take's ask) discards a stored caption with no sections, drops the
  chunks it seeded the index with, and queues the frame again , the repair for the 1051.
- framed runs the stock-take every six hours, not only at start.
- The status line says "N left, nothing queued , the box re-checks every six hours" instead of an
  ETA computed from a queue that is empty.

On the box after the drop: `ghost-cli ghost.framed converge`, then searchd's log: "caption without
sections discarded, frame queued again" per frame is the proof; the described count moves within
the hour at the GPU's pace. If the count of those stays at zero and the 1051 do not move, this
diagnosis is wrong and the searchd log around one of the undescribed frames is the next step.

Tests: NormalizeCaption (markdown headings, a preamble, the colour spelling, a clean caption
unchanged, the section reader on the result; thinking, a refusal, prose without headings and an
empty SCENE refused).

## The map as a map: opens where you are, stops at the world, no grid, you are here; the viewer pans

- OPENS WHERE YOU ARE: the phone's last fix is known at once (prefs), so the first frame is the
  place, ~100 km across; without a fix ever, the newest photo at the same span; without either, the
  world. The never-blank fallback (an empty local view snaps to the world) now applies only on a box
  without map tiles , with the coast and the roads drawn, a view with no photos is still a map.
- ZOOM-OUT STOPS AT THE WORLD: minimum zoom 1 (the world fills the short side), and the centre is
  held so no edge of the world comes inside the screen (clampCamera after every gesture).
- NO GRID: the graticule draws under the debug switch only.
- YOU ARE HERE: on its own canvas over the map (the pulse redraws that layer, not the land and the
  roads): the fix's error circle when wider than the dot at this zoom (LocationLog.last carries
  the accuracy now), a pulse ring growing and fading, a white ring with a green heart, "you" beside
  it in bold with a halo , "you, 3 h ago" and dimmed when the fix is old. Under the title: "[ where
  I am ]" (a town's worth around the phone; tap again for the streets) and "[ the world ]".
- THE VIEWER: the gestures sat AFTER the graphicsLayer, so a finger's pan arrived divided by the
  zoom (100 px of drag moved a 3× photo 33 px). Pointer input before the layer now, in screen
  pixels; pinch zooms about the fingers, double-tap zooms about the tap, the offset is clamped so
  the photo never leaves the screen.

Not run: any of it on a phone (structural check only).

## The days, prebuilt: one summary a day in Postgres, growing as the box learns more; "on this day" reads them

Vlad: "we need the summaries of the days and when we look at history on this day we can just
prebuild it in the evening and slowly add more things for each day, save it in postgres, and the
summaries need to be better."

- TABLE day_summaries (schemadef; created at the next converge): day, built_at, version,
  signature, title, template, summary, written_by (template | model), model_at, prose_tries,
  tries_sig, facts JSONB. One row per day the box knows anything about.
- THE SHEET (days.go, gatherDayFacts): the photos (count, how many described so far, six covers
  spread across the day, places and country from the hierarchy, the top ten tags, five covers'
  SCENE captions), the route as framed told it (line, stays with hours and names, moves with how
  far and how, totals, fixes), raw trail points, the health sync (steps, sleep, exercise), the
  check-in's feeling, the journal's note titles, the chats started that day by title, and the
  outing the day belongs to ("day 3 of 5 of Antipaxos · 19–23 Sep 2026", away or not). Its
  signature (a hash of all of it) is what says the day changed.
- THE PASS (daySummaryPass, every ten minutes inside distillLoop, after the outings): today from
  20:00 UTC (template only), the last fourteen ended days every pass (late syncs, captions landing),
  and a slice of 26 older days walking back to the first photo or trail point (a watermark in
  settings; it starts over from the recent edge when the past is done). A day whose signature is
  unchanged and already summarised is skipped. Otherwise: the TEMPLATE always ("Day 3 of 5 of
  Antipaxos · 19–23 Sep 2026. Near Strada A → Corner Café → Voutoumi · 1.4 km on foot, 10 km by
  road. 14 photos around Gaios and Voutoumi, mostly beach, boat, sea, taverna and dog. 8,400 steps,
  35 min of exercise, 7h 10m of sleep. You said you felt tired but happy. You wrote: "Boat for
  Saturday". You asked the box about ferry times to Corfu."), and the MODEL when it is on the GPU,
  the day is over and has signal (two stays, a 3 km walk, five photos, a feeling, a note, an
  outing), four a pass, three tries per signature: from the sheet only, second person, what
  mattered first, the order of the day, the numbers as given, the evening last; groundedProse
  keeps it or the template stands. A model text is rewritten only when the sheet changed AND the
  last text is twelve hours old , captions land one by one and a rewrite per caption would be
  noise; the template refreshes every time.
- "ON THIS DAY" reads the rows (substr(day,6,5) index): each other year's summary, title, route
  line, covers, places and notes; years the backfill has not reached come with photos and places
  and no narrative until it does. No model at request time; the report cache is an hour.
- The memories feed: a day with signal that no outing already tells is projected as kind='day'
  (title, summary, covers, line); an outing's days stay inside the outing. Yesterday's dayProse
  and the old episodePass (the one-line 'episode' memories) are retired; existing episode rows
  stay where they are.
- secd: GET /v1/days?before=YYYY-MM-DD&limit=N (the feed with the sheet), /v1/onthisday carries
  title and line per year. ghost-cli ghost.synthd days [day=YYYY-MM-DD] [rewrite=true] [pass=true]:
  counts, the backfill's position, a day cleared to be built again (rewrite lets the model write
  it again at once), a pass now. health.sh: "day summaries N (M by the model), back to …,
  backfill at …".
- The app: the on-this-day card shows the day's title and route line above the narrative.

Days are UTC days, like the paths and the routes , a photo at one in the morning in Athens is the
previous day's. A local-day cut is a later change and touches all three.

Tests: dayTemplate/dayTitle/sheet over a full day and a thin one, the signature moving with a
caption landing, a model memory over the sheet passing and an invented one refused, thousands
separators in the model's numbers, joinAnd/thousands/topKeys. Not run: the pass against Postgres
(no Postgres here; the SQL is read, and the shapes match the schema), the model.

## The phone's model, working: it reads pages when the box is slow, and answers alone without a box

Vlad: "the app does the scraping and sends it to the server … even better, make the local model
work on the phone, the phone sends the summary from the scraping to the server: the dumber model
on the phone summarises and the smarter model on the server pulls it together."

The honest number first, and the design follows it: a 2B model on a phone CPU reads ~70–150
tokens a second (llama.cpp measured ~72 t/s prompt, ~15 t/s generation for a 2B Q4_K_M on a
Galaxy S23 Ultra); the 4070 reads the same pages in about two seconds. So the split is ADAPTIVE
(Vlad's pick): the box on its GPU still gets the paragraphs; the phone's model reads the pages only
when that is faster than the box , the box on its CPU, or a box that did not answer the plan in
time , and it answers by itself when there is no box.

THE PHONE MODEL NEVER RAN. The native pieces were written and never built: gradle had no
externalNativeBuild, the llama.cpp pin in CMakeLists.txt was the literal
"e9fb3b3REPLACE_WITH_FULL_40_CHAR_SHA", so System.loadLibrary failed and LocalModel said ABSENT
forever. And the JNI would not have survived first contact: the KV cache was never cleared (the
second question decoded after the first one's tokens), the whole prompt went to llama_decode in one
batch (fails past n_batch , a web page is thousands of tokens), a prompt longer than the context
was undefined, pieces went to Java through NewStringUTF (modified UTF-8: the process ABORTS on an
emoji or on a character split across two tokens), and no chat template was applied.

- BUILD: gradle reads the pin; when it is a real 40-hex commit it wires CMake (arm64-v8a, static
  libc++, -march from -PllamaArch, default armv8.2-a+dotprod+fp16; armv8.6-a+dotprod+i8mm+fp16 is
  faster on Snapdragon 8 Gen 1 and later), else the APK builds without the runtime and says so
  (BuildConfig.HAS_PHONE_MODEL, LLAMA_CPP_COMMIT). CMake: static llama.cpp inside one .so, no
  tools/server/curl/OpenMP, GGML_NATIVE off, the fetched source verified against the pin (or a
  local checkout with -PllamaSrc=…, verified when it is a git checkout). Pin it to the box's:
  `git -C /opt/localghost/llama.cpp rev-parse HEAD` (Gemma 4 needs a 2026 llama.cpp; the box's
  reads Gemma 4 already).
- JNI (llama_jni.cpp, rewritten): backend init once per process; a fresh memory per call
  (llama_memory_clear); prefill chunked by n_batch; the prompt cut to fit n_ctx (head kept, the
  generation cue re-appended); Gemma 4's turn format , `<|turn>system\n…<turn|>\n<|turn>user\n…
  <turn|>\n<|turn>model\n`, thinking off , when the vocabulary has those tokens (llama.cpp's
  built-in template list knows only the classic `<start_of_turn>`), else the model's own template
  through llama.cpp, else Gemma classic; stop on `<turn|>`/`<end_of_turn>` as well as EOG; text
  to Java as bytes, whole UTF-8 sequences only; greedy at temperature 0, top-k/top-p otherwise;
  llama_perf numbers back per call; a mutex per handle; nativeFree waits for a running call.
  Compiled on the host against a stub of the upstream API (declarations read from llama.h master),
  and the UTF-8 splitter and stop pieces unit-tested there.
- LocalModel: complete(system, user) with a lock, the measured speed remembered as a moving
  average (LocalModel.Speed, a guess of 60/12 t/s until the first real call), four threads at most
  (the big cores), n_ctx 4096, the weights dropped after four idle minutes, the thought channel and
  turn markers stripped from what comes back.
- PhoneReader: Plan.route decides BOX_READS / PHONE_READS / PHONE_ALONE / NOBODY from what the box
  said on /plan (onGPU, measured prompt t/s) and the phone's own measured speed , phone reading
  plus the box reading ~150 tokens of notes a page must beat the box reading the pages by 20%.
  digest(): each page read into at most five note lines within a 45-second budget sized by the
  phone's speed (a page gets up to 1400 tokens; under 250 is not worth reading), every number in a
  note must appear in the page or the line is dropped, and each page carries its best paragraph
  verbatim as a QUOTE; a page there was no time for goes as the quote alone. answerAlone(): the
  lifeboat answers from its notes, citing [n].
- synthd: /plan now reports the box's speed ({"box":{"known","onGPU","promptTPS","genTPS"}}) and,
  with the model on the CPU, answers at once without the model (the plan itself would take a
  minute). The web block gains kind "note": NOTES and QUOTE per page, and one line telling the big
  model to trust the quote over the notes and treat an unsupported note with care.
- The chat: web questions route through PhoneReader.Plan.route; the status line says who read what
  ("2 of 3 pages read into notes on this phone in 38 s (the box is on its CPU) , asking your box…").
  Without a box (or local forced): search on the phone, digest, answer by the phone's model,
  streamed, findings numbered under the answer.
- THE MODEL: Gemma 4 E2B, Unsloth's QAT mobile build, UD-Q2_K_XL, 2.19 GB, Apache 2.0, pinned in
  tools/phone_model.pins (kept apart from model.pins, which setup_llama.sh stages for the box's own
  llama-server). `sudo ./tools/phone_model.sh` fetches it (mirror set `phone`, else the pin's
  upstream), checks it, installs it into /var/lib/ghost/models with the catalogue secd already
  serves (/v1/models, /v1/models/<id> resumable); `--file <gguf>` for a copied file, `--check` to
  see what is offered. The phone pulls it from the box (MODELS in the menu) and checks the hash.
  health.sh: "phone model offered: …" under oracled. The MODELS screen says what the phone can do:
  runtime in this build or not, model or not, measured speed, load time, prompt format.

Tests: PhoneReaderTest (who reads in eight situations, per-page tokens, page text cutting, notes
kept only where their numbers are in the page , thousands separators understood , "NOTHING",
five lines, long lines cut; the quote; the prompts; the machinery stripped; the average) and
WebSearchTest, 13 JUnit, run here against the real coroutines library; synthd (notes and quote
in the web block, the caution only with notes, planBox on CPU and GPU); phone_model.sh (refuses a
file that is not the pinned one, installs one that is, writes a catalogue secd's registry reads and
verifies, --check, a rerun is a no-op); the JNI compiled against the upstream declarations with
-Wall -Wextra. NOT RUN: anything on a phone , the native build (no NDK here), the model's notes,
its speed on Vlad's phone, Gemma 4 E2B's real turn tokens in the vocabulary (the probe falls back
if they are not what the Hugging Face template says).

## The phone builds llama.cpp from the mirror's tarball; the embedder under the mirror's name

Vlad, with the mirror's publish log: the mirror already carries `llama/llama.cpp-v0.5.0-7fe450e.tar.gz`
(its SHA-256 pinned on the web side), and now `embeddings/embeddinggemma-300m-qat-Q8_0.gguf`.

- THE PHONE'S llama.cpp is those bytes, not a git fetch from GitHub by commit. CMakeLists.txt pins
  LLAMA_CPP_TARBALL and LLAMA_CPP_SHA256 (tag and commit kept for humans); at configure time CMake
  reads the mirror's MANIFEST.txt only to find WHERE the file is (builds are pruned, so the path is
  looked up, never written down) and FetchContent downloads it with URL_HASH SHA256=<pin>, so any
  other bytes are refused. -PllamaTarball=<file> builds from a local copy (the box keeps its
  verified one in /opt/localghost/llama.cpp.mirror-dl/). gradle builds the runtime when the SHA-256
  pin is set; the provenance file records it. No GitHub, no git at build time; the phone and the
  box run the same llama.cpp. (My first version pinned a git commit and told you to read it with
  `git rev-parse` on the box: a box built from the mirror has no .git, so that would have failed.)
- app/android/tools/pin_llama.sh writes the pin: from the mirror's manifest (its signature checked
  against the site key by fingerprint when gpg and tools/mirror-key.asc are at hand, the way
  mirror_fetch.sh does; refused on a bad signature), or from a tarball you copied (--tarball),
  optionally cross-checked against the box's own hash (--box-sha). The manifest is disallowed to
  crawlers, so I could not read the hash from here; the script does it on your machine.
- Tested here against a local fake mirror: no key (pins, says the signature was not checked), a key
  that did not sign (refused), a good signature (pinned), a box hash that differs (refused), a local
  tarball (pinned; gradle's own regex reads it as buildable); and the CMake file end to end on the
  host: the manifest lookup, the download, the hash check, the configure, and a build of
  liblocalghost_llm.so from a fake llama source against the stub header; a tampered tarball fails
  URL_HASH; a manifest without the pinned hash stops with what to do.
- THE EMBEDDER: nothing asked for the mirror's name. setup_llama.sh now fetches set `embeddings`,
  `embeddinggemma-300m-qat-Q8_0.gguf` (pin_check when model.pins has its line; the manifest's
  signature otherwise), keeps an existing embeddinggemma-300m-q8.gguf when the mirror has none;
  --embed <file> keeps a known name. stage_models.sh stages either. searchd picks the QAT build when
  present, else the old q8, and takes the model ID from the file's name.
- A CHANGED EMBEDDER re-embeds the archive: vectors from the old model are invisible to queries
  from the new one (search's emb_model predicate), so moving a box to the QAT build would have
  quietly emptied semantic search. At start, searchd queues every chunk whose vector came from
  another model (embed jobs, 64 ids each), once, not while a previous re-embed is still queued, and
  logs "embedding model changed: chunks queued to be embedded again".
- model.pins says how to add the embedder's pin line from the manifest.

Tests: pickEmbedModel (none, old only, an empty file ignored, both: the QAT build wins,
defaultConf). Not run: EnqueueReembed against Postgres (none here; the SQL is read against the
schema), the phone build with the NDK.

## Setup takes from the mirror and nowhere else; llama.cpp only from its tarball; the phone's model at setup

Vlad, with the web side's full mirror contract: "If the mirror is unreachable, say so and stop. Do
not fall back to the upstream URLs; the point of the mirror is that a box never trusts a file that
wasn't in a signed manifest." Until now every setup script tried the mirror and then went upstream.

- tools/mirror_fetch.sh follows the contract end to end. The manifest is read up to three times, a
  few seconds apart, before a signature that does not verify is a failure (a publish writes the two
  one after the other). A single file comes with its set's NOTICE.txt and TERMS-*.txt, which every
  caller keeps beside it. A verified file gets a record, `<dir>/.<name>.sha256` (hash, size,
  mtime): a rerun with the record matching the manifest neither downloads nor re-reads it (the
  33 GB Europe extract is not hashed on every run); a file without a record is hashed once. A
  download that does not match is deleted and fetched once more; a second mismatch fails that
  file. A 404 means the build was pruned: the manifest is read again (twice at most) and the fetch
  carries on from the new build. A half download of an older build's version of a file is removed.
  Exit codes: 0 fetched, 1 failed (mirror unreachable, bad signature, a file that would not match,
  the key file not the site key , these used to be 3), 3 nothing to fetch (the set or file is not in
  this build, or GHOST_MIRROR=off). GHOST_MIRROR=file:///<dir> reads a copy of the mirror on a disk
  (a box with no internet), signature and hashes checked the same way.
- NO UPSTREAM unless the operator says GHOST_MIRROR_UPSTREAM=1, and then loudly, each line saying
  the file is not from a signed manifest. setup.sh (Go: go.dev), setup_llama.sh and
  models_check.sh --fix (weights: Hugging Face, still checked by the pins), fetch_geo.sh (GeoNames,
  Natural Earth, osmdata, Geofabrik), phone_model.sh (Hugging Face). Each says whether the set is
  not published yet or the mirror failed, and stops that step: Go and llama.cpp stop setup, the
  weights stop setup_llama.sh, the embedder, the phone's model and each geo set are said and
  skipped (the box works without them; a rerun fetches them). Files copied over by hand (--models,
  --model, phone_model.sh --file) are still taken, checked against the pins.
- LLAMA.CPP: "Build this, never clone master." setup_llama.sh no longer pulls or clones. The source
  is the mirror's set llama, one tarball; the folder inside must be named after the commit in the
  tarball's name or it is not built. It unpacks into /opt/localghost/llama.cpp with its notice and
  terms, and .mirror-src / .mirror-commit / .mirror-sha256 beside it; a new name replaces the folder
  and rebuilds. cmake gets -DLLAMA_BUILD_COMMIT=<commit> (and the build number for a bNNNN tag) so
  `llama-server --version` names what it is instead of "unknown", and -DLLAMA_CURL=OFF (the engine
  downloads nothing, ever; libcurl is no longer installed for it). A box whose llama.cpp is still a
  git checkout (xyntai) gets the mirror's source on its next setup_llama.sh run: one CUDA rebuild,
  the old binary runs until the new one is installed. Mirror away and the mirror's source already
  here: that copy is built. Mirror away and only a git checkout: said, and it stops.
  --llama-tarball <file> takes a copy you brought, still only when its hash is the manifest's (a
  copy that is not it is replaced by the mirror's, and said).
- THE PHONE'S MODEL AT SETUP ("if in doubt, setup"): setup_llama.sh's last step runs
  phone_model.sh (GHOST_PHONE_MODEL=0 skips it). A miss is said and setup completes; the summary
  line says whether phones are offered it.
- NOTICES AND TERMS travel: the weights' and the embedder's go onto the volume with them
  (NOTICE-models.txt, NOTICE-embeddings.txt, TERMS-*.txt, staged by stage_models.sh and ingested at
  unlock like the weights), llama.cpp's into its folder, Go's into /usr/local/go as MIRROR-*.txt,
  the phone model's beside it in the system area, the geo sets' beside their files (as before).
- setup.sh's Go minimum is now go.mod's version (1.25.4), not 1.25: an older system Go passed the
  check and then the go command fetched the newer toolchain from the internet by itself.
- health.sh prints the engine's provenance under oracled ("engine: llama.cpp-v0.5.0-7fe450e.tar.gz
  (commit …, from the mirror)", or that it is still a git checkout).
- READMEs: 0b rewritten for the mirror-only rule, what stops what, GHOST_MIRROR_UPSTREAM, the disk
  copy, and that rerunning the scripts is the update (there is no `ghost update` command yet); 7b
  and 7d updated; section 3's stale "paste the git SHA" paragraph now points at pin_llama.sh.

Tested here against a local signed fake mirror (a throwaway key, two builds): one file with its
notice and terms; a rerun that neither downloads nor hashes (a file changed under the same size and
mtime is trusted by its record, by design; a changed mtime is hashed, found wrong, and replaced); a
set and a file not published (exit 3, said); a whole set; a first download wrong then right; two
wrong (fails, nothing installed); a manifest whose build was pruned (404, re-read, done from the
newer build); a replayed older manifest (refused); a tampered manifest (three reads, refused); the
mirror unreachable (exit 1); the wrong key file (exit 1); file:// (works). setup_llama.sh's llama
step with apt-get and cmake stubbed: fresh from the mirror (commit checked, provenance written,
cmake given the commit), a rerun (no rebuild), the mirror away with its source here (builds it),
the mirror away with a git checkout (stops), a git checkout with the mirror up (replaced), a wrong
--llama-tarball (replaced and said). phone_model.sh: from the mirror, rerun, unreachable (nothing
installed, exit 1), upstream with the flag. fetch_geo.sh: geo from the mirror, roads and land
polygons not published (said, nothing from upstream). Not run: a real CUDA build of the tarball;
whether this llama.cpp's CMake honours LLAMA_BUILD_COMMIT (if --version still says unknown, the
provenance files are the record); setup.sh's Go step (read, and the same pattern as the others).

## Before the redeploy: the whole drop checked end to end, four fixes

Vlad, before redeploying the server and reinstalling the app.

What was run: the whole server built, vetted and tested (31 packages, all pass) from a clean copy
of the tree; then against a real Postgres (16, no pgvector) with the schema a box converges to at
unlock; the app's 113 unit tests (every test file, the pure sources with small Android stubs); a
structural compile of all 101 app sources compared with the original tree; every endpoint the app
calls matched against secd's routes (58, all present) and the JSON of the new ones (route, plan,
chat, tracks, on this day, models) field by field on both sides.

Found and fixed:
- A NEW BOX COULD NOT BE PROVISIONED. The app schema blob (datastore.go, one psql transaction at
  provision and at every unlock) created the frame_tags_hash index above the frame_tags table, so on
  an empty database the whole blob failed: "relation frame_tags does not exist". Existing boxes never
  saw it (the table was already there). The index now follows its table.
- The embedder switch: chunks whose model was never recorded would not have been queued for the
  re-embed (emb_model <> $1 is not true for NULL); IS DISTINCT FROM now.
- The app's chat stream read the closing {"done":true,"more":true} of a second-search round as a
  "more" event (its "more" is a flag, not the object); harmless (the stream ended at EOF) but it
  swallowed the done. Only the object form is a "more" event now.
- framed's log said "a road file is newer than the tiles" when it was re-cutting because the tiles
  had no street graph yet; it now says which (roadtiles.StaleWhy).

New tests, both skipped unless GHOST_PG_SOCKET_DIR points at a Postgres:
- internal/hw/sqlprepare_pg_test.go PREPAREs every SQL statement written as a literal anywhere in
  the server (246 of them) against the full schema: every table, column and parameter type is
  resolved by Postgres itself, the way poltergres's Parse asks at run time. Fragments completed at
  run time, pgvector queries (no extension here) and Sprintf templates are skipped (9).
- cmd/ghost.synthd/days_pg_test.go runs the day pipeline on real rows: photos with places, tags,
  trail points, steps, a check-in, a note, a chat; the summary row and the day memory are written,
  an unchanged day is left alone, a late sync rebuilds it, and "on this day" tells it.

    GHOST_PG_SOCKET_DIR=/tmp GHOST_PG_PORT=5432 GHOST_PG_USER=postgres go test ./internal/hw/ ./cmd/ghost.synthd/

Still not verifiable here: the Android build itself (no SDK or Maven in this sandbox; the structural
check finds no syntax errors, redeclarations or calls to our own functions that do not exist, and
nothing new beyond missing-SDK noise), the native llama.cpp build, and a live box. One app unit test
fails, QrSamplerTest.recoversRotated10Degrees (a synthetic 10-degree QR read as version 37 instead of
25); it fails the same way on the original sources, so it is not from this work.

## tools/update.sh: the box's data from the mirror, in one command

Vlad, after the redeploy: "the redeploy did not pull the maps and everything else". It never did.
redeploy.sh ships code (build, stage, restart) and reaches no network. The data came only from the
setup scripts, one by one, and on a running box they had to be run through ns.sh by hand. The
contract's "ghost update does the same walk" had nothing behind it.

**What update.sh does.** `sudo ./tools/update.sh` runs on an unlocked box. It reads the mirror
first and stops if it cannot. Then it walks five steps: maps, embedder, weights, phone, engine.
Naming steps runs only those. Each step fetches only what the mirror lists differently from what
is there, and hands the result to the daemon that uses it:

- **maps.** `fetch_geo.sh` runs through ns.sh's host-side door onto `<mount>/geo`. It no longer
  cuts tiles itself (`GHOST_GEO_NO_CUT=1`). Afterwards the step chowns to the volume's owner and
  asks ghost.framed for `geo-import` when place names changed, `geo-tiles` when the coastline
  changed or its tiles are missing, and `road-tiles` for new streets. framed does these in the
  background and serves the old tiles meanwhile.
- **embedder.** EmbeddingGemma QAT goes into `ai-models` with its notice and terms, and
  ghost.searchd is restarted. It picks the QAT build and queues the archive to be embedded again.
- **weights.** `models_check.sh --fix`.
- **phone.** `phone_model.sh`.
- **engine.** `setup_llama.sh --build-only`. It rebuilds only when the mirror's tarball changed,
  or when this box still has a git checkout (xyntai does, so the first run is one CUDA rebuild).
  The new `llama-server` is installed onto the volume beside the old one, renamed over it, and
  ghost.oracled is restarted. It refuses to put a CPU-only build over a CUDA one.
- **setup_llama.sh** now finds nvcc under `/usr/local/cuda*/bin`. Root's PATH, and anything run
  from a script, usually lacks it, and that is how a box ends up with a CPU-only engine.

**How fetch_geo.sh knows what is current.** A set installed from the mirror leaves a record:
`.mirror-geo`, `.mirror-landpolygons`, and the roads files' `.<name>.sha256`. When the mirror's
current build lists other bytes, the set is fetched again. A set installed before records existed
is kept, and the output says so (`GHOST_GEO_REFRESH=1` takes the mirror's). Roads that were never
asked for are not added. Continents the box already took from the mirror are kept current.
`mirror_fetch.sh --list <set>` prints the current build's files, with the signature checked and
nothing downloaded.

**redeploy.sh** says in its header and its closing lines that data is update.sh's job.

**Tested here** against the signed fake mirror and a fake unlocked box: a stand-in ghost.secd
process, so ns.sh's `/proc/<pid>/root` door is real, and stub ghost-cli and ghost-ctl.
- A first run fetches geo, the coastline, the embedder and the phone model, and builds the
  engine. It asks framed for geo-import and geo-tiles, and asks watchd to restart searchd and
  oracled.
- A rerun reports everything current and asks for nothing.
- A newly published countryInfo and coastline are fetched again, re-imported and re-cut.
- `GHOST_GEO_ROADS=europe…` fetches the extract and asks for road-tiles. A rerun without it keeps
  the extract current and asks for nothing.

**Not run here:** a real box, ns.sh against a real namespace, CUDA.

**First real run on xyntai (2026-09-28).**
- **Engine refused, and the check was wrong.** The mirror's tarball unpacks into
  `llama.cpp-7fe450e19305b828c199d602c23a8337aaa1f03b/`. I had required the folder to start with
  the commit. It now takes the full 40-hex commit from anywhere in the folder name, requires it to
  match the short one in the file name, and requires exactly one top folder. I tested it on five
  layouts: `llama.cpp-<full>` and `<full>` are built; `llama.cpp-master`, two folders, and another
  commit are refused. `.mirror-commit` now holds the full commit. The stop message no longer
  assumes the mirror was away.
- **Weights.** The volume's `gemma-4-12b-it-Q4_K_M.gguf` was 1,440 bytes short of the pinned
  build, a different upload under the same name. It was replaced from the mirror and oracled
  restarted.
- **models_check.sh cleanup hint.** It printed `ns.sh rm /proc/<pid>/root/...*.replaced`. The
  host shell expands that glob where the volume is not mounted, so the hint did nothing. It now
  names each file by its path inside the namespace.
- **Embedder and phone model** came from the mirror as designed.

## The map shows one day at a time; lines that never happened; the category backlog that never moved

Vlad sent two reports. The map: "some of the travel lines are too much, and they are off, I only
have 1 line coming from the continent, the other are made up… we should just have the last day
plotted on the map and have the others just go back in time". The image processing: "stuck again",
with Box Status at 25% of frames at the latest stage and 24,420 left, while the GPU sat at 0%.

**The map (app).** It draws one day: the newest (today when there is one). `‹` and `›` in the
trail row step to older and newer days, and a step frames that day. The strip picks a day, and an
"all days" chip lays the last sixty over the map as before. The day shown when the map opens does
not move the camera; it still opens where you are. The scrubber's ring is drawn only while the
trail panel is open.

**Lines that never happened.**
- **Phone, `LocationLog.record`.** A coarse fix (radius over 200 m, a tower's or wifi's guess)
  never moves the trail now. Before, a coarse fix whose circle did not hold the last point was
  taken as a move. On an island a phone latches onto the mainland's towers, and each latch was
  drawn as a crossing. A coarse fix is now only a heartbeat at the last place.
- **Box and phone, rule 4 in `clean.go` and `TrailClean.kt`.** A parked excursion is dropped: two
  or three fixes far away (each hop at least 3 km), within 2 km of each other, reached and left at
  car speed or better, between still fixes, and back within 90 minutes. That is a phone on a
  distant tower for a while. Rule 3 already did this for a lone point.
- **The price.** A drive shorter than one sampling gap each way, with a short stop at the far end,
  is not drawn. The raw points stay in the database.
- **Fixture.** It gains `tower_lock`, `tower_lock_three`, `errand_with_route` (kept) and
  `long_visit` (kept). Both tests fail with the rule switched off and pass with it.
- **Day route.** The photos now go through the same rules as the fixes. A photo from a camera with
  a wrong clock sits at the right place at the wrong time, and made a trip there and back in the
  route. It stays a dot on the map.
- **Diagnostic.** `ghost-cli ghost.framed trail day=YYYY-MM-DD [km=2]` lists every stored point
  that makes a long hop or that the rules drop. Each line gives the time, the place, the source
  (phone, watch, google-timeline), the hop and its speed, and kept or dropped. Use it to see which
  points still draw a line and where they came from.

**The category backlog (searchd), the "stuck" pipeline.** "At the latest stage" requires every tag
of a frame to have a category. 24,339 frames had a tag without one, and a night of backfill moved
that by ten. There were two reasons.

1. **The parse.** The categorize prompt asked for "one per tag, in the same order". A model that
   answers one pair per line, or numbers them, gave `ParseTags` a newline-joined blob, which a
   comma split reads as one bad tag. Every tag in the job stayed empty and the job still
   "succeeded".
2. **The retry loop.** A tag the model did place nowhere stayed empty by design, so the stock-take
   queued the same frames again every pass, 5,000 at a time. The same frames were asked the same
   question forever and the rest of the backlog never came up.

The fixes:
- `splitTagList` splits on commas and new lines and drops list markers ("1.", "-", "**").
  `ParseTags` uses it too.
- `AssignCategories` matches by name, forgiving case, hyphens and a plural s. When the answer has
  exactly one pair per tag, it matches by position. It reads "beach: place" and "beach (place)"
  too.
- A tag the model, having followed the format, places nowhere gets `other`, the display bucket, so
  it is never asked about again. An answer with no pair at all is a failed call, retried, and the
  failure line in the log carries the start of the model's answer.
- Categorize asks ten tags per call.
- The backlog is queued whole: framed asks for up to 40,000 in one INSERT…SELECT instead of 5,000
  inserts. Categorize jobs parked by the old parser are given back.
- The worker takes at most 30 categorize jobs per tick, so new photos are captioned and tagged
  within a tick, not after the backlog.

**Tested.** New unit tests cover `AssignCategories` in seven shapes of answer (and three
non-answers) and `ParseTags` on numbered lines. On Postgres: the queue (limit, no frame twice,
"other" counts as done, a parked job is given back), and the SQL prepare test still resolves every
statement. On the phone: TrailClean on the shared fixture and all 113 app tests (the one
pre-existing QR failure is unchanged).

## The check-in: more feelings, two ticked from the day, and a voice note transcribed on the box

Vlad: "can we expand the list of things we can feel in the check in and maybe preselect some, and
let me record a voice note on the daily check in, maybe that's what gets me going to record the day
as a journal".

**Feelings (app, `checkin/Feelings.kt`).** 38 feelings in five groups: the four quadrants of the
usual mood meter (bright, easy, tense, heavy) and "mind" (focused, curious, reflective, nostalgic,
distracted, unsure). The twelve from before are all still there, so past check-ins keep their
words. The card shows a quick row of eight first: the box's guesses from the day, then the
person's own most-picked feelings from the last 30 check-ins, then common ones. The rest are
behind "more feelings". Up to four a day (was three).

**Preselected.** The box's first two guesses are ticked before the person looks, marked "·" as the
box's, one tap to untick. The check-in text gains a `Preselected:` line naming them, so a later
look at the moods can tell a guess left standing from a feeling picked. `hw.DayContext`'s guesses
are strongest first now and read more of the day: under six hours of sleep → tired, seven and a
half or more → rested, 9,000 steps or 30 min of exercise → energised, 20,000 steps → tired,
somewhere green or by water → calm, 25 photos or three places → curious, a short night and a long
day → stressed.

**The voice note.** On the check-in card: record, stop, listen, again, drop. The take goes with the
check-in when it is saved, and after the check-in the card offers "say more about today" for more
notes to the same day. The whole path:

- **Phone.** `voice/VoiceCapture.kt` records the microphone into a 16 kHz mono 16-bit WAV (what
  whisper reads, 1.9 MB a minute) in the no-backup folder, 20 minutes at most. The screen stays on
  while recording, because Android gives a background app silence from the microphone.
  `voice/VoiceNotes.kt` queues a saved take (`<id>.wav` + `<id>.json`) and sends it to `POST
  /v1/voice`. The id is made on the phone, so a retry after a lost answer is the same note. The
  phone deletes its copy only when the box answers 2xx. Retries run when the app opens, from the
  MEMORIES screen, and in the 15-minute poll. A take whose app died before it was saved is found
  again after two minutes and kept as a note of that day.
- **secd, `voice_http.go`.** `POST /v1/voice` spools the body to `<mount>/voiced/inbox/<id>.wav` and
  then `<id>.json` (kind, day, start time, device). It checks only the RIFF/WAVE magic and allows
  128 MB. `GET /v1/voice/notes`, `GET /v1/voice/audio?id=` (Range works) and `POST
  /v1/voice/delete` complete the set. A delete removes the audio, the row and the journal entry. A
  memory synthd already distilled from the note stays until the person deletes that too.
- **ghost.voiced (no longer a stub), `internal/voiced`.** Each pass (15 s) moves every complete
  pair into `voiced/archive/YYYY/MM/<id>.wav` and records it in `voice_notes` as pending. It then
  transcribes the pending notes newest first with whisper.cpp's `whisper-cli`: `-l auto` (English
  or Romanian, detected per note), `-ng` (the GPU stays the chat model's), at most four threads,
  nice 10. A WAV in another format is converted to 16 kHz mono first. whisper's output goes to
  `voiced/work` on the volume, never the OS disk, and is deleted after reading. Markers such as
  `[BLANK_AUDIO]` and a segment repeated back to back are dropped. The transcript becomes a
  journal entry (source `ghost.voiced`, ref `voice:<id>`, at the time it was recorded). A note
  whisper fails on three times is marked failed with its error. `ghost-cli ghost.voiced voice`
  shows counts, engine and the newest notes. `ghost-cli ghost.voiced voice-again id=<id>|failed`
  puts notes back in the queue.
- **No speech engine yet = notes wait.** Without `whisper-cli` and a `ggml-*.bin`, the notes are
  archived and stay pending, and health says so (degraded, with the reason). ghost.voiced looks for
  the engine on every pass, so the first pass after it arrives transcribes the backlog.
- **The check-in knows its note.** The check-in text carries `Voice: <id> m:ss`. `/v1/checkins`
  returns each check-in with its note's status and transcript, and the past check-ins list shows
  them under each day.
- **synthd.** A voice note's words go into the day's sheet (`Spoken`): the template says "You said:
  “…”", and the model's prompt gets the transcript, marked as machine-transcribed. The note also
  becomes a journal entry that synthd distils like any other.
- **Evening reminder.** It now says "a couple of feelings are already ticked from your day ,
  change them, or say a minute about it in a voice note". Still one ask a day, nothing after.

**The speech engine: `tools/setup_whisper.sh`, mirror only.** Two new mirror sets. `whisper` is
one tarball, `whisper.cpp-<tag>-<commit>.tar.gz`, with one folder inside named after the full
commit, checked like llama's. `speech` is a ggml model, `ggml-large-v3-turbo-q5_0.bin` suggested.
The build is CPU-only and static. At setup it is step 6/6 of `setup_llama.sh` (GHOST_SPEECH=0
skips it) and stages both for the next unlock. On a running box, `sudo ./tools/update.sh speech`
puts both straight on the volume. Exit 3 while the mirror does not list the sets: the notes are
kept and wait. `health.sh` prints the voice counts and the engine.

**Tested here.**
- Go: WAV parsing, including an unfinished header and a non-WAV. Stereo 48 kHz converted to 16 kHz
  keeps its tone. whisper's JSON with markers and a repeat. The engine search picks the largest
  model and never a VAD model. `Transcribe` against a stand-in `whisper-cli`: JSON, text-only
  output and a failure.
- Postgres: an inbox pair archived, transcribed, journaled, joined to its check-in, listed and
  deleted, with a non-WAV sent to `rejected/`. Also the day sheet with a voice note, and the SQL
  prepare test (now 261 statements).
- `setup_whisper.sh` against a signed `file://` test mirror: build, install, a rerun that fetches
  nothing new, "not published" (exit 3) and "unreachable" (exit 1).
- App: Feelings and the WAV writer (8 JUnit tests). The phone's WAV header is byte for byte the
  one the box's test asserts. The recorder and queue ran on the JVM with a stand-in microphone:
  record, stop, queue, unreachable box, 503, sent with the right headers, orphan recovery, too
  short.

**Not tested.**
- A real whisper.cpp. GitHub and Hugging Face are out of reach from where this was built, so the
  CLI flags and JSON shape are whisper.cpp's documented ones, exercised through a stand-in.
- The check-in card on a phone: Compose was checked for structure only.
- The CPU time on xyntai. "A three-minute note in about a minute" is an estimate to measure.

## After the engine update: the model did not come back, and nothing said why

xyntai's health on 29 Sep 2026, after `update.sh engine` swapped in the mirror's llama.cpp
(`llama.cpp-v0.5.0-7fe450e`): oracled restarted at 17:57, logged "llama-server did not become ready:
not healthy within 1m30s" at 17:58:30, and stopped there. No model, no retry. The reason lives only
in secd's journal, because llama-server's output goes to oracled's stdout. health.sh showed "UP {"
for every daemon, so nothing looked wrong.

- **oracled notices a child that dies.** One waiter reaps llama-server (`cmd.Wait`, so its last
  lines are in) and `waitHealthy` returns the moment it exits. The error carries what it said: the
  error-looking lines of its last 40, else the last few. A child still running but not healthy
  gets 5 minutes, up from 90 s: a cold 12B right after a build has pushed it out of the page cache.
- **oracled tries again.** It retries after 30 s, 1, 2 and 5 minutes, then every 10 minutes, ending
  the half-started child first. The health line reads "model not running (try N, next in …): <the
  reason>" instead of "model loading".
- **Every daemon's status carries its health.** `ghosthealth.Current()`, read by the base `status`
  command. health.sh prints "ok", "DEGRADED <why>" or "FAILING <why>" per daemon instead of the
  first line of a JSON object.
- **update.sh engine keeps the old build.** It saves the old binary as `llama-server.prev`, waits up
  to 7 minutes for oracled to report the model ready, and if it doesn't, puts the old one back,
  restarts oracled and says "NOT kept" with the reason. The failed build stays as
  `llama-server.failed`. This time the old binary had been renamed away, so there was nothing to go
  back to.
- **setup_llama.sh** no longer prints "EXISTING volume? Seed it now" when update.sh runs it
  (update.sh does that itself; following the printed steps copied the same binary a second time).
- **redeploy.sh** stages `bin/whisper-cli` with the daemons (it staged only `ghost.*` and
  `llama-server`, so a whisper-cli built by setup would never have reached the volume).

**The category backlog, the last 33 frames.** Converge says 32,862 of 32,981 frames are at the
latest stage and 33 have a tag without a category. Their jobs failed every time and were queued
again at every stock-take:
- The model answered with a category of its own, "architecture:vaulted ceiling" on every church
  photo, which the parser dropped as no answer. `canonCategory` now reads plurals ("places",
  "activities") and 60 synonyms (architecture, building and landmark map to place; plant and flower
  to nature; clothing to object; lighting to style; …). A pair whose tag is one of those asked but
  whose category is the model's own ("religion:church") is a verdict: `other`. A parenthesised
  aside after the tag ("vaulted ceiling (Wait, …") is cut.
- A blank tag was sent to the model as an empty list ("Please provide the list of tags…"). It now
  gets `other` without asking.

Tested: the five answer shapes from xyntai's log, and a refusal still counts as no answer (the job
fails and is retried). For oracled, a stand-in llama-server that exits with "error: invalid
argument" is reported in under a second with that line, and a second start reports its own lines,
not the first's (with `-race`).

## The unlock that deadlocked, and the model's load as a number

xyntai, 29 Sep 2026 20:19:59, an unlock failed at START_DB: "converge ownership: deadlock detected …
ALTER TABLE public.notifications OWNER TO ghost". Around it, a Postgres backend sat at 100% CPU,
"starting database" hung, and the gallery crawled.

- **Ownership converge takes no lock on a converged database.** It ran `ALTER … OWNER TO` on every
  table, sequence and view at every unlock, in one transaction. An owner change that changes
  nothing still takes an ACCESS EXCLUSIVE lock. On a warm box, with the cohort running, each ALTER
  queued behind whatever query held its table, and every daemon query queued behind the ALTER. It
  now alters only objects whose owner differs, in name order, with `lock_timeout = 15s`. A deadlock
  is retried once, and a failure is logged, not fatal: the unlock goes on. Tested on Postgres 16:
  mis-owned objects converge, and a second run takes no time while another session holds a lock on
  a table.
- **One unlock at a time.** A second `POST /v1/unlock` while one runs (a second tap on OK) started a
  second run over the same state: two schema converges at once. The same PIN now joins the running
  unlock; a different one gets 409 and is not tried.
- **The model's load, measured (oracled `/load`).** llama-server prints one "." per percent of the
  tensors loaded. oracled counts them in the unfinished line and follows the phases from the
  output: starting, weights, finishing, projector, warmup, ready, failed. The time left comes from
  the rate the dots are arriving at and the post-weights time of the last load (kept in
  `conf/ghost.oracled.load.json`). The percent never goes backwards.
- **The unlock carries it and waits smarter.** `/v1/unlock/poll` has `model: {phase, pct, etaMs,
  elapsedMs}`: numbers only, the same shape for every account. MODEL's 3-minute wait is extended a
  minute at a time while the percent rises (at most 10 minutes). A load that has failed and not
  restarted within 40 s ends the wait, so a broken engine costs an unlock 40 s, not 3 minutes.

Tested: the load tracker against a scripted llama-server output, from history and without it.
`waitModelReady` against a stand-in oracled: progress carried, completion, and a failed load ending
the wait.

Not done yet: the app side of the unlock screen (the bar, the time left, the tidbits). It waits
until the box is stable again.

## The cause, found: `--mlock`

With the new oracled deployed, the health line said it straight away: "llama-server exited (exit
status 1) before it was ready; it said: error: invalid argument: --mlock". The mirror's llama.cpp
(v0.5.0) no longer accepts `--mlock`, and xyntai's `conf/ghost.oracled.conf` passed it in
`extraArgs`. Run by hand without it, the same binary loaded the 12B and the projector in 8 s and
served. Nothing else was wrong with the engine.

- **Fix on the box:** take `"--mlock"` out of `extraArgs` and restart oracled.
- **So an engine update can't do this again:** when llama-server dies on "invalid argument: X" and X
  came from the conf's `extraArgs`, oracled drops X (and its value, when the next element is one)
  and starts again at once. It logs a warning naming the flag and telling you to take it out of the
  conf. oracled's own arguments are never dropped. Also, update.sh now keeps the previous engine and
  puts it back when the model doesn't come up.

## The full re-upload, and why it happened (app)

While the box answered 503 (the stuck and failed unlocks), the phone started a sync, and did two
things wrong:
- **`getCursor` returned (0,0) "on any failure".** A 503, or an answer without the cursor in it,
  became "start from the beginning of the camera roll".
- **`framesHave` returned an empty set "on any failure"**, so every photo counted as missing and was
  uploaded again.

Nothing was stored twice, because the box dedups by content hash, and the box's cursor was never
pushed back (its upsert is monotonic, GREATEST). But the phone re-read and re-sent ~30,000 photos
into a box that was only busy, and everything crawled.

Now:
- **No cursor from the box means no run** (`Command.Idle`), and the next run asks again. Only the
  box saying so (src "none": a new device, or after a reset) means the beginning.
- **An existence check the box didn't answer stops the run.** Nothing is skipped, because the
  cursor never passes an unconfirmed photo, and nothing is uploaded blind.

## A lock that couldn't unmount under the phone's uploads (secd)

At 20:47 the redeploy restarted secd while the phone (still the old app) was re-sending its camera
roll. secd's shutdown lock stopped the cohort and the databases, then `umount` said "target is
busy" for 75 s. The holder was secd itself: the upload handlers were still streaming bodies into
`.part` files on the volume (one "frame spooled took=23s"). After 75 s the lock gave up ("clean
lock on shutdown reported: umount slot 0: exit status 32 … target is busy") and left the LUKS
mapping open. The next unlock repairs that state (it finds the mapping open and the volume
mounted), so nothing is lost, but each restart took 76 s and ended half-locked.

Now every teardown (lock from the app, halt, off, and the shutdown lock) starts by closing the
doors:
- **New uploads are refused** (frames, locations, voice notes) with the usual appears-down before a
  file is made, and the session is revoked at once rather than at the end.
- **Uploads already streaming are cut.** A body still arriving fails at its next read (a gate on the
  reader). A body stalled on the network is cut by setting its connection's read deadline to now.
  Either way the `.part` is removed, the handler answers appears-down, and the phone keeps the photo
  or note for after the next unlock.
- A halt, or a lock that fails, opens the doors again, because the volume stays mounted.

Tested: a streaming upload and a stalled upload are both cut in under 2 s with no file left, and a
new upload while closing is refused (`internal/secd/closing_test.go`). With the read deadline taken
out, the stalled case still holds the volume after 5 s, so the test catches it.

Deploy this when the phone's sync is paused or the new app is installed. A redeploy restarts secd,
which locks the box.

## A model that died after it was ready, reported as "ok" (oracled)

At 21:06:05 oracled logged "model ready". At 21:06:33 the first caption got EOF, and every request
after it got "connection refused". llama-server had died, but health.sh still showed
`ghost.oracled UP ok`. Once the child was ready nothing watched it, so nothing noticed, logged or
restarted it.

Now:
- **The backend hands out `Died()`** after a successful start: a channel closed when the child is
  reaped, and a func that gives its exit state (a signal such as `killed` from the OOM killer, or an
  exit status) and its last lines.
- **oracled waits on it.** On a death it logs "llama-server died while serving" with how long it was
  up and why, and the health line turns degraded with the same reason. It then starts the child
  again: after 10 s the first time, then 30 s, 1, 2, 5 and 10 minutes while it keeps dying within
  10 minutes of starting. Our own shutdown is told apart by the context, so it is not reported as a
  death.
- **`Stop` on a child that already died** returns at once and signals nothing.

Tested: `TestDiedAfterReady` uses the test binary as a fake llama-server that serves `/health`
and then exits 134 with a GGML_ASSERT line. The death is seen, and the account names the exit
status and the assert.

## "Loading database" for 45 s: a repeat unlock of a running box (secd, hw)

At 21:05:54 the box unlocked cold, and START_DB took 0.4 s. At 21:08:39 and 21:10:08 the phone
unlocked the same box again while it was already running, and START_DB took 44.8 s and 45.5 s.
At those moments the Postgres log has pairs of "could not send data to client: Broken pipe /
connection to client lost". Those are queries whose callers gave up while they waited.

A repeat unlock re-runs the schema script with every daemon running. The script's
`ALTER TABLE … ADD COLUMN IF NOT EXISTS` takes the table's exclusive lock even when the column
exists. So it waits for whatever query holds that table, and every new query on the table queues
behind it. The time went there: from the pg_hba reload at the start of the pass to "schema already
converged (no changes)" at its end.

Now the datastore remembers which Postgres instance (postmaster pid and start time, from
`postmaster.pid`) this secd process has already converged. A repeat unlock of that same running
instance skips the schema pass and logs "database already running and converged by this secd ,
schema pass skipped". The schema only changes with secd's own code, and a new secd starts with an
empty memory, so a secd deploy still converges at its first unlock. A Postgres that was restarted
is converged again.

Tested: `TestAlreadyConvergedFollowsTheInstance` covers the same instance (skip), a restarted one,
another slot, and a missing or garbled pid file (run). Not tested here: the whole unlock against a
live Postgres with daemons holding locks.

## tools/llama_probe.sh: the engine by hand, with a photo

watchd starts daemons with no stdout, so llama-server's own output (teed by oracled) goes nowhere.
When it died on the first caption at 21:06:33, nothing recorded why. (The new oracled logs a death
and its last lines.) The probe runs the volume's llama-server as oracled would, with the conf's
`extraArgs` minus `--mlock`, on port 18090, through secd's namespace door. It sends:

1. a text question;
2. a tiny generated photo (a red disc, so the answer shows whether the image arrived);
3. a real JPEG from `frames/`.

For each it prints the HTTP code, the time, the answer and the speed. If llama-server dies, it
prints the exit code and signal, its last lines and the kernel's last relevant lines. `--small`
tries `-c 8192 --parallel 1`, `--cpu` adds `-ngl 0` last, and `--keep` keeps the log. It leaves
the running stack alone and cleans up after itself.

## Box Status froze the database: the stage count read frame_tags five times per photo (hw)

Opening Box Status polls `/v1/pipeline`, and its stage count ran two `EXISTS (… frame_tags …)`
subqueries per frame. The planner inlines a subquery column into every `FILTER` that uses it, so
each frame got five index probes into frame_tags: about a million buffer touches on 33,000 photos.
That is seconds with the pages cached and much longer when they are not. The Postgres log shows it
as pairs of "could not send data to client: Broken pipe" about 30 s apart: the screen's poll gave
up before the count finished. The phone then treated the box as down and offered the unlock, and
that unlock queued behind the count (the 45 s "loading database").

Now frame_tags is read once, grouped by hash (`bool_or` of "waiting for a category"), and
left-joined to frames. Only the columns the counts use are selected.

Tested against Postgres 16 (`TestPipelineStagesGroupedJoinCountsTheSame`): 6,000 frames covering
untagged, categorised, waiting, waiting-but-removed, other kinds and old pipeline versions. Every
count equals the old query's, and the new one took 21 ms against 108 ms warm.

`tools/db_probe.sh` (read-only) shows, on the box's own Postgres (not the host's):
- what is running and what blocks what;
- the sizes of the counted tables, and whether the `hash` indexes exist;
- the old stage count under `EXPLAIN (ANALYZE, BUFFERS)` with a 120 s cap, plus the job counts
  and `search.health`.

`--watch` samples running queries every 5 s for a minute; open Status while it runs.

## The unlock screen: a bar in real time, each step's time, and tidbits (app)

The unlock screen now shows a percentage and the time left, a bar, the box's steps each with how
long it took (the running one counts up live and breathes), and a line underneath that turns every
4.5 s. That line alternates between what the box is doing ("Postgres wakes up inside the encrypted
store", "loading the model's weights into the GPU: 42%") and a "did you know?" about something the
app does. There are 13 tips, each naming where to find the feature (the check-in voice note, ‹ › on
the MAP, ON THIS DAY, NEAR YOU, gallery search, web search, MODELS, PHRASES, jot a note, WHAT YOU
PHOTOGRAPH, LOCK BOX NOW, BOX STATUS, HEALTH). A lock shows the same screen with only the doing
lines.

- **Time, not steps.** `UnlockClock` (pure Kotlin) times each stage on the phone's clock, from the
  previous stage's done to its own. After a cold unlock that finished, it folds each time into what
  it expects next time (old and new averaged), stored in the `unlock_clock` preferences. Before the
  first one, defaults are used (model 30 s) and the screen says "roughly … (first time on this
  phone)".
- **The model's own clock.** While MODEL runs, the box's estimate is used: the poll's
  `model{phase,pct,etaMs,elapsedMs}`, which the app now parses (`ModelLoad`).
- **The bar.** Its value is elapsed / (elapsed + left). It never goes back, and it holds under 98%
  until the box says ready.
- **When a step runs long.** A step at more than twice its usual time (and over 5 s) shows "taking
  longer than usual (0:45 on this step)" in amber, not a countdown stuck at "a few seconds".
- **Nothing is learned** from a warm box (nothing done, then everything done in one look), from a
  failed unlock, or twice from one run.
- **Same for every account.** It is built from the stage stream alone, which the box sends
  identically for every account.

Tested: 7 `UnlockClockTest` cases (timing, learning and the next unlock's estimate, the box's model
estimate, never backwards, overdue, warm, failed, `ModelLoad` parsing) and 3 `UnlockTidbitsTest`
cases. Of the app's unit tests that run without Android, 108 ran, and the only failure is the old
`QrSamplerTest` one. The Compose screen itself is structure-checked only.

## Model downloads: speed and time left (app)

A phone model download shows the percentage, "1.20 GB of 2.19 GB", a thicker bar, and
"▼ 11.3 MB/s · about 2 min left". The notification carries the same numbers. `TransferRate`
measures over the last 8 s, not since the start, so a resumed download or a Wi-Fi change shows the
current speed. The shown rate is smoothed, and the time left is rounded to 5 s under a minute and
to whole minutes under an hour. When no bytes have arrived for 10 s, the row says so in amber
("waiting for the box; it picks up where it stopped"). Tested: 4 `TransferRateTest` cases.

## The phone's llama.cpp pinned to the box's (app)

The phone model's runtime needs `LLAMA_CPP_SHA256` in `app/src/main/cpp/CMakeLists.txt`, and it
was empty ("this build carries no model runtime (its llama.cpp pin is not set)"). The hash could
not be set from here: it is the SHA-256 of the mirror's tarball, which this sandbox cannot fetch.

- `app/android/tools/pin_llama.sh --from-box` pins it on the box itself. It reads the name and
  SHA-256 that `setup_llama.sh` recorded when it verified the tarball against the signed manifest
  (`/opt/localghost/llama.cpp/.mirror-src` and `.mirror-sha256`), hashes the kept tarball in
  `/opt/localghost/llama.cpp.mirror-dl/` again, and refuses if the two differ. The phone then
  builds from the same bytes as the box: `llama.cpp-v0.5.0-7fe450e`.
- `build.gradle.kts` uses that kept copy automatically when `-PllamaTarball` is not given, so a
  build on the box needs no network. CMake still checks it against the pin.

Tested against a fake box directory: it pins, and it refuses a tampered tarball. The native build
itself needs the Android NDK on the build machine.

## "How about now?" searched as it stood: the model was called "on the CPU" (oracled, synthd, app)

After a BTC price question, "how about now?" came back with a Drake song. The box's model plans
every web search: it reads the last turns and says what is needed, so "how about now?" becomes the
BTC price. But synthd refuses to plan when oracled says the model is on the CPU (a plan there takes
a minute), and oracled said so. The mirror's llama.cpp (v0.5.0) prints none of the startup lines
oracled read the GPU from, so a model running at 52 tokens/s on the 4070 was reported
"on the CPU". The phone then planned alone and searched the three words.

The same wrong verdict had two other effects:
- searchd stretched caption deadlines to CPU speed;
- the phone read pages itself, as if the box were slow.

- **oracled asks the driver** when the log says nothing: `nvidia-smi --query-compute-apps`, bounded
  at 5 s. If the child's pid holds GPU memory, the model is on the GPU (`gpuproc.go`). A log that
  did speak (0 layers offloaded) is not overridden. Tested with a stand-in nvidia-smi.
- **The phone's own plan reads follow-ups** (`FollowUp.standalone`), for when the box cannot plan.
  A short follow-up ("how about now?", "and in euros?", "what about ethereum?") borrows the previous
  question plus what the new one adds. A new question stands as it is. 3 JUnit cases.
- **The answer prompt names the follow-up.** When there is history, the model is asked to answer
  "the next message of the conversation above, read as its next turn", with the need when there is
  one, so web findings about the literal words do not outweigh the conversation.

## An answer survives closing the app; its thinking and sources are saved (synthd, hw, secd, app)

The answer was generated on the phone's connection and saved only at its end. Closing the app, or
the phone locking it, stopped the model mid-sentence and saved nothing. Also, only the answer's
text was ever saved, never the thinking or the web sources.

- **`chat_messages` has three new columns:** `reasoning`, `sources` (JSON
  `[{n,title,url,kind,fetched}]`) and `state` (`writing` | `done` | `stopped`). They come from the
  schema registry, so a box gains them at its next unlock.
- **synthd (`answers.go`).**
  - It saves the question first and starts the answer's row (`writing`, with the sources) before the
    first word. It sends the chat id in the first event, so a new chat is found again.
  - It updates the row every 2 s while the model writes, and marks it `done` before the phone is
    told.
  - The generation runs on the box's own clock (at most 15 minutes), not the phone's connection.
    Incognito still ends with the connection, since nothing is saved.
  - A stream that ends early is kept as `stopped`. A synthd restart settles answers left `writing`.
    A newer question in the same chat supersedes an older answer still running.
  - History for the next question leaves out an answer still being written.
- **STOP** (`POST /v1/chat/stop {chatId}` → synthd `/chat/stop`) is now the one way to end an answer
  early. The app's STOP button calls it.
- **`/v1/chats/messages`** returns `reasoning`, `sources` and `state` (only when not `done`).
- **App.**
  - A reopened chat shows each answer's thinking toggle and its numbered sources.
  - An answer still `writing` shows "the box is still writing this answer…" and grows as the app
    polls it every 1.5 s until it is done.
  - The chat id is taken from the first event.

Tested with Postgres 16:
- `TestAnswerIsSavedWhileItIsWritten`: row started, partial saved, done with thinking, history
  skipping the writing row, orphans settled.
- `TestChatMessagesCarryThinkingSourcesAndState`.
- `TestAnswerOutlivesThePhoneButNotStop` (race detector): the connection ending does not stop a
  saved chat's answer; STOP does; incognito ends; supersede.
- The SQL prepare check now covers 266 statements.

Not tested: the whole stream end to end with a real model, and the app's polling on a phone.

## More room for the chat's text (app)

- Answers run the full width with no frame. Questions keep a light box with less padding.
- The list's side margins went from 16 to 10 dp and the gap between messages from 12 to 8 dp.
- The incognito and web lines (four lines of the screen) are now two short toggles on the model
  pill's row inside the composer ("○ saved", "◉ web" / "◐ web auto" / "○ web off"). What they mean
  is in the GLOSSARY ("Chat: saved, incognito, web").
- The composer is tighter, "[ copy ]" is smaller and only under answers, and "retrieving from
  index…" shows only until the answer's own status line takes over.

## The phone's llama.cpp from a Windows build machine (app)

- `CMakeLists.txt` takes the tarball as a plain local path (via `file(TO_CMAKE_PATH)`), not a
  `file://` URL, which Windows paths break. A missing file now says so. Checked with CMake 3.28 and
  a local tarball.
- `build.gradle.kts` also reads `llamaTarball=` from `local.properties`, so the Windows machine
  names its copied tarball once.
- The order is: `-PllamaTarball`, then local.properties, then the box's kept copy, then the mirror.

## Maps on the phone ahead of time, on Wi-Fi (app)

The map fetches tiles from the box as you look: fast where you have been, slow the first time
anywhere else. There is now a SETTINGS › MAPS ON THIS PHONE section:
- **"download maps"** (off by default) turns it on;
- **"keep up to"** 250 MB / 500 MB / 1 GB / 2 GB (500 MB by default);
- **"[ download now ]"** starts a run;
- a status line such as "312 MB of map on this phone · done 2 h ago: everything near you is here".

- **`MapPrefetchWorker`.** It runs once a day, only on an unmetered network (Wi-Fi or ethernet,
  never mobile data), and not on a low battery. It fetches:
  - the world outlines (`/v1/geo/world`, 50m, 110m) and the coast and road indexes, all
    ETag-checked;
  - tiles in `MapPlan` order until the picked size is reached.

  Tiles already on the phone are skipped, so each run carries on from the last. A run ends itself
  after 8 minutes (under WorkManager's 10) and asks to run again in a minute when there is more.
  Five failures in a row (the box locked, say) end it until the next day.
- **`MapPlan` (pure).**
  - The places come from the phone's recent fixes: the busiest tenth-of-a-degree cells, up to 3.
  - First, every street tile within 25 km of each place, nearest first.
  - Then the coast tiles and the major-road tiles together, nearest first from the main place,
    so a small size covers home and its region and a bigger one reaches further.
  - With no location on the phone yet, it fetches the outlines only and says so.
- **The tile caches keep what was downloaded.** They trim at the picked size plus 50 MB when
  downloads are on, instead of the fixed 200 MB (coast) and 400 MB (roads).

Tested: 3 `MapPlanTest` cases:
- streets around home first, then London's roads, Dover's coast, Paris and New York in distance
  order, with inland cells skipped and streets beyond the radius left out;
- the busiest places first;
- distance.

The worker itself is structure-checked only.

## Chat: incognito and web as icons in the top bar; web starts on auto (app)

- The chat's top bar has two icons before ＋, each with a small word under it:
  - **a hat and glasses** for incognito ("saved", or "incognito" in amber);
  - **a globe** for web ("web auto", "web on" or "web off"). Tapping it cycles auto → on → off.
- The composer keeps only the model pill.
- An incognito chat also shows "◉ incognito , this conversation is not saved anywhere" above the
  conversation.
- Web now starts on "auto" (it was "off"); a phone that already chose keeps its choice. Incognito
  starts off, as before.
- The icons are two new vector drawables (`ic_incognito`, `ic_web`), tinted by the app.

## The unlock: one line, not the list; the keypad goes once the code is in (app)

- The steps list is gone. One line says "step 4 of 7 · starting database", with that step's time
  (and the model's percent while it loads) on the right.
- The percentage, time left, bar and tidbit line stay.
- After OK, the PIN screen swaps the keypad for the unlock alone: the ghost, "UNLOCKING", the bar.
  A wrong code (or any failure) brings the keypad back with the reason and where it stopped.

## The phone's llama.cpp pin is set (app)

`LLAMA_CPP_SHA256` = `a6861d549427f814dc591c439e08206f67ffaba0248344d421589abf18199e67`, the
SHA-256 xyntai recorded in `/opt/localghost/llama.cpp/.mirror-sha256` when `setup_llama.sh`
verified `llama.cpp-v0.5.0-7fe450e.tar.gz` against the signed manifest. So the phone builds from
the same bytes as the box. With the pin set, every build compiles the phone model's runtime and
needs the Android NDK. The tarball comes from `-PllamaTarball`, then `llamaTarball=` in
local.properties, then the box's kept copy, then the mirror, and CMake refuses any file whose
hash differs.

## The phone build on Android's CMake 3.22.1 (app)

The first Windows build found the tarball on the mirror through the manifest, then failed with
"URL_HASH is set to SHA256=…;DOWNLOAD_EXTRACT_TIMESTAMP;TRUE but must be ALGO=value".
`DOWNLOAD_EXTRACT_TIMESTAMP` is new in CMake 3.24, and the Android SDK builds with 3.22.1, which
took the option as part of the hash. It was tested here on 3.28 only. The option is gone; policy
CMP0135 (the same setting) is set when the CMake knows it.

Checked on 3.28 against a stub tarball: the project configures with the right hash and refuses a
wrong one ("SHA256 hash … does not match expected value").

## The phone's JNI bridge against llama.cpp v0.5.0 (app)

The Windows build compiled the whole of llama.cpp v0.5.0, then stopped on one line of
`llama_jni.cpp`: "no member named 'use_mmap' in 'llama_model_params'". The same release dropped
`--mlock` from llama-server. The bridge now sets `use_mmap` through an overload that exists only
when the field does (`prefer_mmap`), and otherwise leaves loading to the library's default, so it
builds on either side of that change. The trick was checked with a host compiler against a struct
with the field and one without. That was the only error in the file; the rest of the bridge's
calls compiled against v0.5.0.

## The phone model stays loaded, remembers the chat, and answers (app)

- Picking the phone model in chat loads it once and keeps it (`LocalModel.pin`, `preload`). It was
  loaded again for each message, and the idle timer unloaded it between them. The weights go when
  another model is picked, when the app locks, or after the idle minutes once unpinned.
- The chat's pill says "loading…" and then "ready" (`LocalModel.stateFlow`).
- The phone model sees the conversation so far (`Transcript.withHistory`), so a follow-up works
  on the phone as on the box.
- It answered "I don't know" too often. The lifeboat prompt now asks for its best answer from what
  it knows, and to hand over only questions about the person's own photos, notes, places and
  history. With web notes, the prompt asks it to cite the notes and to say which part is not from
  them, instead of refusing.
- Under a phone answer, one line gives its speed (tokens a second, from llama.cpp's own counts).

Not tested on the phone: whether the 2-bit model answers better with the new prompts. The 4-bit
model is the better test.

## The phone model's benchmark (app)

MODELS › BENCHMARK runs two fixed tests with the phone model and shows the numbers the runtime
measured (`PhoneBench`, `LocalModel.benchmark`):
- **reading:** a passage of about 450 tokens (the cost of web notes and history);
- **writing:** up to 160 tokens of answer.

It shows:
- the load time;
- read and write speed in tokens a second;
- "first word after about …" and "a whole answer in about …" for a typical 250-token question and
  200-token answer;
- the device, SoC and threads.

The last 8 runs are kept on the phone, newest first, one line each.

Tested: 5 `PhoneBenchTest` cases (the numbers, the words, the storage round trip). The run
itself is structure-checked only.

## Unlock: never quicker than 5 to 10 s; the screen stays on; tidbits (app)

- **The floor.** Every unlock now stays on screen for at least a random 5 to 10 s, picked per
  unlock. A box someone already opened answers "ready" at once, which told a watcher it had been
  unlocked before. Until the floor passes, the screen walks the steps in order and never back past
  one it has shown. The bar moves with the floor, and the time left counts down to it.
  `UnlockClock` learns from the real times, not the padded ones.
- "ready , it was already open" is gone for the same reason.
- A failed unlock is not padded.
- If the app goes to the background during the padding, it locks as usual and the unlock does not
  open the shell behind the gate.
- **The screen stays on** for a minute after the last touch while the app is open, and for the
  whole of an unlock (`FLAG_KEEP_SCREEN_ON`, cleared a minute after the last touch and at once when
  the app leaves the screen).
- **Tidbits.** Under the step line, what the box is doing now is always shown. Under that, a "did
  you know?" tip turns every 6 s. There are 10 new tips, for:
  - incognito and web;
  - answers that survive closing the app;
  - follow-ups;
  - saved thinking and sources;
  - maps on the phone;
  - the benchmark;
  - the pinned phone model;
  - the slideshow;
  - the box staying offline.
  While padding, the model's real phase is not shown.

Tested: 4 new `UnlockClockTest` cases:
- a warm box walks the steps to the floor;
- padding never steps back, and learning uses the real times;
- the time left is never under the floor before done;
- a failure is not padded.

## No flash of the last screen before the PIN (app)

Coming back to the app showed the unlocked screen (chat, memories) for a moment before the gate.
It was Android's recents snapshot: the system keeps a picture of the last frame and draws it
until the app draws again. The app locked itself correctly on stop, but the picture was taken
first. `setRecentsScreenshotEnabled(false)` stops it: recents shows a plain card, and the return
shows no stale frame.

Not testable off the phone. Check: open chat, go home, come back. The gate should show, with
nothing before it.

## Voice notes waited: no speech engine yet; the phone now says why (voiced, secd, app)

The check-in's note was safe on the box (archived, 1 pending, 0 failed). There was no
whisper-cli or speech model because the mirror's `whisper` and `speech` sets had not been
installed. `sudo ./tools/update.sh speech` built whisper.cpp v1.9.4 (927cfce) and put
`ggml-large-v3-turbo-q5_0.bin` on the volume; voiced picks the note up on its next pass.

So the phone says why:
- voiced writes `voiced/state.json` every 15 s: the engine, why not, the note being transcribed
  now, and the pending count;
- secd adds it to `/v1/voice/notes` as `queue` (`running` is false when the file is older than
  90 s);
- a waiting note reads, in order of what applies:
  - "being transcribed now";
  - "waiting: no speech engine on this box (…)";
  - "waiting: the box's voice service is not running";
  - "in line to be transcribed".

Tested: `TestStateFile` (voiced) and `TestVoiceQueue` (fresh, stale, missing).

## Captions come back: every image is fitted before llama-server sees it (oracled)

The mirror's llama.cpp v0.5.0 aborted on a full-size photo (the 3.3 MB JPEG in the probe; a tiny
one was fine), which killed chat with it. Now nothing reaches it whole:
- every image is decoded in oracled with Go's standard library;
- its EXIF orientation is applied (stb_image ignores it, so the model saw portraits sideways);
- it is scaled to 1024 px on its long side (`imageMaxSide` in the conf, 256 to 2048) and sent as a
  baseline JPEG;
- WebP, HEIC and JPEGs Go refuses go through dwebp or ffmpeg first (ffmpeg scales on the way out),
  then the same fit;
- an image nothing can decode is not sent at all; its job parks with the reason;
- a header claiming more than 120 megapixels is not decoded.

The downscale and orientation code moved from framed into `internal/imgfit`, shared by both.

**Strikes.** If llama-server dies while an image is in flight, that image gets a strike
(`ghost.oracled.image-strikes`, beside the conf, on the volume). At 2 strikes it is refused before
it is sent, so one bad photo cannot take chat down again and again. Two, not one: the engine can
die for another reason. Deleting the file forgives every image.

Tested:
- a 3000×2000 JPEG with orientation 6 goes out as 682×1024;
- the conf size is used;
- WebP is converted, then fitted;
- a broken JPEG is refused;
- `TestStrikesStopAPoisonImage`: the engine dies twice, the third try is refused without being
  sent, the count survives a restart, and a failure without a death counts nothing.

Not tested: the real engine with a fitted photo. `tools/llama_probe.sh` does that on the box.

Order on the box:
1. deploy this drop (`redeploy.sh`), then unlock;
2. put the projector back and restart oracled:
   ```
   D=/proc/$(pidof ghost.secd | cut -d' ' -f1)/root/var/lib/ghost/mnt/slot0/ai-models
   sudo mv $D/mmproj-F16.gguf.off $D/mmproj-F16.gguf
   sudo ./tools/ns.sh ./bin/ghost-ctl restart-daemon ghost.oracled
   ```
3. give the caption jobs that parked while it was text-only another go:
   `sudo ghost-cli ghost.searchd unpark kind=caption`.

## The map opens from the phone; it reopens where you left it; download now works (app)

The first open waited on a chain of round trips before any tile could draw:
1. the newest photo;
2. the list of world cuts;
3. two world revalidations;
4. the coast index, then the road index;
5. sixty days of tracks.

That happened even with every tile already on the phone. Now:
- the coast and road indexes and the largest world cut already on the phone are drawn at once,
  from disk; the box is asked afterwards, each part on its own, and only what changed is swapped
  in;
- the world-cut list is kept on the phone (`worldIndexOnPhone`);
- **the map reopens where it was left** (`MapCamera`: centre and zoom in the phone's preferences),
  so the tiles it needs are the ones on disk;
- the last view's photo dots, place names, newest photo and day tracks are kept in memory and drawn
  at once on the next open (`MapMemory`), then refreshed from the box. They are the box's data,
  so they are dropped when the app locks, like the rest of the session;
- when the box does not answer, the drawn world stays; the plain cut is not swapped in.

**[ download now ]:**
- It used `KEEP` with the daily run's constraints, so a press did nothing while an earlier run
  waited, on a low battery or in backoff, and nothing on screen changed.
- Now a press replaces whatever waits and needs only Wi-Fi.
- The status line reads "queued … starts on Wi-Fi" until the run starts.
- While it runs, the worker notes its count every 10 tiles, and SETTINGS refreshes the line every
  3 s.

Structure-checked only; not run on a phone.

## Memories: every photo opens; On This Day is a slideshow (app, secd)

- A tap on any photo in MEMORIES (On This Day and an outing's or a day's covers) opens
  `MediaSlideshow` at that photo:
  - full screen, swipe between them;
  - a slideshow that turns every 4 s until a swipe or a tap pauses it;
  - "[ ▶ slideshow ] / [ ❚❚ pause ]", "3 / 12 · 2019", "[ zoom ]" into the pinch-zoom viewer;
  - thumb first, then the box's preview.
- Videos show ▶ on their thumbnail and in the slideshow, and play in the video player.
- To know which hashes are videos, the phone asks the new `POST /v1/frames/kinds`
  (`{"hashes":[…]}` → `{"kinds":{hash:"photo"|"video"}}`, at most 200, hashes checked to the
  character as for `/v1/frames/exists`). An older box does not answer it, and everything then
  opens as a photo.

Tested: `TestFrameKinds` (Postgres) and `TestCleanHashes`. The screens are structure-checked
only.

## Privacy fixes from the 30 Sep notes (secd, profile, watchd, hw, synthd, app, tools)

Eleven of the twelve patches in the privacy field notes are applied (the phone-side unlock replay,
0004, is replaced by the box-side one below):

- **The session token goes only to the unlock that carried the PIN.** `POST /v1/unlock` answers
  with a run id; `/v1/unlock/poll?run=` hands the fresh token to that run alone, within two
  minutes. Before, anything on 127.0.0.1 (a website on the same machine) could read it after any
  unlock. `unlock_token_test.go`.
- **The wipe PIN counts as a wrong PIN in the limiter** and the KDF always runs, so the time to
  the next guess no longer names it. `oracle_probe_test.go`.
- **Temporary files on the volume.** watchd sets `TMPDIR=<mount>/tmp` for the cohort and all it
  runs. `tmpdir_test.go`.
- **The phone at rest.** No keyboard learning on private fields (`PrivateInput.kt`), sensitive
  copies marked as such, the cache swept at lock (`CacheSweep.kt`), notifications of the daemons
  private on the lock screen, no cloud backup or device transfer (`data_extraction_rules`).
- **What leaves the phone.** Weather is asked at two decimals (about a kilometre), the web plan
  never carries the person's own details, and on auto a follow-up never borrows a private question
  for a web search (`FollowUp.mayBorrow`, `looksPersonal`).
- **No core dumps.** `LimitCORE=0` in the unit and `harden.NoDump` in every daemon (a crashed
  process holds decrypted data; systemd-coredump wrote it to the OS disk).
- **Redis passwords off every command line.** An auth file, `REDISCLI_AUTH`, ACLs on stdin.
  `redisargv_test.go`.
- **Logs keep counts, not what was asked** (secd activity and synthd's web plan at Debug).
- **Every rejected PIN is answered at the same moment** (1.5 to 2 s from the request, `rejectFloor`).
  `reject_time_test.go`.
- **`tools/privacy_check.sh`**: what this box shows the rest of the machine.

## The model loads after the unlock; a warm unlock replays a cold one (secd, app)

The unlock no longer waits for the model. MODEL is marked skipped and READY follows DAEMONS;
oracled loads the model in the background, and chat shows that load (next section). On xyntai
that takes the model's 12 s or so out of a cold unlock (modelled from the stages, not yet timed on
the box).

A warm unlock (the volume already open) would then answer in a second, and a second says "this
box was open already". So secd keeps the step times of its last eight cold unlocks, on the
encrypted volume (`<mount>/secd/unlock-times.json`, root's, 0600, written only when that is a
mount point), and a warm unlock runs in a shadow and plays one of them back to the poll: picked
with crypto/rand, stretched or shrunk by up to 8%, every step Running then Complete at its own
time. A failed warm unlock is not played back (the reject floor covers it). Until eight cold
unlocks are kept, a built-in shape stands in. The phone's own 5 to 10 s floor is gone; it shows
what the box streams.

The phone's clock learns a skipped step as next to nothing at once, so the time left no longer
counts 20 s of model that the box no longer loads during the unlock.

Tested: `replay_test.go` (a cold unlock is kept, a warm one replays it at about the same length
with every step completed, a warm reject is not replayed, the scale stays within 8%, the times
never land off the volume), `UnlockClockTest.aSkippedModelIsLearnedAtOnce`.

## Chat shows the model loading (secd, app)

`GET /v1/model` (session) answers `{"ready", "phase", "pct", "etaMs", "elapsedMs", "detail"}` from
oracled's health port and its `/load`. Before a box chat, the phone asks it; while the model loads,
the answer's place shows "your box is loading its model into the GPU · 42% · about 10 s left ,
your question goes as soon as it is ready", once a second, for up to three minutes, then asks
anyway and says so. A box without the route answers nothing usable and chat sends at once, as
before. The phone's own model shows its load the same way, against how long its last load took.

Tested: `TestModelStatus`, `ModelWaitTest`.

## Pictures never go through a temporary directory (oracled, searchd, framed)

- oracled and searchd decode WebP with `dwebp … -o -`: the PNG comes back on a pipe.
- framed re-reads a damaged JPEG from where it lies in the archive (`redecode(src)`), not from a
  copy; a frame ffmpeg grabbed has no original and is never re-read.
- No `os.CreateTemp` is left in the image paths; `TMPDIR` on the volume stays as the fallback for
  what the tools do by themselves.

Tested: `TestWebPNeverTouchesTemp`, `TestDecodeWebPNeverTouchesTemp` (a fake dwebp that insists on
`-o -`, and an empty `TMPDIR` afterwards), `TestDamagedJPEGPreviewGoesThroughFFmpeg`,
`TestRedecodeNeedsTheOriginal`.

## A user of their own for the daemons (hw, tools)

The private mount namespace hides the vault from the host, not from the daemons' own user:
`/proc/<pid>/root` of any daemon is a door into it for that user. On xyntai that user is coder,
so anything else running as coder could read the vault.

`sudo ./tools/own_user.sh` (with the box locked) makes a system user, `ghostd` (no home, no shell,
no password), points ghost.secd's unit at it (`--user ghostd`, the old unit kept beside it) and
restarts secd. The next unlock hands the volume over, once, before Postgres starts
(`internal/hw/adopt.go`):

1. a Postgres superuser named `ghostd`, made in single-user mode as the old owner, so the peer
   line in pg_hba keeps a bootstrap identity;
2. every file the old user owns on the volume chowned (lchown, links never followed; root's files
   stay root's);
3. the database directory and the volume root last: the root's owner is what says "handed over",
   so a walk cut short is finished by the next unlock.

It refuses a run user named like an app role (ghost, ghost_ro, ghost_rw), and it refuses while
Postgres or Redis still run as the old user. The scripts that chown onto the volume follow the
unit's user (`setup_whisper.sh`, `phone_model.sh`; update.sh already followed the volume).
`privacy_check.sh` says whether the cohort's user is a login account and whether the volume still
belongs to someone else. tools/README.md, step 8b.

Tested: `TestAdoptVolumeEndToEnd` (a real Postgres: initdb as one user, handed to another, the new
user's peer login is a superuser; refused under a live database), `TestChownOwnedWalk`,
`TestPreviousOwner`, `TestRunUserMustNotBeAnAppRole`. Run it with
`GHOST_ADOPT_USERS=old,new` as root.

## The phone's trail is sealed (app, secd)

Where the phone has been (the spool waiting for the box, and the last two days it draws itself)
is sealed point by point to an X25519 key (`sync/TrailSeal.kt`): a one-off key per point,
HKDF-SHA256, AES-256-GCM, "s1:" and base64 per line. The worker seals with the public half and
holds nothing that opens what it wrote.

Where the private half lives (`sync/TrailKeys.kt`):
- **box**: in the vault, `<mount>/secd/trail/<device>.json` (root's, 0600). `POST /v1/trail/key`
  hands it over once; `GET /v1/trail/key` gives it back after a PIN unlock; the app holds it in
  memory and forgets it when it locks. Local-only mode never opens the trail of a phone whose key
  is in a vault.
- **phone** (no box yet): wrapped by an AndroidKeyStore RSA key that opens only within 30 s of the
  phone's own unlock. Wrapping needs no unlock, so the key is made the first time a point is
  recorded and nothing is written in the clear. The first PIN unlock of a box hands it over and
  deletes the phone's copy.

secd opens sealed points before it spools a location batch (`openLocationBatch`), so framed reads
plain points and never sees a key; a batch sealed to a key the box does not hold is answered 409
and the phone keeps it. The last point, which the worker compares every fix with, and the point
the country was last looked up at are sealed to the phone's hardware (`security/DeviceSealed.kt`,
readable in the background); the country for the phrases stays plain. The map's last view is
sealed like the trail. Lines written before this build are sealed the first time a key is there.

Tested: `TrailSealTest` and `trailkey_test.go` (each side opens what the other sealed: a vector
from each language), `TestTrailKeyAndSealedUpload` (409 before the key, the spool gets plain
points after, another device cannot read the key), `TestLocationBatchOpened`. The Keystore parts
are structure-checked only.

## The QR's key goes into the Keystore, then is replaced (app, secd)

The enrolment QR carries a device certificate and its private key, which the box made. Now:

1. **Keystore.** The key is imported into AndroidKeyStore, non-exportable, and the app's wrapped
   copy is deleted. A phone enrolled before moves it the first time it connects.
2. **Rotation.** After the first PIN unlock the phone makes a P-256 key inside the Keystore and
   sends `POST /v1/device/rekey {"spki","sig"}` (ECDSA-SHA256 over "localghost rekey v1\n" and the
   key). secd checks the proof, signs a certificate with the box CA (`/etc/ghost/ca`, same name as
   the one presented) and keeps a pending hand-over on the OS disk.
3. The phone switches to the new certificate and sends `POST /v1/device/rekey/confirm` over it.
   secd retires the QR's certificate (`<state>/devices/retired`, fingerprints only, 0600), moves the
   phone's trail key, notification position, sync positions and name to the new device key, and
   from then on answers the old certificate as if the box were down, the PIN entry included. A
   photographed QR is worth nothing after the first unlock.

Until the confirmation both certificates work, so a lost answer is finished by the next unlock.

Tested: `TestDeviceKeyRotation` (a bad proof refused, the certificate signed by the CA for the
phone's key with the old name, the old certificate working until the confirmation and down after,
the trail key moved, a second confirmation harmless, retired across a restart). The phone side is
structure-checked only.

## Vault rings: the unlock and the lock, drawn (app)

Vlad: "the lock on the phone was nice, let's do a cool lock and unlock animation as well" , then
"make the lock animation similar". Both now run on one drawing (`ui/VaultRings.kt`, the rules in
`ui/VaultRingsModel.kt`, pure and tested).

Six rings, one per unlock step from the outside in: RESOLVE, UNSEAL, MOUNT, START_DB, START_CACHE,
DAEMONS (the model loads after the unlock and has no ring; READY is the arrival). Each ring is dark,
filling while its step runs (a scan head sweeps it), lit when the step is done, draining on the way
down, or red when the step failed. A ring locks in with a short overshoot so that its keyway lands at
the top; when all six are home the keyways make one slot, the rings hold for 450 ms and the iris
opens over 800 ms with a radial glow. A light tick on each ring that settles, a long one at the end.

The lock plays it backwards. The rings arrive lit (750 ms), each teardown step puts out its ring
from the inside out (STOP_SERVICES the daemons, STOP_CACHE and STOP_DB theirs, UNMOUNT the volume
and the seal, LOCKED the outer ring), and the screen switches off like an old monitor: squeezed to
a line, a bright band, dark. The phone asks the box to lock at the start and replays the steps
against the real answer, so the drawing never runs ahead of the box.

`vault-rings-preview.html` (sent with this drop) draws the same geometry in a browser, to look at
the timing without building the app.

Tested: `VaultRingsModelTest` (phases while unlocking and locking, a failed step, the rest angle
always a real turn and landing at the top, ring sizes and segment counts). The drawing is
structure-checked only.

## The trail asks "were you there?" (framed, secd, app)

Vlad: "a question on the gps when i look at the day, it looks like today you went to x and then came
back quickly, did you really, and then on no, delete it ... anything that seems out of the obvious
like no road and no other stuff to get there".

The glitch rules (clean.go) already hide what cannot be true. What only looks unlikely was drawn,
and the person is the one who knows, so a day now carries questions (`internal/framed/questions.go`):

- **glitch**: points the glitch rules leave off the map, 2 km or more from the trail. Asked so they
  can go for good; nothing changes on the map either way.
- **fast**: an out-and-back of up to three points, at least 5 km away, back within a third of the
  distance and within 90 minutes, that needs 200 km/h or more on the way out or back (50 km in 15
  minutes). A train or a flight does not come straight back.
- **offroad**: the same shape to a place with no road within 1 km, where the box's road tiles know
  the area (`roadgraph.NearRoad`; unknown ground is never called off-road).

The questions ride on the day's route (`BuildDayPathAsking`, the LineString's `questions`), so the
phone gets them with the tracks it already fetches. The map shows one card at a time: "At 14:10 the
trail goes to Igoumenitsa, 38 km away, and is back 12 min later. That would take 240 km/h. Were you
there?" with **NO, DELETE IT** and **YES, KEEP IT**.

`POST /v1/geo/trail/answer {"from","to","ts":[...],"keep"}` (secd → framed ctl `trail-answer`). No
deletes those points from `location_points` (every source) and the phone's own ring
(`LocationLog.forget`); yes goes into `trail_kept` (new table) and that stretch is never asked about
again. Either way the day (and the next, when the stretch crosses midnight) is rebuilt. A question
spans at most six hours.

Days built before this have no questions until they are rebuilt:

    sudo ./tools/ns.sh ./bin/ghost-cli ghost.framed day-routes days=60

Tested: `questions_test.go` (a glitch asked about, a fast out-and-back asked about, a day trip
not asked about, no road there asked about only where the tiles know the ground, a yes not asked
again, the questions on the day's path), `TestTrailAnswersInTheStore` against Postgres (delete
counts, kept stretches), `TrailQuestionTest` on the phone (the text, the JSON). The card and the
forget are structure-checked only.

## Pictures too big to open are left alone (imgfit, oracled, framed, searchd)

Vlad: "can we make sure we're processing images again? it would be safe to limit them to a size, if
i get a raw image that's 100mb i should not try to parse that".

`internal/imgfit/limits.go`: 64 MB on disk and 60 megapixels, checked before anything is decoded.
The pixel count comes from the file's header (`image.DecodeConfig`), so a small file that claims to
be 30000×30000 is refused before a byte of pixels is allocated. Every decoder checks first: oracled
before it reads a file for a caption, framed before previews and derived files, searchd before it
hashes. A photo past the limit is archived untouched, searchable by its date and place, with no
preview and no caption: a caption job for it is done at once rather than failing five times and
parking (where an unpark would only start it over), and the stock-take no longer queues one.

Whether the captions are moving again:

    sudo ./tools/ns.sh ./bin/ghost-cli ghost.searchd queue
    sudo ./tools/ns.sh ./bin/ghost-cli ghost.oracled status
    sudo ./tools/ns.sh ./bin/ghost-cli ghost.searchd unpark kind=caption   # if parkedJobs > 0

Tested: `TestLimits` (a 48 MP photo passes, a decode bomb and a PNG whose header claims 40000×40000
refused, a file past 64 MB refused), `TestNoPreviewPastTheLimit`, `TestTooLargeToCaption`, the
oracled image tests.

## A new server release from the phone, without root (update, secd, tools, app)

Vlad: "how do we initiate the deploy of a new version without having root on the server, can the app
let us know hey there is a new version, do you want it deployed and it tries to switch to it on the
server side and rolls back if it's buggy ... we can check once a day". The box never goes to the
internet for it: the phone does, and the box checks what the phone brings with the key it already
has.

**Build.** `tools/release_build.sh <version> [outdir]` from a clean tree (it refuses uncommitted
changes). Reproducible: `CGO_ENABLED=0`, `-trimpath`, `-buildid=`, the tar sorted with the commit's
time and owner 0, `gzip -n`. The same commit gives the same bytes anywhere, so anyone can rebuild a
published release and compare. It writes the "server" set:

    localghost-server-<version>-linux-amd64.tar.gz   VERSION, COMMIT, CHANGES.txt, bin/, tools/
    RELEASE.txt      version, commit, date, bundle, and the commits since the last tag
    NOTICE.txt, TERMS-MIT.txt

The bundle holds secd, the cohort, ghost-cli, ghost-ctl, mirror_fetch.sh and mirror-key.asc (so the
next release is checked with the same key). It never holds ghost-update-guard (what undoes a bad
release is never replaced by one) or llama-server and whisper-cli (built on the box from pinned
sources by update.sh). It needs tools/mirror-key.asc in the tree.

**Publish** (the web repo). The set goes on the mirror as `server`, like any other set:
`/<build>/server/<file>`, listed in MANIFEST.txt and signed with the site key. Brief for the web side
below.

**The phone.** Once a day on Wi-Fi with the battery not low (`update/ServerUpdates.kt`, WorkManager)
it reads MANIFEST.txt, then RELEASE.txt (its SHA-256 checked against the manifest). The box's
version comes from `GET /v1/update` after each unlock. A newer release notifies once per version;
SETTINGS › SERVER shows what runs, the offer with its changes, **DEPLOY**, **ROLL BACK** during a
trial, and "check the mirror now". DEPLOY downloads the set to the phone, checks each file's hash
against the manifest, and uploads MANIFEST.txt, its .asc, then each file to the box. A mirror
rebuilt since the check is fine when RELEASE.txt is the same file; a different release says "check
again".

**The box** (`internal/secd/update_http.go`, `internal/update`):

    GET  /v1/update                 {"version", "trial": {...}}
    POST /v1/update/file?name=...   MANIFEST.txt (starts a new upload), MANIFEST.txt.asc,
                                    <build>/server/<file>; names checked, 512 MB each at most
    POST /v1/update/apply           verify, put on, lock, restart; {"ok","version"} or {"ok":false,"why"}
    POST /v1/update/rollback        the earlier build back, lock, restart

All four need a PIN session; apply needs the box unlocked. Apply runs mirror_fetch.sh itself over
a `file://` copy of the upload, with the key installed in /opt/localghost/tools (never one that came
with the upload): the signature, the "# LocalGhost Mirror Manifest" header, every hash, and no
build older than the box last used. Then `update.Unpack` (VERSION, COMMIT, CHANGES.txt, ELF files
under bin/, tools/ only; secd and watchd must be there) and `update.Apply`:

1. what runs now goes to `/var/lib/ghost/update/prev/` (secd, ghost-cli, ghost-ctl, the tools, the
   cohort from the volume);
2. secd, ghost-cli and ghost-ctl over /opt/localghost/bin by rename (the running secd keeps its
   file), the cohort into staging (ingested at the next unlock before the daemons start), the tools
   into /opt/localghost/tools;
3. `trial.json` opens a trial, secd locks and exits 75, and systemd starts the new secd.

**The trial ends one of two ways.** Confirmed when the first unlock completes and the critical
daemons stay up for ten minutes. Rolled back (prev/ put back, lock, restart) when:
- the new secd will not stay up: `ghost-update-guard` runs before every start of the unit
  (`ExecStartPre=-`), counts quick starts, and puts prev/ back on the fourth; the new secd resets the
  count after a minute up;
- the first unlock fails past RESOLVE (a wrong PIN says nothing about the build);
- a critical daemon restarts three times or is down in those ten minutes;
- the person taps ROLL BACK.

`make` now stamps the version (`git describe --tags --always --dirty`), so a box built from source
says what it runs; a build with no tag yet reads as older than any release.

**Once, as root, to take the first release this way:**

    sudo ./tools/redeploy.sh

It installs ghost-update-guard, the unit drop-in that runs it (and `StartLimitBurst=20` in 120 s so
systemd does not give up before the guard acts), and mirror_fetch.sh with the site key into
/opt/localghost/tools. From then on a release needs the phone and the PIN, not root.

Tested: `update_test.go` (Unpack takes only a release, apply then roll back puts every file back,
the guard rolls back a quick crash loop, no rollback without an earlier build),
`update_http_test.go` (the whole exchange from the phone, an unverified release refused, a failed
first unlock rolls back, which daemons count as unhealthy), the whole chain with a throwaway gpg key: release_build.sh → a mirror
layout → mirror_fetch.sh over file:// → Unpack. `ReleaseInfoTest` on the phone (the manifest's
server set, bad paths refused, the notes, version order including git-describe and bare hashes).
The phone's download, upload and settings screen are structure-checked only. Not run: systemd
restarting secd on a real box.

### For the web repo: publishing the server set

The mirror is a proxy of upstream files signed with the site key. The upstream for `server` is the
localghost repository's GitHub release: attach the four files release_build.sh writes to the
release for the tag, then add a set to the mirror config:

    server  localghost-server-<version>-linux-amd64.tar.gz  <terms: MIT>  https://github.com/LocalGhostDao/localghost/releases/download/v<version>/localghost-server-<version>-linux-amd64.tar.gz
    server  RELEASE.txt                                      <terms: MIT>  https://github.com/LocalGhostDao/localghost/releases/download/v<version>/RELEASE.txt

(in whatever form deploy/mirror uses for a set, with NOTICE.txt and TERMS-MIT.txt beside the files
like every other set). The phone reads RELEASE.txt before it downloads anything, and the box
requires one `localghost-server-*.tar.gz` in the set and no more. Only the newest release is in the set:
a release that is replaced drops out of the next build. Anyone can check a published bundle by
running release_build.sh at the same tag and comparing the SHA-256 with the manifest's line.

## Setup and redeploy, looked over (tools, setup)

- New boxes get the daemons' own user (`ghostd`, no home, no shell, no password) from setup.sh
  directly; own_user.sh remains for boxes set up before.
- `LimitCORE=0` reached only units written after the privacy round; redeploy.sh now adds it to an
  older secd unit as a drop-in (a core dump of secd would hold the volume key in the clear on the OS
  disk).
- redeploy.sh reloads systemd before it restarts anything, so a changed unit is what starts.
- setup.sh ends with a warning when swap is on the OS disk unencrypted (a paged-out daemon's memory
  would be there in the clear) and how to make it encrypted with a random key at each boot.
- privacy_check.sh reports the unlock diary as a warning, not a failure (see below).

## The unlock diary is Debug (secd)

"unlock stage begin/ok" at Info wrote the time of every unlock and how long each step took into the
OS disk's journal, where it stays after a lock. It is at Debug now; the phone's own unlock clock
shows the same. What the journal already held goes as it rotates; the vacuum run on 30 Sep
cleared most of it.

## docs/SECURITY.md says what ships (docs)

SECURITY.md describes the design: a FIDO2 key, several PINs, decoy volumes and a hidden volume. The
box has one account, a main PIN and a wipe PIN, no FIDO2 and no decoys (`internal/profile/setup.go`).
Someone who reads the document and counts on a duress PIN at a border would be counting on nothing,
so it now opens with what the box does today, the software seal's limit and what the wipe leaves on
each tier. The design text below it is unchanged.

## The scanner is a vault aperture now (app)

Vlad: "make the scanning of the QR code on the machine equally cypherpunk and futuristic but within
our guidelines, it's a bit silly now." The angry ghost, its speech bubbles and the success fireworks
are gone. In their place, the same language as the unlock rings (`ui/QrApertureModel.kt`, pure and
tested; drawn in `ui/QrScanScreen.kt`):

- an eight-segment reticle sits fixed in the middle of the screen with a crosshair to aim , you point
  it at the QR, it does not chase the code across the frame (that finder-to-view mapping was the
  fragile, device-specific part, and it is gone from the scanning overlay);
- one segment per frame: the eight fill as the box's rotating enrolment frames land (any eight of its
  twelve complete it), or all eight at once for a single clean code;
- the middle stays a crosshair to aim with; each frame read flashes a green check on the code for
  a beat, then it is the crosshair again (a padlock over the code was tried and read oddly, so the
  lock lives in the found sequence instead, where it means "identity pinned");
- a code that reads but is not a box turns the reticle red with one terse line ("that is a Wi-Fi
  code, not a box").

Once the whole code is found the camera is no longer needed: the screen darkens over the first
quarter second (a near-opaque scrim) and the establishing sequence below plays on the dark, so its
words read clearly.

**The found sequence establishes an identity, it does not "open".** Vlad: it should be "more about
establishing connection than opening ... this is establishing identity, slowly build out as we scan
and then show how we gain and sign a new certificate ... and then a now ready to login with your PIN
message ... less messy and more minimalist." Once a box is found the camera view gives way to one
minimalist sequence (`QrApertureModel.Step`, pure and tested; `drawEstablish`): a single ring builds
out clockwise, four beads on it fill as each step lands, and a small glyph in the middle changes per
step , the box's identity read from the code (a code grid), a secure channel to the host (two nodes
and a pulse), this phone's certificate signed (a document with a signature drawn across it), the
box's identity pinned (a padlock closing) , then a steady tick and "READY , unlock with your PIN".
One quiet line of words under it, the rest carried by the animation. The scanning overlay is hidden
the moment a box is found, so the two never sit on screen together.

Three earlier extras went at the same time, all at Vlad's ask:

- **The corner-bracket reticle is gone.** It drew from the sampled quad at the same time as the
  aperture, so two shapes sat over the code. The aperture is the only overlay now.
- **Auto-zoom is gone, replaced by a manual slider.** The old auto-zoom fired on a no-decode streak,
  zoomed 2x, and made a small code it had zoomed into look worse (a 720p frame magnified is not more
  detail). Now a ZOOM slider under the frame drives the camera at the person's pace; it starts at 1x
  every time the scanner opens and is shown only when the camera can zoom.
- **Auto-torch is gone.** The scanner no longer turns the flash on by itself; a dark code is lit by
  moving to better light or the phone's own flashlight. No `enableTorch`, no luma sampling. The
  per-frame pip row under the camera also went , the aperture's own segments show the frame progress.

The decode pipeline (finders, sampling, frame assembly, the two-frame confirmation) is untouched;
only the overlay and the found sequence changed. `qr-aperture-preview.html` (this drop) shows both.

Tested: `QrApertureModelTest` (segments as frames land, a clean code fills at once, the establishing
steps walked then held at READY, per-step progress, the phase from what the scanner sees). The
drawing is structure-checked only.

## The phone's TLS is checked by secd itself, not by a header (secd, setup, ctl)

The security review's second finding: secd served plain HTTP on 127.0.0.1:8443 and believed the
`X-Client-Cert` header nginx set, on every route. Any process on the box , one of the ~20 websites,
an SSRF in any of them , could reach the PIN door without a device certificate and claim to be any
device, retired ones included. Now secd does the TLS.

**secd** (`internal/secd/edge.go`). The listener looks at each connection's first byte: a TLS
handshake (0x16) secd terminates itself, serving `box-server.pem` (the certificate the phone pins)
and checking the client certificate against `devices-ca.pem` , in the handler, not the handshake, so
no certificate, a forged one and a retired one all get the same 503 page a down box gives. A device
is named by the SHA-256 of the certificate's PEM as nginx would have escaped it, so a phone keeps its
trail key, sync positions and notification cursor across the switch; while the old path still runs
secd records, for every certificate it sees, the name nginx gave it (`devices/ids`), so the names
match even if the rebuilt string ever differed. Anything but a TLS byte is the old nginx path,
served as before until `/etc/ghost/edge` says `tls`, then given the 503 too. So a redeploy changes
nothing on its own; the switch is one file and one nginx reload.

**nginx** (`internal/setup/edge.go`, `ghost-ctl edge-passthrough`). nginx's stream module forwards
the phone's raw TLS to secd by the name it asks for (`ssl_preread`), and the box's other sites move
from `:443` to `127.0.0.1:4443` behind the stream with a PROXY line so they keep their clients'
addresses. The runner rewrites the listen lines, adds the stream include, keeps every file it
changes, runs `nginx -t`, reloads, checks from outside (the box's name serves the box's certificate,
another name serves its own), and puts everything back on any failure. `--undo` restores it.

    sudo ghost-ctl edge-passthrough --domain <box name>     # move nginx (backs up, tests, checks, rolls back)
    echo tls | sudo tee /etc/ghost/edge                     # then plain HTTP on :8443 gets the down page
    sudo ghost-ctl edge-passthrough --undo                  # put nginx back

`ghost-cli` and `ghost-ctl` are untouched by this: they reach secd and the daemons over the unix
control sockets in the run dir, never the HTTP port, so CLI access on the box works just as before, locked or unlocked.

Tested: `edge_test.go` in secd (the phone over TLS reaches the routes; no certificate, another CA's,
and a forged header all get the 503; a 404 folds to the 503; plain HTTP served then refused after
the switch; a phone keeps nginx's name for it; a retired certificate stays down; a PROXY line is
taken and the client's address kept), `edge_test.go` in setup (listen lines moved, IPv6 handled, a
specific-address listener refused, quic left alone, the stream include added once, apply end to end,
a rollback when the check fails, undo). The nginx moves are verified against a tree of files with a
fake nginx; not run against a live nginx here.

## The service user's sudo is one exact unit, not ghost.* (setup)

The review also flagged the sudoers rule: `coder` could run `systemctl {start,stop,restart,status,
enable,disable} ghost.*`, where the `*` also matches a trailing ` -H other.host` or ` -M container`
or other systemctl arguments. `server_setup_root.sh` now writes the exact `ghost.secd` unit (with
and without `.service`), only the verbs a deploy uses (restart, start, stop, status, daemon-reload),
and no enable/disable (a one-time admin action). An existing wildcard file is kept as a `.bak` and
replaced. `privacy_check.sh` warns when any sudoers file still grants `ghost.*`.

## The unlock is paced so every step can be read (app)

Vlad: "the unlock now just skips through steps and I can't read them ... add a bit of a delay so we
get to see the animation." A warm box replays a cold unlock so fast that all six rings could light in
one poll. `net/UnlockPacer.kt` (pure, tested) holds each ring step on screen for at least 720 ms and
walks them in order, never ahead of the box and never backward; a step the box really sits on keeps
its real length. Both the vault rings and the progress text read from the paced snapshot, so the
picture and the words move together, and `MainActivity` waits out the floor (about four seconds)
before the iris opens. Each step now also says why it takes the time it does (`UnlockTidbits.why`):
the PIN is checked in the secure chip at its own pace, Postgres replays anything unwritten, and so
on. A failure shows the real snapshot at once, no pacing.

Tested: `UnlockPacerTest` (a warm unlock walked one step at a time, a slow step keeping its length, a
failure passing through, the floor). The screen wiring is structure-checked only.

## Corfu: the trail asks about the sea, and a fix can be picked by hand (framed, landtiles, secd, app)

Vlad: "it sends me to corfu but I have not been there ... should be able to zoom in on a point and
select it and delete it, but only when i'm very zoomed in ... on the map we should be able to get
the question ... the 50km was a bit off it seems we have gaps of 15 min between gps".

Why the line to Corfu was drawn and never asked about. Lakka to Kavos is about 16 km, and with a fix
every quarter hour that is about 65 km/h. The old "fast" rule wanted 200 km/h, there are roads at
both ends so it was not "offroad", and the phone sat on the Kavos tower for four fixes, so the glitch
rules did not call it a parked tower either.

What the questions ask now (`internal/framed/questions.go`):

- **sea** (new), a hop of the trail runs across 2.5 km or more of open water on the box's land
  tiles, 5 km when the run over there is longer than three fixes (a coast road's chord cuts across
  bays, not straits), at 35 km/h or more, or out and back within 90 minutes. The run over there may
  be up to eight fixes and four hours, since a tower across the strait holds the phone for a while.
  `internal/landtiles/lookup.go` (new) answers "is this point on land" from the same tiles the map
  draws, a coast cell's edges filed in 512 bands so a test reads a handful of edges, sixteen cells
  kept in memory. Where there are no tiles the box does not ask.
- **fast** from 90 km/h (was 200) and from a 3 km hop (was 5 km).
- **the edges of the data**, a jump in the first or last few fixes has nothing to come back to, so
  it is asked about on its own when it fits sea, or fast at 180 km/h (a motorway at the edge of the
  data is not odd).
- visits to the same place in one afternoon (a phone flipping between two islands' towers) are one
  question, so one "no" takes them all.

On the map. Each of the lit day's questions is a red "?" where it points, and its card sits under the
map whether the trail panel is open or not, one at a time with "next ›". The card for the sea reads
"At 14:10 the trail goes to Kavos, Greece, 16 km away (4 fixes there until 14:55) and is back 1 h 15
min later. That is 12 km of open sea at 66 km/h. Were you there?"

A fix picked by hand. The map now zooms to 1,000,000x (a phone's screen about 30 m across), and from
about a kilometre across (a metre a pixel, `ui/MapPick.kt`) every fix of the lit day is a ring to tap.
A tapped fix turns red with its clock, the phone asks the box which fixes a delete would take, and
the card says so before anything goes, "The fix at 14:10, and 3 more at this spot (14:10 to 15:00).
Delete them for good?" with **DELETE** and **CANCEL**. The box takes the fix and its neighbours in
time within 150 m of it, up to the first fix somewhere else (`framed/forget.go`), because a tower's
fix repeats and the map's simplified line keeps only one of them. More than 200 at one spot is
refused, that is a place and not a glitch. A fix the phone has not sent yet is sent first, so the
box knows it and does not get it again after the delete.

`POST /v1/geo/trail/forget {"ts","radiusM","dry"}` gives `{"ok","ts":[...],"deleted"}` (secd to the
framed ctl `trail-forget`); dry only says which. The points go from `location_points` (every
source) and the phone's own ring, and the day is rebuilt.

Stay names on the map claim their rectangle now, so two stays a street apart keep both rings and the
second name is left off instead of drawn through the first (the place names skip what the stays
claimed).

Days built before this have the old questions until they are rebuilt:

    sudo ./tools/ns.sh ./bin/ghost-cli ghost.framed day-routes days=60

Tested: `TestLookupOnLand` (an island with a lake, a land cell, the sea, no tiles),
`questions_test.go` (the Corfu crossing asked as sea and not asked without tiles, a slow boat not
asked, a flip between towers as one question, a jump at either edge of the data, 100 km/h out and
back asked, a drive at the data's edge not asked, `WaterRunM`), `TestSpotRunTakesTheTowersRepeats`,
`MapPickTest` (only close in, the nearest fix with a clock, the card's words, labels that do not
overlap), `TrailQuestionTest.asksAboutTheSea`. The map's drawing and taps are structure-checked only.

## Photos are named and tagged while the caption backlog runs (searchd, oracled, procs)

Vlad: "images are still not working ... i mean tagging and naming and all of that".

**The tag pass waited for every caption.** searchd's worker drained each lane in turn, captions
first, and only then the tag pass, which is what writes a photo's tags and its title (the name).
With a backlog of captions (thousands, after the captions came back and were unparked) the tag
pass waited behind all of them. Photos got a description and nothing else, untitled and untagged,
for as long as the backlog lasted, and the embeds for their chunks waited too, so search did not
find them either. The worker now runs in rounds (`Worker.tick`). Each round runs the embeds, every
tag pass that is due (text only, seconds each, and each one names a photo that is already
described), three of the category backfill, then ONE caption. A caption's tag pass runs in the next
round, newest photos first, and a photo that arrives while the backlog runs is described, named
and tagged within a couple of rounds.

**oracled killed searchd's embedder at every model start.** Before it spawns llama-server, oracled
ends any predecessor's orphan (the sixty-day-old one of 21 Sep). It matched every process running
`<mount>/bin/llama-server`, and searchd's embedder is the same binary, so every model start or
restart took the embedder down, and searchd never started it again. Search lost its vectors and
every embed job failed its way to parked until the next unlock. Now oracled ends only a
llama-server on ITS port without `--embedding` (`procs.KillStraysMatching`), and searchd watches
its embedder and starts it again when it dies (`EmbedServer.Watch`, 10 s after a death, longer
while it keeps dying).

**`queue` says whether it is moving.** `ghost.searchd queue` now also gives each job kind's due,
backing-off and parked counts with its newest failure, whether the model lanes are resting (until
when, and the error that rested them), and the photos described, named and tagged, in all and in
the last hour.

    sudo ./tools/ns.sh ./bin/ghost-cli ghost.searchd queue
    sudo ./tools/ns.sh ./bin/ghost-cli ghost.oracled models

Reading it:
- `photos.describedLastHour` climbing and `taggedLastHour` 0 was the old worker, gone with this drop.
- `modelLanes.resting` with "no backend" in `why` for more than a few minutes means the model is
  not up. `ghost.oracled models` says why (`ready`, `verdict`), and oracled's log has the start
  attempts.
- "no vision" in `why` means llama-server runs without its projector. Put `mmproj-F16.gguf` back
  (see "Captions come back") and restart oracled.
- parked jobs with a `lastError` are failures the model keeps making. `unpark kind=caption` (or
  `kind=tag`) once the cause is fixed.

Tested: `TestTagPassesFollowTheirCaptions` against Postgres (three captions queued, the model asked
caption, tags, caption, tags, caption, tags; every photo described, named and tagged; the queue
report's photo counts; a model with no backend rests the lanes with its reason and keeps the job's
lives; a parked tag job's error reported), `TestKillStraysMatchingSparesTheEmbedder` (the orphan on
the model's port ended, the embedder on its own port untouched), `TestEmbedServerComesBack` (the
embedder killed from outside is started again, and Stop ends the watch).

## Vision was switched off by hand and nothing said so (oracled, searchd, rotlog)

Vlad: "how did we lose vision?"

The queue said it: the caption lane resting on "no vision: llama-server takes no images ... you
may need to provide the mmproj". On 29 Sep, a full-size photo made the mirror's llama.cpp v0.5.0
abort and take chat down with it, and the way back that night was to move the projector aside
(`mmproj-F16.gguf` to `mmproj-F16.gguf.off`) and run the model text only. The next drop fitted
every image before the engine sees it ("Captions come back"), and its steps ended with putting
the projector back; that step did not happen, and nothing on the box ever said so.
- oracled noticed (`mmproj not found , starting TEXT-ONLY`), but through the package's default
  logger, which went to stderr, and watchd starts every daemon with no stdout or stderr. The line
  went nowhere.
- It then dropped the projector from its config for good, so even a file put back was not used
  until oracled itself restarted.
- The health line said OK, and `models` said ready: a text-only engine looked like a working one.
- searchd's "no vision" hold rested every model lane, not only captions, so the tag pass and the
  category backfill stopped too (one photo tagged in the last hour), though both are text.

Fixed:
- `rotlog.Logger` also makes itself the process's default logger, so a package-level `slog` line
  in any daemon lands in that daemon's log.
- oracled looks for the projector at every start (`projector()`), keeps whether the running
  engine sees images, and says it three ways: the log ("starting TEXT-ONLY: no photo will be
  described until the projector is back"), the health line (still OK, so chat and the unlock do
  not wait on it, with "text only, no photo is described: ..."), and `models` (`vision`,
  `visionWhy`). A `.off` beside the configured path is named: "the projector is switched off by
  hand (mmproj-F16.gguf.off): rename it back to mmproj-F16.gguf and restart ghost.oracled".
- searchd has two holds. "No backend" or a chat rests every model lane; "no vision" rests only
  captions (`captionLane` in `queue`), and tags and categories go on.
- Categorize: an answer with no category:tag pair at all ("Please provide the list of tags you
  would like me to categorize!") no longer fails the job until it parks. The ten tags are asked
  one at a time, and a tag that still gets no pair is "other".

On the box, put the projector back (the crash it was moved aside for is fixed: every image is
scaled to 1024 px before the engine sees it) and let the parked embeds go again:

    D=/proc/$(pidof ghost.secd | cut -d' ' -f1)/root/var/lib/ghost/mnt/slot0/ai-models
    sudo ls -la $D | grep mmproj
    sudo mv $D/mmproj-F16.gguf.off $D/mmproj-F16.gguf
    sudo ./tools/ns.sh ./bin/ghost-ctl restart-daemon ghost.oracled
    sudo ./tools/ns.sh ./bin/ghost-cli ghost.oracled models      # "vision": true
    sudo ./tools/ns.sh ./bin/ghost-cli ghost.searchd unpark kind=embed_text

Tested: `TestProjectorIsLookedForAtEveryStart` (missing, switched off by hand, put back and used
without a restart of oracled, not configured), `TestNoVisionHoldsOnlyCaptions` against Postgres
(the tag pass runs while captions rest, the caption keeps its lives), `TestCategorizeAsksOddTagsAlone`
(a chunk refused, then tag by tag, the odd one "other").

## Chunks too long for the embedder get a vector (searchd)

After `unpark kind=embed_text` the embedder took 1,392 of the 1,663 waiting chunks, and 131 jobs
went back to waiting on "embeddings: http 500". An embedding model reads an input whole, in one
physical batch, and searchd started it with `-ub 1024` under a 2048 context: an input over 1,024
tokens is refused ("input is too large to process. increase the physical batch size"). The chunker
sizes chunks from their words (400 estimated tokens), and a chunk of numbers, links or text without
spaces is many more tokens than that. One such chunk failed its whole batch of up to 64, five times,
into parked.

- The embedder runs with `-b 2048 -ub 2048`, the batch as large as the context.
- `Embedder.EmbedFitting`: a batch the server refuses is tried input by input, and an input it
  still refuses is cut to half, then half again (five times at most), so its vector is of its
  beginning rather than nothing; full-text search keeps the whole chunk. The log says how many were
  cut. An unreachable server is not retried input by input.
- The refusal now carries the server's own words (`embeddings: http 500: ...`).

After the deploy: `sudo ./tools/ns.sh ./bin/ghost-cli ghost.searchd unpark kind=embed_text` for
whatever parked meanwhile.

Tested: `TestEmbedFittingCutsWhatTheServerRefuses` (a batch with one long input: three vectors, one
from a cut text; a batch taken whole is one call; the server's message in the error; an unreachable
server fails at once).

## Box Status: what the card can do, and what framed and searchd did (gpu, secd, framed, searchd)

Vlad: "on the gpu can we have capabilities listed there and can we see on ghost framed how many
images we process and on ghost searchd how many things we process as well?"

**host.gpu.** Under the verdict, what the box uses the card for (oracled's `models`): the model,
whether it is on the card and how much of the card's memory it holds, what it is used for, whether
it sees photos (with the reason when it does not), and what runs on the CPU by choice (search
embeddings, voice). After the diagnosis rows, the card's own facts, read ONCE from nvidia-smi
after the shared probe's first good answer and kept (`internal/gpu/caps.go`): the chip and its
compute capability with the generation's name, the maths its tensor cores do (FP16, INT8, BF16,
TF32, FP8 on Ada), memory, driver and CUDA version, power limit (and the card's own maximum), top
clocks, PCIe generation. An nvidia-smi that refuses the full field list is asked the base fields.

The link verdict now asks the port above the card how wide it can go. "The link came up at x4 of
x16: physical, reseat the card" was said of a card in a slot WIRED x4, which no reseating changes.
When the port's own maximum is the card's width, the verdict is "working ... at x4, all the slot
is wired for" and the link row says "the slot is wired x4"; a x16 slot with a x4 link is still sent
to be reseated. The link speed row notes that a card lowers its link speed when idle.

**ghost.framed and ghost.searchd.** Each daemon counts what it does by kind, in one-minute buckets
over the last day and since it started (`internal/workcount`), and answers `work` on its control
socket; the drill-in lists it above the database's totals, "N last hour · M since start · last 20 s
ago" (and the last 24 h once it has been up a day).
- framed: photos, videos and other files archived, previews made, duplicates dropped, empty
  uploads skipped, uploads failed, files re-read by the stock-take, track points stored, days
  redrawn; and what waits in the spool.
- searchd: photos described, photos named and tagged, tags given a category, chunks embedded,
  items ingested, stock-take checks, searches answered, and the failures of each; and whether the
  model work or the descriptions are paused, with why.
The counts live in memory: an unlock starts them again, and the first row says since when.

    sudo ./tools/ns.sh ./bin/ghost-cli ghost.framed work
    sudo ./tools/ns.sh ./bin/ghost-cli ghost.searchd work

Tested: `TestCapsReadOnceAfterAGoodAnswer` (the full list, the CUDA header, the rows; an older
nvidia-smi's base fields), `TestArch`, `TestDiagnoseSlotWiredNarrow` (a x4 slot is working, a x16
slot at x4 is still reseat), `TestCountsByHourDayAndSinceStart`, `TestWorkRowsNameAndOrderTheKinds`,
`TestGPUUseRows`. The drill-in screens are the app's generic rows: no app change.

## "Near you" said no position with the trail on; the trail says where it is (app, secd)

Vlad: "It says no location but I do have it set up, is it not sent to the box how can I check?"

"Near you" measured from one thing only: the phone's last point, sealed to the phone's hardware
(`LocationLog.last`). When that could not be read, it said "no position yet , turn on the location
trail" to a person whose trail was on and whose day on the box was drawn from it. It now measures
from the newest point it can read (`LocationLog.newest`: the sealed last point, or the recent ring
while the app is unlocked), and when the phone has none, from the box's newest trail point (the
last vertex of the newest day in `/v1/geo/tracks`), and says which: "around this phone's last fix,
12 min ago" or "around the box's newest trail point, 2 h ago". "Turn on the trail" is said only
when it is off; when it is on and nothing has a point, it says to look in SETTINGS. The map's
"you" and [ where I am ] use the same newest point.

SETTINGS › LOCATION TRAIL now answers "is it reaching the box" on the phone itself:
- "last fix: 12 min ago" (or "none this phone can read yet");
- "to the box: sent 4 · 3 min ago", or why not, with when it last got through: "not sent: the box
  has no key for this phone's trail yet (handed over at the next PIN unlock)", "not sent: no box
  session (it comes with a PIN unlock)", "not sent: the box answered HTTP 503", "not sent: the box
  did not answer";
- [ send the trail to the box now ].
`LocationLog.flush` records each outcome (`lastSend`).

On the box, secd now logs the one refusal that was silent: a sealed batch from a device whose trail
key it does not hold yet ("location upload refused: no trail key for this device yet").

Checking from the box's side:

    sudo ./tools/ns.sh ./bin/ghost-cli ghost.framed trail day=2026-09-30    # the day's points, by source
    sudo ./tools/ns.sh ./bin/ghost-cli ghost.framed queue                   # batches waiting in the spool
    sudo journalctl -u ghost.secd --since today | grep -i "location upload"

Tested: `TrailStatusTest` (the fix line, the send line in its outcomes, where "near you" is
measured from). The screens are structure-checked only.

## The trail says which fix took each point, and how many are on the phone against the box (app, secd, framed, hw)

Vlad: "it stopped reporting, we are at 23:23 now and we went and had food at 19:31 and then came
back around 21:50 ... can we see which one ingested it and how many we have on the phone vs
synced? also no need to say on the map that you can zoom in to delete".

The trail report listed long hops only, so a quiet evening at home (a heartbeat an hour, no hop
of 2 km) read as the trail having stopped at 19:31. Now:
- `ghost-cli ghost.framed trail day=...` ends with the day's first and last point ("last point
  20:40:00 (1 h 43 min before now)") and every gap of an hour or more between points.
- Every point carries how the phone took it, `via`: `w` the quarter-hour fix, `p` a copy of another
  app's fix (PassiveFixReceiver), `a` the fix the app takes when it opens. The spool line is
  "ts lat lon acc via" (`sync/TrailLine.kt`; older lines still read); secd keeps the fifth field
  when it opens a sealed batch (the accuracy stays behind, as before) and framed stores it in
  `location_points.via` (new column, '' for what came before). The report counts them per source
  ("phone-84bcf711 51: 30 quarter-hour, 18 other apps' fixes, 3 app opened") and names each
  listed point's; the framed drill-in has "trail, last 24 h" by via and "newest track point: N
  min ago".
- SETTINGS › LOCATION TRAIL, under the switch: "today: 14 kept (9 quarter-hour · 3 other apps'
  fixes · 2 app opened) · 12 sent to the box · 2 waiting" and "on this phone: 51 points from the
  last two days · 1,204 sent to the box since the trail began" (the phone counts what the box
  acknowledged), with the last fix and the last hand-over lines from the section above.
- The map no longer says "zoom in closer to pick a fix" or "tap a fix to delete it"; only what a
  delete took is still shown for a few seconds.

Tested: `TestTrailReportSaysTheLastPointAndTheGaps`, `TestTrailReportSaysHowThePhoneTookThem`,
`TestValidVia`, `TestLocationBatchOpened` (via through a sealed batch), `TestViaStoredAndSummarised`
against Postgres (stored, read back, the drill-in's lines), `TrailLineTest`, `TrailStatusTest`
(the today and holds lines).

## Thinking was never switched on; the phone's notifications read the wrong list; cued said nothing (oracled, secd, hw, cued, app)

Vlad: "it looks like my setting for thinking from settings does not work ... notifications queue
does not seem to be there when i look at notifications, i expect cued to tell me when it's
something fun close by that i have not been to yet".

- Thinking. The app sent `think` ("", "brief", "deep") all the way to oracled and oracled set a
  token budget from it and nothing else. gemma's reasoning channel is opened by
  `chat_template_kwargs.enable_thinking`, which was never sent, so the model reasoned or did not
  by its own default whatever the setting said. `applyThink` now returns the flag as well: off
  closes the channel (and the budget stays at the caller's), brief opens it with 2048, deep with
  8192. One-shot `Infer` does the same; the multimodal path ignores the flag (the projector has no
  reasoning channel). `TestStreamChatCarriesHistory` asserts the flag and budget for all three.
- Notifications. The app's NOTIFICATIONS screen read the push cursor (`/v1/notifications/poll`),
  which consumes what it returns, so a notification the phone had already been pushed was not
  there when the person came to look. The screen now reads `/v1/notifications/list` (the history,
  seen or not), marks a row seen with `/v1/notifications/seen` and deletes with
  `/v1/notifications/delete`. On the box, `NotifStore.PushBatch` read only the Redis list, so a
  notification a daemon wrote straight into Postgres (framed, shadowd, watchd) never reached the
  phone; it reads the rows since the phone's cursor now, the Redis list only when Postgres fails.
  Mute and mobile-data sync are this phone's own: a stub "box settings" read at unlock used to put
  both back to off, so mute undid itself. Gone.
- cued. Its reflection loop asked for `kind = 'episode'`, a kind synthd retired (the day story is
  `kind = 'day'`), so it found nothing to reflect on. It reads both. And it now offers something
  new nearby once a day (`internal/cued/nearby.go`, `OfferNearby`): between 09:00 and 20:00, when
  the trail's newest point is under 45 min old, it ranks the places within 12 km by synthd's taste
  (the same ranking as MEMORIES › near you), skips what the photos say the person has been to,
  what the trail passed within 200 m, what is under the phone, and what it offered before (the
  last 60, in `settings.cued_nearby_sent`), and posts one notification (kind `nearby`, tapping it
  opens MEMORIES). No taste yet (synthd builds it from the tagged photos) means no offer, and the
  reason is in cued's log.

Tested: `TestStreamChatCarriesHistory` (brief/off/deep), `TestOfferNearbyAgainstPostgres` (been
there, too close, offered before, passed by, the offer), `TestRememberSent`.

## Health: the watch's data reaches the box, and stays (tallyd, secd, hw, app)

Vlad: "i still can't get my health data properly from my watch".

Several things, each enough on its own:
- The phone only sent health when a button was tapped. `HealthWorker` now runs every 6 h
  (`localghost.health`, scheduled at start) and sends the last week; the last run's outcome is
  kept (`HealthSync.lastRun`) and shown.
- The window was seven UTC days from now, so "today" was cut at the wrong hour and the day rows
  disagreed with the watch; it is local midnight minus seven days now, the aggregate is sliced at
  the local day start, and heart-rate buckets are deduped (a TreeMap per day) so the same minute
  never went twice.
- One bad sample (a name tallyd did not know, a duplicate (metric, ts) in the same batch) failed
  the whole upload statement, and the file stayed in the spool to fail again every pass: a poison
  pill. `internal/tally` (`Parse`, `Ingest`) validates the names, dedupes per (metric, ts) within
  a batch, inserts 500 rows a statement, and the journal line upserts. secd validates a batch
  before it lands (`tally.Parse`), writes `.part` and renames, and answers `{"ok","days","samples"}`
  so the phone can say what arrived. tallyd drains at start and every 30 s, keeps the last
  failure (`ghost-cli ghost.tallyd health` says Degraded with the file and the error when one keeps
  failing, instead of the stub "ok"), and prunes `done` to 200 files.
- `DayContext` (the day story's health line) took the day from the window's start in UTC, so a
  day that starts at 23:00 UTC the evening before read the wrong day's steps. It uses the
  window's midpoint. The notes it fed the model were polluted with framed's and tallyd's own
  journal lines and the check-in text; filtered.
- SETTINGS › HEALTH: the grant, the last run, send last week, send the whole history, and "what
  can the box see" (the probe), in one place; SYNC keeps the grant line and points there.

Checking on the box:

    sudo ./tools/ns.sh ./bin/ghost-cli ghost.tallyd health

Tested: `TestTallyIngestAgainstPostgres` (dedupe, upsert, bad names refused, the status),
`TestDayContextDayAndNotes`, `HealthStatusTest`.

## The day, told after the check-in (synthd, secd, hw, app)

Vlad: "a nice summary of the day after my check in would be good, take the audio, take the
pictures, take the locations, take the check in and put it all together".

synthd's `daySummaryPass` already wrote the day story from the photos, the trail, the voice
notes and the health line, but only for days that were over, dropped the check-in's "why", and
nothing on the phone showed it. Now today is a candidate the moment the check-in lands
(`checkedIn(db, today)`), the facts carry the feelings and the why ("You said you felt tired:
“long day at the office”"), and the phone asks for it: `GET /v1/day?d=YYYY-MM-DD&build=1` waits
for the check-in's journal entry (up to 45 s), asks synthd for the pass (`days {day, pass:true}`,
4 min) and returns `{day, checkedIn, built}`. `DayStoryCard` under the check-in's done state
shows it (writing / reading / failed / the story), and MEMORIES shows the day's story when there
is one.

Building it by hand on the box:

    sudo ./tools/ns.sh ./bin/ghost-cli ghost.synthd days day=2026-10-01 pass=true

Tested: `TestTodayTellsAfterTheCheckin`, `TestDayContextDayAndNotes`.

## Settings, and the buttons that did nothing (app, secd, hw)

Vlad: "the download map does not work in settings, can you organize things a bit better in
settings ... spend time and find all the other buttons / settings that don't work ... the popup
on status is too much text to read and does not have a scroll, i just need the important things".

- Map download. The worker centred on the phone's recent fixes and needed an unmetered network;
  with no readable fix (the trail off, or a fresh install) it had nothing to centre on and said
  nothing. It centres on the trail's newest point, then the box's last five days of trail, runs
  on any connection when asked by hand (metered is noted), and shows WorkManager's real state.
- SETTINGS is folded sections now (`Fold`): each closed line says the setting's state, open at
  first only where something needs attention. HEALTH is new (above). EXPORT is gone until the
  box can do it (the button used to share a made-up file). WIPE is "forget the box on this
  phone": that is what it did (the box was never erased) and it says so, one confirmation.
- Chats: the CHATS screen drew the box's conversations twice (the drawer's list, which is the
  same `/v1/chats`, above an "ON THE BOX" list of the same rows). One list, the open chat in
  green, and a rename or delete reloads it.
- SYNC's "test notification" was wired to nothing the screen showed; removed with its stub.
- Box Status: the rows loaded once at unlock and stood still all session; they poll every 10 s
  while the screen is open (a failed poll keeps the last rows). The daemon popup scrolls, and
  shows the rows the box marks as key first (`DaemonKV.Key`: what the daemon did in the last
  hour, what is waiting, what is paused, what is behind, the GPU verdict, the model and its
  speed) with "[ + N more ]" for the rest; a box that marks nothing shows its first five.

Tested: `TestKeyRows`, `DaemonRowsTest`, `TrailStatusTest`, `HealthStatusTest`; the screens are
structure-checked only.

## Whole countries on the phone (countrycells, secd, app)

Vlad: "can we make sure we can download more on the phone, the roads in the country or the
squares that overlap in a country somewhere in settings".

The map download kept streets within 25 km of where the phone had been and then the coast and
main roads outwards until the size was full; a trip to another country started cold. Now a person
picks countries, and the phone keeps every tile the box holds for them.

- `internal/countrycells`: the countries are Natural Earth's admin-0 polygons, the world.geojson
  every box already has under `<mount>/geo` (fetch_geo.sh). Each is rasterised onto the road
  tiles' tenth-of-a-degree grid once per secd run: a cell counts when its centre is inside the
  country or the border passes through it (every vertex's cell and the cells an edge crosses), so
  a thin coast is in and the interior is in; even-odd filling keeps holes out (Lesotho). The
  one-degree cells follow. A million-vertex world rasterises in about a second. ISO alpha-2 where
  Natural Earth has one (ISO_A2_EH, which fixes France and Norway's "-99"), else the ADM0_A3.
- secd `GET /v1/geo/countries`: every country with the tiles the box holds for it (streets, main
  roads, coast) and their size on disk, and whether the box has roads and coast at all; cached and
  recomputed when either index changes. `GET /v1/geo/country?code=GR`: the cells as index keys
  (y*cols+x per grid), only cells with a tile, so the count is the count to fetch.
- Phone, SETTINGS › MAPS ON THIS PHONE › WHOLE COUNTRIES: "[ pick countries ]" lists every
  country with what the box has ("Greece · 1,234 streets · 40 main · 31 coast · 312 MB"; one the
  box has nothing for says so and cannot be picked), with a find box. A picked country downloads
  on this network now (when downloads are on) and the daily run keeps it complete as the box cuts
  more; its line says "812 of 1,274 tiles on this phone (312 MB in all)". Picked countries sit
  outside the size budget (the size was shown when picking) and the cache's trim allows for them.
  The fold's closed line names them. The worker fetches a country's streets first, then its main
  roads, then its coast, then the usual tiles around where you have been; a tile wanted for both
  is fetched once.

On the box nothing to run: the world file is there and the indexes are read as they are. A box
that has not cut roads lists every country with "nothing on the box for it" and the picker says
what to run.

Tested: `TestRasteriseSquareWithHoleAndIsland`, `TestIdentify`,
`TestCountriesFromTheWorldAndTheIndexes` (the empty list without a world file, the sizes, the
keys, the list following a new tile), `MapPlanTest.aWholeCountryIsStreetsThenMainRoadsThenCoast`,
`MapCountryTextTest`.

## News and rates, fetched by the phone, kept by the box; the time zones (feeds, rates, tzgrid, synthd, tallyd, secd, framed, app)

Vlad, forwarding the mirror side's spec: news and rates for the server repo, the tz, elevation
and wikipedia sets on the mirror; the design call to confirm, that the box still never fetches
anything itself. Confirmed and built that way: the phone fetches, the way it already runs the web
search round; the publication list, parsing, dedup, summaries and history live on the box. The
cost stands as stated: publishers and exchanges see the phone's address, and the box gets nothing
while the phone is off.

The round trip:
- `GET /v1/fetch/list`: what to fetch, the box's list (`news_feeds`, seeded with twelve: BBC,
  Guardian, FT, Economist, Telegraph, Al Jazeera, NPR, DW, Politico Europe, Ars Technica, Hacker
  News, CoinDesk; Reuters and AP have no official feed any more) and the market sources (the ECB's
  eurofxref-daily.xml, Coinbase, Kraken, Bitstamp, Gemini tickers, CoinGecko with CoinPaprika as the
  fallback), each with how often. Edit the list on the box: `ghost-cli ghost.synthd news
  add='{"id":"x","name":"X","url":"https://…"}'`, `remove=id`, `enable=id on=false`.
- The phone (`sync/BoxFetch.kt`, `FetchWorker` hourly, UNMETERED unless mobile data may sync):
  fetches each address with a browser user agent and no cookies, 3 MB cap, and posts the bodies as
  they came (`POST /v1/news/fetched`, `POST /v1/rates/fetched`); the tickers every run, the feeds
  every two hours. SETTINGS › NEWS AND RATES: the switch, the last run, [ fetch now ], the cost in
  one line. secd spools each batch whole into synthd's and tallyd's inboxes.
- synthd (`news.go`, `internal/feeds`): parses RSS 2.0, Atom and RDF with the standard library,
  strips the feeds' HTML, keeps new entries (`news_items`, a month), groups entries into stories by
  title (content words, light stemming, half the smaller set in common; `news_stories`), writes a
  one-or-two-sentence summary per story from the outlets' own titles and descriptions on the GPU,
  held to the day memories' check (every number in a report, no list, no refusal), and posts a
  digest at 07:00 and 19:00 in the person's zone (kind `news`, the most-told stories first, eight
  at most; nothing new means no digest). `ghost-cli ghost.synthd news` shows the feeds' health
  and counts; `digest=true` posts one now. Box Status › ghost.synthd: one key row for the news and
  a row per feed; a feed that has failed three times in a row is a key row.
- tallyd (`internal/rates`, `internal/tally/rates.go`): the ECB table (`fx_rates`, one euro in each
  of 30-odd currencies), each exchange's quote (`btc_quotes`), the index (`btc_index`): quotes older
  than fifteen minutes go, then quotes more than 2% from the median, the rest averaged by volume,
  with how many went in and the spread; the top 100 (`coin_ranks`, a week). An exchange missing
  from a batch joins with its last quote when that is still fresh. `ghost-cli ghost.tallyd rates
  amount=100 from=EUR to=GBP` converts; Box Status › ghost.tallyd shows the ECB day, the index and
  the rank list's age.
- Chat: after the archive and before the web, the news the phone fetched (`newsSource`: full-text
  over the week's entries) and, for a money question, the box's own numbers (`ratesSource`: the
  ECB line, the BTC index with its making, the conversion done on the box so the model never does
  the arithmetic). Offline.
- The phone's NEWS screen (`NewsScreen`, in the drawer under YOUR ARCHIVE): the stories of the
  last two days, the most-told first, the summary, the outlets; a story opens to each outlet's own
  headline and "[ open at BBC ]", which opens the article on the phone. The digest's tap lands
  there. A markets line at the top.
- Time zones (`internal/tzgrid`): the mirror's tz set (timezone-boundary-builder with the oceans,
  under `<geo>/tz`, fetched by fetch_geo.sh) rasterised by ghost.framed into `<geo>/tz/grid.bin`
  at a twentieth of a degree (two bytes a lookup; `ghost-cli ghost.framed tz-grid` builds it now,
  `setting lat= lon=` names the zone at a point). The trail's newest point names the person's zone
  (settings `local_tz`); the digests' hours, synthd's "today" and cued's quiet hours read it
  (`hw.LocalZone`). The IANA rules ride in the binaries (`time/tzdata`), so a box with no tzdata
  package still knows Athens. health.sh prints `time zones:`.

Not built this round, said plainly: Wikipedia through kiwix-serve (the 60 GB set is published by
hand and kiwix-serve's search answers want checking against the real thing) and the elevation
tiles (a TIFF reader with no outside libraries, against a tile format not yet looked at closely).
Both are next once the sets are on the mirror.

Tested: `TestParseThreeDialects`, `TestSimilarTitles`, `TestParseECB`, `TestParseQuotes`,
`TestIndexMethod`, `TestParseCoinsBothSources`, `TestConvert`, `TestBuildAndLookup` (tz),
`TestNewsIngestStoriesAndDigest` and `TestRatesIngestAgainstPostgres` against Postgres,
`TestMoneyQuestion`, `TestRatesItems`, `TestGroundedNews`, `TestDigestBody`,
`TestFetchListAndSpools`, `NewsTextTest`. The phone's fetch and the screens are
structure-checked only; the real feeds and tickers were not reached from here.

## The box fetches for itself when the phone is away; prices from seven venues, USDT folded into USD, the daily history (egress, rates, tally, tallyd, synthd, secd, app)

Vlad: "let's get directly from exchanges, coinbase, kraken and a few others, binance, for the
rates, have a usdt to usd rate and combine the usd and usdt prices to just a usd price. let's have
the box pull the prices from the apis and the news … if the phone is on wifi then the phone gets
it and sends it back, if the phone is on 4g or unreachable then we pull from the server wherever we
last left off, we keep daily rates and we can build the daily rates historically".

The rule, in one place (`internal/egress`): the phone tells the box every quarter hour what
network it is on (`POST /v1/phone/net`, with its notification poll). While it said Wi-Fi within the
last twenty minutes, the phone is the proxy: it fetches hourly and the box stays off the wire. On
mobile data, or with the phone silent, ghost.tallyd and ghost.synthd fetch what is due themselves
through the one outbound client (a browser-like agent, no cookies, short deadlines, a 3 MB cap),
from where the marks say they left off (every source ingested leaves `settings fetch_<id>`, whoever
fetched it). `ghost-cli ghost.tallyd rates fetch=true` and `ghost.synthd news fetch=true` fetch now
regardless; both commands say who fetched the last batch and why the box did or did not.

The honest change to the promise: until today no daemon opened a connection. Now two do, to the
public addresses in the sources list and to nothing else, and only when the phone is not on Wi-Fi.
The Hard Truths post and the About page want this sentence: "The box reaches the internet for two
things, the news feeds and the market tickers it follows, and only when your phone is not on Wi-Fi
to fetch them for it; the publishers and exchanges see the box's address, nothing else leaves."

Prices (`internal/rates/markets.go`): seven venues, no key: Coinbase, Kraken, Bitstamp, Gemini,
Bitfinex in USD; Binance and OKX in USDT. The USDT/USD rate is the median of the fresh USDT/USD
quotes (Coinbase, Kraken, Bitstamp, Bitfinex's tUSTUSD); a USDT-quoted price times that rate is a
USD price, so the index is one USD number per symbol (`crypto_index`), made as before (fifteen
minutes stale out, 2% from the median out, volume-weighted, venues named, spread told). Symbols
followed: BTC and ETH to start; `ghost-cli ghost.tallyd rates add=SOL` follows another (the
venues' pairs are derived; Kraken's XBT spelling and Bitfinex's UST are handled). `/v1/fetch/list`
carries the markets for the phone.

The days: every market's daily candles (`crypto_daily`, each venue's own) and the box's daily USD
close per symbol (`crypto_daily_index`: the venues' closes, USDT folded with that day's USDT/USD
close, a venue more than 5% from the median left out). The last week comes with the hourly run,
by phone or box; the years come from the box alone, a venue page a tick (300 days for Coinbase,
1000 for Binance, Bitstamp and Bitfinex, Kraken's 720 and Gemini's window once, OKX 100), walking
back from the oldest day held until a venue has nothing older or 2010 (`hist_done_<market>`).
Where it left off is the table itself, so nothing is lost to a restart or a week on Wi-Fi. The
ECB's table goes back to 1999 in one fetch of its history file (`ecb-hist`, once), and its 90-day
file daily fills any day missed. `GET /v1/rates/history?code=BTC&days=365` (or `code=GBP`) reads
the days back; `/v1/rates` carries the index per symbol and how deep the history goes; Box Status
› ghost.tallyd shows a key row per symbol and the history's depth.

Tested: `TestProxyRule`, `TestClientFetchesAndCaps`, `TestMarketsAndURLs`, `TestParseTickers`
(seven venues), `TestParseCandles` (seven venues, the same two days), `TestIndexMethodAndUSDT`,
`TestDailyIndex`, `TestConvert` (any symbol), `TestRatesIngestAgainstPostgres` (two ECB days, the
USDT fold, the daily index, the marks, the history read), `TestBackfillWalksBack`,
`TestNewsFetchedByTheBoxWhenThePhoneIsAway` (a local feed server: left to the phone on Wi-Fi,
fetched on mobile, nothing due, forced), `TestFetchListAndSpools` (the network word). The real
venues were not reached from here: the first hour's `ghost-cli ghost.tallyd rates` is the test of
the seven; a venue that answers something this build does not read shows in `failed`.

## The top fifty in a few calls, and one number for crypto as a whole (rates, tally, tallyd, secd, synthd, app); the copy on what leaves the box

Vlad: "let's do top 50 and let's do an index out of all of them as well, weighted by average
monthly volume, the weights change every month, it gives us an indication of crypto overall". And:
"update the copy to say we do pull news and things from external sources so your box ip might be
used … but your thoughts and words never leave your box".

- The fifty. The symbols followed are the fifty largest by market cap on the newest day of the rank
  list (stablecoins and wrapped coins out: `rates.NotInIndex`), plus anything added by hand
  (`rates add=SOL`); BTC and ETH until the first rank list lands. Asking a venue for one pair at a
  time was three hundred and fifty calls an hour; six of the seven venues answer every pair they
  trade in one call (Binance, OKX, Kraken, Bitfinex, Bitstamp, Gemini: `<venue>:all`,
  `rates.ParseBatch`, Kraken's X/Z spellings and Bitfinex's UST read back), and Coinbase, which has
  no such call, is asked pair by pair for the ten largest and the USDT leg. Gemini's price feed
  gives no volume, so it is left out of a price that other venues weigh (a price with no weight is
  not a weight of zero). The daily candles are the box's own now, whatever the phone is on: once a
  day for every pair the venues were seen quoting (`tally.MarketsSeen`), and the history a page a
  tick as before, over those pairs.
- The market index, `CRYPTO50` (`internal/rates/marketindex.go`, `internal/tally/marketindex.go`):
  each month's constituents are the fifty largest by market cap on the last day of the previous
  month, weighted by their share of the previous month's average daily dollar volume (the rank
  list's all-exchange volume, kept a row per coin per day in `coin_daily`); base prices are that
  day's closes (the venues' where they have them, the rank list's where not); the value is chained,
  the end of one month the start of the next, from 1000 on the first day there is a rank list, so a
  change of constituents never jumps it. A coin with no price on a day is held at its last and
  named. Rebuilt after every rates batch from two days back (a late candle is taken up); the live
  value uses the venues' indexes of the last two hours, the newest rank price where there is none.
  `/v1/rates` carries `market` (value, change on the last close, constituents, priced live, the
  month's weights, the five heaviest); `/v1/rates/history?code=CRYPTO50` the days; `ghost-cli
  ghost.tallyd rates` shows it; Box Status › ghost.tallyd has a key row; chat answers "how is crypto
  doing" with it (`marketQuestion`); the NEWS line opens with it.
- The copy. Every place that said the box never reaches the internet now says what is true: your
  thoughts and words never leave the box; it pulls general information in to use in context (the
  feeds and the prices it follows, what a plugin asks for), through the phone when it is on Wi-Fi,
  itself otherwise, so a publisher or an exchange may see the box's address; it only pulls and tells
  them nothing. ABOUT has a "WHAT LEAVES THE BOX" section, the glossary a term, the unlock tip and
  SETTINGS › NEWS AND RATES the same words. The post draft (outputs/hard-truths-what-the-box-does-now.md)
  has a "What leaves the box" section with its cost, and the web side has the paragraph for the
  About page (outputs/web-side-what-leaves-the-box.md).

Tested: `TestParseBatchSixVenues`, `TestKrakenAndBitfinexPairs`, `TestSymbolsFromRanks`,
`TestWeightsAndValue`, `TestMarketIndexAgainstPostgres` (two months, a rebalance from September's
volumes, the venues' close over the rank list's, a carried coin, the live value against the live
index, a rebuild that moves nothing), `TestRatesIngestAgainstPostgres` (coin days from the rank
list, the symbols followed), `TestMarketQuestionAndItem`, `NewsTextTest`.

## A price every minute for a week, every hour for thirty days (rates, tally, tallyd, secd, synthd, app); the lock screen at home

Vlad: "i need an hourly price for the last 30 days and a minute price for the last week, we can
pull prices every minute from the sources". And: "can we not have the hey do you want this on the
lock screen, let's have it on by default and when it's on home soil we just show news and crypto
stuff for now, just a news summary with the most interesting news and the btc and eth prices".

- Every minute, from the box. A minute series cannot come from a phone Android wakes every quarter
  hour at best, so the tickers are the box's own now, whatever the phone is on: tallyd ticks on the
  minute and asks the seven venues (six all-pairs calls, Binance's compact `type=MINI` form, and
  Coinbase pair by pair for the ten largest and the USDT leg: seventeen requests). Each minute's
  index per symbol goes into `crypto_series` (res 1m, source live) and the market index's value
  into `crypto_market_series`. The phone keeps the ECB and the rank lists while it is on Wi-Fi
  (`/v1/fetch/list` now lists only those four); the box takes them over otherwise, as before.
- The hours. Every complete hour with at least thirty minutes is rolled up from the minutes (open,
  high, low, close; source minutes), every ten minutes, again when more minutes land under it.
- The history, so the thirty days and the week are there now and not in a month: each symbol's
  hourly and minute candles are walked back from two venues that quote it (hours: Binance,
  Coinbase, Kraken, Bitstamp, Bitfinex, OKX, Gemini in that order; minutes: Binance, Bitstamp,
  Bitfinex, Coinbase, OKX, since Kraken's 720 bars and Gemini's single page cannot walk a week of
  minutes), the USDT/USD leg first, twelve pages a minute within a 45-second budget, into
  `crypto_bars`; each page is folded into the series where the box has no point of its own
  (`rates.FoldBar`: per open, high, low, close the mean of the venues within 5% of their median,
  USDT folded at that time's USDT/USD), source venues. A live point is never overwritten by a
  folded one; a rolled hour replaces a folded one. A venue that brings nothing older, or fails
  three times, ends that market's walk (`bars_done_<res>_<market>`). Once a resolution's walk is
  whole, the market index is made over it (`RefoldMarket`, a day at a time). The hours take about a
  quarter of an hour after the first start, the week of minutes about two hours.
- Kept: minutes eight days, hours thirty-five, the venue bars the same, each venue's raw quotes two
  days (they are a quote a minute per pair now). Pruned once an hour.
- Read: `GET /v1/rates/series?code=BTC|CRYPTO50&res=1m|1h&hours=N`, oldest first, each point
  `{ts, o, h, l, c, n, src}`. `/v1/rates` carries each symbol's change over 24 hours from the hourly
  series; chat says it ("ETH 3,250 USD, -1.50% in 24 h"). `ghost-cli ghost.tallyd rates` shows
  `series` (per resolution: the market's points, how far back, symbols in the newest step, markets
  still being walked); Box Status › ghost.tallyd has a key row per resolution.
- The lock screen. The card is on from the start; the offer is retired (an offer an older build left
  standing is taken down). At home (the network's country is home's, or unknown) the card is the
  home brief: the most-told stories of the last day (several outlets telling the same thing is what
  makes a story worth a glance; the newest first among equals), the headline as the title, BTC and
  ETH with their 24-hour change as the line under it, the summary, the outlets and the next two
  headlines pulled open, NEXT and OPEN NEWS as its buttons, and the BTC price on the live-update
  chip. Away, the phrases as before. The phone fetches the brief with the notification poll every
  quarter hour and when the app opens (`HomeBrief`), keeps it, and the card redraws from it. The
  widget follows the same switch. Welcome, PHRASES and Settings say so.

Tested: `TestBarsAndFold`, `TestMarketsAndURLs` (minute and hour URLs, the phone's and the box's
sources), `TestSeriesAgainstPostgres` (the pages chosen with the USDT leg first, folded hours, the
walk back and its end, live minutes over a folded one, the hour rolled up, the market over the
folded hours without touching the rolled one, depth, pruning), `TestRatesItems` (the 24-hour
change), `TestFetchListAndSpools`, `HomeBriefTextTest` (the most-told first, the prices line, the
chip, the card pulled open). The tick itself and the card are not run here; the real venues were
not reached.

## How the feeds are doing, on Box Status (feedstat, monitor, tally, tallyd, synthd, secd, app)

- The fetch log. Every address fetched for the box leaves a row in `fetch_log` (new table): the
  source, its kind (ticker, ecb, ranks, daily, history, news, tick), who fetched it (box or phone),
  the HTTP status, whether it came and was of use, how long it took, its size, how much it gave
  (quotes, days, candles, coins, new entries) and why not. `IngestRates` writes one per source,
  synthd's news ingest one per feed, the history walk one per page, and tallyd one per minute for
  the minute as a whole. The box times its own fetches (`egress.Fetched.TookMs`); the phone times
  its own too and sends `tookMs` with each body. Kept two days, pruned hourly.
  `internal/feedstat` reads it back per source or per exchange: calls and successes over a window,
  p50, p95 and the slowest, the newest fetch, the newest good one, the fetches failed since (a
  minute's calls count once), the newest error.
- The report. `internal/monitor` reads the database straight, so it answers while a daemon is
  down, and judges nine sections, each with a state (ok, filling, waiting, flaky, late, failing), a
  line, the newest piece's age and the detail rows:
  - who fetches: the phone's network and when it was heard, which side fetches what now, who
    fetched the ECB, the rank lists and the news last;
  - prices: the newest price's age (late past 2.5 min, failing past 15), the minutes held in the
    last sixty (flaky under 57 once the box has run an hour), symbols priced of those followed and
    which have none, venues per symbol, symbols on one venue only, symbols where the venues
    disagree by over 1%, how long each minute takes and whether one ran into the next;
  - exchanges: per venue over the last hour the share that came, p50 and p95, the pairs in the
    newest minute, how far behind its own clock its prices are (the venues that stamp them), how
    often its quotes went into the index and the commonest reason when not (Gemini gives no
    volume, a stale quote, too far from the others); failing after three failed minutes or ten
    without a good one; the section is flaky while one venue is down and failing past half;
  - price history: per resolution how much of the window is held, the markets still walking back,
    the pages left and the time that is at the last ten minutes' pace, CRYPTO50's points against
    the window's, a venue whose pages fail;
  - CRYPTO50: the value, the day's change, constituents priced live (flaky under 80%), the weights'
    month, the days of history, the heaviest five;
  - ECB rates: the newest table and the working days it is behind (the table is due at 16:00
    Frankfurt, Monday to Friday; one missed day is let pass for TARGET holidays, late at two,
    failing at five), each of the three files' last fetch;
  - rank lists: the newest list's source, size and age (late past two hours, failing past six),
    CoinGecko and CoinPaprika each;
  - daily candles: BTC's closes back to when, pairs refreshed in the last day, the years walked back
    and what is next, a venue whose candles fail;
  - news: feeds that answered within the fetch interval and an hour, the last fetch and by whom,
    how long a feed takes, the last day's new entries, stories and summaries, how long after
    publication a story reaches the box (median), the last and next digest, each feed failing
    twice or more in a row.
  The worst section is the report's state; the summary says which want a look, or that all is
  well and what is still filling.
- How far the walks have come is worked out by tallyd once a minute (it holds the markets seen;
  working that out on every status read would scan two days of quotes) and kept in settings
  (`tally_progress`: per resolution the markets, those still walking, pages left, coverage,
  whether the market index was made over it; the daily backfill's markets done, oldest day, next).
  `NextBarPages` and the progress share the one choice of markets (`barMarkets`).
- Read: `GET /v1/feeds/status` (in the OpenAPI document), `ghost-cli ghost.tallyd feeds`, and
  `tools/health.sh` prints the summary and one line per section under ghost.tallyd.
  `ghost-cli ghost.tallyd rates` shows `progress` in place of the heavy series depth.
- tallyd's health turns degraded when no minute of prices has finished for five minutes, so
  watchd and Box Status show the minute stopping.
- Coinbase is no longer asked every minute for a pair it does not list: a 404 for a pair keeps it
  off the list for a week (`coinbase_absent`), and the next symbol takes its place among the ten.
- The app. Box Status has a DATA FEEDS panel under the archive pipeline: the summary and the
  report's state, then a line per section with its mark (● ok, ◐ filling, ○ waiting, ▲ flaky or
  late, ✕ failing), its line and its age, ticking between polls; a tap opens the box's detail rows,
  each coloured by its own state. Polled every 30 s while the screen is open; a report over two
  minutes old says so. The ghost.tallyd drill-in keeps BTC and ETH as key rows and one line for
  the rest (it had a key row per symbol, fifty of them).

Tested: `TestKindsAndVenues`, `TestPriceState`, `TestVenueState`, `TestECBMissed`,
`TestNewsStateAndDigest`, `TestWordsAndSummary`, `TestFetchLogStats` (per venue and per source,
latencies, failures in a row with a minute's calls counted once, the window against the whole log,
pruning), `TestIngestRatesLogsEachSource`, `TestMonitorReport` (a box a few hours in: every
section's state, the lines and rows that carry the delays), `TestHistoryProgress`,
`TestFetchListAndSpools` (the status route appears down with no database), `FeedsTextTest`. The
real venues and feeds were not reached from here; the first hour on the box is the test.

Build fix (app): `BoxClient.countryCells` is internal (it returns `MapPlan.Country`, and `MapPlan`
is internal), and `NotificationsScreen` imports Compose's `getValue`/`setValue` for its `by remember`
state (the fully qualified `remember` did not bring the delegate operators with it).

## Photos the box cannot read, Coinbase's rank list, home, the box's numbers in chat, the articles and the brief (framed, oracled, searchd, rates, synthd, secd, monitor, app)

- Photos nothing can read. Some originals are damaged beyond both decoders (Go's: "missing 0xff00
  sequence", "bad Huffman code"; ffmpeg's: "Picture size 29476x44915 is invalid", "dqt: invalid
  precision"). They had no preview, went to oracled for a caption from the original, failed, and
  came back at every stock-take (three caption jobs failing every few minutes, a screen of ffmpeg
  in the log each time). Now framed marks such a photo (`frames.unreadable`, the reason) when both
  decoders refuse it, and clears the mark if a later read succeeds. Converge counts them apart
  (`unreadable photos: N`), neither re-reads them nor asks searchd for a caption (a pipeline bump or
  `ghost-cli ghost.framed unreadable retry=true` reads them again). `/v1/pipeline` leaves them out
  of the stage totals and says `unreadable`. oracled tells "nothing on the box can read it, the
  file looks damaged" apart from "a converter is missing", with ffmpeg's first line only; searchd
  finishes such a caption job without a caption instead of failing it five times.
  `ghost-cli ghost.framed unreadable` lists them with what each file says: whether its bytes still
  hash to its name (the archive is named by the hash of what arrived, so intact means it arrived
  like this), how it starts, its longest run of zero bytes and the zeros at its end (a copy cut
  short or a cloud placeholder half fetched). `GET /v1/frames/unreadable`; Box Status' pipeline
  panel shows "N photos the box cannot read" and lists them by capture time on a tap.
- Coinbase's rank list in place of CoinGecko and CoinPaprika (`coinbase-ranks`): the coins Coinbase
  lists, by market cap, with price, cap, circulating supply, the day's change and dollar volume,
  from the list coinbase.com's own price pages read (`www.coinbase.com/api/v2/assets/search`,
  `filter=listed`, `sort=rank`). Not a documented API: Coinbase's documented public endpoints give
  products and volumes but an empty market cap. The followed symbols and CRYPTO50's constituents
  are now the largest coins Coinbase lists (BNB, TRX, HYPE and the like drop out unless added by
  hand). A batch from an older phone with the old ids still lands.
- The box's numbers are not searched for. synthd's `/plan` says `search:false, boxHas:<why>` before
  asking the model (and on the CPU too) when the question is a price of a coin the box follows (by
  symbol in capitals, the common ones in any case, or by name: "chainlink price"), the top N coins,
  crypto as a whole, a rate between currencies, or the day's headlines. The phone leaves the web
  alone for these in every mode and says so in the status line; without the box's plan it judges
  the common cases itself (`BoxKnows`). The chat's rates source puts in any followed coin by name
  and, for "top N", the rank list's first N at the box's own prices.
- The articles. For each story about to be summarised synthd reads up to two of its articles'
  pages (twelve a pass), keeps the paragraphs the page serves anyone (`news_items.body`), and marks
  how it went (`body_status`: ok; paywalled, the page's own schema.org isAccessibleForFree false,
  only its free part kept; short; or the HTTP status). A paywall is never got round: no crawler's
  name, no borrowed cookies. The story's summary is written from the reports and the article (two
  or three sentences, numbers held to both). Logged as fetch_log kind `article`; the news section
  of Box Status says how many were read whole, behind a paywall, short or failed.
- The brief: from the summaries of the day's six most-told stories, the day's news in three or four
  sentences (numbers held to the summaries), rewritten when those stories change or after two
  hours (`settings news_brief`, `/v1/news` brief and briefAt).
- Home. The app opens on HOME: BTC and ETH always, at the box's own price with the day's change,
  CRYPTO50 under them; the brief; the five most-told stories; and a box to ask from, which starts a
  new chat. CRYPTO (from home's prices, or the menu) lists the fifty largest, each at the box's own
  price where it follows it. NEWS's market line shows BTC and ETH only. Back goes home.
- `tools/health.sh`: ghost-cli prints indented JSON, so every compact pattern in the script matched
  nothing (oracled's model line, synthd's outings and days, tallyd's feeds, secd's status line
  showed "{"). Answers are folded onto one line first (`cj`). Under ghost.tallyd it prints the
  feeds' summary, a line per section and every detail row that is not well.

Tested: `TestDamagedJPEGIsUnreadable`, `TestUnreadablePhotoSetApart` (marked at the first pass,
not re-read or captioned at the second, out of the stage totals, the file check),
`TestImageForModelConvertsWhatTheModelCannotRead`, `TestParseCoinbaseRanks`, `TestBoxHas`,
`TestTopItem`, `TestArticleText`, `TestGroundedBrief`, `BoxKnowsTest`, `HomeTextTest`,
`UnreadableTextTest`, and the rest as before. Coinbase's list, the articles and the brief were not
reached from here.

## Your papers: articles read as you (hw, egress, synthd, secd, monitor, app)

- A paper you subscribe to can be signed in to once, from the phone: Settings › NEWS AND RATES ›
  YOUR PAPERS › sign in, on the paper's own page inside the app (a WebView). Done hands the box
  the sign-in (the paper's cookies and the WebView's user agent) and the app forgets it; the box
  keeps it on the volume (`news_logins`), never serves it back, never logs it.
- synthd's article reads send that paper's sign-in and agent for its articles (`hw.KeyFor`: the
  host's domain or a parent of it); Go drops a Cookie on a redirect to another domain. The
  paper's articles of the last two days that stopped at the paywall are read again when a sign-in
  is added. A signed-in read that still stops at the paywall marks the sign-in expired: Box
  Status' news section and the Settings row say to sign in again.
- `GET/POST /v1/news/logins` (in the OpenAPI document). A cookie with a line break is refused.
- Nothing pretends to be anyone else (no crawler's name): a paper not signed in to is read only as
  far as it serves anyone.

Tested: `TestArticlesReadAndSignedIn` (paywalled, then whole with the sign-in and its agent, the
sign-in's record, never served back, a header-injecting cookie refused, forgotten),
`TestLoginDomainAndKey`, `PaperTextTest`. The WebView sheet itself is not run here.

## Damaged photos moved aside; no paper sign-ins; the FT retired; feeds that never answer switched off; articles let go

- Damaged photos are moved, not marked. When neither Go's decoder nor ffmpeg can read a photo
  (on arrival, or when the stock-take reads one again), framed moves the original out of the
  archive into `<mount>/frames/damaged`, named by when it was taken
  (`2026-09-30_1432_<hash>.jpg`), adds a line to `damaged/list.txt` with the reason, and forgets
  it: the frame, its tags, its journal line, searchd's original and its queued caption and tag
  jobs (`Store.ForgetFrame`). The stock-take then has nothing left to retry. Deleting them is
  yours: `sudo ./tools/ns.sh ls /var/lib/ghost/mnt/slot0/frames/damaged`. `/v1/pipeline` says how
  many sit there (`damaged`) and Box Status shows the count; `/v1/frames/exists` answers "have"
  for a damaged photo's hash, so the phone does not send it again. The `frames.unreadable`
  column, `/v1/frames/unreadable`, `ghost-cli ghost.framed unreadable` and the app's list are gone
  (a box that got the column keeps an unused one).
- The paper sign-ins are gone (`news_logins`, `/v1/news/logins`, YOUR PAPERS, `egress.GetWith`; a
  box that got the table keeps an empty one).
- The FT is off the default feeds and taken off a box seeded with it, once (`feeds.Retired`,
  `news_retired_v1`): its feed, its entries and its count in the stories it told.
- A feed that has never once given a feed is switched off after six fetches in a row
  (`feeds.GiveUpAfter`, twelve hours): judged from the box's own fetches from home, so only a
  feed that is really not there goes. Box Status' news section lists what is switched off;
  `ghost-cli ghost.synthd news enable=<id>` asks again.
- Articles: asked for the way a browser asks for a page (HTML first, English), read for the
  story's summary and let go once it is written (and after a day whatever was not summarised);
  the box keeps the summary, not the article. A summary written with the article is up to three
  short paragraphs; from the feeds alone, one or two sentences.

Tested: `TestDamagedPhotoSetAside`, `TestFeedsRetiredAndGivenUp`, `TestArticlesRead` (a free page
whole, a paywalled one's free part, both let go after a day), `TestDefaultSourcesAreWellFormed`,
`TestFetchListAndSpools`, and the rest as before.

## Prices and news from Redis, BTC/ETH/SOL every five seconds, the news as points, pull to refresh, back home, the aggregators gone, removed files handled

- Redis holds what the phone wants at once (`hw/hot.go`, the vault's Redis through apparedis):
  `hot:rates` is `/v1/rates` as it stands (tallyd rewrites it after every minute and every batch
  the phone sends, five minutes to live), `hot:news` is the last two days of `/v1/news` with the
  brief (synthd rewrites it after every batch it takes and every five-minute pass, half an hour to
  live). secd answers from there and goes to Postgres only on a miss (and puts what it read back
  for a minute), or for a `since` older than two days or a `limit` of its own. Postgres stays the
  record, the rank list included; Redis is the copy, gone on a restart and rebuilt on the first
  read.
- The fast lane: tallyd asks Coinbase's ticker (`api.exchange.coinbase.com/products/X-USD/ticker`)
  for BTC, ETH and SOL every five seconds (three small requests, well inside its public limit) and
  keeps the last trades in Redis only (`hot:fast`, a minute to live; the minute series stays the
  record). `/v1/rates` puts them over the minute's index (`hw.ApplyFast`: the 24-hour change moved
  to match, a trade over 30 s old or more than 5% off the minute left out, `fast:true` on the
  row), and `GET /v1/rates/fast` gives just those three from Redis, so home and CRYPTO ask it every
  five seconds while they are open. A minute of the lane is one fetch-log line (`coinbase:fast`,
  kind `fast`); Box Status' prices section has a row for it, and `ghost-cli ghost.tallyd rates`
  a `fast` block. The other coins stay on the minute.
- The news as points. A story's summary written with its article is a lead and two to four points
  (`lead\n- point\n- point`); `groundedNews` takes bullets, numbered lines and wrapped lines, and
  holds every number of the kept text to the reports. From the feeds alone it stays one or two
  sentences. The brief is one point per story, in the stories' order (`groundedBrief`: no
  preamble, no more points than stories), written from the stories' leads; `/v1/news` adds
  `briefStories`, so a point opens its story. The digest and the lock screen tell the lead alone.
  Once, the last day's model summaries are cleared and their articles read again
  (`news_points_v1`), and a prose brief is rewritten at once.
- "What's the news" in chat now gets the box's brief and the five most-told stories in its context
  (the plan already told the phone the box has it).
- Articles: a page that builds its paragraphs in the browser often carries the article in its
  schema.org block (`articleBody`); it is read from there when the page says it is free. An outlet
  that refused six pages in a day and gave none is left alone for the day (its stories told from
  the feeds); Box Status lists per outlet what its pages answered ("articles, The Telegraph: 0 of 9
  read · 9 refused or failed (last: HTTP 403)").
- CoinGecko and CoinPaprika are gone everywhere: the parser takes Coinbase's list only, and tallyd
  deletes their rows from `coin_ranks`, today's `coin_daily`, the fetch log and their fetch marks
  once a start (`tally.PurgeOldRankLists`). The rank list stores Coinbase's circulating supply
  (`coin_ranks.supply`); CRYPTO and the chat's top list show the cap at the box's own price
  (price × supply). CRYPTO50's October constituents were fixed on 1 October from the old list;
  November's are Coinbase's.
- The app: pull down to refresh on home, CRYPTO and NEWS (the box's copy at once; the feeds fetched
  too when they are half an hour old); a ‹ beside the title goes home from any screen (the ghost
  and the title too); home's brief is a list of points, each opening its story in NEWS; a story in
  NEWS shows its lead and the first two points (all of them open).
Tested: `TestApplyFast`, `TestNewsDocWithin`, `TestGroundedNews` (lead and points, numbering,
a point's number not in the reports), `TestGroundedBrief`, `TestLDArticleBody`,
`TestRefusedOutletLeftAlone`, `TestRetellAndBriefStories`, `TestSQLPrepareEveryStatement`; app
`HomeTextTest` (cap from supply, fast over the minute, brief points to stories) and
`NewsTextTest` (lead and points). The fast lane and the Redis copies run on the box only.

## Clean-up: no removal script; the removed features' leftovers dropped from the database

- `tools/apply_removed.sh` and `tools/removed.txt` are gone, and `redeploy.sh` no longer calls
  them. A drop that removes a file says so, with the `git rm` line to run.
- Schema data migration 2 (once per box, at the next unlock) drops what the removed features left:
  the `news_logins` table (the paper sign-ins) and `frames.unreadable` (damaged photos are moved to
  `frames/damaged`). The schema's rule stands, a column the registry does not know is only named
  (DRIFT in secd's log), never dropped, unless a dated migration says so.

## The fast lane from every exchange, prices that move, when they were updated, "write now", health fixed

- The fast lane asks every exchange that weighs in the index for BTC, ETH and SOL every five
  seconds, not Coinbase alone (`rates.FastAsks`): one call each to Binance (`symbols=[...]`),
  Kraken (`pair=XBTUSD,ETHUSD,SOLUSD`) and Bitfinex (`tickers?symbols=...`), one per coin to
  Coinbase, Bitstamp and OKX; twelve requests, the venues side by side on kept-alive connections
  (`egress.NewKeepAlive`). Gemini is not asked (no volume, so the index leaves it out anyway), nor
  the USDT/USD leg (the minute's rate folds Binance's and OKX's prices into dollars). Each coin's
  price is made the minute's way (`rates.FastIndex` over `MakeIndex`: fresh quotes, 2% from the
  median, volume-weighted), with the venues that went in on the row. One fetch-log line per venue
  a minute (`fast:<venue>`); Box Status says how many venues answer and gives a venue that misses
  its own row.
- The app: a price that changes rolls digit by digit (up green, down amber) and flashes
  (`TickingPrice`), on home and CRYPTO. Home says when BTC and ETH were updated and from how many
  exchanges ("updated 3 s ago · 6 exchanges", counting each second); CRYPTO says it for the fast
  three and for the rest.
- "[ write now ]" on home writes the day's brief at once (`POST /v1/news/brief`, synthd's
  `news brief=true`, one at a time): the brief comes back, or why not (fewer than two summaries,
  the model on the CPU, an answer that did not hold to the stories).
- Health: the calories were Health Connect's resting estimate (the same 1,564 kcal every day;
  Samsung Health shares no total), so the phone now reads ACTIVE kcal
  (`ActiveCaloriesBurnedRecord`, a new permission to grant), the box ignores "calories" from an
  older phone, and migration 3 deletes the old rows and the "N kcal." in each health day's journal
  line. Steps, distance and active kcal take Samsung Health's own count where it wrote one (the
  same aggregate filtered to its package): Health Connect's priority had put a phone-side counter
  first, a few per cent under Samsung Health. "What is in Health Connect?" now reads newest first,
  so it says when a source stopped writing.
- health.sh prints the feeds' troubled rows even when a value holds an escaped quote (a Go error
  quotes its URL; those rows were the ones silently missing).

Tested: `TestFastAsks`, `TestFastIndexAcrossVenues`, `TestApplyFast` (venues on the row),
`TestBriefNowSaysWhy`, `TestRemovedFeaturesDropped` (migrations 2 and 3),
`TestHealthIngestAgainstPostgres` (the resting estimate refused, active kcal in the line); app
`HomeTextTest` (the prices' line). The lane, the roll and the Health Connect reads run on the box
and the phone only.

## Notifications open what they are about, a week is kept, the weekly ones stop repeating

- A notification carries where a tap goes (`notifications.link`, `hw.Notification.Link`):
  framed's week in frames `map:<day>` (that day lit and framed on MAP), cued's reflection
  `memories:<id>` (MEMORIES filtered to that memory), cued's near-you `memories:near` (NEAR YOU
  open), the check-in `memories`, synthd's digest `news`, watchd's alerts and shadowd's
  observation `status` (Box Status). One from before goes by its service and kind (`NotifLink`).
  A tap in the list marks it read and opens it; each says where ("open on MAP ›"); a tap in the
  shade lands in the same place after unlock.
- A week is kept: `/v1/notifications/list` shows the last seven days, and secd deletes older rows
  (and trims the Redis list) every half hour while unlocked (`PruneNotifications`).
- The weekly highlight came every day: its week key was `Format("2006-W02")`, and "02" in a Go
  layout is the day of the month, so the key changed daily. framed and shadowd now key by
  `ISOWeek()`, and migration 4 keeps one of each repeated weekly text.

Tested: `TestNotificationLinkAndWeek`, `TestRemovedFeaturesDropped` (migration 4), app
`NotifLinkTest`.

## The box's price: a blend of every market, the top 100, coin pages, a nicer roll; memories about me and my people

- The price (`rates.Blend`, `internal/rates/blend.go`): a coin's dollar price from every market it
  trades in, each market's last price converted through its quote currency (USD as it is; USDT,
  USDC, EUR, BTC, ETH through the box's own price of that currency, blended first, the euro from
  the ECB), weighted by the market's 24-hour volume in the coin and a time penalty (1 under 60 s,
  falling in a straight line to 0.001 at 1,500 s); with more than two markets one past A × the last
  price (or under it / A) is left out, A 1.05 with 15 markets or more, 1.10 with 10 to 14, 1.15
  with fewer. A stablecoin is priced from USD and USDT markets only. `BlendAll` works the
  conversion currencies out first (USDT, USDC, BTC, ETH), then every other coin. The minute and the
  five-second lane both blend; `crypto_index` keeps the markets and the currencies each price came
  through (`markets`, `paths`). The old median-and-2% method is gone (`MakeIndex`, `InUSD`,
  `USDTRate`).
- The venues give every pair in one answer each, Coinbase's products list
  (`api.coinbase.com/api/v3/brokerage/market/products?product_type=SPOT`) among them, in place of
  its pair-by-pair tickers: seven calls a minute. The pairs kept are the followed coins and the
  conversion currencies in USD, USDT, USDC, EUR, BTC or ETH (Kraken's XETHXXBT and SOLXBT,
  Bitfinex's UDC, Binance's SOLBTC read too). Quotes go in two hundred rows a statement. The
  candles stay on USD and USDT markets.
- The top 100: `tally.Symbols` is the hundred largest on Coinbase's list, stablecoins and wrapped
  coins too, priced every minute; `tally.HistorySymbols` is the fifty that can be CRYPTO50's, whose
  hours and minutes are walked back from the venues (the others build theirs from the box's own
  minutes from the day they joined).
- A coin's page: `coin_info` keeps Coinbase's description, colour, site and white paper (from the
  rank list, hourly); `GET /v1/coins/info?symbol=` gives them with the list's rank, cap, supply and
  volume, the box's price and the blend right now, market by market (price, dollars, volume, age,
  share, or why left out); `GET /v1/rates/sparks` a week of hourly closes per coin (Redis, ten
  minutes). The app's COIN page: the price rolling (five seconds for BTC, ETH and SOL), the chart
  over 1D, 1W, 1M, 1Y or all (a finger on it reads a point), the figures, what the coin is, and
  where the price comes from with each market's share. CRYPTO lists the hundred with a week's line
  per row and opens a coin on a tap; home's BTC and ETH rows open theirs. CRYPTO's header is one line.
- The roll: only the digits that changed roll, the rightmost first, each a beat after, landing
  with a small overshoot; each glows green or amber and fades; an arrow says which way.
- Memories about me and my people: a note in MEMORIES (`GET/POST /v1/about`, settings `about_me`);
  synthd reads it when it changes (`aboutPass`) and keeps facts about me (kind `me`) and one memory
  per person (kind `person`), and the name it gives (`owner_name`). The distiller writes in the
  first person ("I…", "My…", never "the user"), and a fact about one of my people joins that
  person's memory (`PERSON | name | fact`, `notePerson`). MEMORIES has chips by kind: me, people,
  days, outings, distilled, written by me.
- LocalGhost knows its name: every chat question starts from "You are LocalGhost… When I say
  LocalGhost, the ghost or the box, I mean you", the name and the note (clipped); the context and
  the web findings are introduced in the first person.

Tested: `TestTimePenalty`, `TestBlendWeighsVolumeAndFreshness`, `TestBlendStableOnlyFromDollars`,
`TestBlendAllConvertsInOrder`, `TestBatchKeepsTheConversionPairs`, `TestParseCoinInfo`,
`TestFastAsks`, `TestFastIndexAcrossVenues`, `TestBlendThroughConversionPaths` (Postgres: SOL
through USDT, EUR and BTC, the coin page and its markets), `TestMarketIndexAgainstPostgres` (100
priced, 50 walked), `TestParseAbout`, `TestIdentityAndDistillPrompt`, `TestNotePerson`; app
`CoinTextTest`.

## Coins read up and written by the box, memories by name, home kept on the phone, FOR YOU

- What a coin is, written by the box (`cmd/ghost.synthd/coindesc.go`): for each of the top hundred,
  two a slow pass on the GPU, it reads Wikipedia's summary of the coin's article (found through
  Wikipedia's search, kept only when it names the coin and is about a cryptocurrency), the coin's
  own site (its description and paragraphs; an address with a name only, never a bare or local
  one) and Coinbase's text, and the model writes three or four plain sentences from those: no
  price, no forecast, no advice, every number one the sources give, the coin named. Kept in
  `coin_info` (`written`, `written_at`, `written_from`); written again after ninety days, a try that
  read too little tried again after three. `/v1/coins/info` carries them; the COIN page shows the
  box's text with "written by your box from Wikipedia, Coinbase and solana.com", Coinbase's line
  until then. `ghost-cli ghost.synthd news` says "N of 100 coins written".
- Memories by name: once the box knows the name (the note's `NAME`), the note's memories and the
  distilled ones are written in the third person ("Vlad prefers…", "Cristina is Vlad's partner…"),
  so a search for a name finds them; a slip ("the user", "I am", "my", "me") is mended to the
  name. Without a name, the first person as before. `hw.AboutHash` carries a version
  (`v2-names`), so the note is made into memories again at synthd's next pass; `namePass` writes
  the older first-person distilled and people memories again with the name, eight at a time, three
  batches a pass, on the GPU, a line kept only when it names the person and adds no number (never
  one edited by hand). Chats start from "I am Vlad; the memories you are given say Vlad where they
  mean me". Day and outing memories still speak to "you".
- Home rides along: the notifications poll and the trail's upload answer with `home`
  (`hw.HomeSnap`: BTC, ETH and SOL with the day's change, the fast lane over the minute, CRYPTO50,
  the brief and its stories, the day's six most-told), read from Redis (Postgres on a miss); `GET
  /v1/home` gives the same. The phone keeps the snapshot and the last `/v1/rates` and `/v1/news` it
  read (`HomeCache`, files under the app's own storage), so HOME, CRYPTO and the lock screen open on
  the latest at once; the lock screen's quarter-hour refresh reads `/v1/home` (a few kilobytes)
  instead of the whole news and rates.
- FOR YOU on home (`hw.ForYouNow`, Redis `hot:foryou` for twenty minutes or until the trail moves a
  kilometre): places within 10 km of the trail's newest point (six hours at most) ranked by what
  the person photographs, the new ones first; this date in earlier years (the day story, else the
  outing, else the day's photos and where), opening the memory or the map on that day; the day's
  stories that share words with the `me` memories and the distilled titles ("you mention
  blockchain"), the brief's own left out. FOR YOU stays in the phone's memory, never on its
  storage.

Tested: `TestMakeHomeSnap`, `TestAboutHashCarriesTheVersion`, `TestInterestTerms`,
`TestPickStories`, `TestForYouStillFresh`, `TestForYouNow` (Postgres: the days by the local date,
the stories), `TestParseAbout`, `TestNamed`, `TestNamePromptAndParse`,
`TestIdentityAndDistillPrompt`, `TestPickWikiTitle`, `TestParseWikiSummary`, `TestSiteText`,
`TestPublicSite`, `TestCoinDescPromptAndGrounding`, `TestCoinsToWrite` (Postgres); app
`HomeDataTest`, `CoinTextTest`.

## wisp 0.0.1, the first release cut; Wikipedia and the heights on the box; a page per day and per notification; places and what the box notices

- **The release.** `tools/cut_release.sh 0.0.1` cuts wisp 0.0.1, or cuts it again from the same
  commit to the same bytes: the name in `tools/release.names`, the notes in `releases/0.0.1.md`
  (what it does, what is in it, how it works; NOTES.md in the bundle and the set), the pin in
  `releases/pins.txt` (written at the first cut; a moved tag is refused). The first cut tags HEAD
  as `v0.0.1`; every cut builds from the tag in a clean worktree. secd reports `name` beside
  `version` (/v1/update); the phone says "wisp 0.0.1 is out" and "your box runs wisp 0.0.1".
  `RELEASES.md` at the repo root is the index. The app's versionName is "0.0.1 wisp".
- **Wikipedia on the box** (`internal/zim`, a ZIM reader over `internal/zstd`, the Go standard
  library's own decoder copied in with its licence; `internal/wiki`, an article's lead as plain
  text): the mirror's set wikipedia under `<volume>/wiki` (`sudo ./tools/update.sh wiki`, about 50
  GB). synthd reads it for the coin pages (then the internet is asked about no coin), for the chat
  ("what is X", "tell me about X": the lead in the context, `wikiSource`), and `ghost-cli
  ghost.synthd wiki title=Solana` reads one by hand. health.sh says which file the box has.
- **The heights** (`internal/dem`: a GeoTIFF reader, the standard library and nothing else, for
  the Copernicus DEM at 90 m; `tools/fetch_geo.sh` with `GHOST_GEO_ELEVATION=all` or a box like
  `"34:72,-25:45"`, through `mirror_fetch.sh`'s new `@names-file` mode; `update.sh maps` keeps the
  tiles a box has current): ghost.framed draws each day with `alts` beside `times` and the day's
  `climbM`, `descentM`, `highM`, `lowM` (every day drawn again once when tiles arrive;
  `ghost-cli ghost.framed elevation lat= lon=`); `/v1/geo/tracks` carries them; the MAP's trail
  panel says "climbed 420 m · highest 1,240 m up" and the scrubber's point its height; the day
  story's facts say what was climbed.
- **A page per day** (`DayScreen`, Dest.DAY, link `day:<day>`): the story (or "write this day"),
  the photos, the outing it was part of, where it went, the body, the notes; the day before and
  after, a year either side; the trail on the MAP and "ask about this day". HOME's "this day",
  MEMORIES' on-this-day cards and the week's highlight open it.
- **A page per notification** (`NotificationScreen`, Dest.NOTIFICATION, `notification:<id>`): who
  said it and when, the whole text, an ask's choices and answer, and under it the thing it is
  about shown there (the day's story and photos, the memory, the places near you, the day's
  news) with a button on to the full screen. The shade's tap and the list's tap land on it; the
  green line in the list still goes straight to the thing.
- **Places and what the box notices** (`cmd/ghost.synthd/places.go`): memories of kind `place`
  counted from the day routes and the photos (days, span, hours, the usual weekday, the photos
  there; made again when the routes change, never over an edited one), and kind `insight` once a
  day on the GPU from a sheet of the last weeks (places, walking, steps by weekday, photos and
  their tags, the check-ins' feelings, the people mentioned), kept only when every number is on
  the sheet. MEMORIES' chips: distilled (the chats', people, places, noticed), each on its own.
  FOR YOU brings one memory back a day (what the box noticed lately first).
- **About me from the check-ins** (`checkinAboutPass`): what was said at the check-in (the voice
  note's transcript, the written why) read for facts about me and my people, oldest first, four
  a pass; facts join the check-ins' `me` memories by title (`noteMe`) and the people's memories;
  the name when the box has none.
- **Box Status**: the archive pipeline and the data feeds fold under ghost.framed and
  ghost.tallyd, one dim line each ("data feeds · 2 want a look", "archive pipeline · 99% at the
  latest stage · 26 damaged"), the panel on a tap.

Tested: `internal/zstd` (the upstream testdata), `internal/zim` (a ZIM written by `zimtest`: paths,
titles, redirects, both cluster kinds), `internal/wiki` (the lead, the variants, the shared file),
`internal/dem` (float and int tiles, deflate and LZW, the predictors, big TIFF, pixel is point,
no-data, the set by name, Climb), `TestBuildDayPathHeights`, `TestGeoTracksBatch` (alts through),
`TestWikiSubject`, `TestWikiFromTheBox`, `TestPlacesCounted`, `TestInsightPromptAndParse`,
`TestPlacesPassAndInsightFacts` (Postgres), `TestCheckinWordsAndPrompt`, `TestNoteMe` (Postgres),
`TestForYouNow` (the memory brought back); fetch_geo.sh and mirror_fetch.sh against a signed fake
mirror; cut_release.sh in a scratch repo (a cut, a recut with a dirty tree giving the same sums, a
moved tag refused). App: `DayTextTest`, `NotifLinkTest` (the page), `ReleaseInfoTest` (the name),
`FeedsTextTest` (the folds), `HomeDataTest`, `CoinTextTest`; 206 JVM tests.

## One release for the whole of LocalGhost: the app and the source beside the server set

- `tools/cut_release.sh <version> [--apk <file>]` now makes `release/<version>/` with `server/`
  (the mirror's set, as before), `app/` (`localghost-app-<version>.apk` handed in with `--apk`,
  its GPG signature, `APP.txt`), `source/` (git archive of the tag, which is also how a box is
  set up) and `SHA256SUMS` over everything, signed by the site key when the cutting user's gpg
  holds it (info@localghost.ai; without it the cut says so and signs nothing). The APK is built
  in the cut from the tag's tree when the Android SDK is on the machine (`ANDROID_HOME` or
  `~/.localghost_android_env`) and the keystore is named in `~/.config/localghost/release.env`
  (`LG_KEYSTORE`, `LG_KEY_ALIAS`, `LG_KEYSTORE_PASS`): gradle `assembleRelease` with a public
  `local.properties` (no box baked in), zipalign, apksigner, the version checked with aapt2, the
  signing certificate in APP.txt; else `--apk <file>` hands one in, `--no-apk` leaves it out.
  The app stamps the commit's time as its build time when the tree is clean, so a release built
  again from its tag is the same APK. The `gh release create` line names every file.
- The app's `versionName` is "0.0.1" with `BuildConfig.RELEASE_NAME` "wisp" beside it (VERIFY
  BUILD shows "wisp 0.0.1", and its release link works); `GITHUB_REPO` points at
  LocalGhostDao/localghost; the source manifest link is `app/android/ghost/…`. The app's
  provenance scripts (`sign_source.sh`, `verify_source.sh`, `release.sh`) work from the app's
  folder inside the repository rather than assuming it is the repository.
- The notes (`releases/0.0.1.md`) say what the release holds and how a box and a phone are
  installed from it.

## A release carries the app: the cut stops without it, and the keystore you have is registered in one line

- A cut of wisp on the box came out without the APK: `cut_release.sh` found no SDK or no
  keystore, said so in one line among the build's output and went on, so the release looked
  complete and was not. Now the cut checks the app's tools before it tags or builds anything and
  stops with the reason (no SDK, no keystore, no java) unless `--no-apk` is passed on purpose;
  an app build that fails is a failed cut too, with the server set left in `release/<version>/server`
  for the eye and the tag pinned, so the cut is run again once fixed and gives the same bytes.
- The SDK is found more widely: `ANDROID_HOME`, `ANDROID_SDK_ROOT`, `~/.localghost_android_env`,
  `sdk.dir` in the checkout's `app/android/local.properties` (what gradle used for `installDebug`),
  then `~/android-sdk`, `~/Android/Sdk`, `/opt/android-sdk`. The first line of the cut prints
  which SDK, build-tools and keystore it is using.
- The keystore needs no configuration when it is `~/localghost-release.jks` (where the box's is):
  the cut takes it, apksigner asks for its password on the terminal and signs with its only key
  (`--ks-key-alias` is passed only when `LG_KEY_ALIAS` is set; apksigner needs it only for a
  keystore with several keys). `~/.config/localghost/release.env` names another file, the alias,
  the password (`LG_KEYSTORE_PASS`, then nothing is asked) and `LG_KEY_PASS` for a key whose
  password differs from the store's (Android Studio allows that).
- `tools/app_keystore.sh --use <file.jks> [--alias <alias>] [--store-pass]` writes that
  release.env for a keystore that already exists: it opens it with the password typed, takes its
  only key as the alias or checks the one given (and lists the keys when there are several or the
  alias is not there); without `--use` it makes a new PKCS12 keystore (RSA 4096, a hundred years)
  and refuses to overwrite one.
- `SHA256SUMS` no longer lists the `.asc` signatures (a GPG signature carries its time, so it is
  never the same twice; the sums now are, when the build is: the recut in the scratch repo gives
  an identical SHA256SUMS with the app in it).
- A cut run under sudo once left `release/0.0.1/` root-owned, and the next cut as coder died on
  `rm: Permission denied`. The cut now refuses to run under sudo (root's gpg has no site key, and
  root's files block the user's next cut) and, when it cannot clear `release/<version>/`, says
  whose files are in the way and the `sudo rm -rf` that clears them, instead of six rm errors.
- Two Kotlin warnings the box's build printed are cleared (`UnlockClock.kt` a `!!` on a non-null
  value, `QrScanScreen.kt` a `.toFloat()` on a Float). The three AGP "Project object as a
  dependency notation" deprecations are the Android Gradle plugin's own, for a newer AGP.
- Tested: cut_release.sh in the scratch repo (no SDK: stops before tagging; SDK from
  local.properties and no keystore: stops; `~/localghost-release.jks` found with no release.env,
  SDK from `~/android-sdk`: the full cut with the APK, APP.txt, the signatures; a recut identical;
  a wrong versionName refused), app_keystore.sh --use with one key (alias taken), two keys (asks
  for --alias, lists them), a wrong alias, a wrong password.

## Photos filed as videos: HEIF stills told from clips by all their brands, and the archive put right by itself

- A day of October 2024 showed "84 videos", a play glyph on every thumbnail, and a story quoting
  "video archived , Wed Oct 2 2024, 11:48" six times; every one of them a photo. The sniff read
  only the ftyp box's major brand and knew a handful of HEIF brands (heic, heix, hevc, heim,
  heis, mif1, msf1); a phone's HEIC with any other major brand (hevx for a 10-bit or HDR still,
  heif, a brand the camera maker chose) fell to the default, "an MP4 video". The thumbnails were
  there because ffmpeg's frame grab reads a HEIC as happily as a clip.
- `framed.Sniff` now reads every brand in the ftyp box: an image brand anywhere (ISO 23008-12
  requires mif1 among a HEIF image's compatible brands, whatever the major brand) makes it a
  still, .heic or .avif; a clip's brands (isom, mp42, avc1, qt, 3gp) never include one. With no
  brand to decide by, the box after ftyp decides: a HEIF's meta, a clip's moov or mdat. Truncated
  heads and oversized size fields are read as far as the bytes go.
- The archive puts itself right: `PipelineVersion` is 3, so the stock-take at framed's next start
  re-derives every frame. On re-derive the file is renamed to what it is (`<hash>.heic` in place
  of `.mp4`), the row's kind, mime and archive_path follow the sniff (the one exception to
  InsertFrame's "never replace a fact": the sniff is read from the bytes by the current code and
  is the authority), and the journal line is replaced when its kind word is wrong ("video
  archived …" becomes "photo archived …", with the time the row kept, not the midnight a re-derive
  falls back to); a line whose kind is right is left alone even when a place would now read
  differently. The previews made from the frame grab stay (same picture). The day's story is
  rebuilt by synthd when its facts' signature changes (the backfill reaches it; "write now" on the
  DAY page does it at once).
- Stills Go cannot decode (HEIC, AVIF, WebP) now get their previews through ffmpeg at archive time
  too, as reprocess already did, rather than "archived without preview"; and a still no decoder
  on the box reads is left in the archive previewless, never set aside as damaged (that was the
  reprocess path's fate for a HEIC on a box without ffmpeg's decoder).
- searchd keys a frame by the hash in its file name, so the rename changes nothing there; the
  path search.originals recorded at ingest is only ever inserted, never opened.
- Tested: `TestSniffStillsAndClips` (sixteen heads: JPEG, PNG, WebP, HEIC by major brand, by a
  compatible brand, hevx, a motion-photo sequence, AVIF, unknown brands with a meta box, phone
  MP4, mdat-first MP4, 3GP, QuickTime, WebM, TIFF, too short), `TestSniffTruncatedFtyp` (every
  head length from 12 bytes, an oversized size field, isom major with mif1 compatible),
  `TestRederiveTurnsAMisfiledHEICBackIntoAPhoto` (Postgres: a .mp4 HEIF with a "video archived"
  journal line re-derived: renamed, row converged to photo/image/heic/v3, journal rewritten with
  the row's time; a second re-derive and a place-only rewording leave it alone; a real clip keeps
  its name). 47 packages ok, vet clean.

## The repo as it looks from outside: the README says what is built, CI, a Mac build, a phone retired by its key

Anchor Terminal's review of the repo (anchorterminal.com/tools/localghost) listed what a reader
finds: no CI, tests that do not compile where they tried them, a README, CONTRIBUTING and
SECURITY.md still saying "Phase 0, first commit incoming" against 69,000 lines of Go and a cut
release, an open pull request from July, no per-device revocation. Most of it was true. This round:

- **README.md, CONTRIBUTING.md, SECURITY.md** rewritten to what is built: the two parts, the
  daemons that exist, what leaves the box (the list from the release notes, with "if you find a
  request this section does not name, that is a bug, and a security report"), the vault and the
  PINs as they are (one PIN, a wipe PIN; the decoy volume and the duress flow named as the design
  the vault is built towards, not what wisp ships), how to get it, how to work on it, where it is
  going. The site key's fingerprint in SECURITY.md (DCE9 A3D1 4EB4 6197 1DD5 F393 706E 4194 F08A
  09A0, the same key that signs the mirror and the releases); the scope lists the daemons that
  exist; `docs/SECURITY.md`, which never existed, is no longer linked. The Anchor Terminal badge,
  a CI badge and a release badge at the top.
- **CI** (`.github/workflows/ci.yml`): `go build`, `go vet`, every Go test against the runner's
  own Postgres over its socket (as on a box; `createuser -s runner`, peer auth), a macOS
  cross-compile (`GOOS=darwin go vet ./...`), and the app's JVM tests (`:app:testDebugUnitTest`,
  JDK 21 as the box's setup installs). The app job has not run under gradle here (no SDK in this
  session); run it on the box once before pushing the workflow. Actions are free on a public
  repository; the app job (the heavy one) runs on pull requests and by hand only, and a push
  cancels a run still going on the same branch.
- **The tree builds on a Mac.** Pull request #3 (0ex-d, July) and issue #2 said the tests fail on
  macOS over `Pdeathsig`, a Linux-only field. Fixed on main with build tags rather than the PR's
  branch (it no longer applies): `procs.ChildAttr()` (`attr_linux.go` with Pdeathsig,
  `attr_other.go` with the process group alone) at both spawn sites (searchd's embedder, oracled's
  llama-server); `harden.NoDump` (prctl) and its test Linux-only with a no-op beside it;
  `gpu.readKernelLog` (syslog(2)) split the same way; `secd.devOf` given a non-Linux and a Windows
  counterpart. `GOOS=darwin go vet ./...` is clean; Windows is not a target (roadtiles mmaps).
- **A phone retired by its key.** The retired list and the rekey flow existed (a rekey retires the
  QR's certificate), but nothing let a person retire one particular phone. Now `ghost-cli
  ghost.secd devices` lists the enrolled phones by device key (the 16-hex name every listing
  shows) and `ghost-cli ghost.secd retire id=<key>` puts the key on the retired list on the OS
  disk, so it holds while the box is locked; the front door matches a retired key against the
  first 16 of every certificate id. `POST /v1/devices/retire {"device":"<key>"}` does the same
  from a phone, never for itself. No un-retire: a fresh QR enrols the phone as a new device. The
  app's DEVICES screen does not have the button yet.
- **The flaky timing test** (`TestGateTreatsTheWipePinAsWrong`) takes the median of three KDF runs
  each side and allows 60% between them, so a busy runner's one slow run is not a tell.
- **The app's rotation tests** asserted that the first candidate grid is the true size; the
  sampler's design says the decoder is the judge, and at 5, 10 and 15 degrees a finder-shaped
  coincidence in the data ranked a wrong triple first (37, then 57 modules). The tests now assert
  the true grid is among the candidates the decoder gets, and the synthetic data fill is hashed
  rather than a lattice (a lattice at 10 degrees reads as a timing pattern at the wrong pitch).
  206 JVM tests pass, none ignored.
- **The cut's GitHub upload** leaves the server set's own SHA256SUMS home: GitHub flattens the
  folders, two files of one name collide and the upload stopped at the second, which is why the
  v0.0.1 release on GitHub lacked SHA256SUMS(.asc), APP.txt and the source archive.
- Decided, and said in the README: the box CA stays on the OS disk (a phone can be enrolled and
  refused while the vault is locked; a choice, not an oversight), and there is no agent surface
  for now. The README carries a short "if you are an agent reading this" section instead: the box
  exists, people have one, it answers nobody but its phone, and an agent surface may come later,
  opened by the owner. Still ahead: shorter device certificates renewed by the phone, an offline
  release key, the decoy volume, the retire button on the phone.
- A recut on the box ended without a word right after "the app built and signed": `aapt2 dump
  badging … | head -1` under `pipefail`, where `head` closing the pipe gives aapt2 SIGPIPE
  (141), the command substitution fails and `set -e` ends the script, with no SHA256SUMS written.
  The cut now reads those tools' output whole (`sed -n '/^package:/p'`, an awk that keeps the first
  match) with `|| true`, and the scratch cut runs against a fake aapt2 that prints twenty thousand
  lines. The recut APK's hash (1e2bfa65…) differs from the one on GitHub (2a840655…): gradle's
  build is not byte-reproducible the way the server's is, so the published APK is handed back in
  with `--apk` for the sums rather than replaced.
- Tested: 47 packages ok, vet clean on Linux and darwin; `TestRetireByDeviceKey` (a phone retired
  by its upper-cased key is refused on every route, survives a restart, bad keys refused, a
  sibling untouched); the JVM suite.

## wisp 0.0.2: a second cut with everything in it, rather than patching 0.0.1's page

- wisp's GitHub page is short of SHA256SUMS, its signature, APP.txt and the source archive (the
  upload stopped at a name collision), and the recut APK does not match the published one
  (gradle is not byte-reproducible), so filling the page in meant handing the published APK back
  into the cut, and that failed too: the "is it an APK" check ran `unzip -l | grep -q` under
  `pipefail`, grep closed the pipe at the first match, unzip took SIGPIPE, and the genuine release
  APK was "not an Android app". Fixed (the listing is read whole, `unzip -Z1` into a variable and
  `grep -c`), and the remaining early readers in the cut (`head -1` on `ls` and `sed`) replaced
  for the same reason. The decision: cut 0.0.2 with the day's tree instead, still named wisp (the
  name is the release line, the number is the cut: every 0.x is a wisp, shade is 1.x, then
  specter, phantom, poltergeist; said in release.names and RELEASES.md).
- `tools/release.names` gains "0.0.2 wisp"; `releases/0.0.2.md` is the delta over 0.0.1 (the
  HEIC repair, retire by key, the cut, the repository, the small things), what the release holds,
  installing over wisp (the archive re-read at first start), the known gaps; RELEASES.md and
  CITATION.cff (version 0.0.2, an abstract that describes what is built) follow.
- The app's version and release name come from one place: `appVersion` and `appVersionCode` at
  the top of `app/build.gradle.kts`, and `RELEASE_NAME` read from `server/tools/release.names`
  for that version at build time, the same file the box reads at an exact tag. VERIFY BUILD shows
  "wisp 0.0.2 (2)".
- The cut on the box: apply the drop, commit, `./tools/cut_release.sh 0.0.2` (tags v0.0.2 from a
  clean tree with the notes committed), `git add releases/pins.txt && git commit`, `git push &&
  git push origin v0.0.2`, the `gh release create` line it prints (no collisions now), then the
  web repo's mirror.conf `server` and `app` sets at v0.0.2.

## The first CI run: the stale test on main that the review had seen

- `go vet ./...` on main failed in internal/hw: `dmcrypt_holders_test.go:16: undefined:
  holdersOf`. That test belonged to a `holdersOf` that moved to `internal/procs` (`HoldersOf`,
  with `procs_test.go`) on 21 September; the file stayed behind on main, which is the "internal/hw's
  tests haven't compiled since 21 September" in the Anchor Terminal review. Here it never
  existed, so every test run passed. `git rm server/internal/hw/dmcrypt_holders_test.go`; the drop
  carries a stub of it (package clause only) so a tree the drop is copied over builds either way,
  and DELETE_THESE.txt names it. CI is what catches a file like this from now on.

## The enrolment link is never written out; the one location that leaves the phone is named

Anchor Terminal's second pass, after wisp 0.0.2:

- The enrol link (the QR's content) carries the phone's private key, and `pair.Run` printed it as
  text under the QR ("Link: localghost://enroll?…key=…") and, on a terminal too small to rotate
  the frames, printed all twelve frames statically; `ghost-qr > file` would have written the lot
  to a file. Now the QR is drawn only on an interactive terminal of at least 57 columns by 35
  rows (`pair.MinCols`, `MinRows`, pinned to `frameBudget` by a test), as rotating frames, and
  nothing else is ever written: a pipe or a small window gets `pair.ErrScreen`, one line that
  says to find a bigger screen and run `ghost-qr` again, before any device identity is minted.
  ghost-setup finishes with that line rather than failing (the box is provisioned; the QR comes
  from a bigger screen). The "Link:" line is gone for good. Vlad: "we should never print out the
  code, we should just say that you need a bigger screen." The QR's key remains the phone's only
  until its first unlock, when the phone makes its own in the Keystore and the box retires the
  QR's; the design stays.
- "Never a location" was not quite true: a weather question that names no place sends the
  phone's position at two decimals (about a kilometre) to Open-Meteo (`WebSearch.kt`, `weather`).
  The README's "what leaves the box" now says so, with the web search's question words, as the
  two things that leave the phone, and only when asked. The site's privacy page needs the same
  sentence (web repo).
- Still on the list from that pass: the homepage FAQ (two FIDO2 keys, the Mist as working) on the
  web side; the app's JVM tests to run on the box and then on every push; the replies on the July
  contributions; the stale `dmcrypt_holders_test.go` is removed on main and leaves the source
  archive at the next cut.
- Tested: `TestRunNeverWritesTheLinkOffATerminal` (a non-terminal writer: ErrScreen, nothing
  written, no identity minted, the message names the size), `TestMinScreenIsTheSmallest`,
  the pair package; build and vet.

## redeploy: a halt that did not take is said, not acted on; the postmaster is never a stray

- A redeploy on 2 Oct asked for the main PIN, sent the halt, and for 45 s every process of the
  cohort sat where it was, parents intact, states unchanged. `halt` answers "ok" whatever the PIN
  (PIN-opaque, by design, and `AuthorizesLock` logs nothing), so the only sign was that nothing
  moved; a mistyped PIN is the likely cause. Ten seconds in, the watch's orphan rule (parent 1,
  state R or S, under the volume's bin) matched the postmaster, whose parent is 1 by nature
  (pg_ctl daemonises it), and killed it under the running daemons; then "cohort down after 55s"
  printed, unconditionally, above "still stopping after 45s", and the hard restart followed.
  Postgres recovers from WAL at the next unlock; in-flight writes at that second were lost.
- Now: the watch keeps a signature of the volume's processes (pid:state) and, when nothing has
  changed twelve seconds in, says the halt did not take (a wrong PIN or the wipe PIN, same
  "ok", nothing logged), kills nothing, asks for the PIN once more (a wrong halt PIN costs
  nothing at the limiter) and after that says "no graceful halt: hard restart" plainly. The
  orphan rule runs only once something else has stopped (a teardown under way) and never on
  `postgres` or `redis-server`. "cohort down" prints only when the cohort is down.
- Tested with the halt block extracted into a harness and fake processes under the volume's
  path (argv0 trick, comm from a copied binary): a halt that does nothing ends in 11 s with
  nothing killed; a clean halt names each process as it goes and says "halted cleanly"; a
  teardown with a stray ghost.watchd (parent 1) and the postmaster (parent 1): the stray is
  killed at 10 s, the postmaster is left alone to the end.

## The weather moves to the box: one daily pull of the world's larger places, never a position

- Vlad, after "Open-Meteo do we call this for weather i don't think we do": "let's drop the
  weather tool, or maybe move it to the server where we pull weather in all the major places once
  a day and then the app just figures it out, this way the weather apis don't know where we are
  because we pull all." The phone's weather tool (`WebSearch.Tools.weather`, `weatherByName`,
  `Here`) sent the fix at two decimals to Open-Meteo for a question naming no place; that was the
  one position that ever left either device. Gone.
- `internal/weather`: the places are GeoNames populated places (kind P) of 100,000 or more from
  `geo_points`, the largest 3,000; one request a batch of 100 (Open-Meteo takes comma lists of
  coordinates and answers an array), current conditions plus four days, `timezone=auto`; each
  place's forecast is one JSON row in `weather_places` (schemadef, converged at start). `Nearest`
  is the pulled place within 120 km of a point (bounding box, then haversine), `ByName` the pulled
  place of that name or the geo set's place and then Nearest, `Describe` the paragraph the model
  quotes (where, how far, how old, now, the days by name, the zone). `Pass` logs each batch to
  feedstat as kind `weather`, source `open-meteo`.
- ghost.tallyd: `weatherLoop` looks 90 s after start and hourly, pulls when the table is a day old
  or empty (1.5 s between batches, under a minute for the lot); `ghost-cli ghost.tallyd weather
  [lat= lon= | place=] [fetch=1]` reads the table the way the chat does (default: the trail's
  newest point). Box Status gets a Weather section (`internal/monitor`): places, the pull's age,
  the batches that came in two days; Waiting without the geo set, Late past 36 h, Failing past
  108 h.
- ghost.synthd: `weather.go` answers a weather question from the box: the named place
  (`placeOf`, the phone's regex moved over), else the fix the phone sent with the question
  (`here`, which already travelled to the box for "near here"), else `hw.TrailNewest`. The item
  lands beside the taste items (source `weather`, 700 chars allowed). With nothing on the box the
  item says so, so the model does not make a forecast up. `boxCovers` says the box has it for any
  weather question naming no place (the web could only answer with the position), and for a named
  place when the box has a forecast for it; a named place the box lacks may still go to the web,
  the name says nothing about where the phone is.
- The app: the weather branch, `Here`, `placeOf`, `weather`, `weatherByName`, `formatWeather`,
  `code` and `dayName` are out of `WebSearch.kt`; `search()` and `run()` lost the `here`
  argument, the four call sites in MainActivity with it; the fix still goes to the box with the
  chat. `BoxKnows.weatherHere` (pure) keeps a weather question naming no place off the web when
  the box's plan is late: a capitalised word after in/at/for/around/near names a place, a time word
  does not.
- Docs: README "what leaves the box" (the daily pull by the box in the list, "never a position",
  one thing leaves the phone), tools/README §1b''''', releases/0.0.3.md, the version at 0.0.3
  everywhere the cut reads it (release.names, build.gradle.kts, CITATION.cff, RELEASES.md, the
  README's status). The site's privacy page can say "never a location" again (web repo).
- Tested: weather URL/Parse/Describe/Due pure, and against Postgres the places query (threshold,
  kind, order), a two-batch pass with a fake fetcher, Nearest within and beyond 120 km, ByName
  through the geo set, a 429 pass leaving the good rows; synthd's `placeOf` and `weatherCovers`
  without a box; the app's `BoxKnows.weatherHere` and `Tools.forQuestion` (weather never a
  tool). 48 Go packages, vet on Linux and darwin; 207 JVM tests plus WebSearchTest against a
  coroutines stub.

## Go 1.27.1 from the mirror, the index's arithmetic laid out, the geocoder gone, the map's live dot

- Two outside reviews of 0.0.3 (the Anchor Terminal dossiers) agreed on the gaps: the release
  binaries are Go 1.25.4 with 35 standard-library advisories fixed since; the README says never a
  position while the app sends fixes to Android's Geocoder and the phone asks Frankfurter and
  Wikipedia unnamed; a fresh box trusts the X-Client-Cert header until edge-passthrough, and
  0.0.2/0.0.3's notes had dropped that gap; the README says CI runs the app tests on every push
  and the app job has never run. Vlad: "Can we update to a newer version of go, we'll have it on
  the localghost mirror. Do what you can with the server and app." And, from the phone, CRYPTO50
  at 1001.8 and "-44.8% today" with BTC at -2%: "there has to be an error".
- Go: go.mod 1.27.1 (go.dev's newest stable on 3 Oct 2026; 1.26.8 the other). The install moved
  out of setup.sh into `tools/install_go.sh` (root; GO_PIN 1.27.1, GHOST_GO_PIN overrides;
  `--check` only says): a system Go at least go.mod's is left alone, an older one is replaced
  from the mirror (set go, `go1.27.1.linux-<arch>.tar.gz`, signature and hash as before; unpacked
  beside the old one and swapped). setup.sh calls it; redeploy.sh calls it before `make box`, so
  a box follows go.mod at the next redeploy and a mirror that does not answer stops the deploy
  before anything is touched. Makefile exports GOTOOLCHAIN=local, so the go command never fetches
  a toolchain by itself. cut_release.sh and release_build.sh refuse a Go other than go.mod's
  (GHOST_GO_ANY=1 overrides) and RELEASE.txt/NOTICE.txt now say `go=` which, so a bundle can be
  rebuilt to the byte. BUILDING.md, server_setup_user.sh's fallback and tools/README follow. Not
  built with 1.27.1 here: the toolchain hosts are outside this session's egress; CI (setup-go from
  go.mod) and the box's cut are the first builds with it. For the mirror: the set `go` needs
  go1.27.1.linux-amd64.tar.gz, sha256 63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445, 70,553,950 bytes.
- CRYPTO50: `rates.Terms` is the arithmetic one term a constituent (weight, base, price, where
  it came from: live, carried, base, held; ratio, part), `rates.Value` is built on it, and a
  constituent priced past `MaxMove` (20× its base either way) is held at its carried price, else
  the base, and named "SYM!" in the day's missing list: one such row moved the index by its weight
  times the ratio, which is the shape of the phone's number (a day value of ~1815 against a live
  ~1002; the live path prices most coins from Coinbase's list and the day path from the venues'
  closes, so one venue ticker that is another asset, or a base in another unit, shows on one side
  only). `tally.ExplainMarket` lays both values out (`ghost-cli ghost.tallyd rates index=1`: month,
  chain, now and the last day each with value, stored value, priced, held and terms, and `odd`,
  the terms furthest from flat). Box Status marks a day change past ±25% as a fault with the
  command to run, and lists the held symbols. The guess stays a guess until Vlad runs it; the
  structural fix (one price source per constituent for the month) waits for the culprit.
- The geocoder: `countrycells.Atlas.At(lat, lon)` (the polygons kept with their boxes; even-odd
  over every ring, so a hole is the other country's), `GET /v1/geo/at?lat=&lon=` in secd, and the
  phone's `LocationLog.geocode` asks the box instead of Android's Geocoder (which on most phones
  is a network call to Google carrying the fix); a phone with no box keeps the country it had and
  CountryDetect falls back to the network, the SIM, the time zone as before. `wikiCovers`: a "what
  is X" the box's own Wikipedia answers (an article of its own, not a page of meanings, not a
  relation or a question about now) closes the web, as the weather does, so the phone asks
  Wikipedia's API nothing it has at home. README "what leaves the box" names Frankfurter and
  Wikipedia from the phone and says the country is read on the box; README and CONTRIBUTING say
  what CI runs when (the app's JVM tests: pull requests and on demand; Vlad can run the workflow
  by hand once so a passing app run exists). releases/0.0.3.md carries all of it and the header
  gap is back in the known gaps, with the honest limit: a box reached by IP cannot take the
  passthrough yet (nginx routes it by SNI name), a default route for a LocalGhost-only box is next.
- The map: Vlad: "When we're looking at the map we can update the GPS more often not just every
  15 min." `LocationLog.follow(ctx, onFix)` asks the fused provider (else GPS, else the network)
  every 4 s / 3 m through LocationManager.requestLocationUpdates, hands each fix to the map (the
  dot and its accuracy circle redraw; "[ where I am ]" uses it) and to `record` off the main
  thread (its 25 m / hour rules keep the spool quiet); MapScreen starts it on ON_RESUME and stops
  it on ON_PAUSE and on leaving the map, so nothing runs in the background.
- Tested: rates (held past the bound, a carried price past it, Terms' sources), countrycells.At
  (inside, a hole, an island, at sea, the band), /v1/geo/at (200, 404, appears-down), wikiCovers
  (covered, a page of meanings, a relation, "now", no copy), the scripts with sh -n/bash -n; 48 Go
  packages ok, vet on Linux and darwin; 207 JVM tests; the Kotlin structure check on MapScreen,
  LocationLog and BoxClient.
- Wikipedia, asked after: "is Wikipedia hooked in?" On the box it is (synthd's wikiSource for the
  chat, wikiAboutCoin for the coin pages, wikiCovers for the plan; `<mount>/wiki/*.zim`, the
  largest, reopened when it changes); the mirror page lists the set (`wikipedia`,
  `wikipedia_en_all_nopic.zim`, about 60 GB, by hand); the box only fetched it when named
  (`update.sh wiki`). Now `update.sh` takes it with the rest when the volume has 60 GB free
  (GHOST_WIKI=0 leaves it out), so a box that runs update is a box with Wikipedia. The ZIM reader
  has only ever read files written to the spec here (kiwix.org is outside this session's egress):
  the first real file is the proof, `ghost-cli ghost.synthd wiki title=Corfu` after the download.
- `update.sh wiki` on xyntai: "the volume has 6 GB free" on a volume with room to spare. The
  check was `df` on the volume through the /proc door, and df answers for the OS disk there (it
  matches the path against the host's mount table, where the vault is not; reproduced with a
  tmpfs in a child namespace: df says the root disk, stat -f says the tmpfs). Now `stat -f`,
  which asks the filesystem the path is on. The download was never in a temp directory: it is a
  hidden .part beside its final name on the volume, resumed by the next run.
- redeploy on xyntai: install_go.sh said "1.27.1 on the box", then coder's `make box` failed with
  "go.mod requires go >= 1.27.1 (running go 1.25.4; GOTOOLCHAIN=local)". The probe was
  `/usr/local/go/bin/go version` run inside the module with GOTOOLCHAIN unset, and under auto a
  1.25.4 go command answers that by fetching go1.27.1 from the internet into the caller's module
  cache and printing its version, the very thing the mirror rule forbids; the build then ran the
  real 1.25.4 under local. Now every probe is `cd / && GOTOOLCHAIN=local go version` (outside the
  module nothing is downloaded, and inside it an older Go would not even say its version), and
  `tools/go_for.sh` picks the Go to build with (go.mod's version among /usr/local/go/bin/go,
  /usr/local/bin/go and PATH's go, else the newest): the Makefile's GO defaults to it, so do
  cut_release.sh and release_build.sh, and install_go.sh judges by it. On xyntai: root's module
  cache may hold a toolchain@v0.0.1-go1.27.1 download to delete; `sudo ./tools/install_go.sh`
  then sees 1.25.4 and takes 1.27.1 from the mirror.
- 0.0.4: v0.0.3 was cut at c3726d3 before the Go, index, geocoder and map changes landed, so
  those sections moved out of releases/0.0.3.md (put back to the tag's text) into
  releases/0.0.4.md, and the version is 0.0.4 everywhere the cut reads it (release.names,
  build.gradle.kts code 4, CITATION.cff, RELEASES.md, the README's status). Still a wisp.
- The first real Wikipedia file (wikipedia_en_all_nopic, 52.7 GB, on xyntai 3 Oct): `ghost-cli
  ghost.synthd wiki` answered "zim: cluster 178626 spans 51309802015..52690706539". The header
  has no title list (0xffff…), so the reader took the title order from X/listing/titleOrdered/v1,
  and that entry lives in an uncompressed cluster of 1.4 GB shared with the search indexes
  (X/fulltext/xapian and friends), which the reader loaded whole, under a 128 MB bound. Now
  `blobRange` reads an uncompressed cluster's offsets and the one blob in place (the title list
  is read four bytes at a time where it lies, never loaded), the whole-cluster bound applies to
  compressed clusters only (a 1-2 MB zstd frame each in Kiwix's files), and a plain blob past
  256 MB is refused by name. zimtest.BuildWith(Options{TitleListing, Filler}) writes that shape
  (no header list, the X listing and a filler blob in a third plain cluster with eight-byte
  offsets); the test lowers the bounds and reads titles, articles and the filler blob through it.

## The box's voice, and Wikipedia's place in an answer

- The first real file answers: 19,707,096 entries, "Wikipedia, 2026-06". Then "ok tell me about
  Greenwich london" came back from six memories and no article, and asked "from Wikipedia that
  you have locally" the model denied having one. Two causes. gatherContext capped at six items
  with memories first, so the wiki source never ran; the article's lead now keeps its place
  (wikiSource runs first, the cap applies to the rest). And "Greenwich london" is no title:
  `wiki.Article` now tries "A, B", "A (B)" and A alone when B is a qualifier (a city, a country,
  "park"), so "Greenwich london" is Greenwich and "kassiopi corfu" is "Kassiopi, Corfu".
- Vlad: "can we make sure the voice of localghost on the machine matches the voice of the
  website?", with the web repo's WRITING GUIDELINES. `voice.go`: `voiceRules`, the guidelines a
  12B model can follow (plain, flat where sure and hedged where not, contractions, brackets fine,
  no em dashes, no colons in sentences, no lists or bold unless asked, no exclamation marks, the
  banned phrases and the polish words, no praise of the question, no summary, no verdict, no
  "hope this helps"), appended to every chat question by chatIdentity after `holdingsText` (what
  the box holds: the Wikipedia copy by name, the weather, the prices and news; no internet of its
  own; "say the box has nothing" when nothing was given). What a model will not obey is fixed on
  the way out: `plainDashes` turns em dashes, and en dashes between spaces, into commas with the
  spacing put right (a range's en dash stays), and `voiceFilter` does it over the stream token by
  token (a held trailing space, a comma from a dash at a seam wanting its space); `emit` returns
  what it wrote so the persisted answer is the filtered one, the ctl chat and `groundedProse`
  (day stories, places, coin texts) and `splitStory` (news leads and points) run plainDashes.
  Colons are left alone (times, URLs); the prompt carries that rule.
- Tested: plainDashes on whole strings and the filter over four token splits of one sentence
  against the whole-string result; the rules text passes its own dash and colon rule; Article's
  qualified names; the context's reserved wiki place by review. 48 Go packages, vet on Linux and
  darwin.
- 0.0.5 opened. v0.0.4 was cut on the box at 1062b7f (2c537fd plus the go_for.sh mode), a
  sibling of main's e3bfc21 (the ZIM reader fix), so the release went out with the reader that
  cannot open the real file, and the tag is not on main (main merges it clean; the box's
  `git pull --no-rebase origin main`, then the pin commit and a push, puts both lines together).
  releases/0.0.4.md on main is the tag's text again (the voice and the wiki place had been
  appended to a cut release's notes; they are 0.0.5's, with the reader fix), and
  releases/0.0.5.md is written as the work lands, its head listing what the cut still has to
  touch. release.names and the app are at 0.0.5; CITATION.cff, RELEASES.md and the README's
  status stay at the cut release until the next cut. The tag's own copies date 0.0.4 to
  3 October (the day the notes were written); it was cut on the 4th, and main's 0.0.4.md,
  RELEASES.md row and CITATION.cff say so.

## 4 October 2026 , CHECK-IN and MEMORY pages, a box from the release alone, the volume as a file

- CHECK-IN (app/android/.../ui/CheckinScreen.kt, Dest.CHECKIN under YOUR ARCHIVE): the check-in
  card, the day story, the recorder, the voice notes and the history left MemoriesScreen for a page
  of their own. The strip of the last two weeks (Feelings.strip, one Cell a day, oldest first, no
  streaks), each day's tone (Feelings.tone: the first feeling the person picked themselves, the
  box's guesses after; groupOf, mark), the feelings as the four quadrants two by two with the mind
  row across (FeelingChips over Feelings.rows at 18 chars a half-width row), what recurred this month
  (Feelings.recurring), a past check-in's page (PastCheckin: felt, why, said, the day as the box told
  it, the whole day). Nothing moved on the box: the check-in is a journal entry still; checkins asks
  for 90 days.
- MEMORY (ui/MemoryScreen.kt, Dest.MEMORY): one memory's page with kind, title, covers at 112 dp,
  body, origin (MemoryText.origin, pure), the outing's places and tags and the day's stops and
  distances from meta, the day it was made from (MemoryText.dayOf over the new `ref` field the list
  carries, source_ref for day:/outing: rows only), edit and delete. NotifLink: "memories:<id>" opens
  it (openMemory in MainShell, back to where it came from), "checkin" is a destination, a 0.0.4 box's
  reminder (kind checkin, link memories) resolves to it too; the notification page's memory card
  opens the page; a memory row's title opens it; DayScreen's outing card already linked by id.
  secd's reminder links "checkin" now (internal/secd/notifications.go, one word).
- The bundle (tools/release_build.sh) carries ghost-setup, ghost-qr, ghost-update-guard,
  ghost-landtiles, ghost-roadtiles, ghost-tpmreset and the operator scripts with model.pins,
  phone_model.pins and the key; NOTES.md left the tar (beside it in the set). FOUND: update.Unpack
  admitted only VERSION, COMMIT and CHANGES.txt at the top, so every bundle since 0.0.1 (NOTES.md
  since 558d61e) would have been refused by a phone DEPLOY; the 0.0.5 bundle has nothing a 0.0.4
  box refuses. update.Apply skips setupBins (never staged, the guard never replaced), ghost-qr joined
  systemBins; Unpack admits NOTES.md and install.sh at the top for later bundles. Tests for both.
- tools/install.sh (new, in the bundle): checks a release, copies it to
  /opt/localghost/release/<version>/, lays tools under /opt/localghost/tools and ghost-cli, ghost-ctl,
  ghost-qr under /opt/localghost/bin, runs setup.sh from the release. setup.sh: PREBUILT when VERSION,
  COMMIT and bin/ghost-setup are there (no install_go, no make box, the user check told
  GHOST_PREBUILT=1 says Go is not needed); the default service user is SUDO_USER; a new step 4/7
  runs setup_llama.sh before the volume (GHOST_LLAMA=0 skips; skipped on a provisioned box, or
  when bin/llama-server and staged models are there; a failure asks CONTINUE); step 5/7 takes a
  disk or a file (a path outside /dev: the directory must exist, a new file needs a size, the card
  shows the file and the drive, a LUKS remains is removed on WIPEIT); ghost-setup gets --image
  --size for a file, --disk for a device. The after-apply text names update.sh.
- The volume as a file: setup.IsImage/ParseSize/SizeText (pure, tested); ghost-setup --image PATH
  --size N (or a file picked interactively, 0 in the list), the whole-disk data checks skipped for
  a file;
  debian.System.ImageSize, CreatePartitions allocates (dir must exist, fallocate at the size after
  a free-space check with a gigabyte to spare, chattr +C first on btrfs, 0600; a file already there
  is used as it is), DescribePartitioning says file; the unit gets RequiresMountsFor=<dir> for a
  file (tested); hw.StableDiskName leaves a path outside /dev alone (tested); MapWithKey says the
  file is not there (is the drive mounted?) rather than trying every LUKS device. cryptsetup
  isLuks/luksFormat/open take a file as they take a disk, attaching the loop device themselves;
  the wipe destroys the sealed key, no block-device step in it. Not tested on a real file here (no
  root cryptsetup in this sandbox): the first box to provision one proves it.
- setup_llama.sh: CMAKE_CUDA_ARCHITECTURES from nvidia-smi's compute_cap (8.9 → 89; several cards
  joined with ;), GHOST_CUDA_ARCHS overrides, no card seen → CPU build said loudly.
- server_setup_user.sh: Go is a note under GHOST_PREBUILT=1, and the probe runs from / with
  GOTOOLCHAIN=local like every other.
- tools/README.md: "The short way: from a release" at the top (install.sh, the file volume).
- Tested: 48 Go packages, vet Linux and darwin; 212 JVM tests (Feelings' tone, strip across a month
  end and a leap day, recurring; MemoryText; NotifLink's checkin and memory routes); install.sh
  against a fake release in the sandbox (copies, lays out, runs setup.sh from the release).

## 4 October 2026 , consolidation: one memory per person, trips from outings; Frankfurter out

- cmd/ghost.synthd/consolidate.go (new). PEOPLE: a person's memory is made of meta.note (the
  about note's line), meta.facts [{t, ref}] (chats, check-ins), meta.aliases; the body is rendered
  (personBody: prose + the facts since, else note + facts). addPersonFact (notePerson is now a
  call to it) matches by title, alias or first name (matchPerson; two full names with one first
  name are never merged or matched by the bare first name), dedupes by containment, and takes a
  fact into a hand-edited row's meta only. setPersonNote/clearPersonNotes: aboutPass writes the
  note's people into the one row (AboutVersion v3 so every box makes the note again once); people
  the note drops lose the note's line and keep their facts, or go when nothing is left. mergePeople
  (every pass): groups by name, the survivor is the person:<name> row else the oldest, legacy
  bodies become the note or one fact (adopt/foldPerson), the longest name is the title, duplicates
  deleted; hand-edited rows take no part (so an edited row and the box's can both remain; said in
  the notes). peopleProse (daily, GPU): personPrompt → groundedPerson (groundedProse plus every
  capitalised word inside a sentence must be in the facts or the person's names), 4 a day, 3 tries
  each. namePass skips person rows that have facts.
- TRIPS: chainTrips over the away outings (gap ≤ 3 days, ≥ 2 outings, ≥ a night); tripOf
  (countries and places by photos, two covers per outing up to six, distances summed, fromHome
  max); tripTitle/tripBody (dateRange, joinSome, daysWord); tripPass upserts kind 'trip'
  (source_ref trip:<first day>, created_at = end), keeps prose while the template stands, leaves
  edited/tombstoned alone, deletes dissolved trips, and sets meta.part_of on the outings and the
  days of a trip and on the days of a lone away outing of a night or more (cleared and set again
  each run). tripFacts for prosePass, which now writes kind IN ('outing','trip') ("a trip of
  several days"). consolidatePass in distillLoop after outingPass: merge every pass, trips when
  outings changed or daily, people prose daily (settings synthd_consolidated_day, set only after
  the GPU wrote). ghost-cli ghost.synthd consolidate [run=1] [write=1] with consolidateSummary.
- hw.MemoryRow already carries ref; the app: MemRow.partOf, tripLine, summaryLine; MemoryKinds
  "trips" chip and shown(id, kind, partOf) (a part hides under "all"); MemoryText trip origin and
  kindLabel, dayOf accepts trip:, partLabel; MemoryScreen lists a trip's parts (and an outing's
  folded days) and "part of X ›" on a part; MemoriesScreen shows trip covers.
- Frankfurter removed from WebSearch.Tools (the rate regex, codes, rate(), formatRate()); the
  README's "What leaves the box" and the hit-source comment updated; WebSearchTest expects no rate
  tool. BoxKnows already says the box covers rate questions.
- Tested: consolidate_test.go (samePerson/matchPerson, personBody/has/factCount, foldPerson and
  aliases, groundedPerson's name and number checks, personPrompt, chainTrips with a gap and a
  day out, tripOf/tripTitle/tripBody across month and year ends); consolidate_pg_test.go
  (three Cristinas → one with note, two facts, an alias; two Anas stay; an edited James stays and
  takes facts into meta; setPersonNote/clearPersonNotes; peopleNames shows one; trips: a two-outing
  Canada trip, part_of on outings and days, a lone Paris outing folds its days, idempotent second
  pass, an edited trip survives a changed template, a dissolved trip goes and unfolds; tripFacts).
  48 Go packages, vet Linux and darwin; 213 JVM tests + WebSearchTest 10.
- Box Status, synthd: hw.SynthStatus (internal/hw/synthstatus.go; settings synthd_status, synthd
  the single writer, SaveSynthStatus at the end of every distillLoop pass with the pass's tallies
  by step, wikiStatus() and the consolidation day). wikiStatus (wikipedia.go): open (name, file,
  entries), downloading (a hidden .part in <volume>/wiki with its size), missing, failed (the
  reader's error); wikiAnswers counts the chat questions the article answered since start.
  DaemonSummaryFrom "ghost.synthd" rewritten: last pass (key, LATE past 30 min), memories by kind
  (key), yours/edited/deleted, distill queue (key), written by the model, days told (and folded),
  consolidation, wikipedia (key when downloading or failed), about note, chats answered, then the
  news rows as before. synthDid and humanCount tested.

## 4 October 2026 , Wikipedia into Postgres, the file only imported

- internal/wiki: the package is the file (Open, Lead, Body, Sections, BestSection, Names) and
  the Store (store.go): Import(w, budget) reads entries in path order from ImportState.Next
  (settings synthd_wiki_import), articles in batches of 40 (title, title_lc, lead ≤ 1500, body
  ≤ 60k with "== Heading ==" lines, disamb) and redirects in batches of 500 (title_lc → the
  target's idx), saves every 20k entries, starts over for another file name or size (DELETE both
  tables), counts entries that would not read as skipped and never fails on one. Lookup(q, n,
  maxLead): exact, redirect, qualified ("a, b" / "a (b)"), prefix (text_pattern_ops), like
  (pg_trgm, articles then redirects), text (GIN tsvector over title || lead); Best takes the first
  hit that is not a page of meanings; Article(idx); SectionFor(body, question, max); Counts;
  Ready/Current. The ZIM-time code went: Shared, Article/variants/Titles, the qualifier map,
  index.go. internal/zim stays as the import's reader.
- hw schema: wiki_articles (idx PK, title, title_lc, lead, body, disamb; indexes: pattern btree,
  trigram GIN, tsvector GIN) and wiki_redirects (title_lc, idx unique; pattern btree, trigram
  GIN); EnsureSchema runs CREATE EXTENSION IF NOT EXISTS pg_trgm as the owner (a trusted
  extension; a warning when it fails, the GIN trigram indexes then simply do not come to be).
- cmd/ghost.synthd/wikipedia.go rewritten: wikiStore over chatStore(wikiMount); wikiImportLoop
  (a minute between slices of 45 s; opens the file once; after Done, writes <wiki>/.imported
  "<file> <sha256> <edition>" with the hash from mirror_fetch's record and removes the file; an
  8 GB free-space guard before a slice, said in the state); wikiStatus states ready | importing |
  downloading | missing | failed | locked with articles/redirects/next/total (hw.SynthWiki widened);
  wikiAboutCoin, wikiSource, wikiCovers over the store (a names path: wiki.Names minus the owner
  and the people, exact/redirect/qualified only; the section for a detail question, reDetailAsk);
  wikiCtl: q=, idx=, n= → state, counts, hits, article. aboutme.go holdingsText uses wikiReady.
- secd GET /v1/wiki (?q=&n= | ?idx=) through synthd's ctl, in the OpenAPI document (wikiDoc).
- update.sh wiki: with no file and .imported, the mirror's listed hash for the file name is
  compared with the marker's; the same → "current … in the database"; different → fetched again
  (imported in place of the old, removed again). health.sh reads the new ctl keys.
- App: BoxClient.wiki(q, idx, n); WikipediaScreen (search, hits with how they were found, an
  article with its sections); WikiText (pure, tested); Dest.WIKIPEDIA under YOUR ARCHIVE.
- Tested: internal/wiki (Lead/Body, Sections/BestSection/SectionFor/parseBody, Names, the store
  against Postgres: import in slices, every lookup kind, a page of meanings ordered after an
  article, a new size starts over); synthd's wiki test against Postgres (coin, "tell me about", a
  name with its section, covers/leaves, status); secd's OpenAPI coverage; 48 packages, vet Linux
  and darwin; 215 JVM tests.


## 5 October 2026 , SETTINGS › SERVER whole, the shelf, prices first, check-in notes, a question asked aloud

- The phone said "no server release on the mirror" with 0.0.4 published: ServerUpdates.fetchText
  read a megabyte of MANIFEST.txt and the manifest, listing every elevation tile, had grown past
  it, the server lines after the cut. check() reads it line by line now and keeps the header and
  the /server/ lines (ReleaseInfo.keep, tested), stores only those, and records why it found
  nothing (lastMiss: the mirror did not answer, the build has no server set, the notes did not
  match); SETTINGS says that instead of the one line.
- secd: Commit and BuiltAt ldflags beside Version and ReleaseName (Makefile: git rev-parse HEAD
  and the moment of the build; release_build.sh: the tag's commit and the commit's time, the same
  BUILT as RELEASE.txt's date, so the bytes stay reproducible). GET /v1/update carries commit,
  builtAt, go (runtime.Version()) and the shelf.
- internal/update/shelf.go: the signed set the phone handed over moves to
  releases/<version>/set once the release is on (Keep); Shelf() lists the releases kept (VERSION,
  COMMIT, CHANGES.txt, and RELEASE.txt's name/commit/date/go when the set is there); Prune keeps
  four, never the running one. POST /v1/update/switch {"version"} puts a kept release back on
  through the same putOn as a deploy: verified again by mirror_fetch.sh over the kept set (its
  "newest build used" marker pointed at a scratch file for that run, so the real one neither
  refuses the older manifest nor moves back), unpacked again, Apply, trial, restart. The verifier
  hook takes a shelf flag.
- App SETTINGS › SERVER: "your box runs wisp 0.0.4 (1062b7f), built 4 Oct 2026, 17:12 UTC, Go
  1.27.1" (ReleaseInfo.describe/at/short, tested); the mirror's newest the same way; ON THE SHELF
  rows with [ put on ] → [ sure? ] → updateSwitch; ROLL BACK as before.
- Widget and lock-screen card at home: the prices first (HomeBriefText.stacked for the widget's big
  line, one coin a line; the card's title), the story under them; the fake "prices" card is gone.
- CheckinRow.Voices: every check-in note of the day (voiceOfDays kind=checkin), the "Voice:" one
  first; the app's CheckedIn recorder saves kind checkin ([ add to today's check-in ]) and lists
  the day's notes with the ones still on the phone; PastCheckin lists them all.
- voiced.Daemon.Hear(path): a WAV under <mount>/voiced/ask transcribed and removed, nothing
  written (engMu: one whisper at a time with the queue's); ctl `hear path=`. secd POST
  /v1/voice/ask spools the body there, calls voiced, answers {"text","lang","heard"} or
  {"ok":false,"why"}, removes the file. App: VoiceAskButton in the chat composer (record, stop,
  "your box is listening…", the words into the field; BoxClient.voiceAsk, BoxHttp.postFileJson).
- Tested: update (TestShelf), secd (TestUpdateFromThePhone with the shelf, TestSwitchFromTheShelf,
  TestVoiceAsk), voiced (TestHearAQuestion), hw (the PG voice test with notes added through the
  day); 48 packages ok, vet Linux and darwin; 217 JVM tests.
- feeds.DefaultSources gains wsj-world (https://feeds.content.dowjones.io/public/rss/RSSWorldNews);
  feeds.Added lists it for boxes seeded before, put on once by synthd's addFeeds (settings marker
  news_added_<id>; an operator's removal stands). PG test extended.
- App: RECORD_AUDIO in the welcome chain and its rows ("microphone").

## 5 October 2026 , wisp 0.0.5 published; 0.0.6 opened

- Vlad cut and published wisp 0.0.5 (tag v0.0.5 at b5ef714, pinned in 606ca5c). The baseline for
  drops is main from there. releases/0.0.6.md opened, release.names 0.0.6 wisp, the app 0.0.6 / 6.

## 5 October 2026 , the elevation tiles in packs

- internal/dem/pack.go: the pack format (magic, count, index of lat/lon/off/len/name, the tiles'
  bytes; little-endian, reproducible), ReadPackIndex, WritePack, BlockOf/PackName/PackCorner (30°
  blocks). dem.Open reads .heights packs beside .tif files; a tile is read through a section of
  its pack (ReadTile over io.NewSectionReader); Set.Packs(). pack_test.go.
- cmd/ghost-heights: pack <tiles dir> <out dir> (one pack a block), list, check. In the release
  bundle's setup tools (release_build.sh; update.setupBins leaves it in the unpacked release).
- fetch_geo.sh: elev_names picks packs whose 30° block touches an asked box (and tiles as before);
  .heights kept current like the tiles; the change check reads any .sha256 record.
- framed elevation ctl: packs beside tiles.
- The web side: the mirror's publish packs the Copernicus tiles with ghost-heights before
  signing the set; packs and tiles may be published side by side while boxes move over.

## 5 October 2026 , the Wikipedia import without its indexes

- internal/wiki: lookupIndexes (the five), Store.EnsureIndexes/DropIndexes; Import drops them once
  a process while importing and makes them at Done; schemadef no longer carries them. wiki_test
  checks the drop and the make.
- synthd wikipedia.go: wikiImportSlice 55 s; EnsureIndexes at start with a finished import;
  wikiLeft (time left at the pace so far) in the log line, hw.SynthWiki.Left, ctl leftMinutes.
  notifstore: "about N hours to go" (leftText). health.sh: the same. App: Wiki.leftMinutes,
  WikiText.left (tested), the WIKIPEDIA line.
- Measured before: 15k entries/min on xyntai (22 h for the file).
- Parallel: wiki.Store.ImportWith(w, workers, budget): ImportState.Shards (From/To/Next a
  reader), Shards(total, n), Read(); readShard (own wiki.Open for readers past the first, own
  connection through Store.NewConn, batches per reader, the state saved under a mutex every 20k
  entries, the clock looked at every 1k). Import(w, budget) is ImportWith with one. synthd:
  wikiImportWorkers() = NumCPU/2 in [2,4], NewConn = chatConn(mount); the status and the ctl read
  Read(); ctl again=1 saves an empty state so the next slice starts over. Tested: three readers
  give the same tables, a legacy state resumes as one shard.
- UTF-8: wiki store clean() (ToValidUTF8, NUL out) on titles and texts; batch.flush falls back to
  one row at a time on a class-22 data error (dataError), dropping and counting the refused rows;
  the test wiki has a page with a stray 0xC5 byte. Store.Likeness(); ctl "likeness".
- tools/pg_extensions.sh (vector, pg_trgm in /usr/share/postgresql/*/extension and, through
  /proc/<secd>/root, in <mount>/runtime/pgroot's); setup.sh gate, server_setup_root.sh line,
  redeploy.sh step 0/4, health.sh likeness line; in the release bundle's tools.

## 9 October 2026 , people through edits, names in transcripts, WIKIPEDIA stats, the chat's trail, the card abroad, check-in

- consolidate.go mergePeople: edited rows take part; the survivor is the latest edited row (its
  body and title kept: savePerson writes only meta for an edited row), else person:<name>, else
  the oldest; an edited row folded in gives its body as a fact (ref "edited"); the title is no
  alias of itself; DELETE without the NOT user_edited guard. foldNamedMemories: distilled rows
  whose title looksLikeName and matches a person fold into it as a fact (ref = source_ref, or
  "edited") and go; consolidatePass runs it after mergePeople. PG test extended.
- voiced names.go: Names(db) (owner_name + person titles + meta aliases), Prompt(names) →
  Engine.Prompt → whisper-cli --prompt; FixNames(text, names) (capitalised words: same
  SoundsLike, or Levenshtein 1 against names of six letters or more; possessive kept);
  SoundsLike folds accents, ch/ck/k/q→c, ph→f, th→t, y→i, w→v, ai→a, doubles, trailing e. Read
  each pass (d.names), applied in TranscribeNext and Hear. names_test.go.
- wiki store: lightIndexes (title_lc btrees, kept through an import) and heavyIndexes (the GINs,
  dropped for an import, made at Done); Lookup runs the likeness and text steps only when
  heavyIndexed() (pg_indexes, asked once a minute); Indexed(), Bytes(), ImportState.DoneAt. The
  ctl adds startedAt/doneAt/skipped/bytes/indexed. App: Wiki fields, WikiText.stats/gb/day (tests),
  the WIKIPEDIA page's stats block and importing copy.
- Chat trail: synthd chatSteps(items, web, webNote) → ev["steps"]; app Message.steps, Trail.line
  (status → step), ChatChunk.Steps, status() keeps the trail, the bubble stacks the steps while
  waiting and shows "how (n steps)" after. TrailTest.
- Card abroad: Snapshot.aside and withBrief(kept) (prices into extra, the top story into aside);
  expanded() adds the prices and "news   <story>"; the widget's head line adds
  HomeBriefText.short(prices) (tested); GOT IT and the redraw key carry aside.
- CheckinScreen: weekday initials on the strip, the why+voice card, the summary line, GhostButton
  SAVE CHECK-IN (enabled = canSave), the checked-in card with the day's mark.

## 9 October 2026 , SOURCES, feeds from the app, weather on HOME, the ⓘ explainers, the UX pass

- secd internal/secd/sources_http.go: GET /v1/sources (sourceDoc per source: wikipedia, news,
  crypto, weather, maps, speech; state ready|partial|importing|missing|off|unknown, line, detail,
  action, label, open, bytes; read through ctlJSON from synthd wiki/news, tallyd feeds/weather,
  the mount's geo dirs, voiced), POST /v1/sources/fetch {step, region} (fetchState: one job at a
  time, runUpdateScript runs /opt/localghost/tools/update.sh <step> as root with GHOST_MOUNT and,
  for maps, GHOST_GEO_ELEVATION=region; log under /var/lib/ghost/update/jobs/<step>.log; fetchJob
  Step/Region/StartedAt/EndedAt/Running/Exit/Last/Log), GET/POST /v1/news/feeds (→ synthd ctl
  news add/remove/enable; feedID(url)), GET /v1/weather?lat&lon (→ tallyd ctl weather). Routes in
  server.go (Server.fetch), docs in openapi.go (sourcesDoc, sourcesFetchDoc). tallyd weather
  snapshot: noGeo. sources_http_test.go: TestFeedID, TestFetchJob.
- redeploy.sh: tools/ (update.sh, the setup scripts, pins) and ghost-landtiles/roadtiles/heights
  under /opt/localghost. tools/README: the phone's fetch runs the same script.
- App: BoxClient.sources/sourcesFetch/newsFeeds/newsFeedsChange/weather (Source, FetchJob,
  Sources, Feed, Weather, WeatherDay); ui/SourcesScreen (polls 5 s while a job runs, AskDialog
  before a fetch, region from LocationLog.last via SourcesText.region), ui/SourcesText (pure:
  mark, job, region, span, confirm; tested), ui/NewsFeedsScreen (add by https url + name, ●/○
  toggle, [ remove ] → [ sure? ]; FeedsText.feedLine, tested), ui/Explain (the ⓘ texts, one
  object, tested), ui/InfoSheet (InfoButton, InfoSheet, AskDialog). Menu: Dest.SOURCES ⊛ and
  Dest.FEEDS ¶, a SOURCES group with NEWS/CRYPTO/WIKIPEDIA as sub-rows (fromSources), MainShell
  goBack() shared by the system key and the TopBar. HOME: WeatherCard (HomeText weatherWord/
  weatherNow/weatherDay/weatherSource, tested), the ask box continues the chat touched in the
  last 20 min (MainActivity chatTouchedMs) or starts one, a mic (VoiceAskButton). InfoButton on
  HOME's news/for you/prices and on NEWS, MEMORIES, HEALTH, WIKIPEDIA, CHECK-IN, SOURCES, FEEDS.
- The UX pass (the subagent's audit, the cheap wrongs fixed): failure states instead of
  "reading…" forever (HealthScreen, MemoriesScreen loadFailed, MemoryScreen failed, Wikipedia
  stateFailed/note, CheckinScreen voiceFailed, GalleryScreen failed with framesList returning
  null on failure, ChatsScreen ErrorLine/LoadingRow, HarnessScreen detailFailed); said failures
  on add/edit/delete/jot/About save (MemoriesScreen memNote/changed(), AboutCard failed/saveNote
  with save held until the note was read, MemoryScreen note and the editor kept open);
  AskDialog before DEPLOY/ROLL BACK/LOCK (SettingsScreen ask/askLock); two-tap ✕ on
  NotificationsScreen/NotificationScreen (armed), ModelsScreen DELETE, ConnectorsScreen
  DISCONNECT; BackHandler for an open Wikipedia article and a past check-in; MemoryScreen
  backLabel from memFrom; ChatScreen EmptyState examples send on tap (onAsk), pill copy;
  SettingsScreen VERIFY copy, the stale ghost-cli news line, YOUR DATA copy; Glossary's PIN
  term; SetupScreen's line under a waiting ENROL; QrScanScreen enrolOutcome on the arrival line;
  glyphs (› for opens, …, ✕ and ◍ for the emoji, [ − fewer ]).
- Backlog from the audit, not done: " , " comma-as-dash in ~90 UI strings (the style rule is
  for comments and docs); "!" on TerminalDim lines that are not errors; US vs UK dates
  (NotificationTime, DayText); "(s)" plurals; label case drift; six confirm idioms ([ sure? ],
  [ delete? ], [ SURE? ], AskDialog, 3 s auto-revert, "[ NO, DELETE IT ]" on MAP) could be two;
  Phrases "start over" and MAP's delete still one idiom each; Models' empty copy; DayScreen and
  NotificationScreen's day view still spin on a failed load; a sub-page reached from a
  notification returns HOME rather than NOTIFICATIONS in two cases; the enrol flow could hold
  the arrival until the box answers rather than moving on to Setup.

## 9 October 2026, evening , widget foot, expanded order, wiki state recovery, tallyd line

- PhraseSurface: the abroad head drops the prices; a w_foot TextView (widget_phrase.xml, under
  the buttons, GONE when empty or the head is hidden) carries HomeBriefText.foot(prices, aside)
  (tested). expanded(): prices and the story before next/then; the count line short and last.
- wiki.Store.Recover(dir): with no state (Total 0, not Done), no file in dir and rows in the
  tables, writes a Done+Removed state from Counts() and the .imported marker (file, edition);
  tested in TestStorePGImportsInParallel. synthd: the import loop calls it when no file is found
  (indexed reset so EnsureIndexes runs next tick); wikiStatus calls it before reading the state.
  WikiText.state: "missing"/"" → "none on the box · SOURCES › Wikipedia …", any other state is
  shown as said.
- tally.RatesResult.String(): the index block is a count, the venue spread, the one-venue
  count, and BTC/ETH/SOL (priceWord); the per-coin list is no longer in the health line.

## 9 October 2026, night , weather list and pace, wiki tables owned by ghost_rw

- internal/weather: MinPopulation 15_000, Cell 0.5, MaxPlaces 6000, BatchEvery 2 min; placesSQL
  (DISTINCT ON the cell, largest; the MaxPlaces largest cells); NextBatch(db, now) (the Batch
  longest unpulled by LEFT JOIN weather_places, due when the oldest is a day old or never
  pulled); Due reads OldestAt; Nearest ignores rows older than Stale. Tests: the cell's loser is
  left out, NextBatch's order and due.
- tallyd weather.go: ticker BatchEvery; pass pulls NextBatch when due or forced; counters since
  start and "pace" in the snapshot; ctl adds "list" (the list's size). Wording in synthd's
  weather context, monitor, secd sources, README, the app's Explain.
- internal/hw/daemon_owned.go: DaemonOwnedTables (wiki_articles, wiki_redirects),
  ensureDaemonOwned (superuser, after ConvergeSchema: ALTER OWNER TO rw + GRANT SELECT TO ro
  for a table whose owner is wrong); datastore.go's ownership converge skips them (two lines,
  the file left unformatted as before). synthd wikiImportLoop: EnsureIndexes tried every tick
  until it works, the state's error set and cleared accordingly. WikipediaScreen shows a ready
  state's error.
