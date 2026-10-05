#!/bin/sh
# install.sh , a box from the release alone: no git, no Go, no make. The release bundle carries
# the daemons, the setup tools and the operator scripts (tools/release_build.sh says what), so a
# new box is set up from the bundle the same way a developer's box is set up from the repository,
# with tools/setup.sh, without building anything first.
#
#   curl -LO https://github.com/LocalGhostDao/localghost/releases/download/v<version>/localghost-server-<version>-linux-amd64.tar.gz
#   curl -LO .../SHA256SUMS  .../SHA256SUMS.asc        (or the mirror's server set: the same files)
#   gpg --verify SHA256SUMS.asc SHA256SUMS && sha256sum -c --ignore-missing SHA256SUMS
#   mkdir localghost && tar xzf localghost-server-<version>-linux-amd64.tar.gz -C localghost
#   sudo localghost/tools/install.sh
#
# Verify before you unpack: this script cannot check the bundle it came from, and it is told so
# below. The site key is at https://www.localghost.ai/.well-known/pgp-key.asc (fingerprint
# DCE9 A3D1 4EB4 6197 1DD5 F393 706E 4194 F08A 09A0); the same key signs the mirror's manifests.
#
# What it does:
#   1. checks that this is a release (VERSION and COMMIT at the top, the daemons and ghost-setup
#      under bin/), and says so when it is a repository checkout instead (then tools/setup.sh);
#   2. puts the release where the box keeps it, /opt/localghost/release/<version>/, when it was
#      unpacked somewhere else (a home directory is hidden from the daemons, ProtectHome);
#   3. lays the operator scripts under /opt/localghost/tools and ghost-cli, ghost-ctl and ghost-qr
#      under /opt/localghost/bin, the same places a release taken from the phone puts them, so
#      `sudo /opt/localghost/tools/update.sh` and `/opt/localghost/bin/ghost-cli` are the box's
#      stable paths from the first day;
#   4. runs tools/setup.sh from the release: the database layer, the box prep, the engine and the
#      models, the data disk or the file, the seal tier, the PINs, the QR. setup.sh sees the
#      release's binaries and builds nothing.
#
# The service user (the one who installs and deploys; the daemons run as a system user of their
# own, ghostd) is the first argument, else the user who ran sudo. GHOST_LLAMA=0 skips the engine
# and the models (tools/update.sh engine brings them later); setup.sh's other switches apply.
set -eu

if [ "$(id -u)" != 0 ]; then
    echo "run as root: sudo $0 [service-user]"
    exit 1
fi
HERE="$(cd "$(dirname "$0")/.." && pwd)"
SVC_USER="${1:-}"
if [ -z "$SVC_USER" ] && [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ]; then
    SVC_USER="$SUDO_USER"
fi

# 1. a release, and no other thing
if [ ! -s "$HERE/VERSION" ] || [ ! -s "$HERE/COMMIT" ]; then
    if [ -f "$HERE/go.mod" ]; then
        echo "this is a repository checkout, not a release: run sudo ./tools/setup.sh (it builds first)"
    else
        echo "$HERE is not a LocalGhost release (no VERSION and COMMIT at its top)"
    fi
    exit 1
fi
for b in ghost-setup ghost.secd ghost.watchd ghost-cli ghost-ctl; do
    [ -x "$HERE/bin/$b" ] || { echo "the release at $HERE has no bin/$b , a bundle from 0.0.5 on carries it; unpack the whole file"; exit 1; }
done
VERSION="$(tr -d '[:space:]' < "$HERE/VERSION")"
COMMIT="$(tr -d '[:space:]' < "$HERE/COMMIT")"
case "$VERSION" in *[!A-Za-z0-9._+-]*|"") echo "VERSION reads '$VERSION', which is not a version"; exit 1 ;; esac
echo "LocalGhost server $VERSION ($(printf '%.12s' "$COMMIT")) at $HERE"
echo "  this script trusts the files as they are: verify SHA256SUMS.asc before unpacking, not after"

# 2. where the box keeps it
DEST="/opt/localghost/release/$VERSION"
if [ "$HERE" != "$DEST" ]; then
    mkdir -p "$DEST"
    # cp -a keeps the modes; the owner becomes root, which is what the daemons' unit expects to
    # find under /opt (a tree owned by the person who unpacked it would work too, until it moved)
    cp -a "$HERE/." "$DEST/"
    chown -R root:root "$DEST"
    echo "  copied to $DEST"
else
    echo "  already in place"
fi

# 3. the stable paths: the tools and the command-line binaries a release from the phone would put
#    there; setup.sh stages ghost.secd and ghost-update-guard beside them
mkdir -p /opt/localghost/tools /opt/localghost/bin
for t in "$DEST"/tools/*; do
    [ -f "$t" ] || continue
    case "$t" in
        *.sh) install -m755 "$t" "/opt/localghost/tools/$(basename "$t")" ;;
        *)    install -m644 "$t" "/opt/localghost/tools/$(basename "$t")" ;;
    esac
done
for b in ghost-cli ghost-ctl ghost-qr; do
    [ -x "$DEST/bin/$b" ] && install -m755 "$DEST/bin/$b" "/opt/localghost/bin/$b"
done
echo "  tools in /opt/localghost/tools, ghost-cli ghost-ctl ghost-qr in /opt/localghost/bin"
echo

# 4. the guided setup, from the release
exec sh "$DEST/tools/setup.sh" "$SVC_USER"
