#!/usr/bin/env bash
# LocalGhost server: ROOT setup. Run ONCE as root (via your keyfile session) on the box.
#
#   ./server_setup_root.sh                 # service user defaults to 'ghost'
#   ./server_setup_root.sh --user <name>   # run the daemons as a chosen user instead
#
# Detect-driven and NON-DESTRUCTIVE: this box already runs other sites/APIs, so the script only
# installs what's missing, never reconfigures what's there, and does NOT touch existing nginx config
# (the chosen user is assumed to already have its own nginx deploy path). It:
#   - detects + installs missing packages (cryptsetup, postgres, redis, tpm2-tools, go if absent)
#   - puts the postgres server binaries on PATH (Debian hides them)
#   - loads dm_crypt (now + on boot)
#   - grants the chosen user TPM access (tss group) and scoped sudo for ghost.* services
#   - writes a box-level env file (/etc/ghost/ghost.env) with the non-secret runtime layout the
#     daemons read. Per-account DB ports are DERIVED in code (6000+slot / 6100+slot), so this file
#     holds only box-level config, never per-account/secret data.
#
# Built against what setup/ and hw/ actually invoke. Must be RUN on the box to be proven.
set -uo pipefail

# --- args ---
SVC_USER="ghost"
BOX_HOST=""
DRY=0
while [ $# -gt 0 ]; do
    case "$1" in
        --user) SVC_USER="${2:?--user needs a value}"; shift 2;;
        --host) BOX_HOST="${2:?--host needs a value}"; shift 2;;
        --dry-run|-n) DRY=1; shift;;
        *) echo "unknown arg: $1 (known: --user <name>, --host <fqdn-or-ip>, --dry-run)"; exit 2;;
    esac
done
# In dry-run we mutate NOTHING, so root is not required , you can preview as any user. A real run
# needs root. This lets you audit what would happen on a production box before touching it.
if [ "$DRY" = 0 ] && [ "$(id -u)" != 0 ]; then
    echo "run as root (installs packages, grants access, writes /etc/ghost), or pass --dry-run to preview"
    exit 1
fi

# RUN wraps every MUTATING command. In dry-run it prints '[would] <cmd>' and does nothing; otherwise
# it executes. Detection/inspection (command -v, lsmod, id, ls) is read-only and always runs for
# real, so the preview is accurate , it only proposes changes that are actually needed.
RUN() {
    if [ "$DRY" = 1 ]; then printf '  [would] %s\n' "$*"; else eval "$@"; fi
}
# WRITE_FILE <path> <<heredoc : in dry-run, show that we'd write the file (and its content); else write.
DRY_NOTE() { [ "$DRY" = 1 ] && printf '  [would] %s\n' "$*" || true; }

[ "$DRY" = 1 ] && echo ">>> DRY RUN , nothing will be changed. Showing what a real run would do. <<<"

echo "==================================================================="
echo " LocalGhost ROOT setup   (service user: $SVC_USER)"
echo "==================================================================="

# --- service user: create only if it's the default 'ghost' and missing. If you passed an existing
#     user (coder), we use it as-is and never modify its core identity. ---
if id "$SVC_USER" >/dev/null 2>&1; then
    echo "> service user '$SVC_USER' exists, using it"
else
    if [ "$SVC_USER" = "ghost" ]; then
        RUN "useradd -r -s /usr/sbin/nologin ghost" && echo "> created service user 'ghost'"
    else
        echo "ERROR: user '$SVC_USER' does not exist (create it first, or use the default 'ghost')"; exit 1
    fi
fi

# --- packages: detect, install only what's missing (this box runs other services) ---
echo "> Detecting + installing missing packages..."
APT_UPDATED=0
ensure() {  # ensure <binary> <apt-pkg> <label>
    if command -v "$1" >/dev/null 2>&1; then echo "  $3: present"; return; fi
    [ "$APT_UPDATED" = 0 ] && { RUN "apt-get update -qq"; APT_UPDATED=1; }
    echo "  $3: installing $2 ..."
    if [ "$DRY" = 1 ]; then printf '  [would] apt-get install -y %s\n' "$2"; return; fi
    apt-get install -y "$2" >/dev/null 2>&1 \
        && echo "  $3: installed" || echo "  $3: INSTALL FAILED ($2)"
}
ensure cryptsetup   cryptsetup    "cryptsetup (LUKS)"
ensure tpm2_getcap  tpm2-tools    "tpm2-tools (TPM debug)"
# nginx is assumed already installed + configured on this shared box; we don't touch it.
command -v nginx >/dev/null 2>&1 && echo "  nginx: present (not modifying its config)" \
    || echo "  nginx: NOT present , install + configure it yourself (this box's nginx is shared)"

