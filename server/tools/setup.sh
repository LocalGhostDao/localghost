#!/bin/sh
# setup.sh , the guided LocalGhost bring-up. ONE command, run as root, that walks the whole flow:
# verify the box prep, build the binaries AS THE SERVICE USER (or take the release's), the engine
# and the models, let you pick the disk from a list or name a file on a drive, choose the seal tier,
# dry-run, and (only on an explicit typed confirmation) provision.
#
# Two places it runs from, told apart by VERSION and COMMIT at the top of the tree:
#   a repository checkout   builds bin/ with make box first (Go from the mirror)
#   an unpacked release     bin/ is the release's; nothing is built, no Go is needed
#                           (tools/install.sh puts the release in place and runs this)
#
# Why root, not the service user: privilege drops, it does not round-trip. A script cannot become the
# user, build, and come back to root in one process. So this runs as root and uses `su - <user>`
# for the build step , the one part that must run in the user's context , keeping provisioning
# (which writes the disk and mints the CA) in root's own hands. One uninterrupted run, no manual
# su-ing back and forth.
#
# It changes nothing until you confirm. The disk step and the apply step each require typing an
# explicit word , there is no --apply-by-accident path.
#
# Switches, as environment: GHOST_LLAMA=0 skips the engine and the models (tools/update.sh engine
# later); GHOST_NO_GEO=1 skips the geo data at provision; GHOST_RUN_USER names the daemons' user
# (ghostd).
set -eu

SVC_USER="${1:-}"
if [ "$(id -u)" != 0 ]; then
    echo "run as root: sudo ./tools/setup.sh <service-user>"
    exit 1
fi
if [ -z "$SVC_USER" ]; then
    # the person who ran sudo, when there is one: they install and deploy; the daemons get a user of
    # their own below
    _def="${SUDO_USER:-}"
    [ "$_def" = root ] && _def=""
    printf "the user who installs and deploys (your login) [%s]: " "${_def:-coder}"
    read -r SVC_USER
    [ -z "$SVC_USER" ] && SVC_USER="${_def:-coder}"
fi
if ! id "$SVC_USER" >/dev/null 2>&1; then
    echo "user '$SVC_USER' does not exist , create it or pass a different one"
    exit 1
fi
REPO="$(cd "$(dirname "$0")/.." && pwd)"
ENVFILE=/etc/ghost/ghost.env
say() { printf '\n=== %s ===\n' "$1"; }
# A RELEASE, or a checkout: a release carries VERSION and COMMIT and its bin/, and builds nothing.
PREBUILT=0
if [ -s "$REPO/VERSION" ] && [ -s "$REPO/COMMIT" ] && [ -x "$REPO/bin/ghost-setup" ] && [ -x "$REPO/bin/ghost.secd" ]; then
    PREBUILT=1
    REL_VERSION="$(tr -d '[:space:]' < "$REPO/VERSION")"
fi
ask() { # ask <prompt> <default> ; echoes the answer
    _p="$1"; _d="${2:-}"
    if [ -n "$_d" ]; then printf '%s [%s]: ' "$_p" "$_d" >&2; else printf '%s: ' "$_p" >&2; fi
    read -r _a || true
    [ -z "$_a" ] && _a="$_d"
    printf '%s' "$_a"
}
confirm_word() { # confirm_word <word> ; true only if the user types <word> exactly
    printf 'type %s to proceed (anything else aborts): ' "$1" >&2
    read -r _c || true
    [ "$_c" = "$1" ]
}

# ---------------------------------------------------------------------------
say "1/7  Box prep (root)"
# Database layer first , tools/install_db.sh owns it (pinned PGDG Postgres 18 + redis.io Redis 8,
# pgvector, cluster-creation preseeded off, distro units MASKED). It is idempotent, but skip it when
# everything it provides is already in place; running it costs an apt-get update.
DB_NEEDED=0
ls /usr/lib/postgresql/*/bin/initdb >/dev/null 2>&1 || DB_NEEDED=1
command -v redis-server >/dev/null 2>&1 || DB_NEEDED=1
command -v redis-cli    >/dev/null 2>&1 || DB_NEEDED=1
ls /usr/share/postgresql/*/extension/vector.control >/dev/null 2>&1 || DB_NEEDED=1
sh "$REPO/tools/pg_extensions.sh" >/dev/null 2>&1 || DB_NEEDED=1   # vector and pg_trgm, in the OS tree
# NOTE: unit state (enabled/masked) is deliberately NOT part of this gate. On a shared box the system
# postgres/redis power other things and stay enabled; install_db.sh only neutralises units for
# packages it installed itself. Binaries present = database layer complete.
if [ "$DB_NEEDED" = 1 ]; then
    echo "  database layer incomplete , running tools/install_db.sh"
    "$REPO/tools/install_db.sh"
