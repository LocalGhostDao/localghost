#!/bin/sh
# mirror_fetch.sh <set> <dir> [file] , fetch one set (or one file of it) from the localghost.ai mirror
# into <dir>, every byte checked: the manifest's gpg signature against tools/mirror-key.asc (the key in
# this repo, the same one that signs the releases), then each file's SHA-256 against the manifest.
# Nothing unverified is ever given its real name; a failed download stays a hidden .part that the
# next run resumes. Files already in <dir> with the right hash are kept.
#
# Used at SETUP only (fetch_geo.sh, setup.sh for Go, setup_llama.sh): the box reaches the network
# while it holds nobody's data, never after.
#
# GHOST_MIRROR=<url> points elsewhere (a LAN copy), GHOST_MIRROR=off turns it off.
# Exit: 0 all fetched, 1 something failed (the caller falls back to the upstreams), 3 the mirror has
# nothing to offer here (off, no key in the repo, no gpg, no such set).
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
SET="${1:-}"
DIR="${2:-}"
ONLY="${3:-}"
MIRROR="${GHOST_MIRROR:-https://localghost.ai/mirror}"
MIRROR="${MIRROR%/}"
KEY="${GHOST_MIRROR_KEY:-$HERE/mirror-key.asc}"
# the newest build this box has installed from: a manifest older than that is refused (an old,
# validly signed manifest replayed by whoever controls the web host)
STATE="${GHOST_MIRROR_STATE:-/var/lib/ghost/mirror-build}"

say() { echo "  mirror: $*" >&2; }
na() { say "$*"; exit 3; }
[ -n "$SET" ] && [ -n "$DIR" ] || { echo "usage: mirror_fetch.sh <set> <dir> [file]" >&2; exit 2; }
case "$SET" in *[!a-z0-9-]*) na "bad set name '$SET'" ;; esac
[ "$MIRROR" = off ] && na "off (GHOST_MIRROR=off)"
case "$MIRROR" in
    https://*) ;;
    http://127.0.0.1*|http://localhost*|http://\[::1\]*) ;;
    http://*) [ "${GHOST_MIRROR_ALLOW_HTTP:-}" = 1 ] || na "$MIRROR is plain http , https, or GHOST_MIRROR_ALLOW_HTTP=1 for a copy on the LAN" ;;
    *) na "$MIRROR is not an http(s) URL" ;;
esac
[ -s "$KEY" ] || na "no $(basename "$KEY") in this repo yet , the mirror is not set up"
command -v gpg >/dev/null 2>&1 || na "gpg is not installed (apt-get install gpg)"
command -v curl >/dev/null 2>&1 || na "curl is not installed"

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT INT TERM

# --- the manifest ---
why=""
for try in 1 2 3; do
    [ "$try" -gt 1 ] && sleep 3   # a publish swaps the manifest and its signature one after the other
    if ! curl -fsSL --retry 2 -H 'Cache-Control: no-cache' -o "$T/MANIFEST.txt" "$MIRROR/MANIFEST.txt" ||
       ! curl -fsSL --retry 2 -H 'Cache-Control: no-cache' -o "$T/MANIFEST.txt.asc" "$MIRROR/MANIFEST.txt.asc"; then
        why="$MIRROR did not answer"
        continue
    fi
    rm -rf "$T/gnupg" && mkdir -m 700 "$T/gnupg"
    if gpg --batch --quiet --homedir "$T/gnupg" --import "$KEY" >/dev/null 2>&1 &&
       gpg --batch --homedir "$T/gnupg" --trust-model always --status-fd 1 \
           --verify "$T/MANIFEST.txt.asc" "$T/MANIFEST.txt" 2>/dev/null > "$T/status" &&
       grep -q '^\[GNUPG:\] VALIDSIG ' "$T/status"; then
        why=""
        break
    fi
    why="the manifest's signature does not verify against $(basename "$KEY")"
done
if [ -n "$why" ]; then
    say "$why , nothing fetched"
    exit 1
