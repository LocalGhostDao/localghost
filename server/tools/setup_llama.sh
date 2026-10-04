#!/usr/bin/env bash
# setup_llama.sh , the from-NOTHING inference setup. Run as root, before first unlock.
#
# Assumes a bare box: no llama.cpp, no binary, no model. It:
#   1. installs build deps (cmake, compiler)
#   2. takes llama.cpp's source from the mirror (set llama: ONE tarball at the commit the publisher
#      pinned , never master, never a git clone), unpacks it into ITS OWN FOLDER
#      (/opt/localghost/llama.cpp) and builds llama-server there, stamped with that commit
#   3. installs the binary into the repo's bin/ (seeded onto the volume at provision)
#   4. gets the model weights: the mirror, or files you copied over (a USB stick, scp) , every file
#      checked against tools/model.pins before it is accepted
#   5. hands everything to stage_models.sh , the next unlock ingests onto the encrypted volume
#   6. the phone's model (tools/phone_model.sh; GHOST_PHONE_MODEL=0 skips it), which the box offers
#      its phones , a miss there is said, and setup carries on
#   7. the speech engine for voice notes (tools/setup_whisper.sh; GHOST_SPEECH=0 skips it): whisper.cpp
#      from the mirror's source and a ggml model, both staged , a miss is said, and setup carries on
#
# Usage:
#   sudo ./tools/setup_llama.sh                       # llama.cpp + weights from the mirror
#   sudo ./tools/setup_llama.sh --models /path/to/dir-with-ggufs     # files you already have (checked)
#   sudo ./tools/setup_llama.sh --model /path/a.gguf --mmproj /path/b.gguf [--embed /path/c.gguf]
#   sudo ./tools/setup_llama.sh --llama-tarball /path/llama.cpp-<tag>-<commit>.tar.gz   # a copy of the
#                                                     # mirror's tarball (checked against the signed manifest all the same)
#   sudo ./tools/setup_llama.sh --model-url URL [--mmproj-url URL] [--embed-url URL]   # other weights (unpinned, your call)
#   sudo ./tools/setup_llama.sh --build-only
#
# THE WEIGHTS are Unsloth's Gemma 4 12B GGUF build (Apache 2.0, no account, no token): the exact
# files are named in tools/model.pins with their SHA-256 and size. A download from the mirror is
# checked by the mirror's signed manifest AND the pin; one from Hugging Face by the pin; a file you
# copied by the pin. Nothing else is accepted under those names , a Hugging Face upload that changed
# under the same name (Unsloth's mmproj did, before their F32 patch_embd fix) is refused, not staged.
# No Python, no huggingface-cli, no Hugging Face account anywhere in setup: curl and sha256sum.
#
# THE MIRROR IS THE ONLY SOURCE (tools/mirror_fetch.sh; published from the web repo's mirror/):
# llama.cpp's source and the weights come from https://www.localghost.ai/mirror, each only after the
# manifest's gpg signature verifies against the site key (tools/mirror-key.asc, pinned by fingerprint)
# and the file's SHA-256 matches it. Every box builds the same engine from the same bytes the phone
# app is pinned to. A mirror that cannot be reached, or does not list a file yet, is SAID and setup
# stops there: a box never takes a file that was not in a signed manifest. GHOST_MIRROR_UPSTREAM=1 is
# the operator's explicit exception for the WEIGHTS (Hugging Face, checked by the pins); llama.cpp
# has no upstream exception. A copy of the mirror on a disk: GHOST_MIRROR=file:///media/usb/mirror.
# A box that had a git checkout of llama.cpp here gets the mirror's source instead (a rebuild, once).
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
LLAMA_TARBALL=""
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
        --from-mirror) shift ;;   # always, now
        --llama-tarball) LLAMA_TARBALL="$2"; shift 2 ;;
        *) echo "unknown arg: $1" >&2; exit 2 ;;
    esac
done
. "$(pwd)/tools/model_pins.sh"

echo "=== 1/6  build dependencies ==="
# (no libcurl: llama-server is built without it, LLAMA_CURL=OFF , the engine downloads nothing, ever)
apt-get install -y --no-install-recommends cmake build-essential ca-certificates curl gpg

FETCH="$(pwd)/tools/mirror_fetch.sh"
SRC_DL="$LLAMA_DIR.mirror-dl"   # the verified source tarball stays here: a rerun finds it current