else
    echo "  database layer: present, pgvector in, distro units masked"
fi

# Go toolchain , system-wide at /usr/local/go, from the mirror only, by tools/install_go.sh (the
# version it pins, the checks it makes, and why system-wide rather than $HOME are all in that file;
# redeploy.sh runs the same before every build, so a box follows go.mod to a newer Go later on).
# A release has its binaries: no Go.
if [ "$PREBUILT" = 1 ]; then
    echo "  release $REL_VERSION: binaries in bin/, no Go toolchain needed"
else
    sh "$REPO/tools/install_go.sh" || exit 1
fi
# server_setup_root.sh is idempotent; run it so packages/TPM/sudo/env are all in place. --host is
# only honoured when ghost.env does not yet exist, which is exactly right on a re-run.
CUR_HOST=""
[ -f "$ENVFILE" ] && CUR_HOST="$(grep '^GHOST_HOST=' "$ENVFILE" 2>/dev/null | head -1 | cut -d= -f2- || true)"
if [ -z "$CUR_HOST" ]; then
    CUR_HOST="$(ask "box host (LAN IP or FQDN the phone connects to)" "")"
    "$REPO/tools/server_setup_root.sh" --user "$SVC_USER" --host "$CUR_HOST" >/dev/null
else
    "$REPO/tools/server_setup_root.sh" --user "$SVC_USER" >/dev/null
fi
echo "  host: $CUR_HOST   user: $SVC_USER   repo: $REPO"

# ---------------------------------------------------------------------------
say "2/7  Verify as the service user"
# A LOGIN shell (su -) so the user's own environment loads , their Go toolchain lives in $HOME and
# only a login shell sources the profile that puts it on PATH. sudo -u sh -c does not. A release
# needs no Go: the check is told so (GHOST_PREBUILT) and says it as a note, not a failure.
su - "$SVC_USER" -c "cd '$REPO' && GHOST_PREBUILT=$PREBUILT ./tools/server_setup_user.sh" || {
    echo "  the user-side check reported problems above. Fix them, then re-run."
    exit 1
}

# ---------------------------------------------------------------------------
if [ "$PREBUILT" = 1 ]; then
    say "3/7  The binaries: release $REL_VERSION"
    echo "  $REPO/bin: $(ls "$REPO/bin" | tr '\n' ' ')"
    [ -x "$REPO/bin/ghost-update-guard" ] || echo "  note: no bin/ghost-update-guard in this release; a bad release taken later is not rolled back by itself"
else
    say "3/7  Build the binaries (as $SVC_USER)"
    echo "  make box ..."
    su - "$SVC_USER" -c "cd '$REPO' && make box"
    if [ ! -x "$REPO/bin/ghost-setup" ]; then
        echo "  build did not produce bin/ghost-setup , stopping."
        exit 1
    fi
    echo "  built: $REPO/bin/ghost-setup"
fi

# ---------------------------------------------------------------------------
say "4/7  The engine and the models"
# setup_llama.sh: llama.cpp from the mirror's pinned source, built for THIS machine's GPU (or the
# CPU), into bin/ so provisioning seeds it onto the volume with the daemons; the model weights, the
# phone's model and the speech engine staged for the first unlock. Before the disk, so the volume
# starts complete. It used to be a separate command people had to know about; it is a step now.
# GHOST_LLAMA=0 skips it (tools/update.sh engine, later, on an unlocked box). Already built and
# staged: nothing to do.
if [ "${GHOST_LLAMA:-1}" = 0 ]; then
    echo "  skipped (GHOST_LLAMA=0): sudo ./tools/update.sh engine on the unlocked box brings it"
elif [ -f /var/lib/ghost/registry.blob ]; then
    echo "  this box is provisioned already: the engine and the models are update.sh's (sudo ./tools/update.sh engine, unlocked)"
