# First-time box setup, start to finish

Who runs what, in order. Two users: `root` for anything that touches the system (packages, user
grants, the disk, nginx, systemd) and the service user , `ghost` by default, or `--user <name>` for a
dev box where you want the daemons under your own account. Name the data disk by its STABLE name,
`/dev/disk/by-id/nvme-eui.…` (`ls -l /dev/disk/by-id/` shows which one points at the clean NVMe with
NO partitions in lsblk), never `/dev/nvmeXn1`: those numbers follow the order the kernel probed the
disks in, and on the reference box one power cut swapped them, so the old `/dev/nvme1n1` became the
bitcoin SSD. Setup destroys whatever is on the disk you pass it; given by flag, it now refuses a disk
that is mounted or holds a filesystem unless `--erase-disk-with-data` is added. Read the plan output
before apply.

The order matters at one point: build and INSTALL the app on the phone BEFORE ghost-setup renders
the QR. The QR contains the device certificate and private key , it is a credential , so the right
flow is scan-immediately-and-clear-the-screen, not leave-it-on-screen-while-gradle-runs.

## 0. Prerequisites (root, once)

- fTPM enabled in the BIOS (Intel PTT here). Verify: `ls /dev/tpm*` shows `/dev/tpmrm0`.
- If you want remote access: the domain's A record at your public IP, and the router forwarding
  TCP 443 to this box. LAN-only works without either.
- The repo on the box, readable by both users.
- llama.cpp's `llama-server` built for this box at `/usr/local/bin/llama-server` (ghost.oracled and
  ghost.searchd both spawn it as a private loopback child; the path is a conf key if yours lives
  elsewhere). Build it once, on the box, with whatever acceleration the box actually has.
- pgvector installs automatically in step 1 (postgresql-<ver>-pgvector). If it fails or you skip it,
  nothing breaks: search runs in the documented FTS-only degraded mode and says so in health.

## 0b. Where setup downloads from , https://www.localghost.ai/mirror

