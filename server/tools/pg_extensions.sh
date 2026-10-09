#!/bin/sh
# pg_extensions.sh [mount] , are the Postgres extensions the box needs there? Run by setup.sh and
# server_setup_root.sh (the OS packages, before the first unlock) and redeploy.sh (the OS packages
# and, on an unlocked box, the runtime bundled onto the volume by bundle_db_runtime.sh, which is
# what the vault's Postgres runs from after that).
#
# The extensions, and what wants them:
#   vector    pgvector, the memory and photo indexes (postgresql-<ver>-pgvector)
#   pg_trgm   the Wikipedia lookups by likeness (in postgresql-<ver> itself since PGDG 10; its
#             files are in the server package, nothing more to install)
# An extension is "there" when its control file is in the share tree: Postgres loads it from
# there with CREATE EXTENSION (hw.EnsureSchema does, at every start). Missing in the OS tree:
# sudo ./tools/install_db.sh. Missing on the volume: as the service user, with the box unlocked,
# ./tools/bundle_db_runtime.sh <mount> copies the OS tree over again.
# Exit 0 when all are there, 1 when one is missing (the callers say what to do; redeploy goes on).
set -u
MOUNT="${1:-}"
WANT="vector pg_trgm"
rc=0
tree() { # tree <label> <share dir glob>
    label="$1"; shift
    have=""; miss=""
    for e in $WANT; do
        # shellcheck disable=SC2086
        if ls $1/extension/$e.control >/dev/null 2>&1; then have="$have $e"; else miss="$miss $e"; fi
    done
    if [ -z "$miss" ]; then
        echo "  postgres extensions ($label):$have"
    else
        echo "  postgres extensions ($label): MISSING$miss${have:+ (there:$have)}"
        rc=1
    fi
}
if ls -d /usr/share/postgresql/*/ >/dev/null 2>&1; then
    tree "OS packages" "/usr/share/postgresql/*"
else
    echo "  postgres extensions (OS packages): no Postgres installed (tools/install_db.sh)"
    rc=1
fi
if [ -n "$MOUNT" ]; then
    # the volume is in ghost.secd's mount namespace: reached through /proc/<pid>/root when secd
    # runs and the box is unlocked (ns.sh explains), else nothing to check yet
    PID="$(pidof ghost.secd 2>/dev/null || true)"; PID="${PID%% *}"
    ROOT=""
    [ -n "$PID" ] && [ -d "/proc/$PID/root$MOUNT/runtime/pgroot" ] && ROOT="/proc/$PID/root"
    if [ -n "$ROOT" ]; then
        tree "the volume's runtime" "$ROOT$MOUNT/runtime/pgroot/usr/share/postgresql/*"
    elif [ -n "$PID" ]; then
        echo "  postgres extensions (the volume's runtime): not bundled yet, or the box is locked (the OS packages serve)"
    fi
fi
exit $rc