elif [ -x "$REPO/bin/llama-server" ] && ls /var/lib/ghost/staging/ai-models/*.gguf >/dev/null 2>&1; then
    echo "  llama-server in bin/ and models staged already"
else
    echo "  building llama.cpp from the mirror and fetching the models (a while: the weights are gigabytes)"
    bash "$REPO/tools/setup_llama.sh" || {
        echo "  the engine step did not finish (above). The box can be provisioned without it and"
        echo "  take it later with sudo ./tools/update.sh engine; or fix what it said and re-run."
        confirm_word "CONTINUE" || { echo "  aborted , nothing touched."; exit 1; }
    }
fi

# ---------------------------------------------------------------------------
say "5/7  Choose the volume"
echo "  The volume is one encrypted container: a WHOLE EMPTY DISK (no partitions, not the OS disk),"
echo "  or a FILE on a drive that is mounted at boot (a second disk with a filesystem on it already),"
echo "  allocated in full, e.g. /mnt/data/localghost.img. Either way the key is sealed under your PIN."
echo
echo "  Whole disks on this box:"
echo
lsblk -dpno NAME,SIZE,TYPE,MODEL 2>/dev/null | grep -E 'disk' | sed 's/^/    /'
echo
echo "  Partitions/mounts (for context , a disk mounted here is NOT your target; a file goes on one):"
lsblk -pno NAME,SIZE,MOUNTPOINT 2>/dev/null | grep -E '/' | sed 's/^/    /' || true
echo
DISK="$(ask "data disk (e.g. /dev/nvme1n1), or a file (e.g. /mnt/data/localghost.img)" "")"
IMAGE_SIZE=""
case "$DISK" in
    "") echo "  nothing chosen , aborting."; exit 1 ;;
    /dev/*)
        if [ ! -b "$DISK" ]; then
            echo "  '$DISK' is not a block device , aborting."
            exit 1
        fi ;;
    *)
        # THE VOLUME AS A FILE. The directory must be there (the drive mounted); a file there already
        # with bytes in it is taken as it is (a LUKS one is dealt with below, like a disk's); a new one
        # needs a size, checked against the room on the drive.
        IMG_DIR="$(dirname "$DISK")"
        if [ ! -d "$IMG_DIR" ]; then
            echo "  $IMG_DIR is not a directory , mount the drive first; aborting."
            exit 1
        fi
        if [ -s "$DISK" ]; then
            echo "  $DISK is there already ($(du -h "$DISK" | cut -f1)); it is used at that size"
        else
            FREE_GB="$(df -BG --output=avail "$IMG_DIR" 2>/dev/null | tail -1 | tr -dc '0-9')"
            echo "  $IMG_DIR is on $(df --output=source,fstype "$IMG_DIR" 2>/dev/null | tail -1) with ${FREE_GB:-?} GB free"
            IMAGE_SIZE="$(ask "size of the volume file (e.g. 500G, 1.5T; 20G at least, allocated in full)" "")"
            [ -n "$IMAGE_SIZE" ] || { echo "  a new file needs a size , aborting."; exit 1; }
        fi ;;
esac
IS_IMAGE=1
case "$DISK" in /dev/*) IS_IMAGE=0 ;; esac
# Refuse the obvious footgun: a disk with a mounted partition is almost certainly the OS/data-in-use.
if [ "$IS_IMAGE" = 0 ] && lsblk -pno NAME,MOUNTPOINT "$DISK" 2>/dev/null | awk 'NF>1{f=1} END{exit !f}'; then
    echo
    echo "  WARNING: $DISK has a MOUNTED partition. This is how you destroy the wrong disk."
    lsblk -pno NAME,SIZE,MOUNTPOINT "$DISK" | sed 's/^/    /'
    confirm_word "IUNDERSTAND" || { echo "  aborted."; exit 1; }
fi
# The last look before the point of no return: show what this device IS , model, serial, size,
# transport, SSD or spinning , and what is on it, then require a typed YES. Wrong-disk wipes happen
# to people who were sure; the card is for the ten seconds of reading it. For a file, the card is
# the file: where it is, its size, and whether it holds a LUKS container already.
if [ "$IS_IMAGE" = 1 ]; then
    FSTYPE="$(blkid -o value -s TYPE "$DISK" 2>/dev/null || true)"
    echo
    echo "  ------------------------------------------------------------"
    echo "  The volume will be this FILE:"
    echo
    echo "      $DISK"
    if [ -s "$DISK" ]; then
        echo "      size    $(du -h "$DISK" | cut -f1), as it is"
        if [ "$FSTYPE" = "crypto_LUKS" ]; then echo "      content a LUKS container (see below)"; else echo "      content ${FSTYPE:-not a LUKS container} , it will be OVERWRITTEN"; fi
    else
        echo "      size    $IMAGE_SIZE, allocated in full, new"
    fi
    echo "      drive   $(df --output=source,fstype,size,avail -h "$IMG_DIR" 2>/dev/null | tail -1)"
    echo "  ------------------------------------------------------------"
    if [ "$FSTYPE" = "crypto_LUKS" ]; then
        echo
        echo "  This file already holds a LUKS container. If it is the remains of an earlier incomplete"
        echo "  LocalGhost run, it can be removed here and setup continues. If it might be REAL encrypted"
        echo "  data, abort , this is the wrong file."
        printf '  type WIPEIT to remove the file and continue, anything else aborts: '
        read -r _w
        if [ "$_w" = "WIPEIT" ]; then
            cryptsetup luksErase --batch-mode "$DISK" 2>/dev/null || true
            rm -f "$DISK"
            [ -n "$IMAGE_SIZE" ] || IMAGE_SIZE="$(ask "size of the new volume file (e.g. 500G)" "")"
            echo "  removed , $DISK will be made anew"
        else
            echo "  aborted , nothing touched."
            exit 1
        fi
    fi
    confirm_word "YES" || { echo "  aborted , nothing touched."; exit 1; }
    echo "  confirmed: $DISK"
else
MODEL="$(lsblk -dno MODEL "$DISK" 2>/dev/null | sed 's/^ *//;s/ *$//')"
DSIZE="$(lsblk -dno SIZE "$DISK" 2>/dev/null | tr -d ' ')"
SERIAL="$(lsblk -dno SERIAL "$DISK" 2>/dev/null | tr -d ' ')"
TRAN="$(lsblk -dno TRAN "$DISK" 2>/dev/null | tr -d ' ')"
ROTA="$(lsblk -dno ROTA "$DISK" 2>/dev/null | tr -d ' ')"
KIND="SSD"; [ "$ROTA" = "1" ] && KIND="spinning disk"
NPARTS="$(lsblk -no NAME "$DISK" 2>/dev/null | tail -n +2 | wc -l | tr -d ' ')"
FSTYPE="$(blkid -o value -s TYPE "$DISK" 2>/dev/null || true)"
echo
echo "  ------------------------------------------------------------"
echo "  This is the drive you chose , it will be COMPLETELY WIPED:"
echo
echo "      $DISK , $DSIZE ${TRAN:-unknown-bus} $KIND"
echo "      model   ${MODEL:-unknown}"
echo "      serial  ${SERIAL:-unknown}"
if [ "$NPARTS" = "0" ] && [ -z "$FSTYPE" ]; then
    echo "      content none , no partitions, no signatures (empty, as expected)"
