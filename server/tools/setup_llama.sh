#!/usr/bin/env bash
# setup_llama.sh , the from-NOTHING inference setup. Run as root, before first unlock.
#
# Assumes a bare box: no llama.cpp, no binary, no model. It:
#   1. installs build deps (git, cmake, compiler)
#   2. clones llama.cpp into ITS OWN FOLDER (/opt/localghost/llama.cpp) and builds llama-server there
#   3. symlinks the built binary to /usr/local/bin/llama-server (oracled's default llamaBin)
#   4. downloads the model ggufs if URLs are provided, or takes a local dir of ggufs
#   5. hands everything to stage_models.sh , the next unlock ingests onto the encrypted volume
#
# Usage:
#   sudo ./tools/setup_llama.sh --models /path/to/dir-with-ggufs
#   sudo ./tools/setup_llama.sh --model-url URL [--mmproj-url URL] [--embed-url URL] [--hf-token TOKEN]
#   sudo ./tools/setup_llama.sh --build-only
#   sudo ./tools/setup_llama.sh                     # source + weights from the localghost.ai mirror
#
# THE MIRROR (tools/mirror_fetch.sh; published from the web repo, LocalGhostDao/web mirror/): when
# https://www.localghost.ai/mirror lists them (it does not yet: until it does, the fallbacks below run),
# llama.cpp's source comes from it at the commit the publisher pinned , every box builds the same
# engine, not whatever master was that morning , and so do the weights, without a Hugging Face token.
# Both only after the manifest's gpg signature verifies against tools/mirror-key.asc and each file's
# SHA-256 matches it. A box with a git checkout keeps pulling as before unless --from-mirror.
# GHOST_MIRROR=off turns the mirror off.
#
# HONEST NOTE on the model download: Gemma weights on Hugging Face are LICENSE-GATED , a fully
# unattended download needs an HF token (--hf-token or HF_TOKEN env) from an account that accepted
# the license. Without one, download the ggufs on any machine, copy them over, and use --models.
set -eu

if [ "$(id -u)" -ne 0 ]; then echo "run as root" >&2; exit 1; fi
cd "$(dirname "$0")/.."   # repo root, so ./tools/stage_models.sh resolves

LLAMA_DIR=/opt/localghost/llama.cpp
MODELS_DIR=""
MODEL_URL=""
MMPROJ_URL=""
EMBED_URL=""
HF_TOKEN="${HF_TOKEN:-}"
BUILD_ONLY=0
FROM_MIRROR=0
while [ $# -gt 0 ]; do
    case "$1" in
        --models)     MODELS_DIR="$2"; shift 2 ;;
        --model-url)  MODEL_URL="$2"; shift 2 ;;
        --mmproj-url) MMPROJ_URL="$2"; shift 2 ;;
        --embed-url)  EMBED_URL="$2"; shift 2 ;;
        --hf-token)   HF_TOKEN="$2"; shift 2 ;;
        --build-only) BUILD_ONLY=1; shift ;;
        --from-mirror) FROM_MIRROR=1; shift ;;
        *) echo "unknown arg: $1" >&2; exit 2 ;;
    esac
done

echo "=== 1/4  build dependencies ==="
apt-get install -y --no-install-recommends git cmake build-essential ca-certificates curl libcurl4-openssl-dev gpg

FETCH="$(pwd)/tools/mirror_fetch.sh"
SRC_DL="$LLAMA_DIR.mirror-dl"   # the verified source tarball stays here: a rerun finds it current

