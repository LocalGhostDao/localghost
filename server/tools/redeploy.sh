#!/usr/bin/env bash
# redeploy.sh , rebuild and restart the LocalGhost server after a code change, in one command.
#
# What it does, in order:
#   1. make box                  , rebuild every binary into ./bin
#   2. stage ghost.secd          , atomic-replace the systemd-launched binary in /opt/localghost/bin
#      (the cohort daemons live ON the encrypted volume and are respawned by watchd on the next
#       unlock, so they pick up the new build automatically , no separate step for them)
#   3. reload nginx              , in case the site config changed (harmless if it did not)
#   4. systemctl restart ghost.secd
#   5. print next-step: the restart LOCKED the box, so re-unlock from the app, then run health.sh
#
# It deliberately does NOT try to unlock (that needs the PIN from the app) and does NOT touch the
# volume's DB runtime (that is bundle_db_runtime.sh, a separate deliberate act).
#
#   sudo ./tools/redeploy.sh              # full server redeploy
#   sudo ./tools/redeploy.sh --nginx-only # just re-render + reload nginx, no secd restart
#   sudo ./tools/redeploy.sh --no-build   # skip make box (binaries already built)

set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
SVC_USER="${GHOST_USER:-coder}"
SYSTEM_BIN="/opt/localghost/bin"
NGINX_ONLY=0
NO_BUILD=0

while [ $# -gt 0 ]; do
    case "$1" in
        --nginx-only) NGINX_ONLY=1; shift ;;
        --no-build) NO_BUILD=1; shift ;;
        *) echo "usage: $0 [--nginx-only] [--no-build]"; exit 2 ;;
    esac
done

# STAGE TIMING , "it takes minutes and I have no idea why" is a measurement problem, so every
# banner carries seconds-since-start. One helper, every stage, no new call sites.
_t0=$(date +%s)
say() { printf '\n=== %s ===  (+%ss)\n' "$1" "$(( $(date +%s) - _t0 ))"; }

if [ "$(id -u)" -ne 0 ]; then
    echo "run as root (sudo): it restarts a system service and writes $SYSTEM_BIN"
    exit 1
fi

# nginx-only fast path , config change, no binary, no restart, no re-unlock.
if [ "$NGINX_ONLY" = 1 ]; then
    say "nginx config only"
    su - "$SVC_USER" -c "cd '$REPO' && ./bin/ghost-qr --ca /etc/ghost/ca --host \"\$(sed -n 's/^GHOST_HOST=//p' /etc/ghost/ghost.env | cut -d: -f1)\" --nginx-out /tmp/ghost-secd.conf"
    cp /tmp/ghost-secd.conf /etc/nginx/sites-enabled/ghost-secd
    nginx -t && systemctl reload nginx
    echo "nginx reloaded , no secd restart, box stays in whatever state it was."
    exit 0
fi

if [ "$NO_BUILD" = 0 ]; then
    say "1/4  build (as $SVC_USER)"
    # build as the service user through a login shell so Go is on PATH (system Go at /usr/local/go).
    su - "$SVC_USER" -c "cd '$REPO' && make box"
fi

say "2/4  stage ghost.secd (atomic replace)"
install -d -m755 "$SYSTEM_BIN"
# .new + rename so replacing the RUNNING binary never hits ETXTBSY; the old inode keeps executing
# until the restart below swaps to the new one.
install -m755 "$REPO/bin/ghost.secd" "$SYSTEM_BIN/ghost.secd.new"
mv "$SYSTEM_BIN/ghost.secd.new" "$SYSTEM_BIN/ghost.secd"
echo "staged $(sha256sum "$SYSTEM_BIN/ghost.secd" | cut -c1-12) -> $SYSTEM_BIN/ghost.secd"
# THE OPERATOR TOOLS GO TO /opt TOO. The cohort's control sockets live on the volume, inside secd's
# mount namespace, and /home is EMPTY in there (ProtectHome) , so a ghost-cli that lives only under
# the repo could never reach ghost.framed without a hand-copy through /tmp first. /opt is visible
# on both sides of the namespace: `sudo ./tools/ns.sh ghost-cli ghost.framed reprocess` just works.
for tool in ghost-cli ghost-ctl; do
    [ -e "$REPO/bin/$tool" ] || continue
    install -m755 "$REPO/bin/$tool" "$SYSTEM_BIN/$tool.new"
    mv "$SYSTEM_BIN/$tool.new" "$SYSTEM_BIN/$tool"
