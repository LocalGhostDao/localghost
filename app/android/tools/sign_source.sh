#!/usr/bin/env bash
# Sign the LocalGhost app source — same convention as the website deploy-manifest.
# Run from the repo root on a CLEAN git tree, before the build.
#
#   tools/sign_source.sh
#
# Produces (under ghost/, mirroring the website layout):
#   ghost/source-manifest.txt       header + "sha256  path" for every tracked source file
#   ghost/source-manifest.txt.asc   detached GPG signature (info@localghost.ai)
#   MANIFEST.root                    sha256 of source-manifest.txt (stamped into BuildConfig)
#
# DETERMINISM: the manifest must be a pure function of the committed source, or MANIFEST.root (which
# is stamped into the APK via BuildConfig.MANIFEST_ROOT) changes between builds of identical source
# and the APK never reproduces. So the header timestamp is the COMMIT's committer time, not the
# wall clock. The detached .asc signature is allowed to vary (GPG embeds its own signing time); we
# hash the manifest, never the signature, so that does not affect reproducibility.
set -euo pipefail
# Anchor to the Android project root (this script lives in <project>/tools). We do NOT use the git
# top-level, because the Android app is a subdirectory of the localghost monorepo and the manifest,
# build-env, and APK paths are all relative to the app, not the monorepo root. git commands still
# work from here (git operates from any subdir); git ls-files run from here lists only this app's
# files, app-relative, which is exactly the manifest scope we want.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

GPG_USER="${LG_GPG_USER:-info@localghost.ai}"
BUILD_ID="$(git rev-parse HEAD)"
# Deterministic: the commit's committer date in strict ISO-8601 UTC. Same commit -> same line.
SIGNED_AT="$(git log -1 --pretty=%cI HEAD)"

mkdir -p ghost
MANIFEST_FILE="ghost/source-manifest.txt"
MANIFEST_SIG="ghost/source-manifest.txt.asc"

{
    echo "# LocalGhost App Source Manifest"
    echo "# Build: ${BUILD_ID}"
    echo "# Signed: ${SIGNED_AT}"
    echo ""
    # Every tracked file except build outputs, the manifest, its signature, and the root hash,
    # hashed and sorted by path under a fixed locale so the ordering is deterministic. git ls-files
    # already lists only tracked files, so gitignored build/.gradle/local.properties never appear;
    # the build/ exclusions are belt-and-suspenders.
    git ls-files \
      | grep -vE '^(ghost/source-manifest\.txt(\.asc)?$|MANIFEST\.root$|.*/build/|build/)' \
      | LC_ALL=C sort \
      | while IFS= read -r f; do [ -f "$f" ] && sha256sum "$f"; done \
      | sed "s|  | |" | LC_ALL=C sort -k2
} > "$MANIFEST_FILE"

gpg --batch --yes --armor --local-user "$GPG_USER" \
    --output "$MANIFEST_SIG" --detach-sign "$MANIFEST_FILE"

# Root hash = sha256 of the manifest, stamped into the APK via BuildConfig.MANIFEST_ROOT.
sha256sum "$MANIFEST_FILE" | awk '{print $1}' > MANIFEST.root

FILE_COUNT=$(grep -c "^[a-f0-9]" "$MANIFEST_FILE")
echo "  [lock] source-manifest.txt (${FILE_COUNT} files, signed-at ${SIGNED_AT})"
echo "  [lock] source-manifest.txt.asc"
echo "  [hash] MANIFEST.root: $(cat MANIFEST.root)"