# from_mirror , llama.cpp's source from the mirror (set llama: one tarball, llama.cpp-<tag>-<commit>
# .tar.gz, that unpacks into ONE folder named after the full commit). A tarball with a new name (a
# new pinned commit) replaces the folder whole, build/ included, so the build below runs again.
# Returns mirror_fetch's code: 0 here, 1 failed, 3 not published.
from_mirror() {
    if [ -n "$LLAMA_TARBALL" ]; then
        # a copy you brought: mirror_fetch keeps it only if its hash is the signed manifest's
        [ -f "$LLAMA_TARBALL" ] || { echo "!! no such file: $LLAMA_TARBALL" >&2; return 1; }
        mkdir -p "$SRC_DL"
        cp -f "$LLAMA_TARBALL" "$SRC_DL/$(basename "$LLAMA_TARBALL")"
    fi
    _rc=0; sh "$FETCH" llama "$SRC_DL" || _rc=$?
    [ "$_rc" = 0 ] || return "$_rc"
    TB="$(grep -E '^llama\.cpp-.+\.tar\.gz$' "$SRC_DL/.mirror-files" | head -1)"
    [ -n "$TB" ] && [ -s "$SRC_DL/$TB" ] || { echo "!! the mirror's set llama holds no llama.cpp-*.tar.gz" >&2; return 1; }
    for f in "$SRC_DL"/*.tar.gz; do   # an older pin's tarball (or a copy that was not it) goes
        [ "$(basename "$f")" = "$TB" ] || rm -f "$f"
    done
    TBP="$SRC_DL/$TB"
    if [ -n "$LLAMA_TARBALL" ] && [ "$(sha256sum "$LLAMA_TARBALL" | cut -d' ' -f1)" != "$(sha256sum "$TBP" | cut -d' ' -f1)" ]; then
        echo "-- the copy you gave ($LLAMA_TARBALL) is not the tarball the signed manifest lists; the mirror's was taken instead"
    fi
    if [ "$(cat "$LLAMA_DIR/.mirror-src" 2>/dev/null)" = "$TB" ]; then
        echo "-- llama.cpp from the mirror: $TB already here"
        return 0
    fi
    # llama.cpp-<tag>-<commit>.tar.gz: the folder inside must be that commit, in full
    _base="${TB%.tar.gz}"; _base="${_base#llama.cpp-}"
    _short="${_base##*-}"
    # the one folder inside carries the full commit (the mirror's is llama.cpp-<40 hex>/; a bare
    # <40 hex>/ is fine too): it must be the commit the file is named for, and everything must sit
    # under it, or --strip-components=1 would scatter it
    tar -tzf "$TBP" | cut -d/ -f1 | grep -vx 'pax_global_header' | sort -u > "$SRC_DL/.top"
    _top="$(head -1 "$SRC_DL/.top")"
    _full="$(printf '%s\n' "$_top" | grep -oE '[0-9a-f]{40}' | head -1)"
    if [ "$(wc -l < "$SRC_DL/.top")" != 1 ]; then
        echo "!! $TB holds more than one folder ($(tr '\n' ' ' < "$SRC_DL/.top")) , not built" >&2; return 1
    fi
    case "$_full" in
        "$_short"*) ;;
        *) echo "!! $TB unpacks into '$_top', which does not name commit $_short in full , not built" >&2; return 1 ;;
    esac
    rm -rf "$LLAMA_DIR.new" && mkdir -p "$LLAMA_DIR.new"
    tar -xzf "$TBP" -C "$LLAMA_DIR.new" --strip-components=1 || { rm -rf "$LLAMA_DIR.new"; return 1; }
    # provenance, read by the build below and by health.sh
    echo "$TB" > "$LLAMA_DIR.new/.mirror-src"
    echo "$_full" > "$LLAMA_DIR.new/.mirror-commit"
    sha256sum "$TBP" | cut -d' ' -f1 > "$LLAMA_DIR.new/.mirror-sha256"
    cp "$SRC_DL"/NOTICE.txt "$SRC_DL"/TERMS-*.txt "$LLAMA_DIR.new/" 2>/dev/null || true
    rm -rf "$LLAMA_DIR.old"
    [ -d "$LLAMA_DIR" ] && mv "$LLAMA_DIR" "$LLAMA_DIR.old"
    mv "$LLAMA_DIR.new" "$LLAMA_DIR" && rm -rf "$LLAMA_DIR.old"
    echo "-- llama.cpp source from the mirror: $TB (commit $_full), signature and hash checked"
}

echo "=== 2/6  llama.cpp , the mirror's source, built in its own folder ==="
mkdir -p "$(dirname "$LLAMA_DIR")"
mrc=0; from_mirror || mrc=$?
if [ "$mrc" = 0 ]; then
    :
elif [ -f "$LLAMA_DIR/.mirror-src" ]; then
    echo "-- the mirror did not deliver llama.cpp this time (above); building the mirror's source already"
    echo "   here, $(cat "$LLAMA_DIR/.mirror-src"), checked when it came"
else
    if [ "$mrc" = 3 ]; then
        echo "!! llama.cpp is not on the mirror yet (set llama not published in this build)" >&2
    else
        echo "!! could not take llama.cpp from the mirror (the lines above say why)" >&2
    fi
    if [ -d "$LLAMA_DIR/.git" ]; then
        echo "   $LLAMA_DIR is a git checkout: it is not built any more (every box builds the mirror's" >&2
        echo "   pinned source, never master). The llama-server already installed keeps running." >&2
    fi
    echo "   Stopping (the line above says why). When it was the mirror not answering: re-run, or bring its tarball:" >&2
    echo "     --llama-tarball <llama.cpp-*.tar.gz>   (checked against the signed manifest all the same)" >&2
    echo "     GHOST_MIRROR=file:///<a copy of the mirror on a disk>" >&2
    exit 1
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
    # GPU BUILD. Building without -DGGML_CUDA=ON produces a CPU-only llama-server that runs a 12B
    # at single-digit tokens/s while the GPU idles (the first static build here made that mistake).
    # The CUDA architecture is THIS machine's, read from the driver (nvidia-smi's compute capability,
    # 8.9 on an RTX 4070 → 89; two different cards give "86;89"), so no one edits a number here for
    # their card. Preflight nvcc: no CUDA toolkit = loud CPU-only warning, not a cryptic cmake failure;
    # a toolkit with no card the driver can see = the same, said.
    CUDA_FLAGS=""
    # the CUDA toolkit installs nvcc under /usr/local/cuda*/bin, which root's PATH (sudo, a
    # scripted run) usually lacks: look there before concluding there is none
    if ! command -v nvcc >/dev/null 2>&1; then
        for d in /usr/local/cuda/bin /usr/local/cuda-*/bin; do
            [ -x "$d/nvcc" ] && { PATH="$d:$PATH"; export PATH; break; }
        done
    fi
    if command -v nvcc >/dev/null 2>&1; then
        ARCHS="${GHOST_CUDA_ARCHS:-$(nvidia-smi --query-gpu=compute_cap --format=csv,noheader 2>/dev/null | tr -d ' .' | grep -E '^[0-9]+$' | sort -u | paste -sd ';' -)}"
        if [ -n "$ARCHS" ]; then
            CUDA_FLAGS="-DGGML_CUDA=ON -DCMAKE_CUDA_ARCHITECTURES=$ARCHS"
            echo "[setup_llama] nvcc found , building WITH CUDA for compute capability $ARCHS ($(nvidia-smi --query-gpu=name --format=csv,noheader 2>/dev/null | paste -sd ',' - || echo 'GHOST_CUDA_ARCHS'))"
        else
            echo "[setup_llama] WARNING: nvcc found but nvidia-smi sees no GPU , building CPU-ONLY. A 12B on CPU is ~5 tok/s."
            echo "[setup_llama]          load the driver (or set GHOST_CUDA_ARCHS=89 for an RTX 4070) and rerun for the GPU build."
        fi
    else
        echo "[setup_llama] WARNING: nvcc not found , building CPU-ONLY. A 12B on CPU is ~5 tok/s."
        echo "[setup_llama]          install cuda-toolkit and rerun for the GPU build."
    fi
    # THE VERSION. A tarball has no .git, so llama.cpp's build-info finds no commit and
    # `llama-server --version` would say "unknown"; the commit the mirror's tarball was cut from is
    # passed in instead (and the build number, when the tag is a bNNNN release tag).
    BUILD_ID=""
    if [ -s "$LLAMA_DIR/.mirror-commit" ]; then
        _src="$(cat "$LLAMA_DIR/.mirror-src")"; _base="${_src%.tar.gz}"; _base="${_base#llama.cpp-}"
        _tag="${_base%-*}"
        BUILD_ID="-DLLAMA_BUILD_COMMIT=${_base##*-}"
        case "$_tag" in b[0-9]*) BUILD_ID="$BUILD_ID -DLLAMA_BUILD_NUMBER=${_tag#b}" ;; esac
        echo "[setup_llama] building $_src (commit $(cat "$LLAMA_DIR/.mirror-commit"))"
    fi
    cmake -S "$LLAMA_DIR" -B "$LLAMA_DIR/build" -DGGML_NATIVE=ON -DBUILD_SHARED_LIBS=OFF -DLLAMA_SERVER_WEBUI=OFF $CUDA_FLAGS $BUILD_ID \
        -DLLAMA_BUILD_TESTS=OFF -DLLAMA_BUILD_EXAMPLES=OFF -DLLAMA_BUILD_SERVER=ON -DLLAMA_CURL=OFF
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
if [ "${GHOST_FROM_UPDATE:-}" = 1 ]; then
    : # update.sh puts it on the volume itself, keeps the old one, and puts it back if the model does not come up