done
echo "staged ghost-cli + ghost-ctl -> $SYSTEM_BIN (reachable inside the namespace, no /tmp copy)"

say "2b   stage the COHORT for the volume (ingested at next unlock)"
# The ghost.*d daemons (and llama-server) live on the ENCRYPTED VOLUME and are seeded there at
# provision , but a redeploy used to update only secd, leaving the volume's cohort permanently at
# provision-day builds: new fixes compiled, staged nowhere, and silently never ran. Stage every
# volume binary here; secd ingests staging/bin -> <mount>/bin during unlock, BEFORE the cohort
# spawns, so there is no running-binary replacement problem at all.
install -d -m700 /var/lib/ghost/staging/bin
STAGED=0
for f in "$REPO"/bin/ghost.* "$REPO"/bin/llama-server; do
    [ -e "$f" ] || continue
    base="$(basename "$f")"
    [ "$base" = "ghost.secd" ] && continue   # secd lives in /opt, staged above, not on the volume
    install -m755 "$f" "/var/lib/ghost/staging/bin/$base"
    STAGED=$((STAGED + 1))
done
echo "staged $STAGED volume binaries -> ingested at next unlock"

say "3/4  re-render + reload nginx"
# RE-RENDER from the current template, then reload , a plain reload of a STALE config was the bug
# that let a new client_max_body_size (needed for photo/video uploads) never reach disk while every
# other part of the redeploy succeeded. Rendering here means a full redeploy can never leave the edge
# running an old config again.
su - "$SVC_USER" -c "cd '$REPO' && ./bin/ghost-qr --ca /etc/ghost/ca --host \"\$(sed -n 's/^GHOST_HOST=//p' /etc/ghost/ghost.env | cut -d: -f1)\" --nginx-out /tmp/ghost-secd.conf"
cp /tmp/ghost-secd.conf /etc/nginx/sites-enabled/ghost-secd
if nginx -t >/dev/null 2>&1; then
    systemctl reload nginx
    echo "nginx re-rendered and reloaded"
else
    echo "WARNING: nginx -t failed on the new config; NOT reloading. Check /etc/nginx/sites-enabled/ghost-secd"
    nginx -t
fi

say "4/4  restart ghost.secd"
# GRACEFUL PATH (preferred): with GHOST_PIN set and the box up, ask secd to HALT first , the same
# ordered teardown a lock does minus the unmount: cohort SIGTERMed with time to finish in-flight
# items, redis SHUTDOWN SAVE (RDB written to the volume), pg_ctl stop with a clean checkpoint. Only
# THEN does systemd swap the binary. Without a PIN this falls back to systemctl restart, where
# secd's SIGTERM handler races systemd's kill timeout , the race that produced mounted-but-dead;
# the convergent unlock repairs that state now, but "repairable" is not "good", so say so loudly.
# PROMPT for the PIN rather than taking it from the environment or command line , env vars leak
# into shell history, ps output, and sudo logs; a silent read leaks nowhere. Enter to skip: the
# deploy then falls back to a plain restart, and you unlock from the app afterwards either way.
# LOCKED-VOLUME SHORT-CIRCUIT , when no mount exists there is nothing to halt: no cohort, no
# postgres, no PIN worth typing, and every wait below would be a wait for things that do not
# exist. Detect once, skip the whole ceremony, say so.
# NAMESPACE LESSON , the volume is mounted inside a MOUNT NAMESPACE, so from root's default
# view /var/lib/ghost/mnt/slot0 is empty whether the box is locked or not (this is why every
# psql call in this repo goes through tools/ns.sh). A path test therefore always said "locked"
# and skipped the graceful halt on a live box. Processes DO cross the boundary , the cohort
# running is the honest signal, and the script already trusts it below.
if ! pgrep -f '/var/lib/ghost/mnt/.*/bin/' >/dev/null 2>&1; then
    GHOST_PIN=""
    VOLUME_LOCKED=1
    echo "volume locked , nothing to halt, skipping straight to the binary swap"
