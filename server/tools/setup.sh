#!/bin/sh
# setup.sh , the guided LocalGhost bring-up. ONE command, run as root, that walks the whole flow:
# verify the box prep, build the binaries AS THE SERVICE USER, let you pick the disk from a list,
# choose the seal tier, dry-run, and (only on an explicit typed confirmation) provision.
#
# Why root, not the service user: privilege drops, it does not round-trip. A script cannot become the
# user, build, and come back to root in one process. So this runs as root and uses `sudo -u <user>`
# for the build step , the one part that must run in the user's context , keeping provisioning
# (which writes the disk and mints the CA) in root's own hands. One uninterrupted run, no manual
# su-ing back and forth.
#
# It changes nothing until you confirm. The disk step and the apply step each require typing an
# explicit word , there is no --apply-by-accident path.
set -eu

SVC_USER="${1:-}"
if [ "$(id -u)" != 0 ]; then
    echo "run as root: sudo ./tools/setup.sh <service-user>"
    exit 1
fi
if [ -z "$SVC_USER" ]; then
    printf "service user the daemons run as [coder]: "
    read -r SVC_USER
    [ -z "$SVC_USER" ] && SVC_USER="coder"
fi
if ! id "$SVC_USER" >/dev/null 2>&1; then
    echo "user '$SVC_USER' does not exist , create it or pass a different one"
    exit 1
fi
REPO="$(cd "$(dirname "$0")/.." && pwd)"
ENVFILE=/etc/ghost/ghost.env
say() { printf '\n=== %s ===\n' "$1"; }
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
say "1/6  Box prep (root)"
# Database layer first , tools/install_db.sh owns it (pinned PGDG Postgres 18 + redis.io Redis 8,
# pgvector, cluster-creation preseeded off, distro units MASKED). It is idempotent, but skip it when
# everything it provides is already in place; running it costs an apt-get update.
DB_NEEDED=0
ls /usr/lib/postgresql/*/bin/initdb >/dev/null 2>&1 || DB_NEEDED=1
command -v redis-server >/dev/null 2>&1 || DB_NEEDED=1
command -v redis-cli    >/dev/null 2>&1 || DB_NEEDED=1
ls /usr/share/postgresql/*/extension/vector.control >/dev/null 2>&1 || DB_NEEDED=1
# NOTE: unit state (enabled/masked) is deliberately NOT part of this gate. On a shared box the system
# postgres/redis power other things and stay enabled; install_db.sh only neutralises units for
# packages it installed itself. Binaries present = database layer complete.
if [ "$DB_NEEDED" = 1 ]; then
    echo "  database layer incomplete , running tools/install_db.sh"
    "$REPO/tools/install_db.sh"
else
    echo "  database layer: present, pgvector in, distro units masked"
fi

# Go toolchain , system-wide at /usr/local/go, so every context (login shells, sudo, systemd, this
# wizard) sees the same compiler. A $HOME install only exists in shells that source the right profile,
# which is exactly the kind of environment archaeology one-command setup is meant to end. Pinned
# version, checksum verified against go.dev's official manifest before unpacking.
GO_MIN="1.25"
GO_PIN="1.25.4"
go_ok() {
    command -v go >/dev/null 2>&1 || return 1
    _gv="$(go version 2>/dev/null | sed 's/.*go\([0-9][0-9.]*\).*/\1/')"
    [ -n "$_gv" ] || return 1
    [ "$(printf '%s\n%s\n' "$GO_MIN" "$_gv" | sort -V | head -1)" = "$GO_MIN" ]
}
if go_ok; then
    echo "  go: $(go version | awk '{print $3}') (system)"
