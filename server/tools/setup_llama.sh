#!/usr/bin/env bash
# setup_llama.sh , the from-NOTHING inference setup. Run as root, before first unlock.
#
# Assumes a bare box: no llama.cpp, no binary, no model. It:
#   1. installs build deps (git, cmake, compiler)
#   2. clones llama.cpp into ITS OWN FOLDER (/opt/localghost/llama.cpp) and builds llama-server there
#   3. symlinks the built binary to /usr/local/bin/llama-server (oracled's default llamaBin)
#   4. gets the model weights: the mirror, then Hugging Face, or files you copied over (a USB
#      stick, scp) , every file checked against tools/model.pins before it is accepted
#   5. hands everything to stage_models.sh , the next unlock ingests onto the encrypted volume
#
# Usage:
#   sudo ./tools/setup_llama.sh                       # llama.cpp + weights from the mirror, Hugging Face as the fallback
#   sudo ./tools/setup_llama.sh --models /path/to/dir-with-ggufs     # files you already have (checked)
#   sudo ./tools/setup_llama.sh --model /path/a.gguf --mmproj /path/b.gguf [--embed /path/c.gguf]
#   sudo ./tools/setup_llama.sh --model-url URL [--mmproj-url URL] [--embed-url URL]   # other weights (unpinned)
#   sudo ./tools/setup_llama.sh --build-only
#
# THE WEIGHTS are Unsloth's Gemma 4 12B GGUF build (Apache 2.0, no account, no token): the exact
# files are named in tools/model.pins with their SHA-256 and size. A download from the mirror is
# checked by the mirror's signed manifest AND the pin; one from Hugging Face by the pin; a file you
# copied by the pin. Nothing else is accepted under those names , a Hugging Face upload that changed
# under the same name (Unsloth's mmproj did, before their F32 patch_embd fix) is refused, not staged.
# No Python, no huggingface-cli, no Hugging Face account anywhere in setup: curl and sha256sum.
#
# THE MIRROR (tools/mirror_fetch.sh; published from the web repo, LocalGhostDao/web mirror/): when
# https://www.localghost.ai/mirror lists them (it does not yet: until it does, the fallbacks below run),
# llama.cpp's source comes from it at the commit the publisher pinned , every box builds the same
# engine, not whatever master was that morning , and so do the weights, without a Hugging Face token.
# Both only after the manifest's gpg signature verifies against tools/mirror-key.asc and each file's
# SHA-256 matches it. A box with a git checkout keeps pulling as before unless --from-mirror.
# GHOST_MIRROR=off turns the mirror off.
set -eu

if [ "$(id -u)" -ne 0 ]; then echo "run as root" >&2; exit 1; fi
cd "$(dirname "$0")/.."   # repo root, so ./tools/stage_models.sh resolves

LLAMA_DIR=/opt/localghost/llama.cpp
MODELS_DIR=""
MODEL_URL=""
MMPROJ_URL=""
EMBED_URL=""
MODEL_FILE=""
MMPROJ_FILE=""
EMBED_FILE=""
BUILD_ONLY=0
FROM_MIRROR=0
while [ $# -gt 0 ]; do
    case "$1" in
        --models)     MODELS_DIR="$2"; shift 2 ;;
        --model)      MODEL_FILE="$2"; shift 2 ;;
        --mmproj)     MMPROJ_FILE="$2"; shift 2 ;;
        --embed)      EMBED_FILE="$2"; shift 2 ;;
        --model-url)  MODEL_URL="$2"; shift 2 ;;
        --mmproj-url) MMPROJ_URL="$2"; shift 2 ;;
        --embed-url)  EMBED_URL="$2"; shift 2 ;;
        --hf-token)   echo "--hf-token is gone: the pinned weights (tools/model.pins) need no account" >&2; shift 2 ;;
        --build-only) BUILD_ONLY=1; shift ;;
        --from-mirror) FROM_MIRROR=1; shift ;;
        *) echo "unknown arg: $1" >&2; exit 2 ;;
    esac
