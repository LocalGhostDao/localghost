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
CLI="${GHOST_CLI:-./bin/ghost-cli}"
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
# So if the run dir is not visible here but secd is running, re-exec this script INSIDE secd's
# namespace. Root can always enter deliberately; nothing enters casually.
#
# The re-exec must go THROUGH /tmp: /home is EMPTY inside the namespace (ProtectHome), and both this
# script and ./bin/ghost-cli normally live under the repo there , nsenter with a /home path fails
# "No such file or directory", the exact trap the setup notes warn about for ns.sh. /tmp is shared,
# so stage the script and the CLI there, point GHOST_CLI at the staged copy, and clean up on exit.
if [ -n "${GHOST_STAGE_DIR:-}" ]; then
    trap 'rm -rf "$GHOST_STAGE_DIR"' EXIT
fi
if [ ! -d "$RUN_DIR" ]; then
    SECD_PID="$(pidof ghost.secd || true)"
    if [ -n "$SECD_PID" ] && [ -z "${GHOST_NS_ENTERED:-}" ]; then
        STAGE="$(mktemp -d /tmp/ghost-health.XXXXXX)"
        cp "$0" "$STAGE/health.sh" && chmod 0755 "$STAGE/health.sh"
        if [ -x "$CLI" ]; then
            cp "$CLI" "$STAGE/ghost-cli" && chmod 0755 "$STAGE/ghost-cli"
        fi
        exec env GHOST_NS_ENTERED=1 GHOST_STAGE_DIR="$STAGE" GHOST_CLI="$STAGE/ghost-cli" \
            nsenter -t "$SECD_PID" -m "$STAGE/health.sh" "$@"
    fi
    echo "run dir $RUN_DIR not present , is the box unlocked? (secd mounts the volume on unlock,"
    echo "inside its own mount namespace; this script auto-enters it when secd is running)"
    exit 1
fi

# Discover any extra sockets not in the roster (hand-added daemons), so nothing is missed.
EXTRA=""
for sock in "$RUN_DIR"/*.sock; do
    [ -e "$sock" ] || continue
    name="$(basename "$sock" .sock)"
    case " $ROSTER ghost.secd " in
        *" $name "*) : ;;
        *) EXTRA="$EXTRA $name" ;;
    esac
done

CHECK="$ROSTER$EXTRA"
[ -n "$ONLY" ] && CHECK="$ONLY"

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
    printf '%s  %d/%d supervised daemons up\n' "$(green ALL UP)" "$total" "$total"
    exit 0
else
    printf '%s  %d of %d supervised daemons down , see the DOWN/STALE lines above\n' "$(red DEGRADED)" "$down" "$total"
    exit 1
fi
