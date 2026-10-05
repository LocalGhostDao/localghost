#!/usr/bin/env bash
# Install Postgres + Redis BINARIES for LocalGhost, then make sure NOTHING runs them automatically.
#
# Why this is not a normal install: ghost.secd is the process supervisor, not systemd. Postgres and
# Redis run as PRIVATE instances that ghost.secd starts AFTER the encrypted volume is unlocked and
# mounted , their data dirs, ports, and passwords all live inside that volume (see services.conf).
# The Debian packages want to auto-create a cluster on the system drive and enable a boot-time
# systemd unit; both are wrong here, so we install the binaries and then DISABLE + MASK the units and
# REMOVE the auto-created Postgres cluster. A packaged instance on the system drive would (a) hold a
# default port ghost.secd's private instance must avoid, and (b) put unencrypted DB data on the
# system disk , exactly what this design refuses.
#
# Coexistence, the rule this script now actually keeps: if this box ALREADY runs Postgres/Redis for
# other things, NOTHING here stops, disables, masks, or drops them. Neutralisation (mask + drop the
# auto-created cluster) happens ONLY for packages this very run installed on a previously DB-less box
# , the case where the distro just auto-provisioned services nobody asked for. ghost.secd's instances
# use their own ports (6000+slot / 6100+slot) and unix sockets on the encrypted mount, so they never
# collide with a system 5432/6379.
#
# Idempotent. Reads nothing secret. Run as root. Read it before you run it.
set -euo pipefail

. /etc/os-release
CODENAME="${VERSION_CODENAME:?cannot determine Debian codename}"
ARCH="$(dpkg --print-architecture)"
echo "> Debian ${CODENAME} (${ARCH})"

# Self-heal before ANY apt call: if a previous run of this script added our source files alongside
# pre-existing sources for the same repos (a box that already had PGDG or redis.io configured), apt
# sees one repo with two different Signed-By values and refuses to read ANY source list , which also
# means this script can never fix it via apt. Ours are the removable ones; theirs were here first.
for pair in "apt.postgresql.org:/etc/apt/sources.list.d/pgdg.sources" \
            "packages.redis.io:/etc/apt/sources.list.d/redis.sources"; do
    _host="${pair%%:*}"; _ours="${pair#*:}"
    _others="$(grep -rl "$_host" /etc/apt/sources.list /etc/apt/sources.list.d/ 2>/dev/null | grep -vx "$_ours" || true)"
    if [ -f "$_ours" ] && [ -n "$_others" ]; then
        echo "> removing duplicate apt source $_ours , repo already configured in:"
        echo "$_others" | sed 's/^/    /'
        rm -f "$_ours"
    fi
done

echo "> prerequisites..."
apt-get update
apt-get install -y curl ca-certificates gpg lsb-release

# --- PostgreSQL (PGDG, Postgres 18 , newer than Trixie's default 17) --------------------------
echo "> adding PGDG repository (deb822)..."
if grep -rlq "apt.postgresql.org" /etc/apt/sources.list /etc/apt/sources.list.d/ 2>/dev/null; then
    echo "  PGDG already configured on this box , using the existing source, not adding a second"
else
install -d /usr/share/postgresql-common/pgdg
curl -fsSL -o /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc \
    https://www.postgresql.org/media/keys/ACCC4CF8.asc
cat > /etc/apt/sources.list.d/pgdg.sources <<EOF
Types: deb
URIs: https://apt.postgresql.org/pub/repos/apt
Suites: ${CODENAME}-pgdg
Components: main
Architectures: ${ARCH}
Signed-By: /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc
EOF
fi

# --- Redis (official redis.io repo, Redis 8.x , newer than Trixie's default) -------------------
echo "> adding Redis repository..."
if grep -rlq "packages.redis.io" /etc/apt/sources.list /etc/apt/sources.list.d/ 2>/dev/null; then
    echo "  redis.io already configured on this box , using the existing source, not adding a second"
else
curl -fsSL https://packages.redis.io/gpg | gpg --dearmor -o /usr/share/keyrings/redis-archive-keyring.gpg
chmod 644 /usr/share/keyrings/redis-archive-keyring.gpg
cat > /etc/apt/sources.list.d/redis.sources <<EOF
Types: deb
URIs: https://packages.redis.io/deb
Suites: ${CODENAME}
Components: main
Architectures: ${ARCH}
Signed-By: /usr/share/keyrings/redis-archive-keyring.gpg
EOF
fi

