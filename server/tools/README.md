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

## 1. System prep , root

    cd server
    ./tools/server_setup_root.sh --user <name> --host box.example.com

Packages, TPM (tss) grant, scoped ghost.* sudo, and /etc/ghost/ghost.env owned by the service user
with GHOST_HOST filled in. Idempotent, with one deliberate exception: an EXISTING ghost.env keeps
its contents (so re-runs never clobber a customised host) , delete it first if you want it
rewritten. The env PATH includes /usr/sbin, which the unlock path needs (cryptsetup lives there).

## 2. Check + build , service user, NEW login

Group grants are stamped at login; the session that existed before step 1 does not have tss.
`exec su - <name>` or reconnect, then:

    ./tools/server_setup_user.sh --host box.example.com   # --host optional if already set
    make box TAGS=tpm                                          # the REAL backend; sim is default

Expect all OK except a GHOST_HOST reachability WARN , nothing is listening yet, that is the
timeline, not a fault. The sim/tpm distinction is the one to respect: a sim build on the real box
means your PINs guard a simulation while the disk sits unsealed.

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

## 7. Models , root

    cp <model>.gguf /var/lib/ghost/models/
    sha256sum /var/lib/ghost/models/<model>.gguf

Entry in `/var/lib/ghost/models/catalog.json`:

    [{"id": "qwen-1.5b", "name": "Qwen 1.5B", "detail": "small local model",
      "sizeBytes": 1234567890, "sha256": "<the hash>"}]

Downloads resume across drops (Range). The box never fetches models itself , you put them there,
deliberately.

## Undo

`./tools/server_setup_undo.sh` walks the root-setup pieces back , but note it deletes the tss
GROUP system-wide, which on a shared box is more housekeeping than you asked for. For a config
do-over, `rm /etc/ghost/ghost.env` and re-run step 1 is usually all you want. Nothing resurrects
data on a disk you partitioned; nothing is supposed to.
