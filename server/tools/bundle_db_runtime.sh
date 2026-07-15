#!/bin/sh
# bundle_db_runtime.sh , copy the Postgres and Redis runtimes onto the ENCRYPTED volume, so the OS
# disk can stop carrying database software entirely. Run as the service user, with the volume
# UNLOCKED, after the first successful unlock (the first-ever initdb bootstraps from the OS packages;
# everything after runs from the volume).
#
# What it does:
#   1. Mirrors Debian's Postgres tree , usr/lib/postgresql/<ver>/{bin,lib} AND
#      usr/share/postgresql/<ver> , into <mount>/runtime/pgroot. The STRUCTURE must travel intact:
#      Postgres relocates by relative offset from where the binary actually sits, so bin/lib/share
#      keep their Debian geometry. pgvector's vector.so and extension files live inside that tree and
#      ride along for free.
#   2. Copies redis-server and redis-cli into <mount>/runtime/redis/bin.
#   3. Walks ldd for every binary and copies the shared-library closure into runtime lib dirs ,
#      the daemons run them with LD_LIBRARY_PATH pointed there, so an OS library upgrade cannot break
#      the volume's databases. (Honest limit: the dynamic loader and glibc itself stay the system's ,
#      full isolation would need a chroot, and glibc ABI stability is what makes this cut safe.)
#   4. --verify: runs the bundled initdb into a throwaway dir ON the volume, using ONLY the bundle,
#      and deletes it. If that passes, the OS packages are removable.
#
# After a successful --verify, the box no longer needs the OS packages for RUNTIME:
#     apt-get remove postgresql postgresql-<ver> postgresql-<ver>-pgvector redis-server redis-tools
# (re-running server_setup_root.sh would reinstall them , that script is for bootstrap; skip its
# package step on a bundled box or just let it reinstall, nothing conflicts.)
#
# Re-run any time to refresh the bundle (e.g. after choosing to take a deliberate PG upgrade ,
# which is a data migration you plan, not something apt does to you overnight).

set -eu

MOUNT="${1:-}"
VERIFY=0
RUN_USER=""
shift 2>/dev/null || true
while [ $# -gt 0 ]; do
    case "$1" in
        --verify) VERIFY=1; shift ;;
        --user) RUN_USER="$2"; shift 2 ;;
        --user=*) RUN_USER="${1#--user=}"; shift ;;
        *) shift ;;
    esac
done
if [ -z "$MOUNT" ] || [ ! -d "$MOUNT" ]; then
    echo "usage: $0 <mount-path> [--verify]     e.g. $0 /var/lib/ghost/mnt/slot0 --verify"
    exit 2
fi

# THE NAMESPACE TRAP, defended twice. The volume is mounted inside ghost.secd's PRIVATE mount
# namespace , from the root namespace, $MOUNT is an empty directory on the OS DISK, and a bundle
# written there is 74MB of unencrypted junk secd will never see (observed live). Defence one:
# if $MOUNT does not look like a mounted volume (no services.conf) but secd is running, re-exec this
# script INSIDE secd's namespace , staged through /tmp, because /home (where the repo lives) is EMPTY
# in there. Defence two, below: refuse to write anywhere that still does not look mounted.
if [ ! -e "$MOUNT/services.conf" ]; then
    SECD_PID="$(pidof ghost.secd || true)"
    if [ -n "$SECD_PID" ] && [ -z "${GHOST_NS_ENTERED:-}" ]; then
        STAGE="$(mktemp /tmp/ghost-bundle.XXXXXX.sh)"
        cp "$0" "$STAGE" && chmod 0755 "$STAGE"
        ARGS="$MOUNT"
        [ "$VERIFY" = 1 ] && ARGS="$ARGS --verify"
        [ -n "$RUN_USER" ] && ARGS="$ARGS --user $RUN_USER"
        # shellcheck disable=SC2086
        exec env GHOST_NS_ENTERED=1 GHOST_STAGE_FILE="$STAGE" nsenter -t "$SECD_PID" -m "$STAGE" $ARGS
    fi
    echo "ERROR: $MOUNT does not look like a mounted volume (no services.conf) and no running"
    echo "       ghost.secd namespace to enter. Unlock the box first , bundling writes ONTO the"
    echo "       encrypted volume; writing here would put DB binaries on the OS disk instead."
    exit 1
fi
if [ -n "${GHOST_STAGE_FILE:-}" ]; then
    trap 'rm -f "$GHOST_STAGE_FILE"' EXIT
fi
if [ ! -w "$MOUNT" ]; then
    echo "ERROR: $MOUNT not writable (volume locked, or wrong user?)"
    exit 1
fi

RT="$MOUNT/runtime"

# The service user owns the runtime: secd drops Postgres/Redis to this account at unlock, so a
# root-owned bundle is unexecutable exactly when it matters. Detect from the volume's content dirs
# (provisioning makes them service-user-owned; the mount root stays root). --user overrides.
SVCUSER="$RUN_USER"
if [ -z "$SVCUSER" ]; then
    for d in bin conf run logs ai-models; do
        owner="$(stat -c '%U' "$MOUNT/$d" 2>/dev/null || true)"
        if [ -n "$owner" ] && [ "$owner" != "root" ]; then SVCUSER="$owner"; break; fi
    done