# --- postgres server binaries onto PATH (Debian hides them in /usr/lib/postgresql/<ver>/bin) ---
echo "> Ensuring postgres server binaries are on PATH..."
if command -v initdb >/dev/null 2>&1 && command -v pg_ctl >/dev/null 2>&1; then
    echo "  initdb/pg_ctl: already on PATH"
else
    PGBIN="$(ls -d /usr/lib/postgresql/*/bin 2>/dev/null | sort -V | tail -1)"
    if [ -n "$PGBIN" ]; then
        for b in initdb pg_ctl postgres; do [ -x "$PGBIN/$b" ] && RUN "ln -sf '$PGBIN/$b' '/usr/local/bin/$b'"; done
        echo "  symlinked initdb/pg_ctl/postgres from $PGBIN into /usr/local/bin"
    else
        echo "  WARNING: postgres server binaries not found; is postgresql installed?"
    fi
fi

# --- database binaries: verify only , tools/install_db.sh owns installation ---
# install_db.sh pins PGDG Postgres 18 + redis.io Redis 8, preseeds cluster-creation off, installs
# pgvector, and MASKS the distro units (ghost.secd owns the lifecycles). Installing Debian's default
# packages here would fight those pins, so this script checks the result and stops if it is missing.
echo "> Verifying database binaries (installed by tools/install_db.sh)..."
DB_MISSING=0
if ls /usr/lib/postgresql/*/bin/initdb >/dev/null 2>&1 || command -v initdb >/dev/null 2>&1; then
    echo "  postgres: present"
else
    echo "  postgres: MISSING"; DB_MISSING=1
fi
if command -v redis-server >/dev/null 2>&1 && command -v redis-cli >/dev/null 2>&1; then
    echo "  redis: present"
else
    echo "  redis: MISSING"; DB_MISSING=1
fi
if ls /usr/share/postgresql/*/extension/vector.control >/dev/null 2>&1; then
    echo "  pgvector: present"
else
    echo "  pgvector: MISSING (search runs FTS-only until installed; install_db.sh includes it)"
fi
# every extension the box needs (vector, pg_trgm), from one list
sh "$(dirname "$0")/pg_extensions.sh" || echo "  an extension is missing: run tools/install_db.sh (root); the box runs without it, the lookups that want it do not"
if [ "$DB_MISSING" = 1 ]; then
    echo "  run tools/install_db.sh first (root), then re-run this script"
    exit 1
fi
for u in postgresql.service redis-server.service; do
    state="$(systemctl is-enabled "$u" 2>/dev/null || true)"
    case "$state" in
        masked) echo "  $u: masked (LocalGhost-only box , ghost.secd owns the lifecycle)";;
        "") ;;
        *) echo "  $u: $state (shared box , powers other services; coexisting. ghost.secd's instances use"
           echo "      their own ports and encrypted-volume data, the system service is not ours to touch)";;
    esac
done

# --- dm_crypt module (now + on boot) ---
echo "> dm_crypt (LUKS) kernel module..."
if lsmod 2>/dev/null | grep -q '^dm_crypt'; then echo "  already loaded"
else RUN "modprobe dm_crypt" && echo "  loaded (or would load)" || echo "  WARNING: modprobe dm_crypt failed"; fi
grep -qxF dm_crypt /etc/modules-load.d/localghost.conf 2>/dev/null \
    || RUN "echo dm_crypt > /etc/modules-load.d/localghost.conf"

