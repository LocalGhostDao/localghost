#!/usr/bin/env bash
# Verify the current checkout matches our signed source manifest.
# Run from the repo root after `git checkout <commit>`.
#
#   tools/verify_source.sh
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

MANIFEST_FILE="ghost/source-manifest.txt"
MANIFEST_SIG="ghost/source-manifest.txt.asc"
[ -f "$MANIFEST_FILE" ] || { echo "no $MANIFEST_FILE here"; exit 1; }

# 1. signature
if [ -f "$MANIFEST_SIG" ]; then
    if gpg --verify "$MANIFEST_SIG" "$MANIFEST_FILE" 2>/tmp/lg_gpg; then
        echo "signature: OK"
    else
        echo "signature: FAILED"; cat /tmp/lg_gpg; exit 1
    fi
else
    echo "signature: (no .asc present, skipping)"
fi

# 2. root hash (compare to BuildConfig.MANIFEST_ROOT shown in the app)
echo "root hash : $(sha256sum "$MANIFEST_FILE" | awk '{print $1}')"

# 3. every file matches
fail=0
grep "^[a-f0-9]" "$MANIFEST_FILE" | while read -r want path; do
    [ -f "$path" ] || { echo "MISSING: $path"; fail=1; continue; }
    got=$(sha256sum "$path" | awk '{print $1}')
    [ "$got" = "$want" ] || { echo "CHANGED: $path"; fail=1; }
done

echo "done. (any MISSING/CHANGED lines above = mismatch)"