fi
[ "$(head -1 "$T/MANIFEST.txt")" = "# LocalGhost Mirror Manifest" ] || { say "a signed file, but not a mirror manifest , nothing fetched"; exit 1; }
BUILD="$(sed -n 's/^# Build: //p' "$T/MANIFEST.txt" | head -1)"
case "$BUILD" in
    [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]T[0-9][0-9][0-9][0-9][0-9][0-9]Z) ;;
    *) say "the manifest has no build line , nothing fetched"; exit 1 ;;
esac
OLD="$(cat "$STATE" 2>/dev/null || true)"
if [ -n "$OLD" ] && [ "$OLD" != "$BUILD" ] && [ "$(printf '%s\n%s\n' "$OLD" "$BUILD" | sort | head -1)" = "$BUILD" ]; then
    if [ "${GHOST_MIRROR_ALLOW_OLD:-}" != 1 ]; then
        say "the mirror offers build $BUILD but this box already used $OLD: refusing to go backwards (a stale or replayed manifest); GHOST_MIRROR_ALLOW_OLD=1 if you mean it"
        exit 1
    fi
fi
say "$MIRROR: build $BUILD, signature ok"

# --- the set's files: one level under /<build>/<set>/, names that cannot climb anywhere ---
grep -E "^[0-9a-f]{64}  /$BUILD/$SET/[A-Za-z0-9][A-Za-z0-9._+-]*\$" "$T/MANIFEST.txt" > "$T/files" || true
if [ -n "$ONLY" ]; then
    awk -v p="/$BUILD/$SET/$ONLY" '$2 == p' "$T/files" > "$T/only"
    mv "$T/only" "$T/files"
fi
[ -s "$T/files" ] || na "build $BUILD has no ${ONLY:+file $ONLY in }set '$SET'"
mkdir -p "$DIR" || exit 1

get() { # get <url> <part> , resumes <part>; a server that will not resume starts it over
    curl -fsSL --retry 3 --retry-delay 2 --speed-limit 1024 --speed-time 60 -C - -o "$2" "$1" 2>"$T/curl.err"
    _rc=$?
    if [ "$_rc" = 33 ] || [ "$_rc" = 36 ]; then
        rm -f "$2"
        curl -fsSL --retry 3 --retry-delay 2 --speed-limit 1024 --speed-time 60 -o "$2" "$1"
        _rc=$?
    elif [ "$_rc" != 0 ]; then
        cat "$T/curl.err" >&2
    fi
    return "$_rc"
}

n=0; cur=0; failed=""
while read -r sha path; do
    name="${path##*/}"
    if [ -f "$DIR/$name" ] && [ "$(sha256sum "$DIR/$name" | cut -d' ' -f1)" = "$sha" ]; then
        cur=$((cur + 1))
        continue
    fi
    part="$DIR/.$name.$(printf '%.12s' "$sha").part"
    ok=0
    if [ -f "$part" ] && [ "$(sha256sum "$part" | cut -d' ' -f1)" = "$sha" ]; then
        ok=1   # finished last time, not yet renamed
    elif get "$MIRROR$path" "$part"; then
        if [ "$(sha256sum "$part" | cut -d' ' -f1)" = "$sha" ]; then
            ok=1
        else
            rm -f "$part"
            say "$name: does not match the signed manifest , not installed"
        fi
    else
        say "$name: download failed (a rerun resumes it)"
    fi
    if [ "$ok" = 1 ]; then
        mv -f "$part" "$DIR/$name"
        n=$((n + 1))
        say "$name: $(du -h "$DIR/$name" | cut -f1), sha256 matches the signed manifest"
    else
        failed="$failed $name"
    fi
done < "$T/files"

say "$SET: $n fetched, $cur already here${failed:+, FAILED:$failed}"
[ -z "$failed" ] || exit 1
# what this build's set is, for the caller (a folder that keeps old files can tell them apart)
awk '{ n = $2; sub(".*/", "", n); print n }' "$T/files" > "$DIR/.mirror-files"
# remember the build (best-effort: a non-root run has nowhere to write it)
{ mkdir -p "$(dirname "$STATE")" && echo "$BUILD" > "$STATE"; } 2>/dev/null || true
exit 0
