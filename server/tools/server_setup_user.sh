#!/usr/bin/env bash
# LocalGhost server: USER check. Run as the SERVICE USER (the one you passed to server_setup_root.sh,
# e.g. a chosen service user). No root. Pure inspection , safe to run any time. Confirms the box is ready and that
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

# resolve_ips <host-or-ip> : print the IPs it resolves to (one per line), via nsswitch (files+DNS).
# Works for a DNS name, a .local name, or an IP literal (which resolves to itself).
resolve_ips() { getent ahosts "$1" 2>/dev/null | awk '{print $1}' | sort -u; }

# The self-check is deliberately shallow: the box answers "down" to everything until a PIN is entered,
# so there is NOTHING that legitimately responds "up" , and there must not be, or we would hand an
# observer an oracle that this is a LocalGhost box. So we do not probe content at all. We only ask "did
# we hit a listener at this name+port": open a TLS connection through GHOST_HOST and see if it completes.
# A completed handshake means DNS resolves, the port forwards, and a listener is there; what it returns
# after (the down response) is exactly what we expect and we ignore it. Connect-only, no oracle added.
# port_from_addr <addr> : pull the port off GHOST_ADDR (e.g. 127.0.0.1:8443 -> 8443); default 443.
port_from_addr() { case "$1" in *:*) echo "${1##*:}";; *) echo 443;; esac; }
# tls_reachable <host> <port> : true if a TLS connection to host:port completes. Uses openssl if present
# (handshake only, then close), else falls back to a bare TCP connect via bash's /dev/tcp. No client
# cert is sent and no request is made, so this never depends on the PIN/down behaviour , it only asks
# whether something is listening there.
tls_reachable() {
    if have openssl; then
        printf 'Q\n' | timeout 6 openssl s_client -connect "${1}:${2}" -servername "$1" >/dev/null 2>&1
    elif have timeout; then
        timeout 4 bash -c "exec 3<>/dev/tcp/${1}/${2}" 2>/dev/null
    else
        bash -c "exec 3<>/dev/tcp/${1}/${2}" 2>/dev/null
    fi
}

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
    addr="$(grep -E '^GHOST_ADDR=' /etc/ghost/ghost.env | cut -d= -f2-)"
    pubport="$(port_from_addr "${addr:-:443}")"
    if [ -z "$host" ]; then
        warn "GHOST_HOST" "(empty , set it before provisioning)"
    else
        # Step 1: does the public name resolve at all?
        rip="$(resolve_ips "$host")"
        if [ -z "$rip" ]; then
            warn "GHOST_HOST" "($host does NOT resolve , no public DNS record; the phone can't reach it by name)"
        else
            rip_flat="$(echo $rip | tr '\n' ' ' | sed 's/ $//')"
            # Step 2: did we hit a listener? Connect only , the box answers "down" to everything until a
            # PIN is entered, so there is nothing to inspect and nothing that should look "up". A completed
            # TLS handshake is all we need: DNS resolves, the port forwards, something is listening.
            if tls_reachable "$host" "$pubport"; then
                ok "GHOST_HOST" "($host:$pubport reachable , resolves ($rip_flat), port forwards, a listener is up)"
            else
                bad "GHOST_HOST" "($host:$pubport NOT reachable , resolves ($rip_flat) but nothing is listening/forwarding there)"
            fi
        fi
    fi
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
