#!/usr/bin/env bash
# ns.sh , the operator's door into the unlocked volume.
#
# The volume is mounted inside ghost.secd's PRIVATE MOUNT NAMESPACE, where the decrypted drive is
# visible and the host mount table is not. That is deliberate (privacy by architecture): other host
# processes cannot casually see that anything is mounted. Root enters on purpose, through here.
#
#   sudo ./tools/ns.sh                                        # a SHELL inside the namespace, cwd = the volume
#   sudo ./tools/ns.sh ls /var/lib/ghost/mnt/slot0/            # one command inside the namespace
#   sudo ./tools/ns.sh tail -f /var/lib/ghost/mnt/slot0/logs/ghost.framed-2026-09-18.log
#   sudo ./tools/ns.sh ./bin/ghost-cli ghost.framed reprocess force=true   # a REPO binary against the volume
#
# Two doors, picked per command:
#   * A command given as a PATH (it has a slash: ./bin/ghost-cli, /home/...) runs HERE, in the host
#     namespace, where /home and the repo exist. Every volume path in its arguments is rewritten to
#     /proc/<secd>/root/<path> , the kernel's own link into secd's mount namespace, through which
#     files open and unix sockets connect like any other path , and GHOST_MOUNT / GHOST_RUN_DIR /
#     GHOST_LOG_DIR point there too. No nsenter, no copy, no install: the binary you just built.
#   * A BARE command (ls, cat, tail, bash) runs INSIDE the namespace via nsenter, where the volume
#     paths are their natural selves. /home is empty in there (ProtectHome) , that is the one thing
#     this door cannot show you, and it is why the first door exists.
set -eu
if [ "$(id -u)" -ne 0 ]; then
    echo "run as root (sudo): entering another process's mount namespace needs it" >&2
    exit 1
fi
PID="$(pidof ghost.secd || true)"
PID="${PID%% *}"
if [ -z "$PID" ]; then
    echo "ghost.secd is not running , no namespace to enter" >&2
    exit 1
fi
MOUNT="${GHOST_MOUNT:-/var/lib/ghost/mnt/slot0}"
DOOR="/proc/$PID/root"
WD="$MOUNT"
if [ ! -d "$DOOR$MOUNT/run" ]; then
    echo "-- note: $MOUNT has no run dir inside secd's namespace , the box looks LOCKED; entering anyway" >&2
    WD="/"
fi

# No command: a shell inside, standing on the volume, prompt marked so you always know which side
# of the door you are on. --norc so a host dotfile cannot cd you somewhere that does not exist here.
if [ $# -eq 0 ]; then
    exec nsenter -t "$PID" -m --wd="$WD" env GHOST_NS=1 GHOST_MOUNT="$MOUNT" \
        PS1='[ghost-ns] \w \$ ' bash --norc --noprofile
fi

cmd="$1"; shift
case "$cmd" in
    */*)
        if [ ! -e "$cmd" ]; then
            echo "$cmd: not found on the host (a path runs host-side; a bare name runs inside)" >&2
            exit 127
        fi
        args=()
        for a in "$@"; do
            case "$a" in
                /var/lib/ghost/mnt/*) args+=("$DOOR$a") ;;
                *) args+=("$a") ;;
            esac
        done
        exec env GHOST_MOUNT="$DOOR$MOUNT" GHOST_RUN_DIR="$DOOR$MOUNT/run" GHOST_LOG_DIR="$DOOR$MOUNT/logs" \
            "$cmd" ${args[@]+"${args[@]}"}
        ;;
    *)
        for a in "$@"; do
            case "$a" in /home/*)
                echo "!! $a is under /home , EMPTY inside the namespace (ProtectHome). Give the command as a path (./tool) to run it host-side instead." >&2
            esac
        done
        exec nsenter -t "$PID" -m --wd="$WD" "$cmd" "$@"
        ;;
esac
