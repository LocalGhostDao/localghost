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
# checked by its signed manifest and by the pin; the upstream URL in the pin when the mirror has
# nothing), swaps it in beside the old one (the old file is kept as <name>.replaced until you delete
# it), and restarts ghost.oracled so llama-server loads the new file. A restart on the GPU is about
# ten seconds of no model; captions in flight are retried by searchd.
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
    if sh "$HERE/mirror_fetch.sh" models "$STAGE" "$n" && [ -s "$STAGE/$n" ]; then
        echo "-- $n from the mirror"
    else
        url="$(pin_url "$n")"
        echo "-- fetching $n from $url (resumable)"
        curl -fL --retry 3 --retry-delay 5 -C - --progress-bar -o "$STAGE/$n" "$url" || { echo "!! download failed: $url" >&2; exit 3; }
    fi
    pin_check "$STAGE/$n" || { rm -f "$STAGE/$n"; echo "!! the fetched $n is not the pinned file either , stopping" >&2; exit 3; }
    fetched="$fetched $n"
done
# swap: the old file stays as .replaced (llama-server has it mapped until the restart; the kernel
# keeps the pages until then), the new one takes the name, ownership as the rest of the volume
for n in $fetched; do
    [ -f "$DIR/$n" ] && mv -f "$DIR/$n" "$DIR/$n.replaced"
    mv -f "$STAGE/$n" "$DIR/$n"
    chown "$OWNER:$OWNER" "$DIR/$n" 2>/dev/null || true
    chmod 600 "$DIR/$n"
    echo "-- $n in place (the old file is $n.replaced)"
done
rmdir "$STAGE" 2>/dev/null || true
echo "> restarting ghost.oracled so llama-server loads the pinned files"
if "$HERE/../bin/ghost-ctl" restart-daemon ghost.oracled >/dev/null 2>&1 || /opt/localghost/bin/ghost-ctl restart-daemon ghost.oracled >/dev/null 2>&1; then
    echo "> done. Check: sudo ./tools/health.sh ghost.oracled ; then delete the .replaced files when happy:"
    echo "    sudo ./tools/ns.sh rm $DIR/*.replaced"
else
    echo "!! could not ask watchd to restart ghost.oracled , do it by hand: sudo ./tools/ns.sh ./bin/ghost-ctl restart-daemon ghost.oracled" >&2
    exit 4
fi