else
echo "-- EXISTING volume? Seed it now while unlocked (via /tmp: the repo lives under /home, which is"
echo "   EMPTY inside secd's mount namespace , ProtectHome , so ns.sh cannot see the repo path):"
echo "     cp $REPO_BIN/llama-server /tmp/llama-server"
echo "     sudo ./tools/ns.sh cp /tmp/llama-server /var/lib/ghost/mnt/slot0/bin/llama-server"
echo "     sudo ./tools/ns.sh chown <the daemons' user, coder or ghostd> /var/lib/ghost/mnt/slot0/bin/llama-server"
echo "     rm /tmp/llama-server"
fi

if [ "$BUILD_ONLY" -eq 1 ]; then
    echo "build-only requested , done. Stage models later with tools/stage_models.sh"
    exit 0
fi

echo "=== 3/6  model weights ==="
DL=/var/lib/ghost/staging/download
mkdir -p "$DL"; chmod 700 "$DL"
# set_notice <set> , the set's NOTICE.txt as NOTICE-<set>.txt (two sets land in one folder, and
# both notices travel onto the volume beside the weights, with the TERMS-*.txt files)
set_notice() { [ -f "$DL/NOTICE.txt" ] && mv -f "$DL/NOTICE.txt" "$DL/NOTICE-$1.txt"; return 0; }
# fetch_pinned <name> , the file into $DL from the mirror (checked by its signed manifest), then
# against the pin as well. A file already in $DL that matches the pin is kept: a rerun costs nothing.
# Not on the mirror, or the mirror unreachable: said, and nothing else is tried , except with
# GHOST_MIRROR_UPSTREAM=1, the pin's upstream (Hugging Face, no account), checked by the pin alone.
fetch_pinned() {
    _n="$1"; _out="$DL/$_n"
    if [ -s "$_out" ] && pin_check "$_out" >/dev/null 2>&1; then
        echo "-- $_n already downloaded and matches the pin"
        return 0
    fi
    _rc=0; sh "$FETCH" models "$DL" "$_n" || _rc=$?
    set_notice models
    if [ "$_rc" = 0 ] && [ -s "$_out" ]; then
        echo "-- $_n from the mirror"
    else
        if [ "$_rc" = 3 ]; then
            echo "!! $_n is not on the mirror (set models; not published in this build)" >&2
        else
            echo "!! could not take $_n from the mirror (the lines above say why; a rerun resumes)" >&2
        fi
        _url="$(pin_url "$_n")"
        [ "${GHOST_MIRROR_UPSTREAM:-}" = 1 ] && [ -n "$_url" ] || return 1
        echo "-- !! GHOST_MIRROR_UPSTREAM=1: fetching $_n from $_url , checked by tools/model.pins only, NOT a signed manifest"
        curl -fL --proto-redir =https --retry 3 --retry-delay 5 -C - --progress-bar -o "$_out" "$_url" || { echo "!! download failed: $_url" >&2; return 1; }
    fi
    pin_check "$_out" || { rm -f "$_out"; return 1; }
}
if [ -z "$MODELS_DIR" ] && [ -z "$MODEL_URL" ] && [ -z "$MODEL_FILE$MMPROJ_FILE$EMBED_FILE" ]; then
    # the default: exactly the pinned files
    for n in $(pin_names); do
        fetch_pinned "$n" || { echo "!! could not get $n , stopping. Re-run when the mirror has it, or copy it over and re-run with --models <dir> (or --model/--mmproj): checked against the pin." >&2; exit 3; }
    done
    # THE EMBEDDER: the mirror carries EmbeddingGemma 300M QAT Q8_0 in its own set, `embeddings`
    # (Gemma Terms of Use beside it), checked by the signed manifest; pinned here too once its line
    # is in model.pins. searchd prefers it and, when a box moves to it from an older embedder,
    # embeds the archive again by itself (vectors from two models never mix).
    EMB=embeddinggemma-300m-qat-Q8_0.gguf
    if [ -n "$(pin_url "$EMB")" ]; then
        :  # pinned: fetched above
    elif [ -s "$DL/$EMB" ]; then
        echo "-- $EMB already here"
    else
        erc=0; sh "$FETCH" embeddings "$DL" "$EMB" || erc=$?
        set_notice embeddings
        if [ "$erc" = 0 ] && [ -s "$DL/$EMB" ]; then
            echo "-- $EMB from the mirror (signed manifest checked; the Gemma Terms of Use beside it)"
            pin_check "$DL/$EMB" || { rm -f "$DL/$EMB"; exit 4; }
        else
            [ "$erc" = 3 ] && _why="not on the mirror yet (set embeddings)" || _why="could not be taken from the mirror (above)"
            if [ -s "$DL/embeddinggemma-300m-q8.gguf" ]; then
                echo "-- $EMB $_why; keeping the embeddinggemma-300m-q8.gguf given here"
            else
                echo "-- $EMB $_why , search runs FTS-only until it is provided; a box that has an"
                echo "   embedder already keeps it. (--embed <file>, or re-run when the mirror has it)"
            fi
        fi
    fi
    MODELS_DIR="$DL"
