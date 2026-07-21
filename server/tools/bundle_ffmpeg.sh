#!/bin/sh
# bundle_ffmpeg.sh , copy ffmpeg (and ffprobe) plus its shared-library closure onto the ENCRYPTED
# volume, same philosophy as bundle_db_runtime.sh: the OS disk should carry no media software, and
# an OS upgrade should never break what the volume runs. framed prefers the bundled binary
# (<mount>/runtime/ffmpeg/bin/ffmpeg with LD_LIBRARY_PATH at <mount>/runtime/ffmpeg/lib) and falls
# back to PATH only when no bundle exists.
#
# Run with the volume unlocked:
#     bundle_ffmpeg.sh /var/lib/ghost/mnt/slot0
# After a successful run + a working `... --verify`, the OS package is removable:
#     apt-get remove ffmpeg
#
# Honest limit, same as the DB bundle: the dynamic loader and glibc stay the system's , glibc ABI
# stability is what makes this cut safe; full isolation would need a chroot.

set -eu

MOUNT="${1:-}"
[ -n "$MOUNT" ] || { echo "usage: bundle_ffmpeg.sh <mount> [--verify]" >&2; exit 1; }
[ -d "$MOUNT" ] || { echo "mount $MOUNT does not exist (volume unlocked?)" >&2; exit 1; }
VERIFY=0
[ "${2:-}" = "--verify" ] && VERIFY=1

FF="$(command -v ffmpeg || true)"
[ -n "$FF" ] || { echo "ffmpeg not installed on the OS (apt-get install ffmpeg first, remove it after bundling)" >&2; exit 1; }
FP="$(command -v ffprobe || true)"

DEST="$MOUNT/runtime/ffmpeg"
mkdir -p "$DEST/bin" "$DEST/lib"

echo "> Bundling $FF into $DEST ..."
cp -a "$FF" "$DEST/bin/ffmpeg"
[ -n "$FP" ] && cp -a "$FP" "$DEST/bin/ffprobe"

# Shared-library closure: every "=> /path" line of ldd, deduped, copied. Skips the loader and vdso.
# ffmpeg's closure is large (codecs all the way down) , that is the point: it travels whole.
for bin in "$DEST/bin/"*; do
    ldd "$bin" 2>/dev/null | awk '/=> \//{print $3}' | while read -r lib; do
        [ -f "$DEST/lib/$(basename "$lib")" ] || cp -a "$lib" "$DEST/lib/"
    done
done

COUNT="$(ls "$DEST/lib" | wc -l)"
echo "> bundled: bin/ffmpeg$([ -n "$FP" ] && echo ' bin/ffprobe') + $COUNT shared libraries"

if [ "$VERIFY" = 1 ]; then
    echo "> verify: running the BUNDLED ffmpeg with only the bundled libraries ..."
    if LD_LIBRARY_PATH="$DEST/lib" "$DEST/bin/ffmpeg" -hide_banner -version >/dev/null 2>&1; then
        echo "> verify OK , the OS ffmpeg package is now removable (apt-get remove ffmpeg)"
    else
        echo "> verify FAILED , keep the OS package, paste the error to debug" >&2
        LD_LIBRARY_PATH="$DEST/lib" "$DEST/bin/ffmpeg" -hide_banner -version || true
        exit 1
    fi
fi
