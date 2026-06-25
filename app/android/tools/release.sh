#!/usr/bin/env bash
# Build a verifiable, signed release APK on Debian. Run from the repo root.
#
#   source ~/.localghost_android_env   # JAVA_HOME, ANDROID_HOME, PATH (from debian_setup.sh)
#   tools/release.sh
#
# Produces a signed app-release.apk, the signed source manifest, and the hashes to publish.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

: "${ANDROID_HOME:?source ~/.localghost_android_env first}"
KEYSTORE="${LG_KEYSTORE:?set LG_KEYSTORE to your release keystore path}"
KEY_ALIAS="${LG_KEY_ALIAS:?set LG_KEY_ALIAS}"

echo "> 1/6  Checking the tree is clean..."
if [ -n "$(git status --porcelain)" ]; then
    echo "  ERROR: working tree is dirty. Commit everything first — releases must be clean-tree."
    git status --short
    exit 1
fi
COMMIT="$(git rev-parse HEAD)"
echo "  commit: $COMMIT"

echo "> 2/6  Signing the source manifest (info@localghost.ai)..."
tools/sign_source.sh
# The manifest + root are new files → commit them so the released build's tree stays clean and
# the commit the APK is stamped with actually contains the manifest it claims.
git add ghost/source-manifest.txt ghost/source-manifest.txt.asc MANIFEST.root
git commit -m "build: source manifest for release" >/dev/null
COMMIT="$(git rev-parse HEAD)"
echo "  manifest committed, release commit: $COMMIT"

echo "> 3/6  Writing release local.properties (SDK path + EMPTY box values)..."
# Public release bakes in NO box URL/token — the app reads them from encrypted storage at setup.
cat > local.properties <<PROPS
sdk.dir=$ANDROID_HOME
NAS_BASE_URL=
DEVICE_TOKEN=
PROPS

echo "> 4/6  Building release APK..."
./gradlew --no-daemon clean assembleRelease

UNSIGNED="app/build/outputs/apk/release/app-release-unsigned.apk"
SIGNED="app/build/outputs/apk/release/app-release.apk"
APK_IN="$([ -f "$UNSIGNED" ] && echo "$UNSIGNED" || echo "$SIGNED")"

echo "> 5/6  Zipalign + sign the APK..."
ZIPALIGN="$ANDROID_HOME/build-tools/36.0.0/zipalign"
APKSIGNER="$ANDROID_HOME/build-tools/36.0.0/apksigner"
"$ZIPALIGN" -f 4 "$APK_IN" "$SIGNED.aligned"
"$APKSIGNER" sign --ks "$KEYSTORE" --ks-key-alias "$KEY_ALIAS" \
    --out "$SIGNED" "$SIGNED.aligned"
rm -f "$SIGNED.aligned"
"$APKSIGNER" verify --print-certs "$SIGNED"

echo "  GPG-signing the APK with info@localghost.ai (ties it to the website/source identity)..."
gpg --batch --yes --armor --local-user info@localghost.ai \
    --output "$SIGNED.asc" --detach-sign "$SIGNED"

echo "> 6/6  Hashes to publish in the GitHub release..."
APK_SHA="$(sha256sum "$SIGNED" | awk '{print $1}')"
CERT_SHA="$("$APKSIGNER" verify --print-certs "$SIGNED" | grep -i 'SHA-256' | head -1)"
MANIFEST_ROOT="$(cat MANIFEST.root)"

echo
echo "==================== PUBLISH THESE ===================="
echo "commit         : $COMMIT"
echo "apk sha-256    : $APK_SHA"
echo "manifest root  : $MANIFEST_ROOT"
echo "signing cert   : $CERT_SHA"
echo "apk            : $SIGNED"
echo "apk gpg sig    : $SIGNED.asc"
echo "======================================================="
echo
echo "Next: push the commit, create a GitHub release/tag, attach the APK + its .asc, and paste"
echo "values above into the release notes. The in-app VERIFY BUILD screen will match them."