elif [ -n "$MODEL_FILE" ] || [ -n "$MMPROJ_FILE" ] || [ -n "$EMBED_FILE" ]; then
    # files copied over one by one: each checked against its pin (by its NAME on the box), then
    # linked into $DL under that name , the copy happens once, at staging
    # an embedder keeps its own name when it is one searchd knows (the old q8 or the QAT build);
    # anything else is taken as the QAT build and checked against its pin, if it has one
    case "$(basename "${EMBED_FILE:-x}")" in
        embeddinggemma-300m-q8.gguf|embeddinggemma-300m-qat-Q8_0.gguf) EMB_NAME="$(basename "$EMBED_FILE")" ;;
        *) EMB_NAME=embeddinggemma-300m-qat-Q8_0.gguf ;;
    esac
    for pair in "gemma-4-12b-it-Q4_K_M.gguf=$MODEL_FILE" "mmproj-F16.gguf=$MMPROJ_FILE" "$EMB_NAME=$EMBED_FILE"; do
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

echo "=== 4/6  stage for ingest at next unlock ==="
./tools/stage_models.sh "$MODELS_DIR"
# staged copies live under /var/lib/ghost/staging/ai-models; remove the download scratch if we made it
if [ -d "$DL" ] && [ "$MODELS_DIR" = "$DL" ]; then rm -rf "$DL"; fi

