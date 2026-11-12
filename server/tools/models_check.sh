#!/bin/sh
# models_check.sh [--fix] , are the weights on this box the pinned ones (tools/model.pins)?
#
#   sudo ./tools/ns.sh ./tools/models_check.sh          # say which files match, which do not
#   sudo ./tools/ns.sh ./tools/models_check.sh --fix    # replace what does not match from the mirror
#
# Run through tools/ns.sh while the box is UNLOCKED: the weights live on the encrypted volume, in
# ghost.secd's mount namespace, and ns.sh hands this script the door to it (GHOST_MOUNT).
#
# Why: a model file can be the right size with the wrong bytes , Unsloth's mmproj-F16.gguf was
# re-uploaded under the same name after their F32 patch_embd fix, and a box that fetched the earlier
# one runs a slightly wrong projector for every caption. --fix downloads the pinned file (the mirror,
# checked by its signed manifest and by the pin), swaps it in beside the old one (the old file is kept as <name>.replaced until you delete
# it), and restarts ghost.oracled so llama-server loads the new file. A restart on the GPU is about
# ten seconds of no model; captions in flight are retried by searchd. The mirror only: a file it
# does not list is said and left (GHOST_MIRROR_UPSTREAM=1: the pin's upstream, checked by the pin).
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/model_pins.sh"
MOUNT="${GHOST_MOUNT:-/var/lib/ghost/mnt/slot0}"
DIR="$MOUNT/ai-models"
FIX=0
[ "${1:-}" = "--fix" ] && FIX=1
[ -d "$DIR" ] || { echo "!! $DIR is not there , is the box unlocked, and is this run through tools/ns.sh?" >&2; exit 2; }
# the volume owner (files are chowned to whoever owns ai-models/)
OWNER="$(stat -c %U "$DIR" 2>/dev/null || echo coder)"

echo "> weights in $DIR against tools/model.pins"
bad=""
for n in $(pin_names); do
    if [ ! -f "$DIR/$n" ]; then
        echo "!! $n: missing"
        bad="$bad $n"
    elif ! pin_check "$DIR/$n"; then
        bad="$bad $n"
    fi
done
for f in "$DIR"/*.gguf; do
    [ -f "$f" ] || continue
    n="$(basename "$f")"
    [ -n "$(pin_sha "$n")" ] || echo "   $n: unpinned ($(stat -c%s "$f" | awk '{printf "%.2f GB", $1/1073741824}'))"
done
if [ -z "$bad" ]; then
    echo "> all pinned weights match"
    exit 0
fi
if [ "$FIX" = 0 ]; then
    echo "> not the pinned build:$bad , run again with --fix to replace from the mirror"
    exit 1
fi

echo "> replacing:$bad"
STAGE="$DIR/.pins-fetch"
mkdir -p "$STAGE"
fetched=""
for n in $bad; do
    rc=0; sh "$HERE/mirror_fetch.sh" models "$STAGE" "$n" || rc=$?
    if [ "$rc" = 0 ] && [ -s "$STAGE/$n" ]; then
        echo "-- $n from the mirror"
    elif [ "${GHOST_MIRROR_UPSTREAM:-}" = 1 ]; then
        url="$(pin_url "$n")"
        echo "-- !! GHOST_MIRROR_UPSTREAM=1: fetching $n from $url (resumable) , checked by the pin only, NOT a signed manifest"
        curl -fL --proto-redir =https --retry 3 --retry-delay 5 -C - --progress-bar -o "$STAGE/$n" "$url" || { echo "!! download failed: $url" >&2; exit 3; }
    else
        [ "$rc" = 3 ] && echo "!! $n is not on the mirror (set models; not published in this build) , left as it is" >&2 ||
            echo "!! could not take $n from the mirror (above; a rerun resumes) , left as it is" >&2
        exit 3
    fi
    pin_check "$STAGE/$n" || { rm -f "$STAGE/$n"; echo "!! the fetched $n is not the pinned file either , stopping" >&2; exit 3; }
    fetched="$fetched $n"
done
# swap: the old file stays as .replaced (llama-server has it mapped until the restart; the kernel
# keeps the pages until then), the new one takes the name, ownership as the rest of the volume
# ROOT'S HANDS IN THE VOLUME (second audit, 12 Nov 2026). The volume is the daemons' user's from
# its root down and this script is root (update.sh runs it, for the operator or for ghost.secd when
# the phone asks). A daemon that had been taken over (they read photos, video and mail from
# outside) can put a link where any name in there stands, and whatever root then does by that
# name lands where the link points. Two of the old lines turned that into root: `chown` of a
# name (it re-owns what a link points at) and `cp -f old old.prev` (the daemon's own bytes,
# written wherever old.prev pointed). So nothing here follows a link: a file is renamed into
# place (mv -T: a link to a directory is replaced, never entered), installed fresh (install -T
# unlinks what stood there first), given a second name without a copy (ln -T), owned through
# the name itself (chown -h), and has its mode set only when it is a plain file.
vol_own()  { for _f in "$@"; do if [ -e "$_f" ] || [ -L "$_f" ]; then chown -h "$OWNER:$OWNER" "$_f" 2>/dev/null || true; fi; done; }
vol_mode() { _m="$1"; shift; for _f in "$@"; do if [ -f "$_f" ] && [ ! -L "$_f" ]; then chmod "$_m" "$_f"; fi; done; }
for n in $fetched; do
    [ -f "$DIR/$n" ] && mv -fT "$DIR/$n" "$DIR/$n.replaced"
    mv -fT "$STAGE/$n" "$DIR/$n"
    vol_own "$DIR/$n"
    vol_mode 600 "$DIR/$n"
    echo "-- $n in place (the old file is $n.replaced)"
done
# the set's notice and terms go beside the weights, as setup left them
[ -f "$STAGE/NOTICE.txt" ] && mv -fT "$STAGE/NOTICE.txt" "$DIR/NOTICE-models.txt"
for f in "$STAGE"/TERMS-*.txt; do [ -f "$f" ] && mv -fT "$f" "$DIR/$(basename "$f")"; done
rm -rf "$STAGE"
echo "> restarting ghost.oracled so llama-server loads the pinned files"
if "$HERE/../bin/ghost-ctl" restart-daemon ghost.oracled >/dev/null 2>&1 || /opt/localghost/bin/ghost-ctl restart-daemon ghost.oracled >/dev/null 2>&1; then
    # the paths as the namespace sees them (through ns.sh this script sees /proc/<pid>/root/...), and
    # each file by name: a glob would be expanded by the host shell, where the volume is not mounted
    nat="${DIR#/proc/*/root}"
    echo "> done. Check: sudo ./tools/health.sh ghost.oracled ; then delete the old file(s) when happy:"
    for n in $fetched; do echo "    sudo ./tools/ns.sh rm $nat/$n.replaced"; done
else
    echo "!! could not ask watchd to restart ghost.oracled , do it by hand: sudo ./tools/ns.sh ./bin/ghost-ctl restart-daemon ghost.oracled" >&2
    exit 4
fi
