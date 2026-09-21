#!/usr/bin/env bash
# gpu.sh , is the model on the GPU, and is the GPU the one doing the work? One screen, with the
# clock on every line, from five independent angles that must agree:
#
#   1. nvidia-smi: the card, its memory, and WHICH PROCESSES hold it (llama-server should be
#      there with gigabytes; a second, older llama-server there is the orphan that steals them).
#   2. the llama-server processes themselves: how many, how old, whose child, on which port,
#      with which -ngl. More than one is the bug; parent 1 is an orphan.
#   3. the binary on the volume: linked against CUDA or not.
#   4. oracled's own account (`ghost-cli ghost.oracled models`): what llama-server said at
#      startup , device, layers offloaded, VRAM , and the tokens/second of its answers.
#   5. a timed answer, with the GPU's utilization sampled while it runs: 0% throughout means the
#      CPU did it, whatever anything else says.
#
#   sudo ./tools/gpu.sh            # the whole pass (box unlocked)
#   sudo ./tools/gpu.sh --no-infer # skip the timed answer
#
# Exit status: 0 when every angle says GPU, 1 when any says otherwise, 2 when it could not tell.

set -u
_stamp() { while IFS= read -r _l; do printf '%(%H:%M:%S)T %s\n' -1 "$_l"; done; }
exec > >(_stamp) 2>&1
_stamp_pid=$!
trap 'exec 1>&- 2>&-; wait "$_stamp_pid" 2>/dev/null || true' EXIT

NO_INFER=0
[ "${1:-}" = "--no-infer" ] && NO_INFER=1

MOUNT="${GHOST_MOUNT:-/var/lib/ghost/mnt/slot0}"
CLI="${GHOST_CLI:-./bin/ghost-cli}"
[ -x "$CLI" ] || CLI="/opt/localghost/bin/ghost-cli"
[ -x "$CLI" ] || CLI="$(command -v ghost-cli || echo ./bin/ghost-cli)"
DOOR=""
if [ ! -d "$MOUNT/bin" ]; then
    SECD_PID="$(pidof ghost.secd || true)"; SECD_PID="${SECD_PID%% *}"
    [ -n "$SECD_PID" ] && [ -d "/proc/$SECD_PID/root$MOUNT/bin" ] && DOOR="/proc/$SECD_PID/root"
fi
BIN="$DOOR$MOUNT/bin/llama-server"
LOG_DIR="$DOOR$MOUNT/logs"

green() { printf '\033[32m%s\033[0m' "$1"; }
red()   { printf '\033[31m%s\033[0m' "$1"; }
verdict_gpu=0; verdict_bad=0; verdict_unclear=0
yes() { verdict_gpu=$((verdict_gpu + 1)); printf '  %s %s\n' "$(green OK)" "$1"; }
bad() { verdict_bad=$((verdict_bad + 1)); printf '  %s %s\n' "$(red NO)" "$1"; }
unclear() { verdict_unclear=$((verdict_unclear + 1)); printf '  ?? %s\n' "$1"; }

echo "gpu check $(date '+%Y-%m-%d %H:%M:%S %Z') on $(hostname)"

echo
echo "=== 1. the card (nvidia-smi) ==="
if command -v nvidia-smi >/dev/null 2>&1; then
    if smi=$(timeout 15 nvidia-smi --query-gpu=name,driver_version,memory.used,memory.total,utilization.gpu,temperature.gpu --format=csv,noheader 2>&1); then
        echo "  $smi" | sed 's/, / · /g'
        apps=$(timeout 15 nvidia-smi --query-compute-apps=pid,process_name,used_memory --format=csv,noheader 2>/dev/null)
        if [ -z "$apps" ]; then
            bad "no process holds the GPU right now , the model is not on it (or nothing has loaded yet)"
        else
            llama_on_gpu=0
            while IFS=, read -r pid name mem; do
                pid=$(echo "$pid" | tr -d ' '); name=$(echo "$name" | xargs); mem=$(echo "$mem" | xargs)
                info=$(ps -o ppid=,etimes=,stat= -p "$pid" 2>/dev/null | xargs)
                set -- $info
                ppid="${1:-?}"; up="${2:-?}"; stat="${3:-?}"
                tag=""
                case "$name" in *llama-server*) llama_on_gpu=$((llama_on_gpu + 1)); tag=" ← llama-server";; esac
                [ "$ppid" = "1" ] && tag="$tag, ORPHAN (parent 1)"
                echo "  pid $pid $name $mem · up ${up}s · parent $ppid · state $stat$tag"
            done <<< "$apps"
            case "$llama_on_gpu" in
                0) bad "the GPU is held, but not by llama-server";;
                1) yes "one llama-server holds the GPU";;
                *) bad "$llama_on_gpu llama-servers hold the GPU: all but the newest are orphans stealing VRAM";;
            esac
        fi
    else
        bad "nvidia-smi failed or hung (15s): $smi , a hung nvidia-smi is a wedged driver; dmesg: $(dmesg 2>/dev/null | grep -iE 'xid|nvrm' | tail -2 | cut -c1-160 | tr '\n' ' ')"
    fi