# THE PHONE'S MODEL, at setup (the network is allowed now, never later): the box offers it to its
# own phones, which never fetch it from the internet. A miss here is said, not fatal: phones then
# read with the box's model, and `sudo ./tools/phone_model.sh` installs it later.
PHONE="skipped (GHOST_PHONE_MODEL=0)"
if [ "${GHOST_PHONE_MODEL:-1}" != 0 ]; then
    echo "=== 5/6  the phone's model (offered by this box to its phones) ==="
    if sh ./tools/phone_model.sh; then
        PHONE="installed , the app downloads it from the box (MODELS in the menu)"
    else
        PHONE="NOT installed (above) , the box works without it; later: sudo ./tools/phone_model.sh"
    fi
fi

# THE SPEECH ENGINE for voice notes (whisper.cpp from the mirror's source, a ggml model from set
# speech), at setup for the same reason as the phone's model. A miss is said, not fatal: notes are
# kept on the box and wait, and `sudo ./tools/update.sh speech` brings the engine later.
SPEECH="skipped (GHOST_SPEECH=0)"
if [ "${GHOST_SPEECH:-1}" != 0 ]; then
    echo "=== 6/6  the speech engine (voice notes, transcribed on this box) ==="
    src=0; bash ./tools/setup_whisper.sh || src=$?
    case "$src" in
        0) SPEECH="whisper.cpp $(cat /opt/localghost/whisper.cpp/.mirror-src 2>/dev/null) + model, staged for the next unlock" ;;
        3) SPEECH="not on the mirror yet (sets whisper, speech); voice notes wait. Later: sudo ./tools/update.sh speech" ;;
        *) SPEECH="NOT installed (above); voice notes wait. Later: sudo ./tools/update.sh speech" ;;
    esac
fi

echo "----------------------------------------"
echo "Inference setup complete:"
echo "  llama.cpp    $LLAMA_DIR ($(cat "$LLAMA_DIR/.mirror-src" 2>/dev/null || echo '?'); llama-server in the repo's bin/, seeded to the volume)"
echo "  models       staged , the NEXT UNLOCK ingests them onto the encrypted volume"
echo "  phone model  $PHONE"
echo "  speech       $SPEECH"
echo "Unlock from the app, then verify:"
echo "  sudo journalctl -u ghost.secd --since '2 min ago' | grep -i 'ingested\\|oracled'"
echo "  sudo ./tools/ns.sh ./bin/ghost-cli ghost.oracled status"
