#!/usr/bin/env bash
# cut_release.sh <version> [--apk <file>] , cut a release, or cut it again from the same place.
#
#   ./tools/cut_release.sh 0.0.1                          the first time: tags HEAD as v0.0.1 and builds from the tag
#   ./tools/cut_release.sh 0.0.1 --apk ~/app-release.apk  with the app, built at the same commit (below)
#   ./tools/cut_release.sh 0.0.1                          again: builds from the tag v0.0.1, the same bytes
#
# ONE RELEASE for the whole of LocalGhost, release/<version>/:
#   server/   the mirror's server set (the bundle, RELEASE.txt, NOTES.md, NOTICE.txt, TERMS-MIT.txt):
#             what a box takes from the phone (tools/release_build.sh, reproducible)
#   app/      the Android app, localghost-app-<version>.apk, built here from the tag's tree with the
#             Android SDK on this machine (ANDROID_HOME or ANDROID_SDK_ROOT, ~/.localghost_android_env
#             from app/android/tools/debian_setup.sh, sdk.dir in app/android/local.properties, or the
#             usual places) and signed with the app's keystore (~/localghost-release.jks, or
#             LG_KEYSTORE and LG_KEY_ALIAS in ~/.config/localghost/release.env, which
#             tools/app_keystore.sh writes; LG_KEYSTORE_PASS too, or apksigner asks; the alias is
#             needed only when the keystore holds more than one key); else handed in with --apk
#             from a machine that built it at the same commit. With its GPG signature when the site key is in this user's gpg
#             (info@localghost.ai), and APP.txt (its hash, commit, version and signing certificate).
#             No SDK or no keystore stops the cut before it builds anything: a release carries the
#             app. --no-apk cuts one without it, on purpose.
#   source/   localghost-<version>-source.tar.gz, git archive of the tag: the whole tree, which is
#             also how a box is set up (server/tools/setup.sh, see server/tools/README.md)
#   SHA256SUMS over all of it (the .asc signatures beside it, not in it), and SHA256SUMS.asc when the
#             site key is here
# The GitHub release v<version> carries every file; the mirror's server set takes server/.
#
# A release is three things committed with the code, so cutting it twice gives the same files:
#   tools/release.names      "<version> <name>" (0.0.1 is wisp): the name the phone shows
#   releases/<version>.md    what the release does, what is in it, how it works: the full notes,
#                            which travel beside the bundle (NOTES.md) in the mirror's set
#   releases/pins.txt        "<version> <commit>", written at the first cut: the pin. A later cut
#                            refuses to build when the tag no longer points at the pinned commit.
# The build itself is tools/release_build.sh (reproducible), run in a clean worktree of the tag, so
# the state of the checkout the command runs in does not matter. The output lands in
# release/<version>/server/: the bundle, RELEASE.txt, NOTES.md, NOTICE.txt, TERMS-MIT.txt, and
# SHA256SUMS. The first cut prints what to do next (the GitHub release, the mirror's server set).
set -euo pipefail

VERSION="${1:?usage: cut_release.sh <version> [--apk <file> | --no-apk]}"
shift
APK=""
NOAPK=""
while [ $# -gt 0 ]; do
    case "$1" in
        --apk) APK="${2:?--apk needs a file}"; shift 2 ;;
        --no-apk) NOAPK=1; shift ;;
        *) echo "unknown option $1 (--apk <file> | --no-apk)" >&2; exit 2 ;;
    esac
done
if [ -n "$APK" ]; then
    [ -s "$APK" ] || { echo "no APK at $APK" >&2; exit 2; }
    APK="$(cd "$(dirname "$APK")" && pwd)/$(basename "$APK")"
    # an APK is a zip with the app's manifest and code in it (the listing read whole: `grep -q`
    # closing the pipe early gave unzip SIGPIPE under pipefail, and a real APK's thousands of
    # entries made the genuine release APK "not an Android app")
    NAMES="$(unzip -Z1 "$APK" 2>/dev/null || true)"
    if [ "$(printf '%s\n' "$NAMES" | grep -cx 'AndroidManifest.xml')" -eq 0 ] || [ "$(printf '%s\n' "$NAMES" | grep -cE '^classes[0-9]*\.dex$')" -eq 0 ]; then
        echo "$APK is not an Android app (no AndroidManifest.xml and classes.dex in it; is unzip installed?)" >&2; exit 2
    fi