else
    unclear "nvidia-smi not installed , no NVIDIA driver on this box, or not on PATH"
fi
xid=$(dmesg 2>/dev/null | grep -iE 'NVRM: Xid' | tail -3)
[ -n "$xid" ] && { echo "  GPU faults in dmesg (Xid):"; echo "$xid" | cut -c1-200 | sed 's/^/    /'; }

echo
echo "=== 2. llama-server processes ==="
procs=$(pgrep -f 'bin/llama-server' 2>/dev/null || true)
count=$(echo "$procs" | grep -c . || true)
if [ "$count" -eq 0 ]; then
    bad "no llama-server running , is the box unlocked and oracled up? (health.sh)"
else
    for pid in $procs; do
        line=$(ps -o ppid=,etimes=,stat=,args= -p "$pid" 2>/dev/null)
        set -- $line
        ppid="$1"; up="$2"; stat="$3"; shift 3
        args="$*"
        port=$(echo "$args" | sed -n 's/.*--port \([0-9]*\).*/\1/p')
        ngl=$(echo "$args" | sed -n 's/.*-ngl \([0-9]*\).*/\1/p')
        parent=$(ps -o comm= -p "$ppid" 2>/dev/null || echo "?")
        printf '  pid %s · parent %s (%s) · up %ss (%s) · state %s · port %s · -ngl %s\n' "$pid" "$ppid" "$parent" "$up" "$(( up / 86400 ))d$(( up % 86400 / 3600 ))h" "$stat" "${port:-?}" "${ngl:-none}"
        # SIGKILL pending (bit 9 of the shared or thread mask) on a process that is still running:
        # delivered, not acted on, which only a process that never returns from the kernel does.
        pend=0
        for v in $(grep -E '^(ShdPnd|SigPnd)' "/proc/$pid/status" 2>/dev/null | awk '{print $2}'); do pend=$(( pend | 0x$v )); done
        if [ $(( pend & 0x100 )) -ne 0 ]; then
            bad "pid $pid has SIGKILL PENDING and is still running: stuck inside the kernel (GPU driver). Only a reboot ends it, and it holds the VRAM until then.  sudo reboot"
            echo "      kernel stack: $(head -4 "/proc/$pid/stack" 2>/dev/null | tr '\n' ' ' | cut -c1-200)"
        elif [ "$ppid" = "1" ]; then
            bad "pid $pid is an ORPHAN: its oracled is gone and nothing will stop it , it holds the port and the VRAM the next one needs.  sudo kill -9 $pid"
        elif [ "$parent" != "ghost.oracled" ]; then
            unclear "pid $pid is not a child of ghost.oracled ($parent)"
        else
            yes "pid $pid is oracled's child"
        fi
        [ -z "$ngl" ] && bad "pid $pid runs without -ngl: nothing is offloaded to the GPU"
    done
    [ "$count" -gt 1 ] && bad "$count llama-servers: there must be exactly one. The lock path and oracled now kill strays; this build's first halt clears it, or kill the orphan by hand now."
fi

echo
echo "=== 3. the binary on the volume ==="
if [ -x "$BIN" ]; then
    libs=$(ldd "$BIN" 2>/dev/null | grep -Ei 'cuda|cublas|ggml-cuda' | awk '{print $1}' | sort -u | tr '\n' ' ')
    if [ -n "$libs" ]; then
        yes "linked against CUDA: $libs"
    elif strings -n 8 "$BIN" 2>/dev/null | grep -q 'ggml_cuda_init'; then
        yes "CUDA compiled in (static)"
    else
        bad "no CUDA in this binary , it can only run on the CPU. Rebuild llama-server with GGML_CUDA=ON (tools/setup_llama.sh) and redeploy."
    fi
    echo "  $BIN · $(stat -c '%s bytes · modified %y' "$BIN" 2>/dev/null | cut -c1-60)"
else
    unclear "no llama-server at $BIN , box locked, or the volume is not reachable from here (run as root)"
fi

