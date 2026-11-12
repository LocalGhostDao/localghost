#!/usr/bin/env bash
# setup_whisper.sh , the speech engine for voice notes: whisper.cpp's whisper-cli, built from the
# mirror's pinned source tarball (set whisper: ONE tarball, never master, never a git clone), and a
# ggml speech model (set speech). The mirror only, as for llama.cpp: each file is taken after the
# manifest's signature verifies against the site key and its SHA-256 matches. No git, no Hugging Face.
#
#   sudo ./tools/setup_whisper.sh                          engine + model; staged, the next unlock
#                                                          puts both on the encrypted volume
#   sudo ./tools/setup_whisper.sh --build-only             the engine only (into the repo's bin/)
#   sudo ./tools/setup_whisper.sh --whisper-tarball FILE   a copy of the mirror's tarball (checked
#                                                          against the signed manifest all the same)
#   GHOST_SPEECH_INSTALL_TO=<mount seen from here>         what update.sh uses on an UNLOCKED box:
#                                                          whisper-cli into <it>/bin, the model into
#                                                          <it>/ai-models, straight away
#
# The build is CPU-only on purpose (GGML_CUDA=OFF): the 4070's 12 GB belong to the chat model, and
# a second CUDA client is one more thing that can wedge the driver. ghost.voiced also runs it at
# nice 10 with at most four threads. A three-minute note takes about a minute on the box's CPU with
# large-v3-turbo (an estimate, not yet measured on xyntai).
#
# Exit: 0 done, 1 failed, 3 the mirror does not carry the set(s) yet (voice notes are kept on the
# box and wait; ghost.voiced transcribes them on the first pass after this succeeds).
set -uo pipefail

if [ "$(id -u)" -ne 0 ]; then echo "run as root (sudo)" >&2; exit 1; fi
cd "$(dirname "$0")/.."
REPO="$(pwd)"
FETCH="$REPO/tools/mirror_fetch.sh"
WDIR="${GHOST_WHISPER_DIR:-/opt/localghost/whisper.cpp}"
SRC_DL="$WDIR.mirror-dl"
TO="${GHOST_SPEECH_INSTALL_TO:-}"
# the daemons' user, as ghost.secd's unit names it (coder on a box from before tools/own_user.sh)
UNIT_USER="$(sed -n 's/^ExecStart=.* --user \([a-z_][a-z0-9_-]*\).*/\1/p' /etc/systemd/system/ghost.secd.service 2>/dev/null | head -1)"
OWNER="${GHOST_SPEECH_OWNER:-${UNIT_USER:-coder}}"
BUILD_ONLY=0
TARBALL=""
while [ $# -gt 0 ]; do
    case "$1" in
        --build-only) BUILD_ONLY=1; shift ;;
        --whisper-tarball) TARBALL="$2"; shift 2 ;;
        -h|--help) sed -n '2,24p' "$0"; exit 0 ;;
        *) echo "unknown option $1" >&2; exit 2 ;;
    esac
done

