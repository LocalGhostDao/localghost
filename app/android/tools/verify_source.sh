#!/usr/bin/env bash
# Verify the current checkout matches our signed source manifest.
# Run from the repo root after `git checkout <commit>`.
#
#   tools/verify_source.sh
#
# Exits 0 ONLY if: the signature is valid, every manifest file is present and unchanged, AND no
# tracked source file is missing from the manifest (so nothing can be added without detection).
# Any failure exits non-zero, so CI / automation can trust the exit code, not just the printout.
set -euo pipefail
# Anchor to the Android project root (this script lives in <project>/tools). We do NOT use the git
# top-level, because the Android app is a subdirectory of the localghost monorepo and the manifest,
# build-env, and APK paths are all relative to the app, not the monorepo root. git commands still
# work from here (git operates from any subdir); git ls-files run from here lists only this app's
# files, app-relative, which is exactly the manifest scope we want.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

MANIFEST_FILE="ghost/source-manifest.txt"
MANIFEST_SIG="ghost/source-manifest.txt.asc"
[ -f "$MANIFEST_FILE" ] || { echo "no $MANIFEST_FILE here"; exit 1; }

fail=0

# 1. signature over the manifest.
if [ -f "$MANIFEST_SIG" ]; then
    if gpg --verify "$MANIFEST_SIG" "$MANIFEST_FILE" 2>/tmp/lg_gpg; then
        echo "signature : OK"
    else
        echo "signature : FAILED"; cat /tmp/lg_gpg; fail=1
    fi
else
    echo "signature : (no .asc present, skipping)"
fi

# 2. root hash. Compare against the committed MANIFEST.root so the manifest itself was not swapped.
ROOT_NOW="$(sha256sum "$MANIFEST_FILE" | awk '{print $1}')"
echo "root hash : $ROOT_NOW"
if [ -f MANIFEST.root ]; then
    ROOT_WANT="$(cat MANIFEST.root)"
    if [ "$ROOT_NOW" != "$ROOT_WANT" ]; then
        echo "  MISMATCH: MANIFEST.root says $ROOT_WANT but the manifest hashes to $ROOT_NOW"
        fail=1
    fi
fi

# 3. every manifest file present and unchanged. Use process substitution (NOT a pipe) so the loop
#    runs in THIS shell and 'fail' survives — a piped while-loop runs in a subshell and the failure
#    flag is lost, which would make the verifier report success on a mismatch (fail-open).
declare -A in_manifest
while read -r want path; do
    in_manifest["$path"]=1
    if [ ! -f "$path" ]; then
        echo "MISSING : $path"; fail=1; continue
    fi
    got="$(sha256sum "$path" | awk '{print $1}')"
    if [ "$got" != "$want" ]; then
        echo "CHANGED : $path"; fail=1
    fi
done < <(grep "^[a-f0-9]" "$MANIFEST_FILE")

# 4. no EXTRA tracked source file outside the manifest. An attacker who ADDS a file the manifest does
#    not list must be caught too, so verification is exhaustive in both directions. Mirror the exact
#    exclusions sign_source.sh used, so legitimately-unmanifested paths (build outputs, the manifest,
#    its sig, the root) are not flagged.
while IFS= read -r f; do
    [ -f "$f" ] || continue
    if [ -z "${in_manifest[$f]:-}" ]; then
        echo "EXTRA   : $f (tracked but not in the manifest)"; fail=1
    fi
done < <(git ls-files | grep -vE '^(ghost/source-manifest\.txt(\.asc)?$|MANIFEST\.root$|.*/build/|build/)')

echo
if [ "$fail" -eq 0 ]; then
    echo "VERIFIED: signature valid, all files present and unchanged, nothing added."
    exit 0
else
    echo "FAILED: see MISSING/CHANGED/EXTRA lines above. This checkout does NOT match the manifest."
    exit 1
fi