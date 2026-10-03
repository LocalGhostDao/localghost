#!/bin/sh
# install_go.sh , the Go toolchain the server builds with, system-wide at /usr/local/go, from the
# LocalGhost mirror. Root. Idempotent: a system Go at least as new as go.mod asks for is left alone.
#
#   sudo ./tools/install_go.sh            installs GO_PIN when the box's Go is older than go.mod's
#   sudo ./tools/install_go.sh --check    says which Go the box has and which it needs; exit 0 when fine
#
# setup.sh runs this at bring-up and redeploy.sh before every build, so a go.mod that moves to a newer
# Go (a security release of the standard library, say) carries the box with it at the next
# redeploy, from the mirror and nowhere else. The tarball comes through tools/mirror_fetch.sh: the
# mirror's manifest signed by the site key pinned in tools/mirror-key.asc, the file's SHA-256
# against that manifest, which the publisher checked against go.dev's own checksum before signing.
# A mirror that cannot be reached, or does not list the tarball yet, stops here, and the caller
# stops with it: a box never installs a file that was not in a signed manifest.
# GHOST_MIRROR_UPSTREAM=1 is the operator's explicit exception (dl.google.com, checked against
# go.dev's checksum list).
#
# System-wide, not $HOME: every context (login shells, sudo, systemd, the wizard) sees the same
# compiler. The build itself runs with GOTOOLCHAIN=local (Makefile), so a Go that is too old fails
# the build out loud rather than fetching a newer one from the internet by itself.
set -eu

# the version the mirror carries; bump it with go.mod (the two should agree, go.mod is the floor)
GO_PIN="${GHOST_GO_PIN:-1.27.1}"
REPO="$(cd "$(dirname "$0")/.." && pwd)"
GO_MIN="$(awk '$1 == "go" { print $2; exit }' "$REPO/go.mod" 2>/dev/null)"; GO_MIN="${GO_MIN:-$GO_PIN}"
CHECK=0
[ "${1:-}" = "--check" ] && CHECK=1

newer_or_same() { [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -1)" = "$1" ]; } # $2 >= $1

# the Go the build will use (tools/go_for.sh: /usr/local/go first, then PATH; go.mod's version
# when any candidate has it, else the newest)
GOBIN="$(sh "$REPO/tools/go_for.sh" 2>/dev/null || true)"
HAVE=""
[ -n "$GOBIN" ] && [ -x "$GOBIN" ] && HAVE="$(cd / && GOTOOLCHAIN=local "$GOBIN" version 2>/dev/null | sed 's/.*go\([0-9][0-9.]*\).*/\1/')"
if [ -n "$HAVE" ] && newer_or_same "$GO_MIN" "$HAVE"; then
    echo "  go: $HAVE at $GOBIN (go.mod asks for $GO_MIN)"
    exit 0
fi
if [ "$CHECK" = 1 ]; then
    echo "  go: ${HAVE:-none} on the box, go.mod asks for $GO_MIN: sudo ./tools/install_go.sh installs $GO_PIN from the mirror"
    exit 1
fi
if [ "$(id -u)" != 0 ]; then
    echo "  go: ${HAVE:-none} on the box, go.mod asks for $GO_MIN; run as root to install $GO_PIN: sudo ./tools/install_go.sh" >&2
    exit 1
fi

ARCH="$(dpkg --print-architecture 2>/dev/null || echo amd64)"
TARBALL="go${GO_PIN}.linux-${ARCH}.tar.gz"
echo "  go: ${HAVE:-none} on the box, go.mod asks for $GO_MIN: installing ${GO_PIN} system-wide (/usr/local/go)..."
TMPD="$(mktemp -d)"
trap 'rm -rf "$TMPD"' EXIT
command -v gpg >/dev/null 2>&1 || apt-get install -y gpg >/dev/null 2>&1 || true
WANT_SHA=""
GOT_SHA=""
rc=0; sh "$REPO/tools/mirror_fetch.sh" go "$TMPD/mirror" "$TARBALL" || rc=$?
if [ "$rc" = 0 ] && [ -s "$TMPD/mirror/$TARBALL" ]; then
    mv "$TMPD/mirror/$TARBALL" "$TMPD/$TARBALL"
    GOT_SHA="$(sha256sum "$TMPD/$TARBALL" | awk '{print $1}')"
    WANT_SHA="$GOT_SHA"
    echo "  go: $TARBALL from the mirror, signature and hash checked"
elif [ "${GHOST_MIRROR_UPSTREAM:-}" = 1 ]; then
    echo "  go: !! GHOST_MIRROR_UPSTREAM=1: fetching $TARBALL from dl.google.com, checked against go.dev's"
    echo "      checksum list , NOT a file from the signed mirror manifest"
    curl -fsSL -o "$TMPD/$TARBALL" "https://dl.google.com/go/$TARBALL"
    # include=all: the default manifest lists only the latest patch of each stable branch, and a
    # pinned older patch would come back "not found"
    curl -fsSL 'https://go.dev/dl/?mode=json&include=all' -o "$TMPD/manifest.json"
    WANT_SHA="$(tr ',' '\n' < "$TMPD/manifest.json" | grep -A8 "\"filename\": \"$TARBALL\"" | grep '"sha256"' | head -1 | sed 's/.*"sha256": *"\([0-9a-f]*\)".*/\1/')"
    GOT_SHA="$(sha256sum "$TMPD/$TARBALL" | awk '{print $1}')"
    if [ -z "$WANT_SHA" ]; then
        echo "  go: $TARBALL not found in the release manifest , refusing to install unverified"
        grep -o '"version": "go1\.[0-9.]*"' "$TMPD/manifest.json" 2>/dev/null | sort -u | head -8 | sed 's/^/    /'
        exit 1
    fi
else
    if [ "$rc" = 3 ]; then
        echo "  go: the mirror does not list $TARBALL (set go) , not published there yet"
    else
        echo "  go: could not take $TARBALL from the mirror (see above)"
    fi
    echo "  go: stopping: a box installs nothing that was not in the signed mirror manifest."
    echo "      Re-run when the mirror answers, or install Go $GO_PIN yourself (go version >= $GO_MIN is used as is),"
    echo "      or GHOST_MIRROR_UPSTREAM=1 to take it from go.dev on your own authority."
    exit 1
fi
if [ "$WANT_SHA" != "$GOT_SHA" ]; then
    echo "  go: CHECKSUM MISMATCH (want $WANT_SHA, got ${GOT_SHA:-nothing}) , refusing to install"
    exit 1
fi
# unpack beside the old one and swap, so a failed unpack leaves the box with the Go it had
rm -rf /usr/local/go.new
mkdir -p /usr/local/go.new
tar -C /usr/local/go.new --strip-components=1 -xzf "$TMPD/$TARBALL"
# the mirror's notice and the licence terms of the set travel with what came from it
for f in "$TMPD/mirror/NOTICE.txt" "$TMPD/mirror"/TERMS-*.txt; do
    [ -f "$f" ] && cp "$f" "/usr/local/go.new/MIRROR-$(basename "$f")"
done
rm -rf /usr/local/go.old
[ -d /usr/local/go ] && mv /usr/local/go /usr/local/go.old
mv /usr/local/go.new /usr/local/go
rm -rf /usr/local/go.old
ln -sf /usr/local/go/bin/go /usr/local/bin/go
ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
echo "  go: $(/usr/local/bin/go version | awk '{print $3}') installed, on everyone's PATH via /usr/local/bin"
