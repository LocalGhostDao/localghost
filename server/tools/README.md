# First-time box setup, start to finish

Who runs what, in order. Two users: `root` for anything that touches the system (packages, user
grants, the disk, nginx, systemd) and the service user , `ghost` by default, or `--user <name>` for a
dev box where you want the daemons under your own account. The data disk in these examples is
`/dev/nvme1n1`, the clean NVMe with NO partitions in lsblk. Setup destroys whatever is on the disk
you pass it. Read the plan output before apply.

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

    ./bin/ghost-setup --user <name> --disk /dev/nvme1n1 \
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

**7b. Box inference weights , service user, ENCRYPTED volume, after the first unlock.** These are
what ghost.oracled (chat + vision + captions) and ghost.searchd (embeddings) load. They live inside
the mount so they die with it:

    # unlocked, as the service user (paths for slot 0)
    mkdir -p /var/lib/ghost/mnt/slot0/ai-models
    cp gemma-4-12b-it-Q4_K_M.gguf mmproj-F16.gguf embeddinggemma-300m-q8.gguf \
        /var/lib/ghost/mnt/slot0/ai-models/
    chown ghost:ghost /var/lib/ghost/mnt/slot0/ai-models/*
    shred -u <the source copies on the unencrypted disk>

Missing weights are a named degraded mode, not a crash: oracled reports no model, searchd falls back
to FTS-only, both say so in health. Restart the two daemons (or relock/unlock) after copying and they
pick the weights up.

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