Setup-time downloads come from the LocalGhost mirror, https://www.localghost.ai/mirror (that page
says what it carries and what a box promises), and from nowhere else. It carries the Go toolchain
(`go`), llama.cpp's source at the commit the publisher pinned (`llama`), the weights (`models`), the
embedder (`embeddings`), GeoNames and Natural Earth (`geo`), OpenStreetMap's land polygon zip
(`landpolygons`, cut on the box by `bin/ghost-landtiles`), the continents' roads extracts that were
asked for (`roads`, 1b''') and the phone's model (`phone`, 7d). If the mirror cannot be reached, or
a build does not list a set yet, setup SAYS so and stops that step: a box never takes a file that
was not in a signed manifest. Which step stops what:

- Go (setup.sh) and llama.cpp (setup_llama.sh) stop setup. llama.cpp is built from the mirror's
  tarball only, never a git clone of master; a box that has the mirror's source already builds that
  copy when the mirror is away. A box with an old git checkout gets the mirror's source (one rebuild).
- The weights stop setup_llama.sh; files you copied over (`--models`, `--model`) are still taken,
  checked against `tools/model.pins`.
- The embedder, the phone's model and the geo sets are each said and skipped: the box works without
  them (search FTS-only, phones read with the box's model, the map without that layer), and a rerun
  of the same script fetches what is missing.

`GHOST_MIRROR_UPSTREAM=1` is the operator's explicit exception: each script then takes what the
mirror did not deliver from its upstream (go.dev with its checksum list, Hugging Face checked by the
pins, GeoNames, Natural Earth, osmdata, Geofabrik), loudly, on the operator's own authority. llama.cpp
has no such exception. A box with no internet at all: a copy of the mirror on a disk,
`GHOST_MIRROR=file:///media/usb/mirror` (MANIFEST.txt, its .asc and the build folders as the site
has them); the signature and every hash are checked the same way. The rule underneath is unchanged:
the box reaches the network at SETUP only.

A running box is brought current with one command, unlocked (`redeploy.sh` ships code and reaches
no network; this ships data):

    sudo ./tools/update.sh                  # maps, embedder, weights, phone model, engine
    sudo ./tools/update.sh maps embedder    # only these
    sudo GHOST_GEO_ROADS=europe-latest.osm.pbf ./tools/update.sh maps   # add a continent's streets

It reads the mirror first and stops if it cannot. Then, set by set, it fetches only what the mirror
lists differently from what the box has, and hands the result to the daemon that uses it:
- **Maps.** It fetches straight onto the volume. ghost.framed then imports the place names and cuts
  the coastline and street tiles in the background, and serves the old ones until the new set is
  whole.
- **Embedder.** It goes into `ai-models`. ghost.searchd is restarted and embeds the archive again.
- **Weights.** They are checked against `model.pins`, and any that differ are replaced;
  ghost.oracled is restarted.
- **Phone model.** It goes into the system area.
- **Engine.** It is rebuilt only when the mirror's llama.cpp tarball changed. The new
  `llama-server` goes onto the volume and ghost.oracled is restarted. A CPU-only build never
  replaces a CUDA one.

A set installed from the mirror leaves a record: `<dir>/.<name>.sha256`, `<geo>/.mirror-geo`, and
`.mirror-landpolygons` beside the shapefile. When everything is current, a rerun costs a few
manifest reads. A set installed before records existed, or from an upstream, is kept as it is and
the output says so (`GHOST_GEO_REFRESH=1` takes the mirror's). `mirror_fetch.sh --list <set>`
prints what the current build lists.

Files are published exactly as upstream publishes them, under `MANIFEST.txt`, a sha256sum list
detach-signed by the site key (the one that signs the site deploys). `tools/mirror_fetch.sh`:

- verifies the signature in a throwaway gpg home against `tools/mirror-key.asc`, committed in this
  repo, and requires the signer to be the pinned fingerprint
  `DCE9 A3D1 4EB4 6197 1DD5  F393 706E 4194 F08A 09A0`; the key is never fetched at verify time;
- requires the first line to be exactly `# LocalGhost Mirror Manifest` (a site deploy manifest is
  signed by the same key and must not pass);
- refuses a build older than the one in `/var/lib/ghost/mirror-build` (a replayed manifest), unless
  `GHOST_MIRROR_ALLOW_OLD=1`;
- downloads each file to a hidden `.part` with resume, and names it only when its SHA-256 matches;
- follows redirects (localghost.ai answers 301 to www) but never down to plain http; a plain-http
  mirror is accepted only on loopback or with `GHOST_MIRROR_ALLOW_HTTP=1`;
- reads the manifest three times, a few seconds apart, before it gives up on a signature that does
  not verify (a publish writes the manifest and its signature one after the other);
- takes the set's `NOTICE.txt` and `TERMS-<name>.txt` with any file of it, and the callers keep them
  beside the files (the ODbL and the Gemma Terms of Use require it);
- keeps a file whose recorded hash equals the manifest's; hashes a file that has no record; deletes a
  download that does not match and fetches it once more, and fails that file on a second mismatch;
- re-reads the manifest when a path answers 404 (the last two builds are kept; an older path is gone)
  and carries on from the new build;
- exits 0 when everything came, 1 on any failure (the mirror unreachable, a bad signature, a file
  that would not match, the key file not the site key), 3 when there is nothing to fetch
  (`GHOST_MIRROR=off`, or the set or file is not published in this build). Neither 1 nor 3 is a
  success; the callers say which it was and stop that step.

`GHOST_MIRROR=<url>` points at another copy (https, loopback http, or `file://`). Debian 13 does not
always ship gpg; setup installs it before it verifies anything. The key must be in the repo before
setup runs (without it every mirror step fails, and with them setup); once, by hand, comparing the
fingerprint with the one printed here and on the site:

    curl -s https://www.localghost.ai/.well-known/pgp-key.asc -o tools/mirror-key.asc
    gpg --show-keys --with-fingerprint tools/mirror-key.asc   # must show DCE9 A3D1 4EB4 6197 1DD5  F393 706E 4194 F08A 09A0
    git add tools/mirror-key.asc && git commit -m "mirror: pin the site key"

The publishing side lives in the web repo (LocalGhostDao/web, `deploy/mirror/`) and runs with every
site deploy.

## 1. System prep , root

    cd server
    ./tools/server_setup_root.sh --user <name> --host box.example.com

Packages, TPM (tss) grant, scoped ghost.* sudo, and /etc/ghost/ghost.env owned by the service user
with GHOST_HOST filled in.

One architectural rule across both scripts: the OS disk carries no database DATA ever, and after
bundling (7c below) it need not carry database BINARIES either , install_db.sh bootstraps, the
bundle makes the volume self-sufficient, and the OS packages become removable. ghost.secd runs a private postgres and a private redis per slot, initdb'd at unlock with their
data on the encrypted volume, dead when it locks. The script preseeds Debian so a fresh postgres
install creates no OS-disk cluster, and it detects any existing system cluster or system redis
service and reports them. Pass `--disable-system-dbs` to stop and disable both (services only ,
their data directories are reported but never deleted; removing a data dir is a human decision). Idempotent, with one deliberate exception: an EXISTING ghost.env keeps
its contents (so re-runs never clobber a customised host) , delete it first if you want it
rewritten. The env PATH includes /usr/sbin, which the unlock path needs (cryptsetup lives there).

## 2. Check + build , service user, NEW login

Group grants are stamped at login; the session that existed before step 1 does not have tss.
`exec su - <name>` or reconnect, then:

    ./tools/server_setup_user.sh --host box.example.com   # --host optional if already set
    make box                                                   # ONE build; no tags, no sim

Expect all OK except a GHOST_HOST reachability WARN , nothing is listening yet, that is the
timeline, not a fault.

There is no sim build any more. One binary compiles BOTH seal tiers and the choice is made at
RUNTIME from `GHOST_SEAL_MODE` in seal.env (written by ghost-setup; tpm is the default). The two
tiers differ in KEY CUSTODY only , the volume encryption is always software (LUKS):

- **tpm**: the disk key is sealed in the fTPM (Intel PTT here), with the hardware dictionary-attack
  counter guarding PIN guesses. This is what a real box runs.
- **software**: for TPM-less dev machines. The key is wrapped under Argon2id(PIN) in seal.env; an
  attacker with the raw disk can brute-force the PIN offline with no lockout. Real data does not
  belong on this tier, and the code says so where it lives.

The rule that replaces the old sim warning: the box NEVER silently downgrades. seal.env says tpm and
the TPM is unusable = a hard stop with a message, never a quiet fall to software that could not
unseal the hardware-sealed key anyway.

## 3. Build + install the app , before any QR exists

The phone's own model runtime is llama.cpp built from the same mirror tarball the box builds, pinned
by its SHA-256 in `app/src/main/cpp/CMakeLists.txt`; until the pin is set gradle builds the app
without it (the chat then always asks the box). To set it, on the dev machine (7d):

    app/android/tools/pin_llama.sh        # reads the signed manifest, checks it against the site key

Then build on this box (the Android SDK from `app/android/tools/debian_setup.sh`, once). For
bring-up, from `app/android`:

    ./gradlew installDebug         # the first native build compiles ggml for arm64; it takes a while

(`installDebug` wants the phone on adb; without it, `./gradlew assembleDebug` and serve
`app/build/outputs/apk/debug/app-debug.apk` over the LAN with `python3 -m http.server`, download
on the phone, allow the install.) The real thing is a release: `tools/cut_release.sh <version>`
builds the signed APK beside the server set (see "Cutting a release" below), and VERIFY.md covers
proving an APK matches the source.

## 4. Dry run , root

    ./bin/ghost-setup --user <name> --disk /dev/disk/by-id/<the data disk> \
        --host box.example.com --domain box.example.com

No flag needed: the dry run IS the default , provisioning requires the explicit --apply. Prints
every step, touches nothing. Read the partition line twice: the empty NVMe, not the OS
disk, not anything mounted. There is no undo for picking wrong. The app pins the certificate
FINGERPRINT, not the name, so host-as-domain works on the LAN too (if your router does NAT
hairpinning; if it does not, use the LAN IP as --host and keep --domain).

## 5. Apply , root

Same command with `--apply`. Prompts for the main PIN and the wipe PIN on the tty (no echo, no
history). Different values; the wipe PIN destroys everything and then lies about it, which is the
point. Partitions the disk, mints the CA (issuer is a deliberately boring "ca"), writes nginx and
the units, starts the daemons, renders the QR.

If the domain DNS check fails on NAT (public A record vs LAN address), re-run without --domain,
finish, add the domain config after , enrolment never needed it.

## 6. Enrol , phone in hand, app installed

Scan the QR from step 5. Scanning IS enrolment , no code, no confirmation, no network call. Clear
the terminal once the phone has it. Fresh QR any time (each mints a fresh identity; scan the
newest): `./bin/ghost-qr --ca /etc/ghost/ca --host box.example.com` as root. Then unlock with
the main PIN. A wrong PIN looks exactly like a down box; that is the product, not a bug.

The QR is drawn only on an interactive terminal of at least 57 columns by 35 rows, as rotating
frames, and the link inside it is never written out: not as text under the QR, not to a file, not
as a column of frames on a small screen. It carries the phone's private key, and a credential in a
terminal log is a credential. A window too small, or `ghost-qr` run through a pipe, gets one line
saying to find a bigger screen and run it again; ghost-setup finishes either way and says the same.
The QR's key is the phone's only until its first unlock, when the phone makes a key of its own
inside its Keystore and the box retires the QR's (`app/android/.../DeviceCert.kt`, secd's rekey).

## 7. Models , two different homes, do not mix them up

**7a. App catalog models , root, unencrypted disk.** These are the models the PHONE downloads from
the box for on-device inference:

    cp <model>.gguf /var/lib/ghost/models/
    sha256sum /var/lib/ghost/models/<model>.gguf

Entry in `/var/lib/ghost/models/catalog.json`:

    [{"id": "qwen-1.5b", "name": "Qwen 1.5B", "detail": "small local model",
      "sizeBytes": 1234567890, "sha256": "<the hash>"}]

Downloads resume across drops (Range). The box never fetches models itself , you put them there,
deliberately.

**7b. Box inference weights , ENCRYPTED volume.** These are what ghost.oracled (chat + vision +
captions) and ghost.searchd (embeddings) load. They live inside the mount so they die with it. The
weights are PINNED: `tools/model.pins` names the exact files (Unsloth's Gemma 4 12B GGUF build,
Apache 2.0, no account or token needed) with their SHA-256 and size, and nothing else is accepted
under those names , not from the mirror, not from Hugging Face, not from a USB stick. A Hugging Face
upload that changed under the same name (Unsloth's mmproj did, before their F32 patch_embd fix) is
refused, not staged. No Python, no huggingface-cli anywhere: curl and sha256sum.

Before the first unlock, `tools/setup_llama.sh` gets them (from the mirror, checked by its signed
manifest and again against the pin) and stages them with the set's notice and terms; the unlock
ingests them onto the volume. Not on the mirror, or the mirror away: said, and setup stops there
(0b). Files you already have go in by path, checked against the pin:

    sudo ./tools/setup_llama.sh                                    # the mirror, by pin
    sudo ./tools/setup_llama.sh --models /media/usb/ggufs          # a directory of them
    sudo ./tools/setup_llama.sh --model /media/usb/gemma-4-12b-it-Q4_K_M.gguf --mmproj /media/usb/mmproj-F16.gguf

A box that is already running: is what it has the pinned build, and if not, replace it (from the
mirror, checked, swapped in, ghost.oracled restarted , ten seconds without a model):

    sudo ./tools/ns.sh ./tools/models_check.sh
    sudo ./tools/ns.sh ./tools/models_check.sh --fix

The embedder, `embeddinggemma-300m-qat-Q8_0.gguf`, comes from the mirror's `embeddings` set (the
Gemma Terms of Use travel with it onto the volume); its pin line joins `tools/model.pins` from the
manifest. A box that has the older `embeddinggemma-300m-q8.gguf` keeps it until the new one arrives,
then embeds its archive again by itself. Missing weights are a named degraded mode, not a crash:
oracled reports no model, searchd falls back to FTS-only, both say so in health.

## 7c. Move the database binaries onto the volume , service user, after first unlock

The OS packages exist to BOOTSTRAP: the first-ever initdb runs from them because the volume has
nothing on it yet. Once unlocked, move the runtimes onto the encrypted drive and cut the cord:

    ./tools/bundle_db_runtime.sh /var/lib/ghost/mnt/slot0 --verify

It mirrors the full Postgres tree (pgvector included), copies redis, carries each binary's
shared-library closure, and proves the result by running the bundled initdb with the OS packages out
of the loop. On VERIFIED, remove the OS packages , ghost.secd prefers the volume runtime
automatically from the next unlock, and falls back to PATH only if the bundle is absent. From then
on the databases are version-pinned to their own data and an apt upgrade cannot touch them.

## 7d. The phone's model , at setup (2.2 GB; GHOST_PHONE_MODEL=0 skips it)

The app can run a small model on the phone: it reads web pages into notes when the box's model is on
its CPU, and answers by itself when the box cannot be reached. The box offers it; the phone pulls it
from the box, never from the internet. `setup_llama.sh` fetches it as its last step, from the
mirror's `phone` set; a miss is said and setup carries on (phones then read with the box's model).
By hand:

    sudo ./tools/phone_model.sh            # Gemma 4 E2B QAT, pinned in tools/phone_model.pins; the mirror
    sudo ./tools/phone_model.sh --check    # what phones are offered, and whether the file matches

The APK carries the model runtime only when app/android/app/src/main/cpp/CMakeLists.txt pins the
llama.cpp source tarball the mirror carries (set `llama`, the same bytes the box builds from) by its
SHA-256. On the machine that builds the app, once, and again whenever the mirror moves llama.cpp:

    app/android/tools/pin_llama.sh --key server/tools/mirror-key.asc   # reads the signed manifest, writes the pin
    app/android/tools/pin_llama.sh --tarball <copy of /opt/localghost/llama.cpp.mirror-dl/llama.cpp-*.tar.gz>

then rebuild the app (the build fetches the tarball from the mirror and checks it against the pin;
-PllamaTarball=<file> builds from a local copy). Then MODELS in the app's menu → DOWNLOAD.

## 7e. The speech engine for voice notes , at setup (GHOST_SPEECH=0 skips it)

The daily check-in takes a voice note, and the box transcribes it itself: whisper.cpp's
`whisper-cli`, built on the box from the mirror's pinned source (set `whisper`), and a ggml speech
model (set `speech`). Both come from the mirror only, like llama.cpp. `setup_llama.sh` runs it as
its last step and stages both for the next unlock. A miss is said and setup carries on. Voice notes
are kept on the box meanwhile and transcribed once the engine arrives.

    sudo ./tools/setup_whisper.sh              # engine + model, staged for the next unlock
    sudo ./tools/update.sh speech              # on a running, unlocked box: straight onto the volume
    sudo ./tools/ns.sh ./bin/ghost-cli ghost.voiced voice          # counts, engine, newest notes
    sudo ./tools/ns.sh ./bin/ghost-cli ghost.voiced voice-again id=failed   # retry the failed ones

The build is CPU-only on purpose: the 4070 stays the chat model's. ghost.voiced runs whisper at nice
10 with at most four threads, and passes `-ng` so a CUDA build would leave the GPU alone too.

## 1b. The watchdog , root, once, before the box is ever left alone

    sudo ./tools/watchdog.sh --arm

The board's hardware watchdog (iTCO on Intel, sp5100_tco on AMD; softdog as the fallback), fed by
systemd every few seconds: a kernel that stops scheduling , a GPU driver that took the wrong lock,
a hard lockup , is reset by the chip within a minute, and the box comes back locked for the app to
unlock. Without it a frozen box waits for a hand on the power button (2026-09-23: eight hours). A
smart plug on the mains with the BIOS set to "power on after AC loss" is the other half.

## 1b'. The coastline at full detail , root, once (optional, several hundred MB)

The map's base is Natural Earth: right for a continent, a smudge for an island. OpenStreetMap's land
polygons draw every cove; `tools/fetch_geo.sh` fetches them at setup (from the mirror, 0b) and cuts
them into one-degree tiles with `bin/ghost-landtiles`. On a box that is already running, the same,
straight onto the unlocked volume (it fetches only what is missing, here the polygons, then cuts;
the cut takes a couple of GB of RAM for a few minutes beside whatever the model is using):

    sudo ./tools/ns.sh ./tools/fetch_geo.sh /var/lib/ghost/mnt/slot0/geo
    sudo ./tools/ns.sh chown -R coder:coder /var/lib/ghost/mnt/slot0/landtiles /var/lib/ghost/mnt/slot0/geo   # ghostd:ghostd after own_user.sh

Without the mirror, on your own authority (this file is not checked against a signed manifest), fetch
the shapefile and copy it in, then ask framed to cut it into one-degree tiles:

    cd /tmp && curl -fLO https://osmdata.openstreetmap.de/download/land-polygons-complete-4326.zip
    unzip -q land-polygons-complete-4326.zip
    sudo cp -r land-polygons-complete-4326 /proc/$(pidof ghost.secd)/root/var/lib/ghost/mnt/slot0/geo/
    sudo ./tools/ns.sh ./bin/ghost-cli ghost.framed geo-tiles     # background; watch framed's log

Minutes and a couple of GB of RAM, once; the tiles land in `<volume>/landtiles` and the phone fetches
only the ones under its view when zoomed in. framed rebuilds by itself whenever the shapefile is newer
than the tiles. The map credits "© OpenStreetMap contributors" (ODbL) wherever it draws them.

## 1b''. Names on the map , after a geo-import on a box that predates them

The map labels countries, regions, capitals, cities, towns and villages from the GeoNames rows on
the box (ranked at import; `/v1/geo/labels`). A box provisioned before the `rank` column shows no
names until GeoNames is imported again , twelve million rows, a while, in the background:

    sudo ./tools/ns.sh ./bin/ghost-cli ghost.framed geo-import     # watch framed's log for "geo import done"

## 1b'''. Streets , OpenStreetMap's roads for the world, root, once (optional, ~80 GB and hours)

The map draws roads from OpenStreetMap: the motorways of a country from the coast's zoom, every
street with its name from ten times closer. The box holds Geofabrik's continent extracts (PBF,
ODbL) under `<geo>/roads` and cuts them once into `<volume>/roadtiles` with `bin/ghost-roadtiles`;
the phone fetches one cell at a time as it moves (`/v1/geo/roadtiles/index`, `/v1/geo/roadtile`).
Opt-in, because of the size: Europe 33 GB, North America 18, Asia 15, Africa 7, the rest 6 ,
about 80 GB of PBF, kept on the volume beside the tiles so a newer extract can be cut without a
second download. From the mirror (set `roads`: the continents it was asked to carry); Geofabrik only
with `GHOST_MIRROR_UPSTREAM=1`.

    sudo GHOST_GEO_ROADS=europe-latest.osm.pbf ./tools/ns.sh ./tools/fetch_geo.sh /var/lib/ghost/mnt/slot0/geo   # one continent first
    sudo GHOST_GEO_ROADS=all ./tools/ns.sh ./tools/fetch_geo.sh /var/lib/ghost/mnt/slot0/geo                     # the eight continents
    sudo ./tools/ns.sh chown -R coder:coder /var/lib/ghost/mnt/slot0/roadtiles /var/lib/ghost/mnt/slot0/geo   # ghostd:ghostd after own_user.sh

