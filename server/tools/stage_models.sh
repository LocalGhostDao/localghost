#!/usr/bin/env bash
# stage_models.sh , provision-time model staging. Run as root, BEFORE first unlock.
#
# The encrypted volume does not exist until the first unlock, so models cannot be placed directly.
# This script stages them on the unencrypted disk at /var/lib/ghost/staging/ai-models (root-only),
# and ghost.secd INGESTS them onto the encrypted volume automatically during unlock , move, chown to
# the run user, remove the staged copy. From-scratch flow becomes: provision, stage, unlock, done.
#
#   sudo ./tools/stage_models.sh /path/to/dir-with-ggufs [/path/to/llama-server]
#
# The source dir should contain the gguf files (main model, mmproj, embedding model , whatever the
# conf expects; defaults are gemma-4-12b-it-Q4_K_M.gguf, mmproj-F16.gguf, embeddinggemma-300m-q8.gguf).
# The optional second argument installs the llama-server binary to /usr/local/bin (it is not secret).
#
# NOTE ON PLAINTEXT: staged files sit on the UNENCRYPTED disk until the next unlock ingests them.
# Stage right before unlocking, and if the source copies elsewhere on this disk are no longer needed,
# shred them , this script deliberately does not delete your sources.
set -eu

if [ "$(id -u)" -ne 0 ]; then echo "run as root" >&2; exit 1; fi
SRC="${1:-}"
LLAMA_BIN="${2:-}"
if [ -z "$SRC" ] || [ ! -d "$SRC" ]; then
    echo "usage: $0 /path/to/dir-with-ggufs [/path/to/llama-server]" >&2
    exit 2
fi

STAGING="${GHOST_STAGING_DIR:-/var/lib/ghost/staging}/ai-models"
mkdir -p "$STAGING"
chmod 700 "$(dirname "$STAGING")" "$STAGING"
# secd's unlock ingest reads /var/lib/ghost/staging/ai-models specifically. If staging was pointed at
# a bigger disk, leave a symlink at the default path so the ingest still finds it , the link itself
# costs nothing on /var.
DEFAULT_STAGING=/var/lib/ghost/staging/ai-models
if [ "$STAGING" != "$DEFAULT_STAGING" ]; then
    mkdir -p /var/lib/ghost/staging
    ln -sfn "$STAGING" "$DEFAULT_STAGING"
    echo "-- staging at $STAGING (symlinked from $DEFAULT_STAGING for the unlock ingest)"
fi

# 1. Stop anything already serving a model , a leftover llama-server holds the RAM the cohort needs,
#    and two copies of 12B weights in memory ends with the OOM killer choosing for you.
FOUND="$(pgrep -af 'llama-server|llama\.cpp' | grep -v "$0" || true)"
if [ -n "$FOUND" ]; then
    echo "-- stopping existing llama processes:"
    echo "$FOUND"
    pgrep -f 'llama-server|llama\.cpp' | while read -r pid; do
        kill -TERM "$pid" 2>/dev/null || true
    done
    sleep 2
fi
# disable any unit that would bring one back on boot
for unit in $(systemctl list-unit-files --type=service --no-legend 2>/dev/null | awk '{print $1}' | grep -i llama || true); do
    echo "-- disabling unit $unit"
    systemctl stop "$unit" 2>/dev/null || true
    systemctl disable "$unit" 2>/dev/null || true
done

# 2. Install the binary (not secret , lives on the OS disk like any tool).
if [ -n "$LLAMA_BIN" ]; then
    install -m 0755 "$LLAMA_BIN" /usr/local/bin/llama-server
    echo "-- llama-server installed to /usr/local/bin"
elif ! command -v llama-server >/dev/null 2>&1; then
    echo "!! no llama-server on PATH and none provided , oracled will name this at unlock" >&2
fi

# 3. Stage the weights , ONLY what LocalGhost uses, never "every gguf in the dir". A source dir can
#    hold a person's whole model zoo (a 19GB DeepSeek staged onto a 19GB /var partition filled it to
#    100% and broke journald , learned the hard way). Files are matched by the conf-default names;
#    override the list with GHOST_MODEL_FILES="a.gguf b.gguf" for non-default conf.
WANTED="${GHOST_MODEL_FILES:-gemma-4-12b-it-Q4_K_M.gguf mmproj-F16.gguf embeddinggemma-300m-q8.gguf}"

# Free-space preflight on the staging filesystem , /var is often a small separate partition, and
# filling it takes the whole system's logging and databases down with it.
NEED=0
for name in $WANTED; do
    [ -e "$SRC/$name" ] && NEED=$((NEED + $(stat -c%s "$SRC/$name")))
done
AVAIL=$(df --output=avail -B1 "$STAGING" | tail -1)
if [ "$NEED" -gt 0 ] && [ "$AVAIL" -lt $((NEED + 1073741824)) ]; then  # need + 1GB headroom
    echo "!! not enough space on $(df --output=target "$STAGING" | tail -1): need $((NEED / 1048576))MB + 1GB headroom, have $((AVAIL / 1048576))MB" >&2
    echo "   Options: point staging elsewhere (GHOST_STAGING_DIR=/big/disk/path $0 ...), or , if the" >&2
    echo "   box is already UNLOCKED , skip staging and copy straight onto the volume via tools/ns.sh." >&2
    exit 5
fi

COUNT=0
for name in $WANTED; do
    f="$SRC/$name"
    if [ ! -e "$f" ]; then
        echo "-- $name not in $SRC (fine if your conf does not use it)"
        continue
    fi
    SIZE=$(stat -c%s "$f")
    if [ "$SIZE" -lt 1048576 ]; then
        echo "!! skipping $name , ${SIZE} bytes is not a model (interrupted download?)" >&2
        continue
    fi
    echo "-- staging $name ($((SIZE / 1048576))MB)"
    cp "$f" "$STAGING/"
    COUNT=$((COUNT + 1))
done
chmod 600 "$STAGING"/*.gguf 2>/dev/null || true

if [ "$COUNT" -eq 0 ]; then
    echo "!! no gguf files staged from $SRC" >&2
    exit 3
fi
echo "----------------------------------------"
echo "$COUNT model file(s) staged at $STAGING (root-only, unencrypted until ingest)."
echo "Next unlock moves them onto the encrypted volume automatically and removes the staged copies."
echo "If your source copies at $SRC are on this disk and no longer needed:  shred -u $SRC/*.gguf"