done
. "$(pwd)/tools/model_pins.sh"

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
mkdir -p "$DL"; chmod 700 "$DL"
# fetch_pinned <name> , the file into $DL from the mirror (already checked by its signed manifest)
# or, when the mirror has nothing, from the pin's upstream with curl (resumable), then the pin check
# either way. A file already in $DL that matches the pin is kept: a rerun costs nothing.
fetch_pinned() {
    _n="$1"; _out="$DL/$_n"
    if [ -s "$_out" ] && pin_check "$_out" >/dev/null 2>&1; then
        echo "-- $_n already downloaded and matches the pin"
        return 0
    fi
    if sh "$FETCH" models "$DL" "$_n" && [ -s "$_out" ]; then
        echo "-- $_n from the mirror"
    else
        _url="$(pin_url "$_n")"
        [ -n "$_url" ] || { echo "!! $_n: not on the mirror and not pinned (no upstream to fetch from)" >&2; return 1; }
        echo "-- fetching $_n from $_url (resumable; a rerun continues)"
        curl -fL --retry 3 --retry-delay 5 -C - --progress-bar -o "$_out" "$_url" || { echo "!! download failed: $_url" >&2; return 1; }
    fi
    pin_check "$_out" || { rm -f "$_out"; return 1; }
}
if [ -z "$MODELS_DIR" ] && [ -z "$MODEL_URL" ] && [ -z "$MODEL_FILE$MMPROJ_FILE$EMBED_FILE" ]; then
    # the default: exactly the pinned files
    for n in $(pin_names); do
        fetch_pinned "$n" || { echo "!! could not get $n. Copy it over and re-run with --models <dir> (or --model/--mmproj)." >&2; exit 3; }
    done
    if [ -n "$(pin_url embeddinggemma-300m-q8.gguf)" ]; then
        :  # pinned: fetched above
    elif sh "$FETCH" models "$DL" embeddinggemma-300m-q8.gguf 2>/dev/null && [ -s "$DL/embeddinggemma-300m-q8.gguf" ]; then
        echo "-- embeddinggemma-300m-q8.gguf from the mirror (unpinned)"
    else
        echo "-- embeddinggemma-300m-q8.gguf: not on the mirror and not pinned yet , search runs FTS-only until"
        echo "   it is provided (--embed <file>, or drop it in $DL and re-run)"
    fi
    MODELS_DIR="$DL"
elif [ -n "$MODEL_FILE" ] || [ -n "$MMPROJ_FILE" ] || [ -n "$EMBED_FILE" ]; then
    # files copied over one by one: each checked against its pin (by its NAME on the box), then
    # linked into $DL under that name , the copy happens once, at staging
    for pair in "gemma-4-12b-it-Q4_K_M.gguf=$MODEL_FILE" "mmproj-F16.gguf=$MMPROJ_FILE" "embeddinggemma-300m-q8.gguf=$EMBED_FILE"; do
        n="${pair%%=*}"; f="${pair#*=}"
        [ -n "$f" ] || continue
        [ -f "$f" ] || { echo "!! $f: no such file" >&2; exit 2; }
        ln -f "$f" "$DL/$n" 2>/dev/null || cp "$f" "$DL/$n"
        pin_check "$DL/$n" || { rm -f "$DL/$n"; exit 4; }
    done
    for n in $(pin_names); do
        [ -s "$DL/$n" ] || fetch_pinned "$n" || { echo "!! $n was not given and could not be fetched" >&2; exit 3; }
    done
    MODELS_DIR="$DL"
elif [ -n "$MODELS_DIR" ]; then
    # a directory of files: every pinned name it holds is checked (stage_models.sh checks again)
    for n in $(pin_names); do
        [ -f "$MODELS_DIR/$n" ] || continue
        pin_check "$MODELS_DIR/$n" || exit 4
    done
else
    # other weights by URL: your choice, unpinned, said so
    echo "-- weights by URL are not pinned: whatever these URLs serve is what the box will run"
    fetch() { # url -> file in $DL, resumable, fail loudly with the URL named
        local url="$1" out="$DL/$(basename "${1%%\?*}")"
        echo "-- fetching $(basename "$out")"
        if ! curl -fL --retry 3 -C - -o "$out" "$url"; then
            echo "!! download failed: $url" >&2
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