The cut runs in the foreground of that script: hours for Europe, a day for the world is the honest
guess (three passes over every byte, then a lookup per road vertex), 2 GB of RAM plus whatever page
cache it can get (the node file for Europe is on the order of 10 GB; less RAM than that still
finishes, slower). Run it in `screen` or `tmux`. If the script is interrupted after the downloads,
framed finishes the job: it cuts by itself at its next start whenever the PBFs are newer than the
tiles, or on request, in the background, one build at a time:

    sudo ./tools/ns.sh ./bin/ghost-cli ghost.framed road-tiles     # watch framed's log for "road tiles: done"

`tools/health.sh` prints a `map roads:` line (tiles, or extracts waiting to be cut, or none). The
map credits "© OpenStreetMap contributors" wherever it draws them; `TERMS-osm-odbl.txt` travels with
the extracts and the tiles.

The cut also writes the ROUTING GRAPH beside the tiles (`<volume>/roadtiles/graph/`, the roads as
edges between junctions), which the box uses for the DAY ROUTE: each day told as the places you
stayed and the moves between them, on foot along the streets or by road, from the trail's fixes,
the photos' positions and the day's steps (`<volume>/frames/paths/<day>.route.json`, served at
`/v1/geo/route`). Tiles cut before the graph existed are cut again at framed's next start
(`-no-graph` on `bin/ghost-roadtiles` for tiles only). Without a graph the route draws straight
lines between fixes. To tell the last N days again (after the graph arrived, after a geo-import):

    sudo ./tools/ns.sh ./bin/ghost-cli ghost.framed day-routes days=60

