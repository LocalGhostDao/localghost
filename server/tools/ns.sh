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
exec nsenter -t "$PID" -m "$@"
