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

# EVERY LINE CARRIES THE CLOCK. Stage banners with "+Ns" said how long; they did not say WHEN,
# and when is what gets lined up against watchd's log, oracled's log and journalctl when a halt
# takes 46 seconds. So everything this script prints , its own messages, make's output,
# systemctl's status, the halt watch , goes through one filter that prefixes HH:MM:SS (bash's
# own printf %T, no process per line); the full date is printed once at the top. The PIN prompt
# is the one thing written to the terminal directly, so line buffering cannot hold it back.
_stamp() { while IFS= read -r _l; do printf '%(%H:%M:%S)T %s\n' -1 "$_l"; done; }
exec > >(_stamp) 2>&1
_stamp_pid=$!
# Let the filter drain before the shell goes: the closing banner is the line people read.
_drain() { exec 1>&- 2>&-; wait "$_stamp_pid" 2>/dev/null || true; }
trap _drain EXIT
_t0=$(date +%s)
say() { printf '\n=== %s ===  (+%ss)\n' "$1" "$(( $(date +%s) - _t0 ))"; }
printf 'redeploy started %s on %s (nginx-only=%s no-build=%s)\n' "$(date '+%Y-%m-%d %H:%M:%S %Z')" "$(hostname)" "$NGINX_ONLY" "$NO_BUILD"

if [ "$(id -u)" -ne 0 ]; then
    echo "run as root (sudo): it restarts a system service and writes $SYSTEM_BIN"
    exit 1
fi

