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

Setup-time downloads come from the LocalGhost mirror first, https://www.localghost.ai/mirror (that
page says what it carries and what a box promises), and from each upstream when the mirror cannot
deliver. Today it carries GeoNames and Natural Earth (`geo`), OpenStreetMap's land polygon zip
(`landpolygons`, which the box cuts into map tiles itself with `bin/ghost-landtiles`) and the Go
toolchain (`go`); the roads extracts (`roads`, 1b''') are the next set to publish; llama.cpp and
the model weights are not published yet, so those still come from upstream. The rule that matters is unchanged: the box reaches the network at SETUP only.

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
- exits 3 when there is nothing to offer (off, key missing, gpg missing, set not published) and 1 on
  any failure; every caller then falls back to upstream, except the model weights, which have no
  unattended upstream (Hugging Face gates them): `setup_llama.sh --models` or `--model-url` as before.

`GHOST_MIRROR=off` turns it off, `GHOST_MIRROR=<url>` points at another copy. Debian 13 does not
always ship gpg; setup installs it before it verifies anything. The key, once, by hand:

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

One blocker first: the llama.cpp pin in `app/src/main/cpp/CMakeLists.txt` is a placeholder and
CMake refuses to configure until it is filled. On any trusted machine:

    git ls-remote https://github.com/ggml-org/llama.cpp refs/tags/b9788

Paste the full 40-char SHA into LLAMA_CPP_COMMIT. Then build per COMPILE.md , on your dev machine
with Android Studio/gradle, or on this box after `app/tools/debian_setup.sh`. For bring-up:

    ./gradlew assembleDebug        # first native build compiles ggml for arm64; it takes a while
    adb install -r app/build/outputs/apk/debug/app-debug.apk

No adb? Serve the APK over the LAN (`python3 -m http.server` in the outputs dir), download on the
phone, allow the install. For the real thing later, `tools/release.sh` builds the signed release
and VERIFY.md covers proving the APK matches the source.

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

Before the first unlock, `tools/setup_llama.sh` gets them (the mirror, then Hugging Face, each file
checked against its pin) and stages them; the unlock ingests them onto the volume. Files you already
have go in by path, checked the same way:

    sudo ./tools/setup_llama.sh                                    # mirror, then Hugging Face, by pin
    sudo ./tools/setup_llama.sh --models /media/usb/ggufs          # a directory of them
    sudo ./tools/setup_llama.sh --model /media/usb/gemma-4-12b-it-Q4_K_M.gguf --mmproj /media/usb/mmproj-F16.gguf

A box that is already running: is what it has the pinned build, and if not, replace it (the mirror
or the pin's upstream, checked, swapped in, ghost.oracled restarted , ten seconds without a model):

    sudo ./tools/ns.sh ./tools/models_check.sh
    sudo ./tools/ns.sh ./tools/models_check.sh --fix

`embeddinggemma-300m-q8.gguf` (search's embedder) is not pinned yet and not on the mirror; it is
accepted from any source. Missing weights are a named degraded mode, not a crash: oracled reports no
model, searchd falls back to FTS-only, both say so in health.

## 7c. Move the database binaries onto the volume , service user, after first unlock

The OS packages exist to BOOTSTRAP: the first-ever initdb runs from them because the volume has
nothing on it yet. Once unlocked, move the runtimes onto the encrypted drive and cut the cord:

    ./tools/bundle_db_runtime.sh /var/lib/ghost/mnt/slot0 --verify

It mirrors the full Postgres tree (pgvector included), copies redis, carries each binary's
shared-library closure, and proves the result by running the bundled initdb with the OS packages out
of the loop. On VERIFIED, remove the OS packages , ghost.secd prefers the volume runtime
automatically from the next unlock, and falls back to PATH only if the bundle is absent. From then
on the databases are version-pinned to their own data and an apt upgrade cannot touch them.

## 1b. The watchdog , root, once, before the box is ever left alone

    sudo ./tools/watchdog.sh --arm

The board's hardware watchdog (iTCO on Intel, sp5100_tco on AMD; softdog as the fallback), fed by
systemd every few seconds: a kernel that stops scheduling , a GPU driver that took the wrong lock,
a hard lockup , is reset by the chip within a minute, and the box comes back locked for the app to
unlock. Without it a frozen box waits for a hand on the power button (2026-09-23: eight hours). A
smart plug on the mains with the BIOS set to "power on after AC loss" is the other half.

## 1b'. The coastline at full detail , root, once (optional, several hundred MB)

The map's base is Natural Earth: right for a continent, a smudge for an island. OpenStreetMap's land
polygons draw every cove; `tools/fetch_geo.sh` fetches them at setup (mirror first, 0b) and cuts
them into one-degree tiles with `bin/ghost-landtiles`. On a box that is already running, the same,
straight onto the unlocked volume (it fetches only what is missing, here the polygons, then cuts;
the cut takes a couple of GB of RAM for a few minutes beside whatever the model is using):

    sudo ./tools/ns.sh ./tools/fetch_geo.sh /var/lib/ghost/mnt/slot0/geo
    sudo ./tools/ns.sh chown -R coder:coder /var/lib/ghost/mnt/slot0/landtiles /var/lib/ghost/mnt/slot0/geo

Without the mirror, fetch the shapefile and copy it in, then ask framed to cut it into one-degree tiles:

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
second download. Mirror first (set `roads`), Geofabrik itself when the mirror has no copy.

    sudo GHOST_GEO_ROADS=europe-latest.osm.pbf ./tools/ns.sh ./tools/fetch_geo.sh /var/lib/ghost/mnt/slot0/geo   # one continent first
    sudo GHOST_GEO_ROADS=all ./tools/ns.sh ./tools/fetch_geo.sh /var/lib/ghost/mnt/slot0/geo                     # the eight continents
    sudo ./tools/ns.sh chown -R coder:coder /var/lib/ghost/mnt/slot0/roadtiles /var/lib/ghost/mnt/slot0/geo

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

## Undo

`./tools/server_setup_undo.sh` walks the root-setup pieces back , but note it deletes the tss
GROUP system-wide, which on a shared box is more housekeeping than you asked for. For a config
do-over, `rm /etc/ghost/ghost.env` and re-run step 1 is usually all you want. Nothing resurrects
data on a disk you partitioned; nothing is supposed to.
