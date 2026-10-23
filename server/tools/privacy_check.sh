#!/usr/bin/env bash
# privacy_check.sh , what this box leaks to the machine it runs on. Read-only: it changes nothing,
# starts nothing, and never calls secd's API (a poll could log the phone out). Run as root, best
# while UNLOCKED (some checks look at the running cohort).
#
#   sudo ./tools/privacy_check.sh
#
# Each line is PASS, WARN (worth fixing) or INFO. Exit status is the number of WARN lines.
# The reasons are in the free-time notes of 30 Sep 2026 (LocalGhost privacy field notes).

set -u
MOUNT="${GHOST_MOUNT:-/var/lib/ghost/mnt/slot0}"
STATE="${GHOST_STATE_DIR:-/var/lib/ghost}"
warns=0
pass() { printf '\033[32mPASS\033[0m  %s\n' "$1"; }
warn() { printf '\033[33mWARN\033[0m  %s\n' "$1"; warns=$((warns + 1)); }
info() { printf '\033[2mINFO\033[0m  %s\n' "$1"; }

if [ "$(id -u)" -ne 0 ]; then echo "run as root (sudo)" >&2; exit 1; fi

echo "== the OS disk"
fs=$(findmnt -n -o FSTYPE /tmp 2>/dev/null || true)
if [ "$fs" = tmpfs ]; then pass "/tmp is tmpfs (RAM)"
else warn "/tmp is on ${fs:-the root filesystem}: temporary files there are on the unencrypted disk (the cohort's TMPDIR fix moves ours to the volume; old leftovers below)"; fi
left=$(ls /tmp/lg-* 2>/dev/null | wc -l)
[ "$left" -gt 0 ] && warn "$left LocalGhost temp file(s) in /tmp (decoded photos): $(ls /tmp/lg-* 2>/dev/null | head -3 | tr '\n' ' ')" || pass "no LocalGhost temp files in /tmp"

sw=$(swapon --noheadings --show=NAME 2>/dev/null)
if [ -z "$sw" ]; then pass "no swap (nothing of the vault's memory is paged to disk)"
else
    for s in $sw; do
        case "$s" in
            /dev/zram*) pass "swap $s is zram (RAM)";;
            *) t=$(lsblk -no TYPE "$s" 2>/dev/null | head -1)
               if [ "$t" = crypt ]; then pass "swap $s is encrypted"
               else warn "swap $s is plain: Postgres, llama-server and the daemons can be paged out to the OS disk unencrypted (encrypted swap with a random key, or zram)"; fi;;
        esac
    done
fi

cp=$(cat /proc/sys/kernel/core_pattern 2>/dev/null)
info "core_pattern: $cp"
lim=$(systemctl show -p LimitCORE ghost.secd 2>/dev/null | cut -d= -f2)
if [ "$lim" = 0 ]; then pass "ghost.secd and its children cannot dump core (LimitCORE=0)"
else warn "ghost.secd's LimitCORE is ${lim:-unknown}: a crashed llama-server or Postgres can leave its memory on the OS disk"; fi
if command -v coredumpctl >/dev/null 2>&1; then
    cores=$(coredumpctl list --no-pager --no-legend 2>/dev/null | grep -E "llama|postgres|redis|whisper|ffmpeg|ghost\." | wc -l)
    [ "$cores" -gt 0 ] && warn "$cores stored core dump(s) of LocalGhost processes (plaintext memory): coredumpctl list | grep -E 'llama|postgres|ghost'" || pass "no stored core dumps of LocalGhost processes"
fi

if [ -d /var/log/journal ]; then
    info "journald is persistent (/var/log/journal, on the OS disk)"
    n=$(journalctl -u ghost.secd --no-pager -q 2>/dev/null | grep -c "geo lod" || true)
    [ "${n:-0}" -gt 0 ] && warn "$n 'geo lod' line(s) in secd's journal: the bounding box of every map view (journalctl --vacuum-time=1s after the fix, or rotate)" || pass "no map views in secd's journal"
    u=$(journalctl -u ghost.secd --no-pager -q 2>/dev/null | grep -c "unlock stage ok" || true)
    [ "${u:-0}" -gt 0 ] && warn "$u unlock stage line(s) in secd's journal: a timeline of when the box was opened (at debug level since 30 Sep 2026; they go when the journal rotates)" || pass "no unlock timeline in secd's journal"
else pass "journald is volatile (RAM only)"; fi

mode=$(sed -n 's/^GHOST_SEAL_MODE=//p' "$STATE/seal.env" 2>/dev/null | tr -d '"' | head -1)
case "$mode" in
    tpm) pass "the volume key is sealed in the TPM";;
    software) warn "software seal: the wrapped key lives in $STATE/seal.env on the OS disk; a wipe deletes it by rewriting the file, and the old blocks stay until reused";;
    *) info "seal mode: ${mode:-unknown}";;
esac

echo "== other users of this machine"
ps_scope=$(cat /proc/sys/kernel/yama/ptrace_scope 2>/dev/null || echo none)
case "$ps_scope" in
    1|2|3) pass "ptrace is restricted (yama ptrace_scope=$ps_scope)";;
    *) warn "ptrace is not restricted (yama: $ps_scope): any process can attach to another of the same user";;
esac
if findmnt -n -o OPTIONS /proc 2>/dev/null | grep -q hidepid; then pass "/proc hides other users' processes"
else warn "/proc shows every process to every user: ps reveals the cohort's command lines (voice note paths, photo hashes on ffmpeg's)"; fi