fi
if [ -z "${GHOST_PIN:-}" ] && [ -t 0 ] && [ "$NGINX_ONLY" = "0" ] && [ "${VOLUME_LOCKED:-0}" = "0" ]; then
    printf "main PIN for graceful halt (Enter to skip): "
    read -rs GHOST_PIN
    echo
fi
if [ -n "${GHOST_PIN:-}" ] && systemctl is-active --quiet ghost.secd; then
    echo "graceful halt before the binary swap"
    "$REPO/bin/ghost-cli" --run-dir=/var/lib/ghost/run ghost.secd halt "pin=$GHOST_PIN" || true
    # halt replies ok unconditionally (PIN-opaque); confirm by watching the volume's services die,
    # and SAY WHICH ONES are slow: each survivor is named with the second it finally went, so a
    # halt that takes 20s points at one daemon instead of at "the cohort".
    _halt0=$(date +%s)
    _seen=""
    _gone=""
    for i in $(seq 1 45); do
        _alive=$(pgrep -fa '/var/lib/ghost/mnt/.*/bin/' 2>/dev/null | sed -E 's|.*/bin/([^ ]+).*|\1|' | sort -u | tr '\n' ' ')
        [ -z "$_alive" ] && break
        _seen="$_seen $_alive"
        # anything seen before that is no longer alive: record when it went
        for n in $(echo "$_seen" | tr ' ' '\n' | sort -u); do
            [ -z "$n" ] && continue
            case " $_alive " in *" $n "*) ;; *)
                case "$_gone" in *" $n="*) ;; *) _gone="$_gone $n=$((i-1))s" ;; esac ;;
            esac
        done
        if [ $((i % 5)) -eq 0 ]; then echo "  still up after ${i}s: $_alive"; fi
        sleep 1
    done
    # the last survivors went between the final two polls
    for n in $(echo "$_seen" | tr ' ' '\n' | sort -u); do
        [ -z "$n" ] && continue
        case "$_gone" in *" $n="*) ;; *) _gone="$_gone $n=$((i-1))s" ;; esac
    done
    echo "cohort down after $(( $(date +%s) - _halt0 ))s${_gone:+ , slowest:$_gone}"
    _left=$(pgrep -fa '/var/lib/ghost/mnt/.*/bin/' 2>/dev/null | sed -E 's|.*/bin/([^ ]+).*|\1|' | sort -u | tr '\n' ' ')
    if [ -n "$_left" ]; then
        echo "still stopping after 45s: $_left , hard restart; interrupted work heals on the next stock-take"
        echo "      (per-daemon stop timings are in watchd's log: 'service stopped' and 'cohort down' lines)"
    else
        echo "halted cleanly , cohort down, redis saved, postgres checkpointed."
    fi
elif [ "${VOLUME_LOCKED:-0}" = "0" ]; then
    echo "NOTE: no GHOST_PIN in the environment , falling back to systemctl restart, which races"
    echo "      secd's SIGTERM lock against systemd's kill timeout. For a guaranteed-clean teardown:"
    echo "        sudo GHOST_PIN=<main pin> ./tools/redeploy.sh"
fi
systemctl restart ghost.secd
sleep 1
systemctl --no-pager --lines=0 status ghost.secd 2>/dev/null | head -3 || true

cat <<EOF

----------------------------------------
Server redeployed. The box is now LOCKED (restart tears the stack down cleanly).

Next:
  1. Open the app and unlock with your main PIN , secd re-mounts the volume and
     ghost.watchd respawns the whole cohort from the NEW build on the volume.
  2. Verify everything came up:
       sudo ./tools/health.sh
     (run it AFTER unlocking , the cohort daemons only exist while unlocked.)

App side (separate, on your dev machine):
  cd app/android && ./gradlew installDebug
----------------------------------------
EOF
