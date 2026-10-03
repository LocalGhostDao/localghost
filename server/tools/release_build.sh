#!/usr/bin/env bash
# release_build.sh <version> [outdir] , build a server release for the mirror's "server" set.
#
#   ./tools/release_build.sh 0.9.3                  from a clean tree at tag v0.9.3 (or any commit)
#   ./tools/release_build.sh 0.9.3 /tmp/rel         the set's files land in /tmp/rel/server/
#
# A release's name (0.0.1 is "wisp") comes from tools/release.names, committed with the code.
#
# What it makes, in <outdir>/server/ (default ./release/server/), for the web repo's mirror publish
# to put under /<build>/server/ and sign into MANIFEST.txt like every other set:
#   localghost-server-<version>-linux-amd64.tar.gz   VERSION, COMMIT, CHANGES.txt, NOTES.md, bin/, tools/
#   RELEASE.txt    what the phone shows before anyone downloads anything: version, commit, date,
#                  and the commits since the last tag, one line each
#   NOTES.md       the release's notes (releases/<version>.md), the same text as in the bundle
#   NOTICE.txt, TERMS-MIT.txt   the set's notice and licence, which travel with every set
#
# REPRODUCIBLE: CGO off, -trimpath, no build id, the tar sorted with the commit's time on every
# entry and owner 0, gzip without a name or time. The same commit gives the same bytes on any
# machine, so anyone can rebuild a published release and compare its SHA-256 with the manifest's.
#
# In the bundle: ghost.secd, the cohort (every cmd/ghost.<x>), ghost-cli, ghost-ctl, and the tools
# secd needs to take the next release (mirror_fetch.sh and the site key it pins). Not in it:
# ghost-update-guard (what undoes a bad release is never replaced by one; redeploy.sh installs it),
# llama-server and whisper-cli (built on the box from the mirror's pinned sources: update.sh engine,
# update.sh speech), and the setup tools.
set -euo pipefail

VERSION="${1:?usage: release_build.sh <version> [outdir]}"
case "$VERSION" in *[!A-Za-z0-9._+-]*) echo "a version is [A-Za-z0-9._+-]" >&2; exit 2 ;; esac
HERE="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${2:-$HERE/release}/server"
GO="${GO:-go}"
cd "$HERE"
# the Go named in go.mod and no other (the bytes differ from one Go to the next; GHOST_GO_ANY=1 builds
# with another on purpose), and never one fetched from the internet by the go command itself
export GOTOOLCHAIN=local
GO_MOD_VER="$(awk '$1 == "go" { print $2; exit }' go.mod)"
GO_HAVE="$("$GO" version 2>/dev/null | sed 's/.*go\([0-9][0-9.]*\).*/\1/')"
if [ "$GO_HAVE" != "$GO_MOD_VER" ] && [ "${GHOST_GO_ANY:-}" != 1 ]; then
    echo "go ${GO_HAVE:-none} here, go.mod names $GO_MOD_VER: a release is built with that Go and no other (sudo ./tools/install_go.sh; GHOST_GO_ANY=1 to build with this one anyway)" >&2
    exit 1
fi

# the release's name, from tools/release.names ("<version> <name>" a line), so the same commit
# names it the same way on any machine; "" for a version without one
RELNAME="$(awk -v v="$VERSION" '$1 == v { print $2; exit }' tools/release.names 2>/dev/null || true)"
case "$RELNAME" in *[!a-z0-9-]*) echo "a release name is [a-z0-9-] (tools/release.names)" >&2; exit 2 ;; esac
COMMIT="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
if [ -n "$(git status --porcelain 2>/dev/null)" ]; then
    echo "the tree has uncommitted changes: a release is built from a commit" >&2
    exit 1
fi
EPOCH="$(git log -1 --format=%ct 2>/dev/null || date +%s)"
PREV="$(git describe --tags --abbrev=0 HEAD^ 2>/dev/null || true)"

W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT
mkdir -p "$W/b/bin" "$W/b/tools" "$OUT"

LDFLAGS="-s -w -buildid= -X github.com/LocalGhostDao/localghost/server/internal/secd.Version=$VERSION -X github.com/LocalGhostDao/localghost/server/internal/secd.ReleaseName=$RELNAME"
build() { CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$GO" build -trimpath -ldflags "$LDFLAGS" -o "$W/b/bin/$1" "./cmd/$1"; }
build ghost.secd
for d in cmd/ghost.*; do
    n="$(basename "$d")"
    [ "$n" = ghost.secd ] && continue
    build "$n"
done
build ghost-cli
build ghost-ctl
install -m755 tools/mirror_fetch.sh "$W/b/tools/mirror_fetch.sh"
install -m644 tools/mirror-key.asc "$W/b/tools/mirror-key.asc"
# the release's notes (releases/<version>.md: what it does, what is in it, how it works), in the
# bundle and beside it in the set
if [ -s "releases/$VERSION.md" ]; then
    install -m644 "releases/$VERSION.md" "$W/b/NOTES.md"
    install -m644 "releases/$VERSION.md" "$OUT/NOTES.md"
fi

echo "$VERSION" > "$W/b/VERSION"
echo "$COMMIT" > "$W/b/COMMIT"
if [ -n "$PREV" ]; then
    git log --no-merges --format='%h %s' "$PREV..HEAD" > "$W/b/CHANGES.txt"
else
    git log --no-merges --format='%h %s' -n 50 > "$W/b/CHANGES.txt" 2>/dev/null || : > "$W/b/CHANGES.txt"
fi

NAME="localghost-server-$VERSION-linux-amd64.tar.gz"
( cd "$W/b" && tar --sort=name --mtime="@$EPOCH" --owner=0 --group=0 --numeric-owner --format=gnu \
      -cf - VERSION COMMIT CHANGES.txt bin tools $( [ -f NOTES.md ] && echo NOTES.md ) ) | gzip -n -9 > "$OUT/$NAME"

{
    echo "version=$VERSION"
    [ -n "$RELNAME" ] && echo "name=$RELNAME"
    echo "commit=$COMMIT"
    echo "date=$(date -u -d "@$EPOCH" +%Y-%m-%dT%H:%M:%SZ)"
    echo "go=$GO_HAVE"
    echo "bundle=$NAME"
    echo "since=${PREV:-the first release}"
    echo "changes:"
    sed 's/^/  /' "$W/b/CHANGES.txt"
} > "$OUT/RELEASE.txt"
cat > "$OUT/NOTICE.txt" <<EOF
LocalGhost server ${RELNAME:+$RELNAME }$VERSION (commit $COMMIT), built reproducibly from the public repository:
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -buildid=" for linux/amd64 with Go $GO_HAVE (tools/release_build.sh;
the same Go gives the same bytes, another Go gives others).
The box checks this set's signature and hashes before it puts a release on, and rolls it back if
the release fails its first unlock.
EOF
cp LICENSE "$OUT/TERMS-MIT.txt" 2>/dev/null || cp ../LICENSE "$OUT/TERMS-MIT.txt"

echo "release ${RELNAME:+$RELNAME }$VERSION ($COMMIT) in $OUT:"
( cd "$OUT" && sha256sum ./* | sed 's|  \./|  |' )
