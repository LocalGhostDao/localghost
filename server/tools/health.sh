#!/usr/bin/env bash
# health.sh , one glance at whether the box is actually alive.
#
# For every daemon that has a control socket on the unlocked volume, this pings it, prints its status
# line, and tails its most recent log. It discovers services from the run dir rather than a baked-in
# list, so it always reflects what is really running (a daemon watchd has not started yet simply has no
# socket, and shows as DOWN). Run it on the box while UNLOCKED.
#
#   sudo ./tools/health.sh                 # all services, status + 5 log lines each
#   sudo ./tools/health.sh -n 20           # 20 log lines each
#   sudo ./tools/health.sh ghost.oracled   # just one service, more detail
#
# Exit status is non-zero if any expected daemon is down, so it is usable in a check.

set -u

MOUNT="${GHOST_MOUNT:-/var/lib/ghost/mnt/slot0}"
RUN_DIR="${GHOST_RUN_DIR:-$MOUNT/run}"
LOG_DIR="${GHOST_LOG_DIR:-$MOUNT/logs}"
# The repo's own build first (freshest), then the /opt copy redeploy installs, then PATH.
CLI="${GHOST_CLI:-./bin/ghost-cli}"
[ -x "$CLI" ] || CLI="/opt/localghost/bin/ghost-cli"
[ -x "$CLI" ] || CLI="$(command -v ghost-cli || echo ./bin/ghost-cli)"
LINES=5
ONLY=""

while [ $# -gt 0 ]; do
    case "$1" in
        -n) LINES="$2"; shift 2 ;;
        -n*) LINES="${1#-n}"; shift ;;
        ghost.*) ONLY="$1"; shift ;;
        *) echo "usage: $0 [-n LINES] [ghost.SERVICE]"; exit 2 ;;
    esac
done

# The canonical roster , the ten supervised daemons plus watchd. secd is checked separately (it lives
# on the UNENCRYPTED state dir, not the volume, because it runs before unlock). If a services.conf adds
# more, socket discovery below still catches them.
ROSTER="ghost.watchd ghost.oracled ghost.searchd ghost.framed ghost.noted ghost.cued ghost.synthd ghost.shadowd ghost.tallyd ghost.voiced"

green() { printf '\033[32m%s\033[0m' "$1"; }
red()   { printf '\033[31m%s\033[0m' "$1"; }
dim()   { printf '\033[2m%s\033[0m'  "$1"; }

# The volume is mounted inside ghost.secd's PRIVATE MOUNT NAMESPACE , a deliberate design choice: the
# host mount table never shows the decrypted volume, and other host processes cannot casually see it.
# From here (root), the way in is the kernel's own link: /proc/<secd>/root resolves into that
# namespace, files open through it and unix sockets connect through it. So when the run dir is not
# visible, every path this script touches is simply prefixed with that door , no nsenter, no
# re-exec, no copying the script or the CLI through /tmp. ghost-cli takes the same door on its own
# (internal/nsreach), so the plain `$CLI <svc> ping` calls below just work.
if [ ! -d "$RUN_DIR" ]; then
    SECD_PID="$(pidof ghost.secd || true)"
    SECD_PID="${SECD_PID%% *}"
    if [ -n "$SECD_PID" ] && [ -d "/proc/$SECD_PID/root$RUN_DIR" ]; then
        DOOR="/proc/$SECD_PID/root"
        MOUNT="$DOOR$MOUNT"; RUN_DIR="$DOOR$RUN_DIR"; LOG_DIR="$DOOR$LOG_DIR"
        export GHOST_RUN_DIR="$RUN_DIR" GHOST_LOG_DIR="$LOG_DIR"
        echo "(volume reached through ghost.secd's namespace: $DOOR)"
    else
        echo "run dir $RUN_DIR not present , is the box unlocked? (secd mounts the volume on unlock,"
        echo "inside its own mount namespace; run as root and this script reaches it through /proc)"
        exit 1
    fi
fi

