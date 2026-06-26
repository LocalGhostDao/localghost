#!/usr/bin/env bash
# LocalGhost server: USER check. Run as the SERVICE USER (the one you passed to server_setup_root.sh,
# e.g. coder). No root. Pure inspection , safe to run any time. Confirms the box is ready and that
# the access root granted actually works for you.
#
#   tools/server_setup_user.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# This script lives in server/tools/, so the server module's go.mod is one level up. Fall back to a
# search if the layout differs, then to the known-required version.
GO_MOD="$SCRIPT_DIR/../go.mod"
[ -f "$GO_MOD" ] || GO_MOD="$(find "$SCRIPT_DIR/../.." -maxdepth 3 -name go.mod -path '*server*' 2>/dev/null | head -1)"
GO_WANT="$(grep -E '^go ' "$GO_MOD" 2>/dev/null | awk '{print $2}')"; GO_WANT="${GO_WANT:-1.25.4}"

problems=0
note() { printf '  %-26s %s\n' "$1" "$2"; }
ok()   { note "$1" "OK    $2"; }
bad()  { note "$1" "FAIL  $2"; problems=1; }
warn() { note "$1" "WARN  $2"; }
have() { command -v "$1" >/dev/null 2>&1; }

echo "==================================================================="
echo " LocalGhost server check   (as $(whoami), read-only)"
echo "==================================================================="

echo; echo "--- packages (root setup should have installed these) ---"
for b in cryptsetup psql redis-server redis-cli; do
    have "$b" && ok "$b" "(present)" || bad "$b" "(missing , run server_setup_root.sh as root)"
done
if have initdb && have pg_ctl; then ok "initdb / pg_ctl" "(on PATH)"
else bad "initdb / pg_ctl" "(not on PATH , root setup symlinks them)"; fi
have nginx && ok "nginx" "(present , shared on this box)" || warn "nginx" "(absent; you host other sites here?)"

echo; echo "--- box env (written by root setup) ---"
if [ -f /etc/ghost/ghost.env ]; then
    ok "/etc/ghost/ghost.env" "(present)"
    host="$(grep -E '^GHOST_HOST=' /etc/ghost/ghost.env | cut -d= -f2-)"
    [ -n "$host" ] && ok "GHOST_HOST" "($host)" || warn "GHOST_HOST" "(empty , set it before provisioning)"
else bad "/etc/ghost/ghost.env" "(missing , run server_setup_root.sh as root)"; fi
[ -d /var/lib/ghost ] && [ -w /var/lib/ghost ] && ok "state dir" "(/var/lib/ghost writable)" \
    || warn "state dir" "(/var/lib/ghost missing or not writable by you)"

echo; echo "--- your granted access ---"
# nginx deploy (you set this up yourself on this box): can you reload nginx without a password?
if sudo -n -l 2>/dev/null | grep -q 'reload nginx'; then ok "sudo: reload nginx" "(granted)"
else warn "sudo: reload nginx" "(not visible; you deploy other sites , should already have this)"; fi
# ghost.* services (granted by root setup)
if sudo -n -l 2>/dev/null | grep -q 'systemctl.*ghost'; then ok "sudo: ghost.* services" "(granted)"
else bad "sudo: ghost.* services" "(not granted , run server_setup_root.sh, then re-login)"; fi
# TPM
if [ -e /dev/tpmrm0 ]; then
    if [ -r /dev/tpmrm0 ] && [ -w /dev/tpmrm0 ]; then ok "TPM /dev/tpmrm0" "(you have rw)"
    elif id -nG | grep -qw tss; then warn "TPM /dev/tpmrm0" "(in tss but no rw yet , did you re-login?)"
    else bad "TPM /dev/tpmrm0" "(not in tss group , root grant + re-login needed)"; fi
else bad "TPM /dev/tpmrm0" "(no TPM device , enable Intel PTT in BIOS)"; fi

echo; echo "--- Go toolchain (you can install this yourself, no root) ---"
if have go; then
    GO_HAVE="$(go version 2>/dev/null | grep -oE 'go[0-9.]+' | head -1 | sed 's/^go//')"
    if [ "$(printf '%s\n%s\n' "$GO_WANT" "$GO_HAVE" | sort -V | head -1)" = "$GO_WANT" ]; then
        ok "go" "(go$GO_HAVE >= go$GO_WANT)"
    else bad "go" "(go$GO_HAVE < go$GO_WANT , install official tarball into \$HOME, no root)"; fi
else bad "go" "(not installed , fetch go$GO_WANT from https://go.dev/dl/ into \$HOME, no root)"; fi

echo
echo "==================================================================="
if [ "$problems" = 0 ]; then
    echo " READY. Build (no root):  cd <server> && make box TAGS=tpm"
    echo " Then provision in your ROOT session:  ./bin/ghost-setup --disk <dev> --host <ip> --apply"
else
    echo " NOT READY , see FAIL lines. Missing packages/access => run server_setup_root.sh as root,"
    echo " then RE-LOGIN (for the tss group). Missing only Go => install it into \$HOME yourself."
fi
echo "==================================================================="
[ "$problems" = 0 ]