# --- TPM access for the service user (kernel resource manager, /dev/tpmrm0) ---
# hw/tpm.go opens /dev/tpmrm0 directly (the KERNEL resource manager), so we do NOT need tpm2-abrmd.
# We just need: a 'tss' group, the device group-owned by tss with group rw, and the service user in
# the group. We create the group if absent and install a udev rule so the device permissions persist
# across reboots (the device is recreated on each boot, so a one-off chown would not survive).
echo "> TPM access for $SVC_USER (kernel RM, no abrmd)..."
if [ -e /dev/tpmrm0 ]; then
    # 1. ensure the tss group exists (this box has no tpm packages, so it likely does not yet)
    if getent group tss >/dev/null; then
        echo "  tss group exists"
    else
        RUN "groupadd --system tss"
        echo "  created system group tss"
    fi
    # 2. udev rule: on boot, set /dev/tpmrm0 (and /dev/tpm0) to root:tss mode 0660 so the group has rw.
    UDEV=/etc/udev/rules.d/60-localghost-tpm.rules
    if [ "$DRY" = 1 ]; then
        echo "  [would] write $UDEV granting group tss rw on /dev/tpm0 and /dev/tpmrm0"
        echo "  [would] udevadm control --reload && udevadm trigger (apply now)"
        echo "  [would] chgrp tss /dev/tpmrm0 /dev/tpm0 && chmod 0660 ... (immediate, pre-reboot)"
    else
        cat > "$UDEV" <<'EOF'
# LocalGhost: give group 'tss' read/write on the TPM so ghost.secd (kernel RM) can seal/unseal
# without root. Applies on every boot since the device nodes are recreated each time.
KERNEL=="tpm0",   MODE="0660", OWNER="root", GROUP="tss"
KERNEL=="tpmrm0", MODE="0660", OWNER="root", GROUP="tss"
EOF
        chmod 0644 "$UDEV"
        echo "  wrote $UDEV"
        # apply the rule now, and also chgrp the live device so we do not have to reboot first
        udevadm control --reload >/dev/null 2>&1 || true
        udevadm trigger -c add -s tpm >/dev/null 2>&1 || true
        chgrp tss /dev/tpmrm0 /dev/tpm0 2>/dev/null || true
        chmod 0660 /dev/tpmrm0 /dev/tpm0 2>/dev/null || true
        echo "  applied group rw on /dev/tpmrm0 now (and persistent via udev)"
    fi
    # 3. add the service user to tss
    id -nG "$SVC_USER" | grep -qw tss && echo "  $SVC_USER already in tss" \
        || { RUN "usermod -aG tss '$SVC_USER'"; echo "  added $SVC_USER to tss (RE-LOGIN to activate)"; }
else
    echo "  WARNING: no /dev/tpmrm0 (enable Intel PTT / firmware TPM in BIOS)"
fi

# --- scoped sudo: ONE unit, by exact name, for the service user (nginx is already granted) ---
# ghost.secd is the only LocalGhost systemd unit , ghost.watchd and the cohort are its children, not
# units , so the service user needs to manage that one and nothing else. The old rule used
# "ghost.*", where the "*" also matches a trailing " -H other.host", " -M container" or extra
# arguments, so it let the service user reach systemctl options it was never meant to. Named units,
# no wildcard, and only the verbs a deploy uses (restart, status, start, stop, plus daemon-reload
# for a changed unit). enable/disable are a one-time admin action, left to root.
echo "> Scoped sudo for $SVC_USER to manage ghost.secd (exact unit, no wildcard)..."
SYSTEMCTL="$(command -v systemctl || echo /usr/bin/systemctl)"
SUDOERS=/etc/sudoers.d/localghost-services
write_sudoers() {
    cat <<EOF
# LocalGhost: let $SVC_USER manage ONLY the ghost.secd unit (and reload a changed unit file). Narrow
# by design , exact names, no wildcard (a "ghost.*" pattern also matches extra systemctl arguments).
# nginx access is NOT here , $SVC_USER has its own nginx deploy rule on this box.
$SVC_USER ALL=(root) NOPASSWD: $SYSTEMCTL restart ghost.secd, $SYSTEMCTL restart ghost.secd.service
$SVC_USER ALL=(root) NOPASSWD: $SYSTEMCTL start ghost.secd, $SYSTEMCTL start ghost.secd.service
$SVC_USER ALL=(root) NOPASSWD: $SYSTEMCTL stop ghost.secd, $SYSTEMCTL stop ghost.secd.service
$SVC_USER ALL=(root) NOPASSWD: $SYSTEMCTL status ghost.secd, $SYSTEMCTL status ghost.secd.service
$SVC_USER ALL=(root) NOPASSWD: $SYSTEMCTL --no-pager status ghost.secd, $SYSTEMCTL --no-pager status ghost.secd.service
$SVC_USER ALL=(root) NOPASSWD: $SYSTEMCTL daemon-reload
EOF
}
if [ "$DRY" = 1 ]; then
    echo "  [would] write $SUDOERS granting $SVC_USER passwordless: $SYSTEMCTL {restart,start,stop,status} ghost.secd + daemon-reload (no wildcard)"
    echo "  [would] validate it with visudo -c (and delete it if invalid)"