# from_mirror , llama.cpp's source from the mirror; a tarball with a new name (a new pinned commit)
# replaces the folder whole, build/ included, so the build below runs again
from_mirror() {
    sh "$FETCH" llama "$SRC_DL" || return 1
    TB="$(grep '\.tar\.gz$' "$SRC_DL/.mirror-files" | head -1)"
    [ -n "$TB" ] && [ -s "$SRC_DL/$TB" ] || return 1
    for f in "$SRC_DL"/*.tar.gz; do   # an older pin's tarball goes
        [ "$(basename "$f")" = "$TB" ] || rm -f "$f"
    done
    TB="$SRC_DL/$TB"
    if [ "$(cat "$LLAMA_DIR/.mirror-src" 2>/dev/null)" = "$(basename "$TB")" ]; then
        echo "-- llama.cpp from the mirror: $(basename "$TB") already here"
        return 0
    fi
    rm -rf "$LLAMA_DIR.new" && mkdir -p "$LLAMA_DIR.new"
    tar -xzf "$TB" -C "$LLAMA_DIR.new" --strip-components=1 || { rm -rf "$LLAMA_DIR.new"; return 1; }
    basename "$TB" > "$LLAMA_DIR.new/.mirror-src"
    cp "$SRC_DL"/TERMS-*.txt "$LLAMA_DIR.new/" 2>/dev/null || true
    rm -rf "$LLAMA_DIR.old"
    [ -d "$LLAMA_DIR" ] && mv "$LLAMA_DIR" "$LLAMA_DIR.old"
    mv "$LLAMA_DIR.new" "$LLAMA_DIR" && rm -rf "$LLAMA_DIR.old"
    echo "-- llama.cpp source from the mirror: $(basename "$TB"), signature and hash checked"
}

echo "=== 2/4  llama.cpp , source + build in its own folder ==="
mkdir -p "$(dirname "$LLAMA_DIR")"
if [ -d "$LLAMA_DIR/.git" ] && [ "$FROM_MIRROR" -eq 0 ]; then
    git -C "$LLAMA_DIR" pull --ff-only || echo "-- pull failed (offline?), building what is checked out"
elif from_mirror; then
    :
elif [ -f "$LLAMA_DIR/.mirror-src" ]; then
    echo "-- the mirror did not answer , building the mirror's copy already here"
elif [ ! -d "$LLAMA_DIR/.git" ]; then
    git clone --depth 1 https://github.com/ggml-org/llama.cpp "$LLAMA_DIR"
fi
if [ ! -x "$LLAMA_DIR/build/bin/llama-server" ]; then
    # STATIC single-binary CPU build. Static matters: the binary is seeded onto the ENCRYPTED VOLUME
    # (<mount>/bin/llama-server , everything except secd lives there and dies with the mount), and a
    # dynamic build would need its .so files carried along. -DGGML_NATIVE=ON tunes to THIS machine.
    # LLAMA_SERVER_WEBUI=OFF asks cmake to skip the embedded browser UI, but MANY llama.cpp
    # checkouts have no such option and cmake silently ignores unknown -D vars , so the UI may
    # still BUILD (observed: it does). That costs build minutes, not security: the enforced
    # guarantee is oracled passing --no-webui at RUNTIME, hardcoded, so the UI is never served
    # regardless of what got compiled in.
    # GPU BUILD. The box carries an RTX 4070 (12GB, Ada = SM 8.9); building without -DGGML_CUDA=ON
    # produces a CPU-only llama-server that runs a 12B at single-digit tokens/s while the GPU idles ,
    # which is exactly the bug this line fixes (the first static build here made that mistake).
    # Preflight nvcc: no CUDA toolkit = loud CPU-only warning, not a cryptic cmake failure.
    CUDA_FLAGS=""
    if command -v nvcc >/dev/null 2>&1; then
        CUDA_FLAGS="-DGGML_CUDA=ON -DCMAKE_CUDA_ARCHITECTURES=89"
        echo "[setup_llama] nvcc found , building WITH CUDA (SM 8.9 for the 4070)"
    else
        echo "[setup_llama] WARNING: nvcc not found , building CPU-ONLY. A 12B on CPU is ~5 tok/s."
        echo "[setup_llama]          install cuda-toolkit and rerun for the GPU build."
    fi
    cmake -S "$LLAMA_DIR" -B "$LLAMA_DIR/build" -DGGML_NATIVE=ON -DBUILD_SHARED_LIBS=OFF -DLLAMA_SERVER_WEBUI=OFF $CUDA_FLAGS         -DLLAMA_BUILD_TESTS=OFF -DLLAMA_BUILD_EXAMPLES=OFF -DLLAMA_BUILD_SERVER=ON
    cmake --build "$LLAMA_DIR/build" --target llama-server -j"$(nproc)"
else
    echo "-- llama-server already built, skipping (delete $LLAMA_DIR/build to force rebuild)"
fi
# Into the repo's bin/ (ExecDir): provisioning seeds everything there onto the volume's bin, so the
# engine rides the same mechanism as the cohort daemons. NOT /usr/local , a host path is both outside
# the volume (wrong place for the engine) and a trap under ProtectHome namespaces.
REPO_BIN="$(pwd)/bin"
mkdir -p "$REPO_BIN"
install -m 0755 "$LLAMA_DIR/build/bin/llama-server" "$REPO_BIN/llama-server"
echo "-- llama-server installed to $REPO_BIN (seeded to <mount>/bin at provision)"
echo "-- EXISTING volume? Seed it now while unlocked (via /tmp: the repo lives under /home, which is"
echo "   EMPTY inside secd's mount namespace , ProtectHome , so ns.sh cannot see the repo path):"
echo "     cp $REPO_BIN/llama-server /tmp/llama-server"
echo "     sudo ./tools/ns.sh cp /tmp/llama-server /var/lib/ghost/mnt/slot0/bin/llama-server"
echo "     sudo ./tools/ns.sh chown coder /var/lib/ghost/mnt/slot0/bin/llama-server"
echo "     rm /tmp/llama-server"

if [ "$BUILD_ONLY" -eq 1 ]; then
    echo "build-only requested , done. Stage models later with tools/stage_models.sh"
    exit 0
fi

echo "=== 3/4  model weights ==="
DL=/var/lib/ghost/staging/download
if [ -z "$MODELS_DIR" ] && [ -z "$MODEL_URL" ]; then
    mkdir -p "$DL"; chmod 700 "$DL"
    if sh "$FETCH" models "$DL"; then
        echo "-- weights from the mirror, checked"
        MODELS_DIR="$DL"
    else
        echo "!! no --models dir, no --model-url, and no weights from the mirror. Download the ggufs" >&2
        echo "   elsewhere and re-run with --models." >&2
        exit 3
    fi
fi
if [ -z "$MODELS_DIR" ]; then
    mkdir -p "$DL"; chmod 700 "$DL"
    AUTH=()
    [ -n "$HF_TOKEN" ] && AUTH=(-H "Authorization: Bearer $HF_TOKEN")
    fetch() { # url -> file in $DL, resumable, fail loudly with the URL named
        local url="$1" out="$DL/$(basename "${1%%\?*}")"
        echo "-- fetching $(basename "$out")"
        if ! curl -fL --retry 3 -C - "${AUTH[@]}" -o "$out" "$url"; then
            echo "!! download failed: $url" >&2
            echo "   (gated model? pass --hf-token, or download manually and use --models)" >&2
            exit 4
        fi
    }
    fetch "$MODEL_URL"
    [ -n "$MMPROJ_URL" ] && fetch "$MMPROJ_URL"
    [ -n "$EMBED_URL" ] && fetch "$EMBED_URL"
    MODELS_DIR="$DL"
fi

echo "=== 4/4  stage for ingest at next unlock ==="
./tools/stage_models.sh "$MODELS_DIR"
# staged copies live under /var/lib/ghost/staging/ai-models; remove the download scratch if we made it
if [ -d "$DL" ] && [ "$MODELS_DIR" = "$DL" ]; then rm -rf "$DL"; fi

echo "----------------------------------------"
echo "Inference setup complete:"
echo "  llama.cpp    $LLAMA_DIR (self-contained; binary symlinked to /usr/local/bin/llama-server)"
echo "  models       staged , the NEXT UNLOCK ingests them onto the encrypted volume"
echo "Unlock from the app, then verify:"
echo "  sudo journalctl -u ghost.secd --since '2 min ago' | grep -i 'ingested\\|oracled'"
echo "  sudo ./tools/ns.sh ./bin/ghost-cli ghost.oracled status"
