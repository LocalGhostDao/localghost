#!/bin/sh
# mirror_fetch.sh <set> <dir> [file | @names-file] | --list <set> , fetch one set (or one file of it, or
# the files named one a line in names-file) from the LocalGhost mirror
# (https://www.localghost.ai/mirror, what it is and what it promises) into <dir>, every byte checked:
# the manifest's gpg signature against tools/mirror-key.asc, the site key committed in this repo and
# pinned here by fingerprint (never fetched at verify time: a key from the same server as the
# manifest proves nothing), then each file's SHA-256 against the manifest. Nothing unverified is
# ever given its real name; a failed download stays a hidden .part that the next run resumes. Files
# already in <dir> with the right hash are kept.
#
# Used at SETUP only (fetch_geo.sh, setup.sh for Go, setup_llama.sh, phone_model.sh): the box
# reaches the network while it holds nobody's data, never after. THE MIRROR IS THE ONLY SOURCE: a
# box never trusts a file that was not in a signed manifest, so the callers do not fall back to the
# upstream URLs (GHOST_MIRROR_UPSTREAM=1 is the operator's explicit, loud exception).
#
# The contract (the web repo's mirror/): MANIFEST.txt + MANIFEST.txt.asc at the base, three header
# lines (# LocalGhost Mirror Manifest, # Build: <build>, # Signed: <time>), a blank line, then
# "<sha256>  /<build>/<set>/<file>". Every set also holds NOTICE.txt and TERMS-<name>.txt, which
# come with any file of the set and stay beside it (the Gemma terms and the ODbL require it). A
# build directory never changes; the last two are kept, so a 404 on an old path means "re-read the
# manifest and carry on from the new build". A file whose recorded hash equals the manifest's is
# not downloaded, nor hashed again (a 33 GB extract is not re-read on every run). A download that
# does not match is deleted and fetched once more; a second mismatch fails that file.
#
# GHOST_MIRROR=<url> points elsewhere (a LAN copy over https, or file:///media/usb/mirror for a copy
# on a disk , MANIFEST.txt, its .asc and the build folders as the site has them), GHOST_MIRROR=off
# turns it off.
# Exit: 0 all fetched; 1 failed (the mirror unreachable, a bad signature, a file that would not
# match, the key file not the site key); 3 nothing to fetch here (GHOST_MIRROR=off, or the set / file
# is not published in this build). Either way the caller says so and stops; neither is a success.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
# mirror_fetch.sh --list <set> , the set's files in the current build, "<sha256>  <name>" a line,
# the signature checked the same way, nothing downloaded (an update compares it with what it has)
LIST=0
if [ "${1:-}" = "--list" ]; then
    LIST=1; shift
    set -- "${1:-}" "-" ""
fi
SET="${1:-}"
DIR="${2:-}"
ONLY="${3:-}"
MIRROR="${GHOST_MIRROR:-https://www.localghost.ai/mirror}"
MIRROR="${MIRROR%/}"
KEY="${GHOST_MIRROR_KEY:-$HERE/mirror-key.asc}"
# the site key (LocalGhost (The Only Cloud Is You) <info@localghost.ai>), which signs the site deploys
# and the mirror. The manifest must be signed by THIS key, whatever else tools/mirror-key.asc holds.
FPR="${GHOST_MIRROR_FPR:-DCE9A3D14EB461971DD5F393706E4194F08A09A0}"
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
    file:///*) ;;   # a copy of the mirror on a disk (a USB stick): the same signature and hashes
    http://*) [ "${GHOST_MIRROR_ALLOW_HTTP:-}" = 1 ] || na "$MIRROR is plain http , https, or GHOST_MIRROR_ALLOW_HTTP=1 for a copy on the LAN" ;;
    *) na "$MIRROR is not an http(s) URL" ;;
esac
fail() { say "$*"; exit 1; }
[ -s "$KEY" ] || fail "no $KEY , the site key belongs in this repo (tools/mirror-key.asc)"
command -v gpg >/dev/null 2>&1 || fail "gpg is not installed (apt-get install gpg)"
command -v curl >/dev/null 2>&1 || fail "curl is not installed (apt-get install curl)"

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT INT TERM
# every request: redirects followed (the bare domain answers 301 to www) but never down to plain http
c() { curl -fsSL --proto-redir =https --max-redirs 5 "$@"; }

# the key file must hold the pinned key; imported into a throwaway gpg home, never the operator's
mkdir -m 700 "$T/gnupg"
if ! gpg --batch --quiet --homedir "$T/gnupg" --import "$KEY" >/dev/null 2>&1 ||
   ! gpg --batch --homedir "$T/gnupg" --with-colons --list-keys 2>/dev/null | awk -F: '$1 == "fpr" { print $10 }' | grep -qx "$FPR"; then
    fail "$KEY is not the LocalGhost site key ($FPR) , check it: gpg --show-keys --with-fingerprint $KEY"