## 1b''''. The days, prebuilt , nothing to run

ghost.synthd builds one summary per day (`day_summaries`): the template the evening the day ends,
the model's prose over the following passes when the GPU is up, richer as captions land. History is
backfilled a slice per pass. `tools/health.sh` shows the count and where the backfill is;
`ghost-cli ghost.synthd days` the same with detail, `days day=2026-09-25 rewrite=true` to have one
day written again. "On this day" and the memories feed read these rows.

## 1b'''''. The weather, pulled by the box , nothing to run

ghost.tallyd asks Open-Meteo once a day for the forecast of the world's three thousand larger
places (GeoNames populated places of 100,000 or more, from the geo set of 1b'), the same list
whoever and wherever the person is, in batches of a hundred a request. The chat answers "what's
the weather like" from that table for the phone's fix or the trail's newest point, and "weather
in Faro" for the place named; the phone never asks a weather service anything, so none learns
where it is. `ghost-cli ghost.tallyd weather` shows the table and the forecast where the trail
says the phone is, `weather place=Faro` or `weather lat=37.0 lon=-7.9` a place, `weather fetch=1`
pulls now. Box Status has a Weather section. Without the geo set there is nothing to pull.

## 1c. When the GPU misbehaves , root

    sudo ./tools/gpu.sh          # is the model on the card, and is the card doing the work
    sudo ./tools/unwedge.sh      # a process that survives SIGKILL, a card off the bus: who, where, why
    sudo ./tools/unwedge.sh --reset   # the levers, one at a time , ONLY with the watchdog armed or
                                      # someone at the power button; the driver levers can freeze the box

## 8. First unlock , the checklist

If the fTPM is in dictionary-attack lockout from earlier attempts (Intel PTT here), COLD power cycle
first , full power off, wait ten seconds, power on. PTT ignores timed recovery; a warm reboot does
not clear it. Then spend PIN attempts like they cost something, because they do.

Unlock from the app with the main PIN, then verify in order , each step gates the next:

    sudo grep GHOST_SEAL_MODE /var/lib/ghost/seal.env   # says tpm , if it says software on real
                                                        # hardware, stop and re-provision
    ./bin/ghost-cli ghost.secd status            # unlocked, slot mounted
    ./bin/ghost-cli ghost.watchd status          # cohort up; searchd/oracled supervised
    ./bin/ghost-cli ghost.oracled status         # model loading; ~30s degraded while llama warms is normal
    ./bin/ghost-cli ghost.searchd ready          # false until T1/T2 exist , that is honest, not broken
    ./bin/ghost-cli ghost.searchd queue          # pendingEmbeds/parkedJobs; parked captions drain once
                                                 # oracled finishes loading the vision model
    ./bin/ghost-cli ghost.searchd search query=anything   # FTS answers even before embeddings exist

Take a photo in the app: framed archives it, hands it to searchd, a caption job appears in the queue,
and once oracled is warm the caption lands and the photo becomes text-searchable. That single photo
exercises upload, EXIF, archive, ingest, the job queue, vision, chunking, and embedding , the whole
spine in one tap.

The lock test matters as much as the unlock: lock from the app, watch the spin-down mirror the mount
tick-up, then confirm the box answers 503 to everything and a wrong PIN is indistinguishable from a
dead box. That indistinguishability is the product.

A phone that is lost, sold or lent and not returned is retired, at the box or from another phone,
and refused from then on, locked or not:

    sudo ./tools/ns.sh ./bin/ghost-cli ghost.secd devices            # the enrolled phones, by device key
    sudo ./tools/ns.sh ./bin/ghost-cli ghost.secd retire id=<key>    # that phone answered as if the box
                                                                     # were down, the PIN entry included

The retired list is on the OS disk (`/var/lib/ghost/devices/retired`, root's, keys only), so it
holds while the volume is locked. There is no un-retire: the phone that should be back scans a
fresh QR (`ghost-qr`) and is a new device. The same from a phone is `POST /v1/devices/retire`
with a sibling's key, never its own.

## 8b. A user of their own for the daemons , root, once, with the box locked

A box set up from 30 Sep 2026 has this already: setup.sh makes `ghostd` (GHOST_RUN_USER to name it
otherwise) and the daemons run as it, while the service user builds and deploys. On a box set up
before, the daemons run as the service user from step 1. When that is an account people log in as
(coder here), anything else running as it (a shell, an editor, a website on the same machine) can
read the decrypted volume through /proc/<daemon pid>/root. Give the daemons a system user nobody
logs in as:

    # lock from the app first (SETTINGS › LOCK BOX NOW)
    sudo ./tools/own_user.sh            # makes ghostd, points ghost.secd at it, restarts secd
    # unlock from the app: that one unlock hands the volume to ghostd (a few seconds more)
    sudo journalctl -u ghost.secd | grep "volume handed to its own user"
    sudo ./tools/privacy_check.sh       # "the cohort has a user of its own"

The service user keeps building and deploying (redeploy.sh builds as it); it loses only the door.
Undo: copy /etc/systemd/system/ghost.secd.service.before-own-user back, daemon-reload, restart,
lock and unlock.

## 8c. New releases from the phone , root once, then never

After this the box takes a new server release from the app, with the PIN and no root: the phone
reads the mirror once a day on Wi-Fi, says when there is a newer release, and DEPLOY in SETTINGS ›
SERVER hands the signed set to the box. The box checks the signature with the key it already holds,
puts the release on, locks, and restarts onto it; the first unlock after is a trial, and the earlier
build comes back by itself if the new one fails it (or from ROLL BACK). Once, as root:

    sudo ./tools/redeploy.sh    # installs ghost-update-guard, its unit drop-in, and the verifier +
                                # site key in /opt/localghost/tools

Cutting a release, the whole of LocalGhost in one (the web repo publishes its `server/` as the
mirror's set `server`):

    ./tools/cut_release.sh 0.0.1

Run it as the user whose gpg holds the site key (info@localghost.ai), so the sums and the APK
get their signatures; without the key it says so and signs nothing. The app is built in the same
cut, from the tag's tree, with the Android SDK on the machine (found through `ANDROID_HOME` or
`ANDROID_SDK_ROOT`, the `~/.localghost_android_env` that `app/android/tools/debian_setup.sh`
writes, `sdk.dir` in `app/android/local.properties`, or `~/android-sdk`) and the app's keystore:
`~/localghost-release.jks` when it is there (apksigner asks for its password on the terminal, and
takes its only key without an alias), else what `~/.config/localghost/release.env` names, which
`tools/app_keystore.sh` writes:

    ./tools/app_keystore.sh --use ~/keys/other.jks [--alias <key>] [--store-pass]   # a keystore you have
    ./tools/app_keystore.sh                                                         # or a new one, made once

    # what it writes (mode 600):
    LG_KEYSTORE=/home/coder/keys/other.jks
    LG_KEY_ALIAS=localghost      # the keystore's only key, or the --alias given
    LG_KEYSTORE_PASS=…           # with --store-pass; apksigner asks on the terminal without it
    LG_KEY_PASS=…                # only when the key's password differs from the store's

The keystore is the app's identity for good (every later release must be signed by the same key,
or phones refuse the update): keep a copy somewhere safe. A release carries the app, so the cut
checks the SDK and the keystore before it tags or builds anything and stops with the reason when
either is missing; `--apk <file>` hands in an APK built elsewhere at the same commit, `--no-apk`
cuts without the app on purpose. The APK is checked against the release's version (aapt2) and
signed by the site key beside it.

A release is its name (`tools/release.names`: 0.0.1 is wisp), its notes (`releases/0.0.1.md`:
what it does, what is in it, how it works) and its pin (`releases/pins.txt`: the commit it was
cut from, written at the first cut). The first cut tags HEAD as `v0.0.1` (a clean tree, the notes
committed); every cut after builds from that tag in a clean worktree, whatever the checkout holds,
and refuses to build when the tag no longer points at the pinned commit. The output,
`release/0.0.1/`, is `server/` (the bundle, RELEASE.txt with `name=`, NOTES.md, NOTICE.txt,
TERMS-MIT.txt, built reproducibly by `tools/release_build.sh`: the same tag gives the same bytes on
any machine, so anyone can rebuild a published release and compare it with the manifest), `app/`
(the APK, its signature, APP.txt), `source/` (git archive of the tag) and SHA256SUMS with its
signature. The script prints what comes next: push the tag, make the GitHub release with every
file (the `gh release create` line it prints; GitHub flattens the folders, so the server set's
own SHA256SUMS stays home and every other name is unique), point the mirror's `server` set at the
server files. The box reports itself as "wisp 0.0.1" (SETTINGS ›
SERVER), and the phone offers the next release by its name. DEV_UPDATE.md, "A new server release
from the phone", has how the box takes one.

## 8d. Move the phone's TLS into secd , root once (recommended on a box that hosts other sites)

By default nginx terminates the phone's TLS and passes the device certificate to secd as a header,
which any local process on the box can forge. To have secd check the certificate itself , nginx then
forwards the raw TLS stream by name and never sees the plaintext , while the box's other sites move
to an internal port behind the stream:

    sudo ghost-ctl edge-passthrough --domain <box name>   # backs up nginx, tests, checks, rolls back on failure
    echo tls | sudo tee /etc/ghost/edge                   # then a plain-HTTP client on :8443 gets the down page
    # undo, if a site misbehaves behind the stream:
    sudo ghost-ctl edge-passthrough --undo

`ghost-cli` and `ghost-ctl` are unaffected , they use the box's control sockets, not the TLS port ,
so everything you run here keeps working. `sudo ./tools/privacy_check.sh` says whether the move is in
place. DEV_UPDATE.md, "The phone's TLS is checked by secd itself", has the design.

## Undo

`./tools/server_setup_undo.sh` walks the root-setup pieces back , but note it deletes the tss
GROUP system-wide, which on a shared box is more housekeeping than you asked for. For a config
do-over, `rm /etc/ghost/ghost.env` and re-run step 1 is usually all you want. Nothing resurrects
data on a disk you partitioned; nothing is supposed to.