# TOOLS HYGIENE. A drop that passed through a Windows machine arrives with CRLF line endings and
# no execute bit: the shebang then reads "bash\r", env finds no such interpreter, and sudo says
# "command not found" about a file that is right there. Strip the CR from every script here and
# make them executable, so the one script that always runs repairs the others.
for f in "$REPO"/tools/*.sh; do
    if grep -q $'\r' "$f" 2>/dev/null; then
        sed -i 's/\r$//' "$f"
        echo "tools: stripped CRLF from $(basename "$f")"
    fi
    [ -x "$f" ] || { chmod +x "$f"; echo "tools: made $(basename "$f") executable"; }
done

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
    printf "main PIN for graceful halt (Enter to skip): " > /dev/tty
    read -rs GHOST_PIN
    echo > /dev/tty
fi
if [ -n "${GHOST_PIN:-}" ] && systemctl is-active --quiet ghost.secd; then
    echo "graceful halt before the binary swap"
    "$REPO/bin/ghost-cli" --run-dir=/var/lib/ghost/run ghost.secd halt "pin=$GHOST_PIN" || true
    echo "halt sent to secd; watching the volume's processes"
    # halt replies ok unconditionally (PIN-opaque); confirm by watching the volume's services die,
    # and SAY WHICH ONES are slow: each one is named the second it goes, and a survivor is shown
    # with its pid, parent, state and kernel wait channel every five seconds , the difference
    # between a process that is ignoring SIGTERM (state S, parent 1: an orphan nobody signals) and
    # one the kernel is still tearing down (state D or Z, wchan in exit_mmap or the GPU driver:
    # already dead, releasing memory), which is the llama-server question.
    _halt0=$(date +%s)
    _seen=""
    _left=""
    _killed=""
    _unkillable=""
    for i in $(seq 1 45); do
        _alive=$(pgrep -fa '/var/lib/ghost/mnt/.*/bin/' 2>/dev/null | sed -E 's|.*/bin/([^ ]+).*|\1|' | sort -u | tr '\n' ' ')
        if [ -z "$_alive" ]; then _left=""; break; fi
        _left="$_alive"
        # anything seen before that is no longer alive: say so now, with the second
        for n in $(echo "$_seen" | tr ' ' '\n' | sort -u); do
            [ -z "$n" ] && continue
            case " $_alive " in *" $n "*) ;; *) echo "  gone after $((i-1))s: $n"; _seen=$(echo " $_seen " | sed "s/ $n / /g") ;; esac
        done
        for n in $_alive; do case " $_seen " in *" $n "*) ;; *) _seen="$_seen $n" ;; esac; done
        if [ $((i % 5)) -eq 0 ]; then
            echo "  still up after ${i}s: $_alive"
            for pid in $(pgrep -f '/var/lib/ghost/mnt/.*/bin/' 2>/dev/null); do
                ps -o pid=,ppid=,stat=,wchan:28=,etimes=,comm= -p "$pid" 2>/dev/null | sed 's/^/      pid ppid stat wchan up comm: /'
            done
        fi
        # AN ORPHAN IS NOT A TEARDOWN. Ten seconds in, a survivor whose parent is init and whose
        # state is R or S is alive and ignoring the halt , the llama-server seen at 60 days old,
        # parent 1, state R , and nothing else will ever stop it: not the cohort (already down),
        # not this script's patience, and systemd only after its three-minute timeout, which is
        # what "systemctl restart took three minutes" was. The new build's lock path kills such
        # strays itself; this is the same act for the halt that is already under way.
        if [ "$i" -ge 10 ]; then
            for pid in $(pgrep -f '/var/lib/ghost/mnt/.*/bin/' 2>/dev/null); do
                set -- $(ps -o ppid=,stat=,etimes=,comm= -p "$pid" 2>/dev/null)
                [ "${1:-}" = "1" ] || continue
                case "${2:-}" in R*|S*) ;; *) continue ;; esac
                case " $_killed " in *" $pid "*)
                    # Already SIGKILLed and still here: SIGKILL cannot be ignored, only outrun by a
                    # process that never returns from the kernel. Once, with the evidence, then quiet.
                    case " $_unkillable " in *" $pid "*) ;; *)
                        _unkillable="$_unkillable $pid"
                        echo "  UNKILLABLE: ${4:-?} pid $pid survived SIGKILL , it is stuck inside the kernel (state ${2}); no signal, no systemd timeout and no patience ends it."
                        echo "      pending signals: $(grep -E '^(ShdPnd|SigPnd)' /proc/$pid/status 2>/dev/null | tr '\n' ' ')"
                        echo "      kernel stack:    $(head -4 /proc/$pid/stack 2>/dev/null | tr '\n' ' ' | cut -c1-200) (empty = it is ON a cpu right now, spinning)"
                        echo "      GPU faults:      $(dmesg 2>/dev/null | grep -c 'NVRM: Xid') Xid line(s); first: $(dmesg 2>/dev/null | grep 'NVRM: Xid' | head -1 | cut -c1-160)"
                        echo "      what now:        sudo ./tools/unwedge.sh  (who, where, since when; --reset for the levers; else a cold reboot)"
                        ;;
                    esac
                    continue ;;
                esac
                echo "  orphan: ${4:-?} pid $pid (parent 1, state ${2}, up ${3:-?}s) ignores the halt , killing it"
                kill -TERM "$pid" 2>/dev/null || true; sleep 2
                if kill -0 "$pid" 2>/dev/null; then kill -KILL "$pid" 2>/dev/null || true; fi
                _killed="$_killed $pid"
            done
        fi
        sleep 1
    done
    for n in $_seen; do [ -n "$n" ] && case " $_left " in *" $n "*) ;; *) echo "  gone after $((i-1))s: $n" ;; esac; done
    echo "cohort down after $(( $(date +%s) - _halt0 ))s"
    if [ -n "$_unkillable" ]; then
        echo "an unkillable process holds the volume and the service's cgroup: the restart below will wait out"
        echo "systemd's stop timeout (minutes), then start the new secd beside it. The volume cannot be fully"
        echo "locked, and the GPU it holds stays held, until the driver gives it back or the box reboots:"
        echo "finish this redeploy, then  sudo ./tools/unwedge.sh  , it says which. A card that fell off the"
        echo "bus (Xid 79) wants a COLD reboot (poweroff, 30s, on), and the app unlocks the volume after."
        echo "The new build kills strays before they can grow old."
    elif [ -n "$_left" ]; then
        echo "still stopping after 45s: $_left , hard restart; interrupted work heals on the next stock-take"
        echo "      (per-daemon stop timings: watchd's log 'service stopped' / 'cohort down'; llama-server's: oracled's log 'llama-server stop')"
    else
        echo "halted cleanly , cohort down, redis saved, postgres checkpointed."
    fi
elif [ "${VOLUME_LOCKED:-0}" = "0" ]; then
    echo "NOTE: no GHOST_PIN in the environment , falling back to systemctl restart, which races"
    echo "      secd's SIGTERM lock against systemd's kill timeout. For a guaranteed-clean teardown:"
    echo "        sudo GHOST_PIN=<main pin> ./tools/redeploy.sh"
fi
echo "systemctl restart ghost.secd"
systemctl restart ghost.secd
echo "restart returned"
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