fi

# --- the manifest ---
# read_manifest: fetch and verify MANIFEST.txt (three tries: a publish writes the manifest and its
# signature one after the other), refuse a build older than this box has used, and list the set's
# files (with ONLY: that file plus the set's NOTICE.txt and TERMS-*.txt) in $T/files.
read_manifest() {
    why=""
    for try in 1 2 3; do
        [ "$try" -gt 1 ] && sleep 4
        if ! c --retry 2 -H 'Cache-Control: no-cache' -o "$T/MANIFEST.txt" "$MIRROR/MANIFEST.txt" ||
           ! c --retry 2 -H 'Cache-Control: no-cache' -o "$T/MANIFEST.txt.asc" "$MIRROR/MANIFEST.txt.asc"; then
            why="$MIRROR did not answer (or redirected off https, which is refused)"
            continue
        fi
        # VALIDSIG names the signing key and, last, its primary key: one of them must be the pinned one
        if gpg --batch --homedir "$T/gnupg" --trust-model always --status-fd 1 \
               --verify "$T/MANIFEST.txt.asc" "$T/MANIFEST.txt" 2>/dev/null > "$T/status" &&
           awk '$2 == "VALIDSIG" { print $3; print $NF }' "$T/status" | grep -qx "$FPR"; then
            why=""
            break
        fi
        why="the manifest's signature is not a valid signature by the site key ($FPR)"
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
    # the set's files: one level under /<build>/<set>/, names that cannot climb anywhere
    grep -E "^[0-9a-f]{64}  /$BUILD/$SET/[A-Za-z0-9][A-Za-z0-9._+-]*\$" "$T/MANIFEST.txt" > "$T/files" || true
    if [ -n "$ONLY" ]; then
        case "$ONLY" in
        @*)
            # @<file>: the names listed in it (one a line: a set of thousands, the elevation
            # tiles, fetched in part), and the set's notice and terms
            LISTF="${ONLY#@}"
            [ -r "$LISTF" ] || na "no list of names at $LISTF"
            awk -v d="/$BUILD/$SET/" 'NR == FNR { if ($1 != "") want[$1] = 1; next }
                index($2, d) == 1 { n = substr($2, length(d) + 1); if ((n in want) || n == "NOTICE.txt" || n ~ /^TERMS-.*\.txt$/) print }' "$LISTF" "$T/files" > "$T/only"
            if ! awk -v d="/$BUILD/$SET/" '{ n = substr($2, length(d) + 1); if (n != "NOTICE.txt" && n !~ /^TERMS-.*\.txt$/) f = 1 } END { exit !f }' "$T/only"; then
                na "build $BUILD lists none of the names in $LISTF in set '$SET'"
            fi
            ;;
        *)
            # the file asked for, and the set's notice and terms, which travel with every file
            awk -v p="/$BUILD/$SET/$ONLY" -v d="/$BUILD/$SET/" '$2 == p { print; next }
                index($2, d) == 1 { n = substr($2, length(d) + 1); if (n == "NOTICE.txt" || n ~ /^TERMS-.*\.txt$/) print }' "$T/files" > "$T/only"
            if ! awk -v p="/$BUILD/$SET/$ONLY" '$2 == p { f = 1 } END { exit !f }' "$T/only"; then
                na "build $BUILD does not list $ONLY in set '$SET' (not published there yet)"
            fi
            ;;
        esac
        mv "$T/only" "$T/files"
    fi
    [ -s "$T/files" ] || na "build $BUILD has no set '$SET' (not published there yet)"
}
read_manifest
if [ "$LIST" = 1 ]; then
    awk '{ n = $2; sub(".*/", "", n); print $1 "  " n }' "$T/files"
    exit 0
fi
mkdir -p "$DIR" || exit 1

get() { # get <url> <part> , resumes <part>; a server that will not resume starts it over. The HTTP
      # status lands in $T/code (a 404 is an old build's path: the caller re-reads the manifest).
    c --retry 3 --retry-delay 2 --speed-limit 1024 --speed-time 60 -C - -o "$2" -w '%{http_code}' "$1" 2>"$T/curl.err" > "$T/code"
    _rc=$?
    if [ "$_rc" = 33 ] || [ "$_rc" = 36 ]; then
        rm -f "$2"
        c --retry 3 --retry-delay 2 --speed-limit 1024 --speed-time 60 -o "$2" -w '%{http_code}' "$1" 2>"$T/curl.err" > "$T/code"
        _rc=$?
    fi
    if [ "$_rc" != 0 ] && [ "$(cat "$T/code" 2>/dev/null)" != 404 ]; then
        cat "$T/curl.err" >&2
    fi
    return "$_rc"
}