fi
case "$VERSION" in *[!0-9.]*) echo "a version is numbers and dots (0.0.1); the name comes from tools/release.names" >&2; exit 2 ;; esac
HERE="$(cd "$(dirname "$0")/.." && pwd)"
cd "$HERE"
# not under sudo: root's gpg has no site key, and root-owned output stops the next cut as the user
if [ -n "${SUDO_USER:-}" ]; then
    echo "run the cut as yourself, not under sudo: the signatures come from $SUDO_USER's gpg, and files root writes under release/ cannot be cleared by the next cut" >&2
    exit 2
fi
TAG="v$VERSION"
NOTES="releases/$VERSION.md"
PINS="releases/pins.txt"
OUT="${GHOST_RELEASE_OUT:-$HERE/release/$VERSION}"

NAME="$(awk -v v="$VERSION" '$1 == v { print $2; exit }' tools/release.names 2>/dev/null || true)"
[ -n "$NAME" ] || { echo "no name for $VERSION in tools/release.names (a line: \"$VERSION <name>\")" >&2; exit 2; }
[ -s "$NOTES" ] || { echo "no notes at $NOTES: write what the release does, what is in it and how it works, and commit it" >&2; exit 2; }
git rev-parse --git-dir >/dev/null 2>&1 || { echo "not a git checkout" >&2; exit 2; }
# where this folder sits in the repository ("server/" when the module is a folder of it, "" when
# it is the repository): the worktree of the tag is the whole repository, the build runs here
PREFIX="$(git rev-parse --show-prefix)"
# Go, for the build: on PATH, else the system Go setup installs
GO="${GO:-$(sh tools/go_for.sh 2>/dev/null || true)}" # go.mod's Go when the box has it, whatever PATH says
[ -x "${GO:-/nonexistent}" ] || GO=/usr/local/go/bin/go
[ -x "$GO" ] || { echo "no go on PATH and none at /usr/local/go/bin/go" >&2; exit 2; }
TOP="$(git rev-parse --show-toplevel)"

# THE APP'S TOOLS, checked before anything is tagged or built: the SDK and the keystore.
BT=""
find_sdk() { # sets ANDROID_HOME from the first place that has one
    [ -n "${ANDROID_HOME:-}" ] || ANDROID_HOME="${ANDROID_SDK_ROOT:-}"
    if [ -z "$ANDROID_HOME" ] && [ -f "$HOME/.localghost_android_env" ]; then
        . "$HOME/.localghost_android_env"
    fi
    if [ -z "${ANDROID_HOME:-}" ] && [ -f "$TOP/app/android/local.properties" ]; then
        # what gradle used in this checkout (sdk.dir, with its escaped colons and backslashes)
        ANDROID_HOME="$(sed -n 's/^sdk\.dir=//p' "$TOP/app/android/local.properties" | sed 's/\\:/:/g; s/\\\\/\//g; s/\r$//' | sed -n '1p')"
    fi
    if [ -z "${ANDROID_HOME:-}" ] || [ ! -d "$ANDROID_HOME/build-tools" ]; then
        for d in "$HOME/android-sdk" "$HOME/Android/Sdk" /opt/android-sdk /usr/lib/android-sdk; do
            [ -d "$d/build-tools" ] && { ANDROID_HOME="$d"; break; }
        done
    fi
    [ -n "${ANDROID_HOME:-}" ] && [ -d "$ANDROID_HOME/build-tools" ] || return 1
    export ANDROID_HOME
    BT="$(ls -d "$ANDROID_HOME"/build-tools/*/ 2>/dev/null | sort -V | tail -1)"
    BT="${BT%/}"
    [ -x "$BT/apksigner" ] && [ -x "$BT/zipalign" ]
}
app_tools() { # the SDK and the keystore, or the reason there is no app
    [ -f "$HOME/.config/localghost/release.env" ] && . "$HOME/.config/localghost/release.env"
    [ -z "${LG_KEYSTORE_PASS:-}" ] || export LG_KEYSTORE_PASS
    [ -z "${LG_KEY_PASS:-}" ] || export LG_KEY_PASS
    if ! find_sdk; then
        echo "no Android SDK with build-tools found (looked at ANDROID_HOME, ANDROID_SDK_ROOT, ~/.localghost_android_env, app/android/local.properties sdk.dir, ~/android-sdk, ~/Android/Sdk, /opt/android-sdk)." >&2
        echo "  app/android/tools/debian_setup.sh installs one; or put ANDROID_HOME=<sdk> in ~/.config/localghost/release.env" >&2
        return 1
    fi
    # the keystore: named in release.env, else the usual file in the home folder
    if [ -z "${LG_KEYSTORE:-}" ]; then
        for k in "$HOME/localghost-release.jks" "$HOME/.config/localghost/localghost-release.jks"; do
            [ -s "$k" ] && { LG_KEYSTORE="$k"; break; }
        done
    fi
    if [ -z "${LG_KEYSTORE:-}" ] || [ ! -s "$LG_KEYSTORE" ]; then
        echo "no keystore for the app: none at ~/localghost-release.jks or ~/.config/localghost/localghost-release.jks and no LG_KEYSTORE=<file.jks> in ~/.config/localghost/release.env (tools/app_keystore.sh --use <file.jks> writes it; without --use it makes a new keystore)." >&2
        return 1
    fi
    export LG_KEYSTORE
    # the alias: LG_KEY_ALIAS, or the keystore's only key (apksigner takes it without an alias)
    [ -n "${JAVA_HOME:-}" ] && [ -x "$JAVA_HOME/bin/java" ] || command -v java >/dev/null 2>&1 ||
        { echo "no java for gradle (JAVA_HOME, or java on PATH; a JDK 17 or newer)" >&2; return 1; }
    echo "app: SDK $ANDROID_HOME, build-tools $(basename "$BT"), keystore $LG_KEYSTORE (${LG_KEY_ALIAS:-its only key}${LG_KEYSTORE_PASS:+, password from release.env})"
}
if [ -z "$NOAPK" ] && [ -z "$APK" ]; then
    app_tools || { echo "the release carries the app, so nothing is cut. Fix the above and run again, or --no-apk for a cut without it (on purpose)." >&2; exit 1; }