fi
PGBIN_SRC="$(ls -d /usr/lib/postgresql/*/bin 2>/dev/null | sort -V | tail -1 || true)"
if [ -z "$PGBIN_SRC" ]; then
    echo "ERROR: no OS Postgres found to bundle from (install postgresql first; bundling copies FROM it)"
    exit 1
fi
PGVER_DIR="$(dirname "$PGBIN_SRC")"                       # /usr/lib/postgresql/<ver>
PGVER="$(basename "$PGVER_DIR")"
PGSHARE_SRC="/usr/share/postgresql/$PGVER"

echo "> Bundling Postgres $PGVER + Redis into $RT ..."
mkdir -p "$RT/pgroot/usr/lib/postgresql" "$RT/pgroot/usr/share/postgresql" "$RT/pgroot/lib" \
         "$RT/redis/bin" "$RT/redis/lib"

cp -a "$PGVER_DIR"   "$RT/pgroot/usr/lib/postgresql/"
cp -a "$PGSHARE_SRC" "$RT/pgroot/usr/share/postgresql/"
if [ -f "$RT/pgroot/usr/lib/postgresql/$PGVER/lib/vector.so" ]; then
    echo "  pgvector rode along (vector.so present in the bundled tree)"
else
    echo "  NOTE: pgvector not in the bundled tree , search will run FTS-only from this bundle"
fi

for b in redis-server redis-cli; do
    src="$(command -v "$b" || true)"
    [ -z "$src" ] && { echo "ERROR: $b not installed to bundle from"; exit 1; }
    cp -a "$src" "$RT/redis/bin/"
done

# Shared-library closure: every "=> /path" line of ldd, deduped, copied. Skips the loader and vdso.
collect_libs() {  # collect_libs <dest-libdir> <binary>...
    dest="$1"; shift
    for bin in "$@"; do
        ldd "$bin" 2>/dev/null | awk '/=> \//{print $3}' | while read -r lib; do
            [ -f "$dest/$(basename "$lib")" ] || cp -a "$lib" "$dest/"
        done
    done
}
echo "> Collecting shared-library closures..."
collect_libs "$RT/pgroot/lib" "$RT/pgroot/usr/lib/postgresql/$PGVER/bin/"*
collect_libs "$RT/redis/lib"  "$RT/redis/bin/"*

printf 'pg=%s\nbundled=%s\n' "$PGVER" "$(date -u +%FT%TZ)" > "$RT/VERSION"
echo "  bundle written ($(du -sh "$RT" | cut -f1))"

# Hand the whole runtime to the service user , it must be able to execute initdb/postgres/redis at
# unlock. Without this the bundle is root-owned and the DBs (dropped to the service user) get
# "Permission denied", which is both the verify failure and a real unlock failure waiting to happen.
if [ -n "$SVCUSER" ] && [ "$SVCUSER" != "root" ]; then
    chown -R "$SVCUSER":"$SVCUSER" "$RT" 2>/dev/null || true
    echo "  runtime owned by $SVCUSER (the account secd runs the DBs as)"
else
    echo "  NOTE: could not determine the service user , runtime left root-owned; DBs may fail to start."
    echo "        re-run with --user <name> so the bundle is owned by the account that runs the DBs."
fi

if [ "$VERIFY" = 1 ]; then
    echo "> Verifying: bundled initdb into a throwaway dir on the volume, OS packages not consulted..."
    # Postgres REFUSES to run as root (initdb: "cannot be run as root") , so verify must run as the
    # unprivileged service user, exactly as secd does at real unlock. Default to the mount's owner,
    # which provisioning set to the service user; --user overrides.
    VUSER="$SVCUSER"
    if [ -z "$VUSER" ] || [ "$VUSER" = "root" ]; then
        echo "  could not auto-detect the service user from the volume (all dirs root-owned?)"
        echo "  pass --user <name> once, e.g.:  sudo $0 $MOUNT --verify --user coder"
        exit 1
    fi
    VD="$MOUNT/.verify.$$"           # under the mount (owned by VUSER), not under root-owned $RT
    install -d -o "$VUSER" -g "$VUSER" "$VD.parent" 2>/dev/null || mkdir -p "$VD.parent"
    chown "$VUSER" "$VD.parent" 2>/dev/null || true
    echo "  running bundled initdb as user $VUSER ..."
    if su - "$VUSER" -s /bin/sh -c \
        "LD_LIBRARY_PATH='$RT/pgroot/usr/lib/postgresql/$PGVER/lib:$RT/pgroot/lib' '$RT/pgroot/usr/lib/postgresql/$PGVER/bin/initdb' -D '$VD.parent/data' --auth=trust --encoding=UTF8" >/tmp/ghost-verify.$$ 2>&1; then
        rm -rf "$VD.parent"; rm -f /tmp/ghost-verify.$$
        echo "  VERIFIED , the volume runtime stands alone. The OS database packages are now removable:"
        echo "    apt-get remove postgresql 'postgresql-*' redis-server redis-tools"
    else
        echo "  VERIFY FAILED , initdb output:"
        sed 's/^/    /' /tmp/ghost-verify.$$ 2>/dev/null | tail -15
        rm -rf "$VD.parent"; rm -f /tmp/ghost-verify.$$
        echo "  (the daemons fall back to OS packages automatically, so the box still works)"
        exit 1
    fi
fi
echo "> Done. ghost.secd prefers $RT automatically on the next unlock; no config needed."
