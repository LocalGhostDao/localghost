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
DRY=0
while [ $# -gt 0 ]; do
    case "$1" in
        --user) SVC_USER="${2:?--user needs a value}"; shift 2;;
        --dry-run|-n) DRY=1; shift;;
        *) echo "unknown arg: $1"; exit 2;;
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
ensure psql         postgresql    "postgresql (client+server)"
ensure redis-server redis-server  "redis-server"
ensure redis-cli    redis-tools   "redis-cli"
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

# --- scoped sudo: ghost.* service management for the service user (nginx is already granted) ---
echo "> Scoped sudo for $SVC_USER to manage ghost.* services..."
SYSTEMCTL="$(command -v systemctl || echo /usr/bin/systemctl)"
SUDOERS=/etc/sudoers.d/localghost-services
if [ "$DRY" = 1 ]; then
    echo "  [would] write $SUDOERS granting $SVC_USER passwordless: $SYSTEMCTL {start,stop,restart,status,enable,disable} ghost.* + daemon-reload"
    echo "  [would] validate it with visudo -c (and delete it if invalid)"
else
cat > "$SUDOERS" <<EOF
# LocalGhost: let $SVC_USER manage ONLY ghost.* systemd units (and daemon-reload). Narrow by design.
# nginx access is NOT here , $SVC_USER already has its own nginx deploy sudo rule on this box.
$SVC_USER ALL=(root) NOPASSWD: $SYSTEMCTL start ghost.*
$SVC_USER ALL=(root) NOPASSWD: $SYSTEMCTL stop ghost.*
$SVC_USER ALL=(root) NOPASSWD: $SYSTEMCTL restart ghost.*
$SVC_USER ALL=(root) NOPASSWD: $SYSTEMCTL status ghost.*
$SVC_USER ALL=(root) NOPASSWD: $SYSTEMCTL enable ghost.*
$SVC_USER ALL=(root) NOPASSWD: $SYSTEMCTL disable ghost.*
$SVC_USER ALL=(root) NOPASSWD: $SYSTEMCTL daemon-reload
EOF
chmod 440 "$SUDOERS"
if visudo -c -f "$SUDOERS" >/dev/null 2>&1; then echo "  installed + validated $SUDOERS"
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
    echo "  /etc/ghost/ghost.env exists, leaving it (edit by hand to change host/addr)"
elif [ "$DRY" = 1 ]; then
    echo "  [would] write /etc/ghost/ghost.env with GHOST_ADDR=127.0.0.1:8443, GHOST_STATE_DIR=$STATE_DIR,"
    echo "          GHOST_CA_DIR=/etc/ghost/ca, GHOST_SERVICE_USER=$SVC_USER, PATH including $PGBIN_DIR"
    echo "          (GHOST_HOST left empty for you to set)"
else
    cat > /etc/ghost/ghost.env <<EOF
# LocalGhost box runtime config. Box-specific, NOT committed to the repo. Read by the systemd units.
# Per-account Postgres/Redis ports are derived in code (6000+slot / 6100+slot); not configured here.
GHOST_HOST=
GHOST_ADDR=127.0.0.1:8443
GHOST_STATE_DIR=$STATE_DIR
GHOST_CA_DIR=/etc/ghost/ca
GHOST_SERVICE_USER=$SVC_USER
# postgres server binaries (initdb/pg_ctl) must be on the daemon's PATH:
PATH=$PGBIN_DIR:/usr/local/bin:/usr/bin:/bin
EOF
    chmod 0644 /etc/ghost/ghost.env
    echo "  wrote /etc/ghost/ghost.env  (set GHOST_HOST to the box IP/hostname before provisioning)"
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
    echo "   box env at /etc/ghost/ghost.env (set GHOST_HOST before provisioning)."
    echo
    echo " NEXT (as $SVC_USER, after RE-LOGIN so the tss group applies):"
    echo "   1) edit /etc/ghost/ghost.env , set GHOST_HOST to the box IP/hostname"
    echo "   2) tools/server_setup_user.sh        # confirm everything as $SVC_USER"
    echo "   3) cd <server> && make box TAGS=tpm  # build the daemon"
    echo "   4) provisioning (ghost-setup --apply) still needs root , run it in this root session"
fi
echo "==================================================================="