fi
# THE GO THE RELEASE IS BUILT WITH is the one go.mod names and no other: the bytes differ from one Go
# to the next, and the server set's hashes are only worth checking when anyone can make the same
# bytes. An older Go would fail the build; a newer one would build different bytes (GHOST_GO_ANY=1
# cuts with it anyway, on purpose, and RELEASE.txt says which Go it was).
GO_MOD_VER="$(awk '$1 == "go" { print $2; exit }' go.mod)"
GO_HAVE="$(cd / && GOTOOLCHAIN=local "${GO:-go}" version 2>/dev/null | sed 's/.*go\([0-9][0-9.]*\).*/\1/')"
if [ -z "$GO_HAVE" ]; then
    echo "no go on this box; sudo ./tools/install_go.sh installs it (from the mirror)" >&2; exit 1
elif [ "$GO_HAVE" != "$GO_MOD_VER" ] && [ "${GHOST_GO_ANY:-}" != 1 ]; then
    echo "go $GO_HAVE at $GO, go.mod names $GO_MOD_VER: a release is built with that Go and no other, so its bytes can be made again." >&2
    echo "  sudo ./tools/install_go.sh brings the box's Go to it (from the mirror); GHOST_GO_ANY=1 cuts with go $GO_HAVE anyway." >&2
    exit 1
fi
echo "go: $GO_HAVE at $GO (go.mod $GO_MOD_VER)"

say() { printf '\n== %s ==\n' "$*"; }

if git rev-parse -q --verify "refs/tags/$TAG^{commit}" >/dev/null; then
    COMMIT="$(git rev-parse "$TAG^{commit}")"
    PINNED="$(awk -v v="$VERSION" '$1 == v { print $2; exit }' "$PINS" 2>/dev/null || true)"
    if [ -n "$PINNED" ] && [ "$PINNED" != "$COMMIT" ]; then
        echo "the tag $TAG points at $COMMIT but $PINS pins $VERSION to $PINNED: the tag was moved. Put it back (git tag -f $TAG $PINNED) or pin the new commit on purpose." >&2
        exit 1
    fi
    [ -n "$PINNED" ] || echo "note: $PINS has no line for $VERSION; the tag $TAG is the pin ($COMMIT)"
    say "cutting $NAME $VERSION again from $TAG ($COMMIT)"
else
    # the first cut: from a clean tree, the notes and the name committed
    if [ -n "$(git status --porcelain)" ]; then
        echo "the tree has uncommitted changes: a release is cut from a commit (commit $NOTES and tools/release.names with the code first)" >&2
        exit 1
    fi
    for f in "$NOTES" tools/release.names; do
        git ls-files --error-unmatch "$f" >/dev/null 2>&1 || { echo "$f is not committed" >&2; exit 1; }
    done
    COMMIT="$(git rev-parse HEAD)"
    say "cutting $NAME $VERSION from $COMMIT: tagging $TAG"
    # the first paragraph of the notes is the tag's message
    MSG="$(awk 'NR == 1 { next } /^$/ { if (n) exit; next } { print; n = 1 }' "$NOTES")"
    git tag -a "$TAG" -m "$NAME $VERSION" -m "$MSG" "$COMMIT"
    mkdir -p releases
    echo "$VERSION $COMMIT" >> "$PINS"
    echo "pinned: $VERSION $COMMIT in $PINS (commit it: git add $PINS && git commit -m 'pin $NAME $VERSION')"
