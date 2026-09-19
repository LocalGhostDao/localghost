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