ru=$(systemctl show -p ExecStart ghost.secd 2>/dev/null | sed -n 's/.*--user \([a-z_][a-z0-9_-]*\).*/\1/p' | head -1)
if [ -n "$ru" ]; then
    others=$(ps -u "$ru" -o comm= 2>/dev/null | grep -vE '^(ghost\.|postgres|redis-server|redis-check|llama-server|whisper-cli|ffmpeg|dwebp|cwebp|psql|pg_ctl|sh|bash|sleep)' | sort | uniq -c | sort -rn | head -5)
    if [ -n "$others" ]; then
        warn "the cohort runs as '$ru', and so do other programs: $(echo "$others" | awk '{print $2"("$1")"}' | tr '\n' ' ') , each can read the decrypted volume through /proc/<cohort pid>/root"
    else pass "only the cohort runs as '$ru'"; fi
    ru_shell=$(getent passwd "$ru" | cut -d: -f7); ru_uid=$(id -u "$ru" 2>/dev/null || echo 0)
    case "$ru_shell" in
        */nologin|*/false) login=0 ;;
        *) login=1 ;;
    esac
    if [ "$login" = 1 ] || [ "$ru_uid" -ge 1000 ]; then
        warn "the cohort runs as '$ru', an account people log in as: anything run as '$ru' reads the vault through /proc/<cohort pid>/root (sudo ./tools/own_user.sh gives the daemons a user of their own)"
        pg=$(pgrep -u "$ru" -x postgres | head -1)
        if [ -n "$pg" ] && su -s /bin/sh "$ru" -c "test -r /proc/$pg/root$MOUNT" 2>/dev/null; then
            warn "as '$ru', /proc/$pg/root$MOUNT is readable right now"
        fi
    else pass "the cohort has a user of its own ('$ru', no login, no shell)"; fi
    # and the volume is that user's: what the old user owned was handed over at the first unlock
    sp=$(pidof ghost.secd 2>/dev/null || true); sp="${sp%% *}"
    if [ -n "$sp" ] && [ -d "/proc/$sp/root$MOUNT/run" ]; then
        vo=$(stat -c %U "/proc/$sp/root$MOUNT" 2>/dev/null || true)
        if [ -n "$vo" ] && [ "$vo" != "$ru" ] && [ "$vo" != root ]; then
            warn "the volume still belongs to '$vo', not '$ru': lock and unlock once to hand it over"
        fi
    fi
else info "no ghost.secd unit (or no --user): cohort user checks skipped"; fi

echo "== loopback"
ss -ltnH 2>/dev/null | awk '{print $4}' | grep -E '^127\.0\.0\.1:' | sort -u | while read -r a; do
    p=${a##*:}
    who=$(ss -ltnpH "sport = :$p" 2>/dev/null | sed -n 's/.*users:(("\([^"]*\)".*/\1/p' | head -1)
    case "$who" in ghost.*|postgres|redis-server|llama-server) info "127.0.0.1:$p ($who) answers any local process";; esac
done
if ss -ltnH 2>/dev/null | grep -q '127.0.0.1:8443 '; then
    if [ "$(cat /etc/ghost/edge 2>/dev/null)" = tls ] && [ -f /etc/nginx/localghost-stream.conf ]; then
        pass "ghost.secd terminates the phone's TLS itself (edge=tls, nginx forwards the raw stream by name): a plain-HTTP client on :8443 gets the down page, not the door"
    else
        warn "ghost.secd still trusts the X-Client-Cert header on plain 127.0.0.1:8443: any local process that forges the header reaches the PIN door without the phone's certificate. Move it with: sudo ghost-ctl edge-passthrough --domain <box name> (secd does its own TLS, nginx forwards the raw stream by name, and the edge flag is written for you)"
    fi
fi
for p in 18080 18081; do
    if out=$(curl -s -m 2 "http://127.0.0.1:$p/slots" 2>/dev/null) && echo "$out" | grep -q '"id"'; then
        echo "$out" | grep -q '"prompt"' && warn "llama-server on :$p serves /slots WITH prompts to any local process (--no-slots)" \
            || warn "llama-server on :$p serves /slots to any local process (--no-slots)"
    fi
done
if ps -eo args 2>/dev/null | grep -v grep | grep -q 'redis-server.*--requirepass'; then
    warn "Redis's password is on its command line (ps shows it to every user)"
else pass "no Redis password on a command line"; fi
if grep -rqs 'systemctl.*ghost\.\*' /etc/sudoers.d/ 2>/dev/null; then
    warn "a sudoers rule grants systemctl on 'ghost.*' (a wildcard that also matches extra arguments like -H/-M): re-run server_setup_root.sh to replace it with the exact ghost.secd unit"
elif grep -rqs 'ghost\.secd' /etc/sudoers.d/ 2>/dev/null; then
    pass "the service user's systemctl sudo is the exact ghost.secd unit, no wildcard"
fi

echo "== what the network sees"
dom=$(grep -h server_name /etc/nginx/sites-enabled/* 2>/dev/null | grep -i localghost | head -1 | awk '{print $2}' | tr -d ';')
[ -n "$dom" ] && warn "the box answers as '$dom': the name travels in DNS lookups and in clear in TLS (SNI), and says LocalGhost" || info "no localghost name in nginx's sites"
if command -v nft >/dev/null 2>&1 && nft list ruleset 2>/dev/null | grep -q "skuid"; then info "nftables has per-user rules (check the cohort's egress is dropped)"
else info "no per-user egress rule: 'never reaches the internet' rests on the code alone (an nftables 'meta skuid <cohort> oifname != lo drop' makes the kernel hold it)"; fi

echo
echo "$warns warning(s)"
exit "$warns"