else
    ARCH="$(dpkg --print-architecture 2>/dev/null || echo amd64)"
    TARBALL="go${GO_PIN}.linux-${ARCH}.tar.gz"
    echo "  go: installing ${GO_PIN} system-wide (/usr/local/go)..."
    TMPD="$(mktemp -d)"
    # THE MIRROR FIRST. tools/mirror_fetch.sh takes the tarball from https://www.localghost.ai/mirror
    # only if the manifest's gpg signature is by the site key pinned in tools/mirror-key.asc (in this
    # repo) and the file's SHA-256 matches that manifest , the publisher checked it against go.dev's
    # own checksum before signing. It is shell and gpg, so it works before Go exists.
    # No mirror (GHOST_MIRROR=off, not set up, down): go.dev's checksum list, as before.
    command -v gpg >/dev/null 2>&1 || apt-get install -y gpg >/dev/null 2>&1 || true
    WANT_SHA=""
    GOT_SHA=""
    if sh "$REPO/tools/mirror_fetch.sh" go "$TMPD/mirror" "$TARBALL" && [ -s "$TMPD/mirror/$TARBALL" ]; then
        mv "$TMPD/mirror/$TARBALL" "$TMPD/$TARBALL"
        GOT_SHA="$(sha256sum "$TMPD/$TARBALL" | awk '{print $1}')"
        WANT_SHA="$GOT_SHA"
        echo "  go: $TARBALL from the mirror, signature and hash checked"
    else
        curl -fsSL -o "$TMPD/$TARBALL" "https://dl.google.com/go/$TARBALL"
        # include=all is required: the default manifest lists only the LATEST patch of each stable branch,
        # and a pinned older patch (ours) would come back "not found" , which is a lookup gap, not a
        # missing checksum.
        curl -fsSL 'https://go.dev/dl/?mode=json&include=all' -o "$TMPD/manifest.json"
        WANT_SHA="$(tr ',' '\n' < "$TMPD/manifest.json" | grep -A8 "\"filename\": \"$TARBALL\"" | grep '"sha256"' | head -1 | sed 's/.*"sha256": *"\([0-9a-f]*\)".*/\1/')"
        GOT_SHA="$(sha256sum "$TMPD/$TARBALL" | awk '{print $1}')"
        if [ -z "$WANT_SHA" ]; then
            echo "  go: $TARBALL not found in the release manifest , refusing to install unverified"
            echo "  (manifest entries near our version, for debugging:)"
            grep -o '"version": "go1\.[0-9.]*"' "$TMPD/manifest.json" 2>/dev/null | sort -u | head -8 | sed 's/^/    /'
            rm -rf "$TMPD"
            exit 1
        fi
    fi
    if [ "$WANT_SHA" != "$GOT_SHA" ]; then
        echo "  go: CHECKSUM MISMATCH (want $WANT_SHA, got ${GOT_SHA:-nothing}) , refusing to install"
        rm -rf "$TMPD"
        exit 1
    fi
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "$TMPD/$TARBALL"
    ln -sf /usr/local/go/bin/go /usr/local/bin/go
    ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
    rm -rf "$TMPD"
    echo "  go: $(/usr/local/bin/go version | awk '{print $3}') installed, on everyone's PATH via /usr/local/bin"
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
say "2/6  Verify as the service user"
# A LOGIN shell (su -) so the user's own environment loads , their Go toolchain lives in $HOME and
# only a login shell sources the profile that puts it on PATH. sudo -u sh -c does not.
su - "$SVC_USER" -c "cd '$REPO' && ./tools/server_setup_user.sh" || {
    echo "  the user-side check reported problems above. Fix them, then re-run."
    exit 1
}

# ---------------------------------------------------------------------------
say "3/6  Build the binaries (as $SVC_USER)"
echo "  make box ..."
su - "$SVC_USER" -c "cd '$REPO' && make box"
if [ ! -x "$REPO/bin/ghost-setup" ]; then
    echo "  build did not produce bin/ghost-setup , stopping."
    exit 1
fi
echo "  built: $REPO/bin/ghost-setup"

# ---------------------------------------------------------------------------
say "4/6  Choose the data disk"
echo "  Whole disks on this box (the data disk should be EMPTY , no partitions, not the OS disk):"
echo
lsblk -dpno NAME,SIZE,TYPE,MODEL 2>/dev/null | grep -E 'disk' | sed 's/^/    /'
echo
echo "  Partitions/mounts (for context , anything mounted here is NOT your target):"
lsblk -pno NAME,SIZE,MOUNTPOINT 2>/dev/null | grep -E '/' | sed 's/^/    /' || true
echo
DISK="$(ask "data disk to provision (e.g. /dev/nvme1n1)" "")"
if [ -z "$DISK" ] || [ ! -b "$DISK" ]; then
    echo "  '$DISK' is not a block device , aborting."
    exit 1
fi
# Refuse the obvious footgun: a disk with a mounted partition is almost certainly the OS/data-in-use.
if lsblk -pno NAME,MOUNTPOINT "$DISK" 2>/dev/null | awk 'NF>1{f=1} END{exit !f}'; then
    echo
    echo "  WARNING: $DISK has a MOUNTED partition. This is how you destroy the wrong disk."
    lsblk -pno NAME,SIZE,MOUNTPOINT "$DISK" | sed 's/^/    /'
    confirm_word "IUNDERSTAND" || { echo "  aborted."; exit 1; }
fi
# The last look before the point of no return: show exactly what this device IS , model, serial,
# size, transport, SSD or spinning , and what is on it, then require a typed YES. Wrong-disk wipes
# happen to people who were sure; the card is for the ten seconds of actually reading it.
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

# ---------------------------------------------------------------------------
say "5/6  Seal tier"
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
say "6/6  Dry run, then apply"
COMMON="--user $SVC_USER --disk $DISK --host $CUR_HOST $DOMAIN_ARG --seal $SEAL"
echo "  ghost-setup $COMMON"
echo
echo "  DRY RUN (touches nothing):"
# shellcheck disable=SC2086
"$REPO/bin/ghost-setup" $COMMON || { echo "  dry run failed , not applying."; exit 1; }
echo
echo "  Review the plan above , especially the partition line for $DISK."
echo "  APPLYING will partition $DISK, mint the box CA, write nginx + units, start the daemons, and"
echo "  render the enrolment QR. It prompts for the main PIN and the wipe PIN on this terminal."
echo
if confirm_word "APPLY"; then
    # shellcheck disable=SC2086
    "$REPO/bin/ghost-setup" $COMMON --apply
    echo
    echo "=== Provisioned. Next: scan the QR above with the app (scanning IS enrolment), then unlock"
    echo "    with the main PIN. For inference from NOTHING (builds llama.cpp, fetches + stages"
    echo "    the model , ingested onto the volume at unlock):  sudo ./tools/setup_llama.sh --help-ish:"
    echo "      sudo ./tools/setup_llama.sh --models /path/with/ggufs"
    echo "    See tools/README.md steps 6-8 for models, the DB bundle, and the"
    echo "    first-unlock checks (including the PTT cold-power-cycle if the TPM is in lockout)."
else
    echo "  Not applied. Re-run tools/setup.sh when ready; nothing was changed."
fi