fi

# the build, in a worktree of the tag: whatever this checkout holds, the release is the tag's
W="$(mktemp -d)"
cleanup() { git worktree remove --force "$W/src" >/dev/null 2>&1 || true; rm -rf "$W"; }
trap cleanup EXIT
git worktree add --detach "$W/src" "$TAG" >/dev/null 2>&1 || { echo "could not make a worktree of $TAG" >&2; exit 1; }
if [ -e "$OUT" ] && ! rm -rf "$OUT" 2>/dev/null; then
    echo "cannot clear $OUT: an earlier cut left files another user owns there (root, when it ran under sudo). sudo rm -rf $OUT, then run the cut again as $(id -un)." >&2
    exit 1
fi
mkdir -p "$OUT/source"
( cd "$W/src/$PREFIX" && GO="$GO" ./tools/release_build.sh "$VERSION" "$OUT" )
# the site key, for the signatures (info@localghost.ai; the key that signs the site and the mirror)
SIGNER="${GHOST_RELEASE_SIGNER:-info@localghost.ai}"
sign() { # sign <file>: <file>.asc when the key is in this user's gpg, said once when it is not
    if gpg --batch --list-secret-keys "$SIGNER" >/dev/null 2>&1; then
        gpg --batch --yes --armor --local-user "$SIGNER" --output "$1.asc" --detach-sign "$1"
    else
        [ -n "${nosign:-}" ] || echo "note: no secret key for $SIGNER in this user's gpg: nothing is GPG-signed (run the cut as the user that holds it, or sign SHA256SUMS by hand)"
        nosign=1
    fi
}
# THE APP. Built here from the tag's tree with the SDK found above, else handed in.
APPOUT="$OUT/app/localghost-app-$VERSION.apk"
build_apk() {
    APPDIR="$W/src/app/android"
    [ -x "$APPDIR/gradlew" ] || { echo "no app/android/gradlew in the tag"; return 1; }
    # the public build: no box baked in; the SDK; the pinned llama.cpp tarball where the box has it
    {
        echo "sdk.dir=$ANDROID_HOME"
        echo "NAS_BASE_URL="
        echo "DEVICE_TOKEN="
        [ -n "${LG_LLAMA_TARBALL:-}" ] && echo "llamaTarball=$LG_LLAMA_TARBALL"
        true
    } > "$APPDIR/local.properties"
    echo "building the app (gradle assembleRelease; the phone's model runtime takes a few minutes the first time)"
    ( cd "$APPDIR" && ./gradlew --no-daemon -q assembleRelease ) || { echo "the app's build failed (above)"; return 1; }
    UNSIGNED="$(ls "$APPDIR"/app/build/outputs/apk/release/*.apk 2>/dev/null | sed -n '1p' || true)"
    [ -s "$UNSIGNED" ] || { echo "no APK came out of the build"; return 1; }
    mkdir -p "$OUT/app"
    "$BT/zipalign" -f 4 "$UNSIGNED" "$W/aligned.apk"
    SIGNARGS=(--ks "$LG_KEYSTORE")
    [ -z "${LG_KEY_ALIAS:-}" ] || SIGNARGS+=(--ks-key-alias "$LG_KEY_ALIAS")
    [ -z "${LG_KEYSTORE_PASS:-}" ] || SIGNARGS+=(--ks-pass env:LG_KEYSTORE_PASS)
    [ -z "${LG_KEY_PASS:-}" ] || SIGNARGS+=(--key-pass env:LG_KEY_PASS)
    [ -n "${LG_KEYSTORE_PASS:-}" ] || echo "apksigner asks for the keystore's password now (LG_KEYSTORE_PASS in ~/.config/localghost/release.env stops the asking)"
    "$BT/apksigner" sign "${SIGNARGS[@]}" --out "$APPOUT" "$W/aligned.apk" || { echo "signing the app failed"; return 1; }
    rm -f "$W/aligned.apk"
    echo "the app built and signed: $APPOUT"
}
if [ -z "$NOAPK" ]; then
    if [ -n "$APK" ]; then
        mkdir -p "$OUT/app"
        cp "$APK" "$APPOUT"
        [ -s "$APK.asc" ] && cp "$APK.asc" "$APPOUT.asc"
    else
        build_apk || { echo "no app, so no release: the server set is in $OUT/server, the cut is not complete. Fix the above and run the cut again (the tag $TAG is pinned, the same bytes come out), or --no-apk on purpose." >&2; exit 1; }
    fi
