#!/usr/bin/env bash
# ns.sh , run a command inside ghost.secd's mount namespace, where the decrypted volume is visible.
#
# The volume being invisible to the host mount table is DELIBERATE (privacy by architecture): other
# host processes cannot casually see that anything is mounted. Root enters on purpose, through here.
#
#   sudo ./tools/ns.sh ls /var/lib/ghost/mnt/slot0/
#   sudo ./tools/ns.sh ./bin/ghost-cli ghost.oracled status
#   sudo ./tools/ns.sh cat /var/lib/ghost/mnt/slot0/logs/watchd-2026-07-10.log
set -eu
PID="$(pidof ghost.secd || true)"
if [ -z "$PID" ]; then
    echo "ghost.secd is not running , no namespace to enter" >&2
    exit 1
fi
# /home is EMPTY inside secd's namespace (ProtectHome=yes , deliberate). A /home path in the command,
# or a relative path resolved under a /home working directory, will "not exist" once inside , the
# single most repeated trap of using this tool. Warn loudly; hop files through /tmp (shared).
for arg in "$@"; do
    case "$arg" in /home/*)
        echo "!! $arg is under /home , EMPTY inside the namespace (ProtectHome). Copy via /tmp first." >&2
    esac
done
case "$(pwd)" in /home/*)
    echo "-- note: cwd is under /home; relative paths will not resolve inside the namespace" >&2
esac
exec nsenter -t "$PID" -m "$@"
