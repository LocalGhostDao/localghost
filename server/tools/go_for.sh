#!/bin/sh
# go_for.sh , prints the path of the Go to build with: the one whose version is go.mod's, looked for
# at /usr/local/go/bin/go (where install_go.sh puts the mirror's), /usr/local/bin/go, then whatever
# `go` is on PATH; failing an exact match, the newest of them; failing any, "go".
#
# Exit 0 when what it printed is go.mod's version, 1 otherwise (older, newer, or none). The
# Makefile, redeploy.sh, cut_release.sh and release_build.sh all take their Go from here, so a
# user whose PATH still points at an older Go (a login shell's PATH on 3 Oct 2026 found 1.25.4
# while /usr/local/go held 1.27.1) builds with the right one all the same.
REPO="$(cd "$(dirname "$0")/.." && pwd)"
WANT="$(awk '$1 == "go" { print $2; exit }' "$REPO/go.mod" 2>/dev/null)"
best=""
bestv=""
for g in /usr/local/go/bin/go /usr/local/bin/go "$(command -v go 2>/dev/null || true)"; do
    [ -n "$g" ] && [ -x "$g" ] || continue
    # asked from / : inside the module an older Go refuses even `go version` under GOTOOLCHAIN=local
    v="$(cd / && GOTOOLCHAIN=local "$g" version 2>/dev/null | sed 's/.*go\([0-9][0-9.]*\).*/\1/')"
    [ -n "$v" ] || continue
    if [ "$v" = "$WANT" ]; then
        echo "$g"
        exit 0
    fi
    if [ -z "$best" ] || [ "$(printf '%s\n%s\n' "$bestv" "$v" | sort -V | tail -1)" = "$v" ]; then
        best="$g"
        bestv="$v"
    fi
done
echo "${best:-go}"
exit 1