# the recorded hash of a file: <sha256> <size> <mtime> in <dir>/.<name>.sha256, written when the file
# was verified; a file whose size and mtime still match its record is not read again
recorded() { # recorded <name> , prints the recorded sha256 when the record still describes the file
    _f="$DIR/$1"; _r="$DIR/.$1.sha256"
    [ -f "$_f" ] && [ -f "$_r" ] || return 1
    read -r _sha _size _mtime < "$_r" || return 1
    [ "$_size" = "$(stat -c%s "$_f")" ] && [ "$_mtime" = "$(stat -c%Y "$_f")" ] || return 1
    echo "$_sha"
}
record() { # record <name> <sha256>
    echo "$2 $(stat -c%s "$DIR/$1") $(stat -c%Y "$DIR/$1")" > "$DIR/.$1.sha256" 2>/dev/null || true
}

# fetch_all: every file of $T/files; sets $n, $cur, $failed; returns 2 when a path 404'd (an old
# build was pruned under us), else 0
fetch_all() {
    n=0; cur=0; failed=""; stale=0
    total="$(wc -l < "$T/files" | tr -d ' ')"   # a set of thousands says how far it is, not every file
    while read -r sha path; do
        name="${path##*/}"
        if [ "$(recorded "$name")" = "$sha" ]; then
            cur=$((cur + 1)); continue
        fi
        if [ -f "$DIR/$name" ] && [ "$(sha256sum "$DIR/$name" | cut -d' ' -f1)" = "$sha" ]; then
            record "$name" "$sha"
            cur=$((cur + 1)); continue
        fi
        part="$DIR/.$name.$(printf '%.12s' "$sha").part"
        # a half download of another version of this file (an older build's) will never finish
        for p in "$DIR/.$name."*.part; do
            [ -f "$p" ] && [ "$p" != "$part" ] && rm -f "$p"
        done
        ok=0
        for attempt in 1 2; do
            if [ -f "$part" ] && [ "$(sha256sum "$part" | cut -d' ' -f1)" = "$sha" ]; then
                ok=1; break   # finished last time, not yet renamed
            fi
            if get "$MIRROR$path" "$part"; then
                if [ "$(sha256sum "$part" | cut -d' ' -f1)" = "$sha" ]; then
                    ok=1; break
                fi
                rm -f "$part"
                say "$name: does not match the signed manifest$([ "$attempt" = 1 ] && echo ', fetching it once more' || echo ' again , not installed')"
                continue
            fi
            if [ "$(cat "$T/code" 2>/dev/null)" = 404 ]; then
                stale=1   # this build is gone: the caller re-reads the manifest
            else
                say "$name: download failed (a rerun resumes it)"
            fi
            break
        done
        if [ "$ok" = 1 ]; then
            # -T: the name is replaced, never entered. When <dir> is on the unlocked volume it
            # is the daemons' user's, and a link to a directory put where the file's name
            # stands would otherwise have this (root) move the file into that directory.
            mv -fT "$part" "$DIR/$name"
            record "$name" "$sha"
            n=$((n + 1))
            if [ "$total" -le 200 ]; then
                say "$name: $(du -h "$DIR/$name" | cut -f1), sha256 matches the signed manifest"
            elif [ $((n % 500)) = 0 ]; then
                say "$n fetched of $total, each sha256 matching the signed manifest"
            fi
        elif [ "$stale" = 1 ]; then
            return 2
        else
            failed="$failed $name"
        fi
    done < "$T/files"
    return 0
}

rereads=0
while :; do
    rc=0; fetch_all || rc=$?
    [ "$rc" = 2 ] || break
    rereads=$((rereads + 1))
    if [ "$rereads" -gt 2 ]; then
        say "build $BUILD's files keep answering 404 , the mirror is mid-publish or broken; a rerun resumes"
        exit 1
    fi
    say "an old build's path answered 404 (it was pruned): reading the manifest again"
    read_manifest
done

say "$SET: $n fetched, $cur already here${failed:+, FAILED:$failed}"
[ -z "$failed" ] || exit 1
# what this build's set is, for the caller (a folder that keeps old files can tell them apart)
awk '{ n = $2; sub(".*/", "", n); print n }' "$T/files" > "$DIR/.mirror-files"
# remember the build (best-effort: a non-root run has nowhere to write it)
{ mkdir -p "$(dirname "$STATE")" && echo "$BUILD" > "$STATE"; } 2>/dev/null || true
exit 0