# Discover any extra sockets not in the roster (hand-added daemons), so nothing is missed.
# *.stream sockets are EXCLUDED by name: they are streamsock endpoints (unix-socket HTTP for token
# streaming), not control sockets , they will never answer a ctlsock ping, so including them reads
# as two permanently-STALE daemons and poisons the DEGRADED count on a perfectly healthy box.
EXTRA=""
for sock in "$RUN_DIR"/*.sock; do
    [ -e "$sock" ] || continue
    name="$(basename "$sock" .sock)"
    case "$name" in
        *.stream) continue ;;
    esac
    case " $ROSTER ghost.secd " in
        *" $name "*) : ;;
        *) EXTRA="$EXTRA $name" ;;
    esac
done

CHECK="$ROSTER$EXTRA"
[ -n "$ONLY" ] && CHECK="$ONLY"

# The clock, once, at the top: a health readout pasted into a chat an hour later still says when
# it was true, and lines up with the daemon logs it tails (each of which carries its own time).
printf 'health as of %s on %s\n' "$(date '+%Y-%m-%d %H:%M:%S %Z')" "$(hostname)"

down=0
total=0
for svc in $CHECK; do
    total=$((total + 1))
    sock="$RUN_DIR/$svc.sock"
    printf '\n=== %s ===\n' "$svc"

    if [ ! -S "$sock" ]; then
        printf '  %s   (no control socket at %s)\n' "$(red DOWN)" "$sock"
        down=$((down + 1))
    else
        # ping first , cheapest liveness check; then status for the detail line.
        if "$CLI" "$svc" ping >/dev/null 2>&1; then
            printf '  %s   ' "$(green UP)"
            # status is best-effort: a daemon can be up (ping ok) but mid-init; show whatever it gives.
            "$CLI" "$svc" status 2>/dev/null | head -1 || echo "(no status line)"
            if [ "$svc" = "ghost.oracled" ]; then
                # The GPU question, from oracled itself (tools/gpu.sh has the whole picture).
                m=$("$CLI" ghost.oracled models 2>/dev/null)
                v=$(echo "$m" | sed -n 's/.*"verdict":"\([^"]*\)".*/\1/p' | head -1)
                sp=$(echo "$m" | sed -n 's/.*"speed":"\([^"]*\)".*/\1/p' | head -1)
                [ -n "$v" ] && printf '  model %s\n' "$v"
                [ -n "$sp" ] && printf '  %s\n' "$sp"
            fi
        else
            printf '  %s   (socket present but not answering ping , wedged or mid-restart)\n' "$(red STALE)"
            down=$((down + 1))
        fi
    fi

    # Most recent log lines for this service. Logs are <dir>/<name>-YYYY-MM-DD.log; today's is the one
    # without .gz. Fall back to the newest matching file if today's is absent.
    latest="$(ls -1t "$LOG_DIR/$svc-"*.log 2>/dev/null | head -1)"
    if [ -n "$latest" ]; then
        printf '  %s\n' "$(dim "last $LINES log lines ($(basename "$latest")):")"
        tail -n "$LINES" "$latest" 2>/dev/null | sed 's/^/    /'
    else
        printf '  %s\n' "$(dim "no log file yet in $LOG_DIR")"
    fi
done

# secd separately , its socket is on the unencrypted state dir, reachable even pre-unlock.
# secd resolves its own state-dir socket (ghost-cli special-cases it), so call it plainly , no flags.
printf '\n=== ghost.secd (state dir) ===\n'
if "$CLI" ghost.secd ping >/dev/null 2>&1; then
    printf '  %s   ' "$(green UP)"
    "$CLI" ghost.secd status 2>/dev/null | head -1 || echo ""
else
    printf '  %s   (secd is the root daemon , if this is down the box is locked or crashed)\n' "$(red DOWN)"
fi

printf '\n----------------------------------------\n'
if [ "$down" -eq 0 ]; then
    printf '%s  %d/%d supervised daemons up  (%s)\n' "$(green ALL UP)" "$total" "$total" "$(date +%H:%M:%S)"
    exit 0
else
    printf '%s  %d of %d supervised daemons down , see the DOWN/STALE lines above  (%s)\n' "$(red DEGRADED)" "$down" "$total" "$(date +%H:%M:%S)"
    exit 1
fi