# from_mirror , whisper.cpp's source (whisper.cpp-<tag>-<commit>.tar.gz, ONE folder inside named
# after the full commit). A new file name replaces the folder whole, build/ included, so the build
# below runs again. Returns mirror_fetch's code: 0 here, 1 failed, 3 not published.
from_mirror() {
    mkdir -p "$SRC_DL"
    if [ -n "$TARBALL" ]; then
        [ -f "$TARBALL" ] || { echo "!! no such file: $TARBALL" >&2; return 1; }
        cp -f "$TARBALL" "$SRC_DL/$(basename "$TARBALL")"
    fi
    _rc=0; sh "$FETCH" whisper "$SRC_DL" || _rc=$?
    [ "$_rc" = 0 ] || return "$_rc"
    TB="$(grep -E '^whisper\.cpp-.+\.tar\.gz$' "$SRC_DL/.mirror-files" | head -1)"
    [ -n "$TB" ] && [ -s "$SRC_DL/$TB" ] || { echo "!! the mirror's set whisper holds no whisper.cpp-*.tar.gz" >&2; return 1; }
    for f in "$SRC_DL"/*.tar.gz; do
        [ "$(basename "$f")" = "$TB" ] || rm -f "$f"
    done
    TBP="$SRC_DL/$TB"
    if [ "$(cat "$WDIR/.mirror-src" 2>/dev/null)" = "$TB" ]; then
        echo "-- whisper.cpp from the mirror: $TB already here"
        return 0
    fi
    _base="${TB%.tar.gz}"; _base="${_base#whisper.cpp-}"
    _short="${_base##*-}"
    tar -tzf "$TBP" | cut -d/ -f1 | grep -vx 'pax_global_header' | sort -u > "$SRC_DL/.top"
    _top="$(head -1 "$SRC_DL/.top")"
    _full="$(printf '%s\n' "$_top" | grep -oE '[0-9a-f]{40}' | head -1)"
    if [ "$(wc -l < "$SRC_DL/.top")" != 1 ]; then
        echo "!! $TB holds more than one folder ($(tr '\n' ' ' < "$SRC_DL/.top")) , not built" >&2; return 1
    fi
    case "$_full" in
        "$_short"*) ;;
        *) echo "!! $TB unpacks into '$_top', which does not name commit $_short in full , not built" >&2; return 1 ;;
    esac
    rm -rf "$WDIR.new" && mkdir -p "$WDIR.new"
    tar -xzf "$TBP" -C "$WDIR.new" --strip-components=1 || { rm -rf "$WDIR.new"; return 1; }
    echo "$TB" > "$WDIR.new/.mirror-src"
    echo "$_full" > "$WDIR.new/.mirror-commit"
    sha256sum "$TBP" | cut -d' ' -f1 > "$WDIR.new/.mirror-sha256"
    cp "$SRC_DL"/NOTICE.txt "$SRC_DL"/TERMS-*.txt "$WDIR.new/" 2>/dev/null || true
    rm -rf "$WDIR.old"
    [ -d "$WDIR" ] && mv "$WDIR" "$WDIR.old"
    mv "$WDIR.new" "$WDIR" && rm -rf "$WDIR.old"
    echo "-- whisper.cpp source from the mirror: $TB (commit $_full), signature and hash checked"
}

echo "=== speech 1/2  whisper.cpp , the mirror's source, built for this CPU ==="
mkdir -p "$(dirname "$WDIR")"
mrc=0; from_mirror || mrc=$?
if [ "$mrc" != 0 ]; then
    if [ -f "$WDIR/.mirror-src" ] && [ "$mrc" != 3 ]; then
        echo "-- the mirror did not deliver whisper.cpp this time (above); building the source already here, $(cat "$WDIR/.mirror-src")"
    elif [ "$mrc" = 3 ] && [ -f "$WDIR/.mirror-src" ]; then
        echo "-- the mirror no longer lists set whisper; keeping $(cat "$WDIR/.mirror-src"), checked when it came"
    else
        [ "$mrc" = 3 ] && echo "-- whisper.cpp is not on the mirror yet (set whisper). Voice notes are kept on the box and wait for it." \
                       || echo "!! could not take whisper.cpp from the mirror (the lines above say why)" >&2
        exit "$mrc"
    fi
fi
if [ ! -x "$WDIR/build/bin/whisper-cli" ]; then
    command -v cmake >/dev/null 2>&1 && command -v c++ >/dev/null 2>&1 || \
        apt-get install -y --no-install-recommends cmake build-essential >/dev/null
    echo "[setup_whisper] building $(cat "$WDIR/.mirror-src" 2>/dev/null) (CPU, static, this machine's instructions)"
    # static, like llama-server: the binary lives on the encrypted volume and carries no .so files
    # of its own. Unknown -D options are ignored by cmake, so older trees build the same way.
    cmake -S "$WDIR" -B "$WDIR/build" -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF -DGGML_NATIVE=ON \
        -DGGML_CUDA=OFF -DWHISPER_BUILD_EXAMPLES=ON -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_SERVER=OFF \
        -DWHISPER_SDL2=OFF -DWHISPER_CURL=OFF -DWHISPER_FFMPEG=OFF >/dev/null || { echo "!! cmake configure failed" >&2; exit 1; }
    cmake --build "$WDIR/build" --target whisper-cli -j"$(nproc)" 2>&1 | tail -3
    [ -x "$WDIR/build/bin/whisper-cli" ] || { echo "!! the build did not produce build/bin/whisper-cli" >&2; exit 1; }
else
    echo "-- whisper-cli already built (delete $WDIR/build to force a rebuild)"
fi
# into the repo's bin/, like llama-server: redeploy stages it onto the volume with the daemons
mkdir -p "$REPO/bin"
install -m 0755 "$WDIR/build/bin/whisper-cli" "$REPO/bin/whisper-cli"
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
if [ -n "$TO" ]; then
    # a running box: beside the old one, then renamed over it (a running binary is never rewritten)
    install -T -m 0755 "$WDIR/build/bin/whisper-cli" "$TO/bin/whisper-cli.new" && vol_own "$TO/bin/whisper-cli.new" \
        && mv -fT "$TO/bin/whisper-cli.new" "$TO/bin/whisper-cli" || { echo "!! could not put whisper-cli on the volume" >&2; exit 1; }
    echo "-- whisper-cli on the volume"
else
    mkdir -p /var/lib/ghost/staging/bin
    install -m 0755 "$WDIR/build/bin/whisper-cli" /var/lib/ghost/staging/bin/whisper-cli
    echo "-- whisper-cli staged (the next unlock puts it on the volume)"
fi
[ "$BUILD_ONLY" = 1 ] && exit 0

echo "=== speech 2/2  the speech model (set speech) ==="
if [ -n "$TO" ]; then
    DEST="$TO/ai-models"
else
    DEST=/var/lib/ghost/staging/download-speech
    mkdir -p -m 700 "$DEST"   # root's own staging, not the volume
fi
src=0; sh "$FETCH" speech "$DEST" || src=$?
[ -f "$DEST/NOTICE.txt" ] && mv -fT "$DEST/NOTICE.txt" "$DEST/NOTICE-speech.txt"
rm -f "$DEST/.mirror-files"
if [ "$src" = 3 ]; then
    echo "-- no speech model on the mirror yet (set speech). Voice notes are kept on the box and wait for it."
    exit 3
elif [ "$src" != 0 ]; then
    echo "!! the speech model could not be taken from the mirror (above); a rerun resumes" >&2
    exit 1
fi
got="$(ls "$DEST"/ggml-*.bin 2>/dev/null | head -3 | xargs -r -n1 basename | tr '\n' ' ')"
[ -n "$got" ] || { echo "!! the set speech holds no ggml-*.bin" >&2; exit 1; }
if [ -n "$TO" ]; then
    vol_own "$DEST"/ggml-*.bin "$DEST"/NOTICE-speech.txt "$DEST"/TERMS-*.txt
    vol_mode 600 "$DEST"/ggml-*.bin
    echo "-- on the volume: $got"
else
    mkdir -p /var/lib/ghost/staging/ai-models && chmod 700 /var/lib/ghost/staging /var/lib/ghost/staging/ai-models
    mv -f "$DEST"/ggml-*.bin /var/lib/ghost/staging/ai-models/
    mv -f "$DEST"/NOTICE-speech.txt "$DEST"/TERMS-*.txt /var/lib/ghost/staging/ai-models/ 2>/dev/null || true
    rm -rf "$DEST"
    echo "-- staged: $got(the next unlock puts it on the volume)"
fi
exit 0