else
if [ -f "$SUDOERS" ] && grep -q 'ghost\.\*' "$SUDOERS"; then
    cp -a "$SUDOERS" "$SUDOERS.wildcard-$(date -u +%Y%m%dT%H%M%SZ).bak"
    echo "  the existing rule used ghost.* ; the old file is kept as $SUDOERS.wildcard-*.bak"
fi
write_sudoers > "$SUDOERS"
chmod 440 "$SUDOERS"
if visudo -c -f "$SUDOERS" >/dev/null 2>&1; then echo "  installed + validated $SUDOERS (ghost.secd only, no wildcard)"
else echo "  ERROR: sudoers validation failed; removing to avoid breaking sudo"; rm -f "$SUDOERS"; exit 1; fi
fi

# --- box-level runtime env the daemons read (NON-secret, NON-committed, box-specific) ---
# Per-account DB ports are derived in code (6000+slot, 6100+slot), so this holds ONLY box layout:
# where state lives, the CA dir, listen addr, the host for cert issuance, and the postgres bin dir.
echo "> Writing box-level env /etc/ghost/ghost.env..."
RUN "install -d -m 0755 /etc/ghost"
STATE_DIR=/var/lib/ghost
RUN "install -d -o '$SVC_USER' -g '$SVC_USER' -m 0700 '$STATE_DIR'"
PGBIN_DIR="$(dirname "$(command -v pg_ctl 2>/dev/null || echo /usr/local/bin/pg_ctl)")"
# Only write if absent, so re-running root setup doesn't clobber a host/addr you've customised.
if [ -f /etc/ghost/ghost.env ]; then
    if [ -n "$BOX_HOST" ]; then
        echo "  /etc/ghost/ghost.env exists, leaving it , --host $BOX_HOST IGNORED (edit GHOST_HOST by hand,"
        echo "  or delete the file and re-run to have it rewritten)"
    else
        echo "  /etc/ghost/ghost.env exists, leaving it (edit by hand to change host/addr)"
    fi
    # One surgical heal on an existing file: the PATH line must include /usr/sbin , cryptsetup lives
    # there and ghost.secd shells it at unlock. An env written by an older revision lacks it, and the
    # failure mode (luksOpen: command not found, at unlock, on the console) is too obscure to leave in.
    if grep -q '^PATH=' /etc/ghost/ghost.env && ! grep '^PATH=' /etc/ghost/ghost.env | grep -q '/usr/sbin'; then
        sed -i 's|^PATH=\(.*\)$|PATH=\1:/usr/sbin:/sbin|' /etc/ghost/ghost.env
        echo "  healed PATH in existing ghost.env: appended /usr/sbin:/sbin (cryptsetup lives there)"
    fi
elif [ "$DRY" = 1 ]; then
    echo "  [would] write /etc/ghost/ghost.env with GHOST_HOST=$BOX_HOST, GHOST_ADDR=127.0.0.1:8443,"
    echo "          GHOST_STATE_DIR=$STATE_DIR, GHOST_CA_DIR=/etc/ghost/ca, GHOST_SERVICE_USER=$SVC_USER,"
    echo "          PATH including $PGBIN_DIR"
    [ -z "$BOX_HOST" ] && echo "          (no --host given: GHOST_HOST left empty for you to set)"
else
    cat > /etc/ghost/ghost.env <<EOF
# LocalGhost box runtime config. Box-specific, NOT committed to the repo. Read by the systemd units.
# Per-account Postgres/Redis ports are derived in code (6000+slot / 6100+slot); not configured here.
GHOST_HOST=$BOX_HOST
GHOST_ADDR=127.0.0.1:8443
GHOST_STATE_DIR=$STATE_DIR
GHOST_CA_DIR=/etc/ghost/ca
GHOST_SERVICE_USER=$SVC_USER
# postgres server binaries (initdb/pg_ctl) must be on the daemon's PATH:
PATH=$PGBIN_DIR:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin
EOF
    chmod 0644 /etc/ghost/ghost.env
    if [ -n "$BOX_HOST" ]; then
        echo "  wrote /etc/ghost/ghost.env  (GHOST_HOST=$BOX_HOST)"
    else
        echo "  wrote /etc/ghost/ghost.env  (set GHOST_HOST to the box IP/hostname before provisioning)"
    fi
fi