echo
echo "=== 4. oracled's own account (ghost-cli ghost.oracled models) ==="
if models=$("$CLI" ghost.oracled models 2>&1); then
    echo "$models" | head -c 1200 | sed 's/^/  /'
    echo
    v=$(echo "$models" | sed -n 's/.*"verdict":"\([^"]*\)".*/\1/p' | head -1)
    sp=$(echo "$models" | sed -n 's/.*"speed":"\([^"]*\)".*/\1/p' | head -1)
    case "$v" in
        "on the GPU"*) yes "$v";;
        "") unclear "oracled answered without a verdict (a build before this one?)";;
        *) bad "$v";;
    esac
    [ -n "$sp" ] && echo "  speed: $sp"
else
    unclear "oracled did not answer: $models"
fi
if [ -d "$LOG_DIR" ]; then
    latest=$(ls -1t "$LOG_DIR"/ghost.oracled-*.log 2>/dev/null | head -1)
    if [ -n "$latest" ]; then
        echo "  oracled's log, the GPU lines ($(basename "$latest")):"
        grep -h 'llama-server on the GPU\|llama-server on the CPU\|llama-server unknown\|stray llama-server\|CUDA device found but' "$latest" 2>/dev/null | tail -3 | cut -c1-220 | sed 's/^/    /'
        grep -h 'ggml_cuda_init\|offloaded .* layers\|no usable GPU' "$latest" 2>/dev/null | tail -3 | cut -c1-160 | sed 's/^/    /'
    fi
fi

if [ "$NO_INFER" = 0 ]; then
    echo
    echo "=== 5. a timed answer, with the GPU watched while it runs ==="
    samples=""
    if command -v nvidia-smi >/dev/null 2>&1; then
        ( for i in 1 2 3 4 5 6; do sleep 0.7; nvidia-smi --query-gpu=utilization.gpu --format=csv,noheader,nounits 2>/dev/null; done ) > /tmp/gpu-util.$$ &
        sampler=$!
    fi
    t0=$(date +%s.%N)
    out=$("$CLI" ghost.oracled infer input="Count from one to thirty in words, separated by commas." class=local-small priority=1 maxTokens=96 2>&1)
    rc=$?
    t1=$(date +%s.%N)
    took=$(awk -v a="$t0" -v b="$t1" 'BEGIN{printf "%.1f", b-a}')
    if [ -n "${sampler:-}" ]; then wait "$sampler" 2>/dev/null; samples=$(tr '\n' ' ' < /tmp/gpu-util.$$ 2>/dev/null); rm -f /tmp/gpu-util.$$; fi
    if [ $rc -ne 0 ]; then
        bad "infer failed in ${took}s: $(echo "$out" | head -c 200)"
    else
        chars=$(echo "$out" | sed -n 's/.*"output":"\(.*\)".*/\1/p' | wc -c)
        echo "  answered in ${took}s (~$(( chars / 4 )) tokens of output)"
        [ -n "$samples" ] && echo "  GPU utilization while it ran: ${samples}%"
        after=$("$CLI" ghost.oracled models 2>/dev/null | sed -n 's/.*"tokPerSecLast":\([0-9.]*\).*/\1/p' | head -1)
        if [ -n "$after" ] && [ "$after" != "0" ]; then
            echo "  llama-server's own timing for that answer: $after tok/s"
            if awk -v t="$after" 'BEGIN{exit !(t >= 15)}'; then yes "$after tok/s is GPU speed"
            elif awk -v t="$after" 'BEGIN{exit !(t >= 6)}'; then unclear "$after tok/s is slow for a GPU , partial offload, a shared card, or a throttled one"
            else bad "$after tok/s is CPU speed"; fi
        fi
        if [ -n "$samples" ]; then
            peak=$(echo "$samples" | tr ' ' '\n' | sort -n | tail -1)
            if [ "${peak:-0}" -ge 30 ] 2>/dev/null; then yes "the GPU worked while it answered (peak ${peak}%)"
            else bad "the GPU sat idle while it answered (peak ${peak:-0}%) , whatever else says, the CPU did this one"; fi
        fi
    fi
fi

echo
echo "----------------------------------------"
if [ "$verdict_bad" -gt 0 ]; then
    printf '%s  %d checks say the GPU is NOT doing the work , read the NO lines above, top to bottom\n' "$(red "GPU: NO")" "$verdict_bad"
    exit 1
elif [ "$verdict_gpu" -gt 0 ] && [ "$verdict_unclear" -eq 0 ]; then
    printf '%s  every angle agrees\n' "$(green "GPU: YES")"
    exit 0
else
    printf 'GPU: UNCLEAR  %d checks passed, %d could not tell\n' "$verdict_gpu" "$verdict_unclear"
    exit 2
fi
