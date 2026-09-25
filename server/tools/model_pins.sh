#!/bin/sh
# model_pins.sh , the shared reading of tools/model.pins, sourced by setup_llama.sh, stage_models.sh
# and models_check.sh (`. "$(dirname "$0")/model_pins.sh"`). POSIX sh: no arrays, no bashisms.
#
#   pin_sha <name>       the pinned SHA-256, or nothing when the file is not pinned
#   pin_size <name>      the pinned size in bytes
#   pin_url <name>       where the file comes from upstream
#   pin_names            every pinned name, one per line
#   pin_check <path>     0 when the file matches its pin (or is not pinned: prints "unpinned"),
#                        1 when it does not; prints one line either way
PINS="${GHOST_MODEL_PINS:-$(cd "$(dirname "$0")" && pwd)/model.pins}"

pin_line() { # pin_line <name> , the pin line's fields, or nothing
    [ -f "$PINS" ] || return 1
    awk -v n="$1" '$1 !~ /^#/ && $1 == n && NF >= 4 { print $2, $3, $4; exit }' "$PINS"
}
pin_sha()  { pin_line "$1" | cut -d' ' -f1; }
pin_size() { pin_line "$1" | cut -d' ' -f2; }
pin_url()  { pin_line "$1" | cut -d' ' -f3; }
pin_names() { [ -f "$PINS" ] && awk '$1 !~ /^#/ && NF >= 4 { print $1 }' "$PINS"; }

pin_check() { # pin_check <path>
    _p="$1"; _n="$(basename "$_p")"
    _want="$(pin_sha "$_n")"
    if [ -z "$_want" ]; then
        echo "   $_n: unpinned (accepted as is)"
        return 0
    fi
    _size="$(stat -c%s "$_p" 2>/dev/null || echo 0)"
    _wsize="$(pin_size "$_n")"
    if [ "$_size" != "$_wsize" ]; then
        echo "!! $_n: $_size bytes, the pinned build is $_wsize , not the same file"
        return 1
    fi
    _got="$(sha256sum "$_p" | cut -d' ' -f1)"
    if [ "$_got" != "$_want" ]; then
        echo "!! $_n: sha256 $_got, pinned $_want , not the same file"
        case "$_n" in mmproj*) echo "   (an mmproj-F16.gguf of the right size with the wrong hash is Unsloth's earlier upload, before their F32 patch_embd fix; replace it with the pinned one)" ;; esac
        return 1
    fi
    echo "   $_n: matches the pin"
    return 0
}