echo
echo "==================================================================="
if [ "$DRY" = 1 ]; then
    echo " DRY RUN complete , nothing was changed. The [would] lines above are what a real"
    echo " run (as root, without --dry-run) would do for service user: $SVC_USER"
    echo " Re-run without --dry-run, as root, to apply."
else
    echo " ROOT setup complete for service user: $SVC_USER"
    echo "   packages detected/installed, postgres bins on PATH, dm_crypt loaded,"
    echo "   $SVC_USER granted TPM (tss) + scoped ghost.* service sudo,"
    CUR_HOST="$(grep '^GHOST_HOST=' /etc/ghost/ghost.env 2>/dev/null | head -1 | cut -d= -f2-)"
    if [ -n "$CUR_HOST" ]; then
        echo "   box env at /etc/ghost/ghost.env (GHOST_HOST=$CUR_HOST , read from the file, not the flag)."
    else
        echo "   box env at /etc/ghost/ghost.env , GHOST_HOST is EMPTY. Set it before provisioning:"
        echo "     either edit the file, or: rm /etc/ghost/ghost.env && re-run this script with --host"
    fi
    echo
    REPO="$(cd "$(dirname "$0")/.." && pwd)"   # the server/ dir this script lives under
    echo " ------------------------------------------------------------------"
    echo " NEXT , copy/paste in order. Two roles: the SERVICE USER builds, ROOT provisions."
    echo " ------------------------------------------------------------------"
    echo
    echo " A) Become $SVC_USER with a FRESH login (required , the tss group is stamped at login,"
    echo "    your current shell predates the grant):"
    echo
    echo "      exec su - $SVC_USER"
    echo
    echo " B) As $SVC_USER, from the server dir , confirm the environment, then build:"
    echo
    echo "      cd $REPO"
    echo "      ./tools/server_setup_user.sh          # expect all OK except a GHOST_HOST reachability WARN"
    echo "      make box                              # one binary; the seal tier is chosen at provision, not build"
    echo
    echo " C) Back as ROOT (this session is fine , provisioning writes the disk, mints the CA, starts units):"
    echo
    echo "      cd $REPO"
    echo "      # DRY RUN first , prints every step, touches nothing. Read the partition line twice:"
    echo "      ./bin/ghost-setup --user $SVC_USER --disk /dev/nvmeXnY --host $CUR_HOST --domain $CUR_HOST"
    echo "      # then APPLY (prompts for main PIN + wipe PIN on the tty, no echo):"
    echo "      ./bin/ghost-setup --user $SVC_USER --disk /dev/nvmeXnY --host $CUR_HOST --domain $CUR_HOST --apply"
    echo
    echo "    Replace /dev/nvmeXnY with the EMPTY data disk from lsblk , NOT the OS disk. Setup destroys it."
    echo "    LAN-only (no public domain)? Drop --domain and pass the LAN IP as --host."
    echo
    echo " D) SEAL TIER , the default is HARDWARE (TPM). Choose per box by adding --seal to BOTH ghost-setup"
    echo "    commands above:"
    echo
    echo "      (default)        --seal tpm        # disk key sealed in the fTPM (Intel PTT); hardware"
    echo "                                         # dictionary-attack lockout guards PIN guessing. Real boxes."
    echo "      software lock    --seal software   # disk key wrapped under Argon2id(PIN) in seal.env. Still"
    echo "                                         # genuinely encrypted, but NO hardware lockout , a stolen"
    echo "                                         # disk can be brute-forced offline. For TPM-less/dev boxes."
    echo
    echo "    Example, software tier, apply:"
    echo "      ./bin/ghost-setup --user $SVC_USER --disk /dev/nvmeXnY --host $CUR_HOST --seal software --apply"
    echo
    echo "    The box never silently downgrades: if you provision --seal tpm and the TPM is later unusable,"
    echo "    unlock hard-stops rather than falling back. Pick the tier deliberately here."
    echo
    echo " E) Enrol + unlock: scan the QR ghost-setup renders (scanning IS enrolment), then unlock with the"
    echo "    main PIN from the app. Inference from nothing:  sudo ./tools/setup_llama.sh --models /path/with/ggufs"
    echo "    (builds llama.cpp in /opt/localghost/llama.cpp, stages weights; unlock ingests them)."
    echo "    See tools/README.md steps 6-8 for models, the bundle, and the checks."
    echo " ------------------------------------------------------------------"
fi
echo "==================================================================="
