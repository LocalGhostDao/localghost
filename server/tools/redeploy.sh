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

say() { printf '\n=== %s ===\n' "$1"; }

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

say "3/4  reload nginx (harmless if unchanged)"
nginx -t >/dev/null 2>&1 && systemctl reload nginx && echo "nginx reloaded" || echo "nginx config unchanged or test skipped"

say "4/4  restart ghost.secd"
# On SIGTERM secd does a clean lock (cohort down, DBs stopped, volume unmounted, LUKS closed), then
# systemd starts the new binary. So after this the box is LOCKED , expected, not an error.
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