fi
if [ -s "$APPOUT" ]; then
    [ -s "$APPOUT.asc" ] || sign "$APPOUT"
    # what the APK says of itself, when the build tools are here to ask
    [ -n "$BT" ] || find_sdk || true
    # (read whole, never `head`: under pipefail a reader that closes early gives the tool
    # SIGPIPE, the substitution fails, and set -e ends the cut without a word, as it did once
    # right after "the app built and signed")
    CERT=""; VN=""; VC=""
    if [ -n "$BT" ] && [ -x "$BT/apksigner" ]; then
        CERT="$("$BT/apksigner" verify --print-certs "$APPOUT" 2>/dev/null | awk 'tolower($0) ~ /certificate sha-256/ && !c { c = $NF } END { print c }' || true)"
    fi
    if [ -n "$BT" ] && [ -x "$BT/aapt2" ]; then
        BADGE="$("$BT/aapt2" dump badging "$APPOUT" 2>/dev/null | sed -n '/^package:/p' || true)"
        VN="$(printf '%s' "$BADGE" | sed -n "s/.*versionName='\([^']*\)'.*/\1/p")"
        VC="$(printf '%s' "$BADGE" | sed -n "s/.*versionCode='\([^']*\)'.*/\1/p")"
        if [ -n "$VN" ] && [ "$VN" != "$VERSION" ]; then
            echo "the APK says it is version $VN, the release is $VERSION (app/android/app/build.gradle.kts versionName): not a release" >&2
            exit 1
        fi
    fi
    {
        echo "app=localghost-app-$VERSION.apk"
        echo "version=$VERSION"
        echo "name=$NAME"
        echo "commit=$COMMIT"
        echo "sha256=$(sha256sum "$APPOUT" | cut -d' ' -f1)"
        [ -n "$VN" ] && echo "versionName=$VN"
        [ -n "$VC" ] && echo "versionCode=$VC"
        [ -n "$CERT" ] && echo "signingCertSha256=$CERT"
        echo "built=from the tree at commit $COMMIT, signed with the app's keystore; the signing certificate is what the app shows under VERIFY BUILD"
    } > "$OUT/app/APP.txt"
fi
# the whole tree at the tag: the source, and how a box is set up from it
( cd "$W/src" && git archive --format=tar.gz --prefix="localghost-$VERSION/" -o "$OUT/source/localghost-$VERSION-source.tar.gz" "$TAG" )
# the sums over everything, signed
cd "$OUT"
# the sums over the files, not over the signatures (a GPG signature carries its time, so it is never
# the same twice; the sums are, when the build is)
find . -type f ! -name SHA256SUMS ! -name '*.asc' | sed 's|^\./||' | LC_ALL=C sort | xargs sha256sum > SHA256SUMS
sign SHA256SUMS
say "$NAME $VERSION ($COMMIT) in $OUT"
cat SHA256SUMS
[ -s "$APPOUT" ] || echo "(no app in this cut, --no-apk: the server set and the source are here; a release with the app is cut with the SDK and a keystore on this machine, or --apk)"
echo
echo "next:"
echo "  git push origin $TAG"
# GitHub flattens the folders: every asset is its file name, so two files of one name collide and
# the upload stops at the second. The server set's own SHA256SUMS (release_build.sh writes it for
# the mirror) says nothing the release's SHA256SUMS does not, so it stays home; every other name
# is unique across server/, app/ and source/.
FILES="$(cd "$OUT" && find . -type f ! -path './server/SHA256SUMS' | sed "s|^\./|$OUT/|" | LC_ALL=C sort | tr '\n' ' ')"
if command -v gh >/dev/null 2>&1; then
    echo "  gh release create $TAG --title \"$NAME $VERSION\" --notes-file $HERE/$NOTES $FILES"
else
    echo "  the GitHub release, from a machine with gh (sudo apt install gh puts it on this one):"
    echo "    gh release create $TAG --title \"$NAME $VERSION\" --notes-file $NOTES \$(find release/$VERSION -type f ! -path '*/server/SHA256SUMS')"
    echo "  or in the browser: https://github.com/LocalGhostDao/localghost/releases/new?tag=$TAG"
    echo "    title \"$NAME $VERSION\", the notes from $NOTES, every file under $OUT attached"
fi
echo "  then point the mirror's server set at the release's server files (the web repo's mirror.conf) and publish"
echo
echo "to cut it again anywhere: git clone … && ./tools/cut_release.sh $VERSION (the same bytes, from $TAG)"