elif [ "$NPARTS" = "0" ]; then
    echo "      content a $FSTYPE signature on the raw disk (NOT empty , see below)"
else
    echo "      content $NPARTS EXISTING partition(s) , they will be destroyed:"
    lsblk -no NAME,SIZE,FSTYPE,MOUNTPOINT "$DISK" 2>/dev/null | tail -n +2 | sed 's/^/        /'
fi
echo "  ------------------------------------------------------------"
# Deal with an existing LUKS container NOW, not after the PINs. ghost-setup refuses a provisioned-
# looking disk (re-provisioning would silently keep the original PIN), so surfacing it here saves the
# whole ceremony. Two honest cases: a previous incomplete run of THIS setup (wipe and carry on), or
# somebody's real encrypted data (that is the wrong disk , walk away).
if [ "$FSTYPE" = "crypto_LUKS" ]; then
    echo
    echo "  This disk already holds a LUKS container. If it is the remains of an earlier incomplete"
    echo "  LocalGhost run, it can be wiped here and setup continues. If it might be REAL encrypted"
    echo "  data, abort , this is the wrong disk."
    printf '  type WIPEIT to erase the container and continue, anything else aborts: '
    read -r _w
    if [ "$_w" = "WIPEIT" ]; then
        cryptsetup luksErase --batch-mode "$DISK" 2>/dev/null || true
        wipefs -a "$DISK" >/dev/null
        echo "  wiped , $DISK is bare again"
    else
        echo "  aborted , nothing touched."
        exit 1
    fi
fi
confirm_word "YES" || { echo "  aborted , nothing touched."; exit 1; }
echo "  confirmed: $DISK"
fi

