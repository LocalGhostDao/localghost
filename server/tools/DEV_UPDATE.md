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
