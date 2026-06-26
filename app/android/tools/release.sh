#!/usr/bin/env bash
# Build a verifiable, signed release APK on Debian. Run from the repo root.
#
#   source ~/.localghost_android_env   # JAVA_HOME, ANDROID_HOME, PATH (from debian_setup.sh)
#   tools/release.sh
#
# Produces a signed app-release.apk, its GPG detached signature, the signed source manifest, and the
# hashes to publish. Two APK hashes are published for two different audiences:
#   - UNSIGNED apk sha-256: what a third party matches after rebuilding from source themselves. This
#     is the REPRODUCIBILITY check. It does not depend on the private signing key.
#   - SIGNED apk sha-256 + signing cert: what someone downloading THIS release verifies for
#     integrity. The signature comes from the private key and cannot be reproduced by others.
#
# Reproducibility requires a fixed build clock and locale, or the APK's zip entry timestamps vary and
# the bytes never match. We pin SOURCE_DATE_EPOCH (from the release commit), TZ, and LC_ALL up front.
set -euo pipefail
# Anchor to the Android project root (this script lives in <project>/tools). We do NOT use the git
# top-level, because the Android app is a subdirectory of the localghost monorepo and the manifest,
# build-env, and APK paths are all relative to the app, not the monorepo root. git commands still
# work from here (git operates from any subdir); git ls-files run from here lists only this app's
# files, app-relative, which is exactly the manifest scope we want.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

: "${ANDROID_HOME:?source ~/.localghost_android_env first}"
KEYSTORE="${LG_KEYSTORE:?set LG_KEYSTORE to your release keystore path}"
KEY_ALIAS="${LG_KEY_ALIAS:?set LG_KEY_ALIAS}"
GPG_USER="${LG_GPG_USER:-info@localghost.ai}"

# --- Determinism: pin the build clock, timezone and locale BEFORE anything builds. -------------
# SOURCE_DATE_EPOCH is the release commit's committer time, so the APK's zip entry mtimes are fixed
# and a rebuild from the same commit yields identical bytes. TZ/LC_ALL remove locale-dependent
# ordering and timestamps. These must be exported before writeBuildEnv (which records the epoch) and
# before the build, so move them to the very top.
export TZ=UTC
export LC_ALL=C.UTF-8
export LANG=C.UTF-8
COMMIT="$(git rev-parse HEAD)"
export SOURCE_DATE_EPOCH="$(git log -1 --pretty=%ct "$COMMIT")"
echo "  determinism: SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH TZ=$TZ LC_ALL=$LC_ALL"

# --- Preflight: fail early on missing signing material, not after a long build. -----------------
echo "> 0/6  Preflight checks..."
[ -f "$KEYSTORE" ] || { echo "  ERROR: keystore not found at $KEYSTORE"; exit 1; }
if ! gpg --list-secret-keys "$GPG_USER" >/dev/null 2>&1; then
    echo "  ERROR: no GPG secret key for $GPG_USER; the APK GPG-signing step would fail at the end."
    exit 1
fi

echo "> 1/6  Checking the tree is clean..."
if [ -n "$(git status --porcelain)" ]; then
    echo "  ERROR: working tree is dirty. Commit everything first — releases must be clean-tree."
    git status --short
    exit 1
fi
echo "  commit: $COMMIT"

echo "> 2/6  Writing build environment + signing the source manifest..."
./gradlew --no-daemon --no-configuration-cache writeBuildEnv
git add ghost/build-env.txt                 # track it first so the manifest hashes it
tools/sign_source.sh
# The manifest + root are new files → commit them so the released build's tree stays clean and the
# commit the APK is stamped with actually contains the manifest it claims.
git add ghost/build-env.txt ghost/source-manifest.txt ghost/source-manifest.txt.asc MANIFEST.root
git commit -m "build: source manifest for release" >/dev/null
COMMIT="$(git rev-parse HEAD)"
# The release commit changed, so re-pin the epoch to THIS commit (the one the APK is stamped with and
# the one a reproducer will check out).
export SOURCE_DATE_EPOCH="$(git log -1 --pretty=%ct "$COMMIT")"
echo "  manifest committed, release commit: $COMMIT (epoch re-pinned: $SOURCE_DATE_EPOCH)"