# Preseed BEFORE the packages land: postgresql-common auto-creates a 'main' cluster at install time
# unless told not to. With this, no cluster is ever created on the system drive , the drop further
# down becomes belt-and-braces for boxes that installed before this preseed existed.
echo "> preseeding: no auto-created cluster..."
install -d /etc/postgresql-common
if ! grep -qE '^\s*create_main_cluster\s*=\s*false' /etc/postgresql-common/createcluster.conf 2>/dev/null; then
    printf '\n# LocalGhost: clusters live on the encrypted volume, never the system drive\ncreate_main_cluster = false\n' \
        >> /etc/postgresql-common/createcluster.conf
fi

echo "> installing binaries..."
# postgresql-18 pulls the server + client + postgresql-common; postgresql-18-pgvector is the search
# layer's vector index (rides into the volume bundle later). redis-server + redis-tools give the
# server binary and redis-cli.
#
# Install ONLY what is missing. "Ensure installed" is this script's whole contract , never "ensure
# latest": upgrading present packages as a setup side effect would fight apt-mark holds (refused with
# -y, correctly) and violate the version-pinning philosophy everything else here is built on. On this
# box, database upgrades are a deliberate act (see bundle_db_runtime.sh), not something setup does to
# you in passing.
# postgresql-18 carries the contrib extensions (pg_trgm, for the Wikipedia lookups by likeness);
# pgvector is its own package. tools/pg_extensions.sh checks both are in the tree.
MISSING=""
for _pkg in postgresql-18 postgresql-client-18 postgresql-18-pgvector redis-server redis-tools; do
    dpkg -s "$_pkg" >/dev/null 2>&1 || MISSING="$MISSING $_pkg"
done
if [ -n "$MISSING" ]; then
    echo "  installing:$MISSING"
    apt-get update
    # shellcheck disable=SC2086
    apt-get install -y $MISSING
else
    echo "  all packages already installed , versions left exactly as they are (holds respected)"
fi

# --- Neutralise auto-provisioning , ONLY for what this run installed --------------------------
# The dangerous half of this script, so the rule is explicit: a service that predates this run is
# somebody's production database and gets left exactly as found. Only when THIS run installed the
# packages (fresh box , the distro auto-provisioned a service and an OS-disk cluster nobody asked
# for) do we stop/mask the unit and drop the auto-created empty cluster.
case " $MISSING " in
    *" postgresql-18 "*)
        echo "> postgres was installed by THIS run , neutralising the distro auto-provisioning..."
        systemctl stop    postgresql.service   2>/dev/null || true
        systemctl disable postgresql.service   2>/dev/null || true
        systemctl mask    postgresql.service   2>/dev/null || true
        if pg_lsclusters -h 2>/dev/null | awk '{print $1"/"$2}' | grep -qx "18/main"; then
            pg_dropcluster --stop 18 main || true
            echo "  dropped the just-auto-created 18/main"
        fi
        ;;
    *)
        echo "> postgres pre-existed on this box , leaving its service, clusters, and data COMPLETELY alone"
        echo "  (ghost.secd runs its own instances on ports 6000+slot with data on the encrypted volume;"
        echo "   the system 5432 is not ours and is not touched)"
        ;;
esac
case " $MISSING " in
    *" redis-server "*)
        echo "> redis was installed by THIS run , neutralising the distro unit..."
        systemctl stop    redis-server.service 2>/dev/null || true
        systemctl disable redis-server.service 2>/dev/null || true
        systemctl mask    redis-server.service 2>/dev/null || true
        ;;
    *)
        echo "> redis pre-existed on this box , leaving its service and data COMPLETELY alone"
        ;;
esac

# Redis leaves /var/lib/redis and a default /etc/redis/redis.conf. We leave the files (harmless,
# unused , ghost.secd writes its own redis.conf inside the volume) but the masked unit guarantees
# the distro config never starts a server on the default port.

echo
echo "> done. Installed versions:"
/usr/lib/postgresql/18/bin/postgres --version 2>/dev/null || postgres --version 2>/dev/null || true
redis-server --version || true
echo
echo "Binaries are installed; NOTHING is running. ghost.secd starts private instances against the"
echo "encrypted volume at unlock, using ports + credentials from <mount>/services.conf."
echo "If you run a SEPARATE system Postgres/Redis for other apps, it is untouched , this only masked"
echo "the units for the packages we just installed."