# ---------------------------------------------------------------------------
say "6/7  Seal tier"
echo "  How the disk key is protected:"
echo "    tpm       hardware , key sealed in the fTPM (Intel PTT), hardware lockout on PIN guessing."
echo "              The real-box default. Requires a usable TPM 2.0."
echo "    software  PIN-derived , key wrapped under Argon2id(PIN) in seal.env. Genuinely encrypted,"
echo "              but NO hardware lockout: a stolen disk can be brute-forced offline. Dev/TPM-less."
echo
SEAL="$(ask "seal tier (tpm/software)" "tpm")"
case "$SEAL" in
    tpm|software) ;;
    *) echo "  '$SEAL' is not a tier , aborting."; exit 1;;
esac
DOMAIN="$(ask "public domain (blank for LAN-only)" "$CUR_HOST")"
DOMAIN_ARG=""
[ -n "$DOMAIN" ] && DOMAIN_ARG="--domain $DOMAIN"

# ---------------------------------------------------------------------------
say "7/7  Dry run, then apply"
# THE DAEMONS' OWN USER. The service user builds and deploys; the daemons run as a system user
# nobody logs in as, so nothing else running as the service user (a shell, an editor, a website on
# this machine) can read the decrypted volume through /proc/<daemon>/root (tools/own_user.sh says
# more; a box set up before this moves over with it).
RUN_USER="${GHOST_RUN_USER:-ghostd}"
case "$RUN_USER" in ghost|ghost_ro|ghost_rw|root) echo "  GHOST_RUN_USER=$RUN_USER is a database role or root; pick another"; exit 1;; esac
if ! id "$RUN_USER" >/dev/null 2>&1; then
    useradd --system --user-group --no-create-home --home-dir /nonexistent \
        --shell /usr/sbin/nologin --comment "LocalGhost daemons" "$RUN_USER"
    passwd -l "$RUN_USER" >/dev/null 2>&1 || true
    echo "  made system user $RUN_USER for the daemons (no home, no shell, no password)"
fi
if [ "$IS_IMAGE" = 1 ]; then
    VOL_ARG="--image $DISK${IMAGE_SIZE:+ --size $IMAGE_SIZE}"
else
    VOL_ARG="--disk $DISK"
fi
COMMON="--user $RUN_USER $VOL_ARG --host $CUR_HOST $DOMAIN_ARG --seal $SEAL"
echo "  ghost-setup $COMMON"
echo
echo "  DRY RUN (touches nothing):"
# shellcheck disable=SC2086
"$REPO/bin/ghost-setup" $COMMON || { echo "  dry run failed , not applying."; exit 1; }
echo
echo "  Review the plan above , especially the volume line for $DISK."
if [ "$IS_IMAGE" = 1 ]; then
    echo "  APPLYING will make $DISK the volume, mint the box CA, write nginx + units, start the daemons,"
else
    echo "  APPLYING will partition $DISK, mint the box CA, write nginx + units, start the daemons,"
fi
echo "  and render the enrolment QR. It prompts for the main PIN and the wipe PIN on this terminal."
echo
if confirm_word "APPLY"; then
    # shellcheck disable=SC2086
    "$REPO/bin/ghost-setup" $COMMON --apply
    echo
    echo "=== Provisioned. Next: scan the QR above with the app (scanning IS enrolment), then unlock"
    echo "    with the main PIN. The first unlock ingests the engine and the models staged above."
    if [ "${GHOST_LLAMA:-1}" = 0 ] || [ ! -x "$REPO/bin/llama-server" ]; then
        echo "    No engine yet: on the unlocked box, sudo ./tools/update.sh engine (and speech)."
    fi
    echo "    Then, unlocked:  sudo ./tools/update.sh   (the streets, the models' updates; Wikipedia"
    echo "    when the volume has 60 GB free, GHOST_WIKI=0 leaves it out). tools/README.md has the"
    echo "    first-unlock checks (including the PTT cold-power-cycle if the TPM is in lockout)."
    echo "    The daemons run as $RUN_USER, a user nobody logs in as; $SVC_USER installs and deploys."
    if swapon --noheadings --show=NAME 2>/dev/null | grep -qv '^/dev/zram\|^/dev/dm-'; then
        echo "    Swap on this machine is not encrypted: the model, Postgres and the daemons can be"
        echo "    paged out to the OS disk in the clear. Encrypted swap (a random key at each boot,"
        echo "    /etc/crypttab) or zram closes it; tools/privacy_check.sh checks."
    fi
else
    echo "  Not applied. Re-run tools/setup.sh when ready; nothing was changed."
fi