echo "> 3/6  Writing release local.properties (SDK path + EMPTY box values)..."
# Public release bakes in NO box URL/token — the app reads them from encrypted storage at setup.
# local.properties is gitignored and machine-specific; writing it here does not dirty the tree.
cat > local.properties <<PROPS
sdk.dir=$ANDROID_HOME
NAS_BASE_URL=
DEVICE_TOKEN=
PROPS

echo "> 4/6  Building release APK (clean, deterministic clock)..."
./gradlew --no-daemon --no-configuration-cache clean assembleRelease

UNSIGNED="app/build/outputs/apk/release/app-release-unsigned.apk"
SIGNED="app/build/outputs/apk/release/app-release.apk"
APK_IN="$([ -f "$UNSIGNED" ] && echo "$UNSIGNED" || echo "$SIGNED")"

# Capture the UNSIGNED, pre-signing hash now — this is the reproducibility anchor. A third party who
# rebuilds from the release commit with the same toolchain (see ghost/build-env.txt) and the same
# SOURCE_DATE_EPOCH should get THIS hash. We zipalign first so the comparison is against the aligned
# layout, which is what a reproducer also produces.
echo "> 5/6  Zipalign, hash the reproducible artifact, then sign..."
ZIPALIGN="$ANDROID_HOME/build-tools/36.0.0/zipalign"
APKSIGNER="$ANDROID_HOME/build-tools/36.0.0/apksigner"

"$ZIPALIGN" -f 4 "$APK_IN" "$SIGNED.aligned"
UNSIGNED_SHA="$(sha256sum "$SIGNED.aligned" | awk '{print $1}')"  # the reproducible bytes

"$APKSIGNER" sign --ks "$KEYSTORE" --ks-key-alias "$KEY_ALIAS" \
    --out "$SIGNED" "$SIGNED.aligned"
rm -f "$SIGNED.aligned"
"$APKSIGNER" verify --print-certs "$SIGNED" >/dev/null

echo "  GPG-signing the APK with $GPG_USER (ties it to the website/source identity)..."
gpg --batch --yes --armor --local-user "$GPG_USER" \
    --output "$SIGNED.asc" --detach-sign "$SIGNED"

echo "> 6/6  Hashes to publish in the GitHub release..."
SIGNED_SHA="$(sha256sum "$SIGNED" | awk '{print $1}')"
# Grab the CERTIFICATE digest specifically (apksigner prints several SHA-256 lines: the cert digest
# and the public-key digest). Match the cert line so verifiers compare the right field.
CERT_SHA="$("$APKSIGNER" verify --print-certs "$SIGNED" \
    | grep -iE 'Signer #1 certificate SHA-256' | head -1 | awk -F': ' '{print $2}')"
MANIFEST_ROOT="$(cat MANIFEST.root)"

echo
echo "==================== PUBLISH THESE ===================="
echo "commit              : $COMMIT"
echo "source_date_epoch   : $SOURCE_DATE_EPOCH   (reproducers must export this)"
echo "manifest root       : $MANIFEST_ROOT"
echo "--- reproducibility (rebuild from source, compare this) ---"
echo "unsigned apk sha-256: $UNSIGNED_SHA"
echo "--- integrity of THIS download (cannot be reproduced) ---"
echo "signed apk sha-256  : $SIGNED_SHA"
echo "signing cert sha-256: $CERT_SHA"
echo "apk                 : $SIGNED"
echo "apk gpg sig         : $SIGNED.asc"
echo "======================================================="
echo
echo "Reproducer's path: check out \$commit, source the same toolchain (ghost/build-env.txt),"
echo "export SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH TZ=UTC LC_ALL=C.UTF-8, run a clean assembleRelease"
echo "+ zipalign, and compare against 'unsigned apk sha-256'. The signature is yours alone; the"
echo "'signed apk sha-256' + cert are for people verifying the downloaded release, not rebuilders."
echo
echo "Next: push the commit, create a GitHub release/tag, attach the APK + its .asc, and paste the"
echo "values above into the release notes. The in-app VERIFY BUILD screen will match them."