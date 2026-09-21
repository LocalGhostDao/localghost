#!/usr/bin/env bash
# unwedge.sh , a process that survives SIGKILL is not a process problem, it is a driver problem.
# The task is spinning inside the NVIDIA kernel module and never comes back to user space, so the
# kernel never gets to act on the SIGKILL that is pending on it. Root cannot signal it out, cannot
# attach to it, cannot unload a module a CPU is executing. What root can do is find out exactly
# what it is stuck on, and , in the one case where the card is merely wedged rather than gone ,
# reset the card underneath it so the wait bails. A card that fell off the bus (Xid 79) answers no
# reset; then the only lever left is a PCI remove + rescan of the slot, which sometimes retrains
# the link, and the clean exit is a COLD reboot (power off, wait, on: a warm reboot often leaves
# a dropped card dropped, because the PCIe rail never falls).
#
#   sudo ./tools/unwedge.sh            # diagnose: who is stuck, on which core, since when, why
#   sudo ./tools/unwedge.sh --reset    # diagnose, then the levers, one at a time, asking first
#   sudo ./tools/unwedge.sh --pid N    # this process rather than the one found
#   sudo ./tools/unwedge.sh --gpu 0000:2e:00.0
#
# With nothing stuck it is the after-the-reboot check: the card is back, what the PCIe link and
# its power management say, whether it fell off in this boot as well.
#
# Diagnose writes nothing but a backtrace request into the kernel log (sysrq l). --reset stops the
# stack first (a reset crashes whatever is on the card), asks before each lever, checks after each.
# Exit: 0 nothing stuck (or cleared), 1 stuck and the reboot is the way, 2 could not tell.

set -u
_stamp() { while IFS= read -r _l; do printf '%(%H:%M:%S)T %s\n' -1 "$_l"; done; }
exec > >(_stamp) 2>&1
_stamp_pid=$!
trap 'exec 1>&- 2>&-; wait "$_stamp_pid" 2>/dev/null || true' EXIT

RESET=0; PID=""; GPU=""; SYSRQ=1; FORCE=0
while [ $# -gt 0 ]; do
    case "$1" in
        --reset) RESET=1 ;;
        --pid) PID="${2:-}"; shift ;;
        --gpu) GPU="${2:-}"; shift ;;
        --no-sysrq) SYSRQ=0 ;;
        --force) FORCE=1 ;;
        -h|--help) sed -n '2,22p' "$0"; exit 0 ;;
        *) echo "unknown flag $1 (see --help)"; exit 2 ;;
    esac
    shift
done
if [ "$(id -u)" != 0 ]; then echo "run as root: sudo $0"; exit 2; fi

HZ=$(getconf CLK_TCK 2>/dev/null || echo 100)
NOW=$(date +%s)
UP=$(cut -d. -f1 /proc/uptime)
BOOT=$(( NOW - UP ))
say() { echo "  $*"; }
head_() { echo; echo "=== $* ==="; }
# stat fields after "pid (comm) ": 1 state, 12 utime, 13 stime, 20 starttime, 37 processor.
statrest() { sed 's/^.*) //' "$1" 2>/dev/null; }
pending_kill() { # true when SIGKILL is pending on the thread or the group (bit 9 of the masks)
    local pend=0 v
    for v in $(grep -E '^(ShdPnd|SigPnd)' "/proc/$1/status" 2>/dev/null | awk '{print $2}'); do pend=$(( pend | 0x$v )); done
    [ $(( pend & 0x100 )) -ne 0 ]
}
pstate() { set -- $(statrest "/proc/$1/stat"); echo "${1:-gone}"; }
alive() { local s; s=$(pstate "$1"); [ "$s" != gone ] && [ "$s" != Z ]; }
kmsg() { dmesg -T 2>/dev/null || dmesg 2>/dev/null || true; }
# "[4274592.825604] NVRM: Xid ..." , the seconds since boot, as a wall-clock time.
when_uptime() { date -d "@$(( BOOT + ${1%%.*} ))" '+%Y-%m-%d %H:%M' 2>/dev/null || echo "uptime ${1}s"; }

echo "unwedge $(date -u '+%Y-%m-%d %H:%M:%S UTC') on $(hostname) · up $(( UP / 86400 ))d$(( UP % 86400 / 3600 ))h · booted $(date -d @$BOOT '+%Y-%m-%d %H:%M')"

# ---------------------------------------------------------------- 1. who is stuck
head_ "1. processes with SIGKILL pending that are still running"
STUCK=""
if [ -n "$PID" ]; then
    [ -d "/proc/$PID" ] || { say "no pid $PID"; exit 2; }
    STUCK="$PID"
else
    for d in /proc/[0-9]*; do
        p=${d#/proc/}
        [ "$p" = "$$" ] && continue
        alive "$p" || continue
        pending_kill "$p" && STUCK="$STUCK $p"
    done
    STUCK=${STUCK# }
fi
if [ -z "$STUCK" ]; then
    say "none: nothing on this box is unkillable"
fi
for p in $STUCK; do
    set -- $(statrest "/proc/$p/stat")
    say "pid $p · $(cat /proc/$p/comm 2>/dev/null) · state $1 · parent $(awk '/^PPid/{print $2}' /proc/$p/status) · threads $(awk '/^Threads/{print $2}' /proc/$p/status) · started $(date -d "@$(( BOOT + ${20:-0} / HZ ))" '+%Y-%m-%d %H:%M' 2>/dev/null) · cmd: $(tr '\0' ' ' < /proc/$p/cmdline | cut -c1-120)"
done

# ---------------------------------------------------------------- 2. the card
head_ "2. the card"
if [ -z "$GPU" ]; then
    GPU=$(ls /sys/bus/pci/drivers/nvidia/ 2>/dev/null | grep -E '^[0-9a-f]{4}:' | head -1)
    [ -z "$GPU" ] && GPU=$(lspci -Dd 10de: 2>/dev/null | grep -Ei 'VGA|3D' | awk '{print $1}' | head -1)
    [ -z "$GPU" ] && GPU=$(lspci -Dd 10de: 2>/dev/null | awk '{print $1}' | head -1)
fi
CARD_GONE=0; CARD_ABSENT=0
if [ -z "$GPU" ]; then
    if command -v lspci >/dev/null; then say "no NVIDIA device on the PCI bus at all (lspci -d 10de: is empty) , the card is off the bus, or this is not the box"
    else say "no nvidia-bound device in /sys and no lspci to look further (apt install pciutils)"; fi
    CARD_ABSENT=1
else
    say "device $GPU · $(lspci -s "$GPU" 2>/dev/null | cut -d' ' -f2- | cut -c1-90)"
    if [ -r "/sys/bus/pci/devices/$GPU/config" ]; then
        vend=$(od -An -tx1 -N2 "/sys/bus/pci/devices/$GPU/config" 2>/dev/null | tr -d ' ')
        if [ "$vend" = "ffff" ]; then
            say "config space reads 0xffff: the slot answers nothing , the card has FALLEN OFF THE BUS"
            CARD_GONE=1
        else
            say "config space answers (vendor 0x${vend:2:2}${vend:0:2}) · driver: $(basename "$(readlink "/sys/bus/pci/devices/$GPU/driver" 2>/dev/null)" 2>/dev/null || echo none) · power: $(cat "/sys/bus/pci/devices/$GPU/power/runtime_status" 2>/dev/null || echo ?)"
        fi
    fi
    lnk=$(lspci -vv -s "$GPU" 2>/dev/null | grep -E 'LnkSta:|DevSta:' | sed 's/^[[:space:]]*//' | tr '\n' ' ' | cut -c1-160)
    [ -n "$lnk" ] && say "$lnk"
fi
mods=$(lsmod 2>/dev/null | awk '/^nvidia/{printf "%s(%s) ", $1, $3}')
say "modules: ${mods:-none loaded}"
smi=$(timeout 15 nvidia-smi --query-gpu=index,name,pci.bus_id,memory.used,memory.total,temperature.gpu --format=csv,noheader 2>&1); rc=$?
command -v nvidia-smi >/dev/null || rc=127
case $rc in
    127) say "nvidia-smi: not installed here" ;;
    0) say "nvidia-smi: $(echo "$smi" | head -3 | tr '\n' ';')" ;;
    124) say "nvidia-smi HANGS (15s): the driver is wedged for everyone, not only the stuck process" ;;
    *) say "nvidia-smi: $(echo "$smi" | head -1 | cut -c1-120)" ;;
esac

# ---------------------------------------------------------------- 3. the kernel's account
head_ "3. the kernel log"
XIDS=$(kmsg | grep -E 'NVRM: Xid')
XID79=0
if [ -z "$XIDS" ]; then
    say "no Xid (GPU fault) logged in this boot"
else
    n=$(echo "$XIDS" | wc -l)
    say "$n Xid line(s) in this boot , codes: $(echo "$XIDS" | grep -oE '\): [0-9]+' | awk '{print $2}' | sort -n | uniq -c | awk '{printf "%s×%s ", $2, $1}')"
    say "first: $(echo "$XIDS" | head -1 | cut -c1-150)"
    [ "$n" -gt 1 ] && say "last:  $(echo "$XIDS" | tail -1 | cut -c1-150)"
    # the same lines in dmesg's raw form carry seconds-since-boot, which turn into wall clock.
    raw=$(dmesg 2>/dev/null | grep -E 'NVRM: Xid' | head -1 | sed -n 's/^\[ *\([0-9.]*\)\].*/\1/p')
    [ -n "$raw" ] && say "the first fault was at $(when_uptime "$raw") , $(( (UP - ${raw%%.*}) / 86400 ))d$(( (UP - ${raw%%.*}) % 86400 / 3600 ))h ago"
    for code in $(echo "$XIDS" | grep -oE '\): [0-9]+' | awk '{print $2}' | sort -un); do
        case $code in
            13) m="graphics engine exception , the program's fault, usually; the card survives" ;;
            31) m="GPU memory page fault , the program's, the card survives" ;;
            43) m="GPU stopped processing a channel , the program's" ;;
            45) m="channels reset after an earlier error , the cleanup, not the cause" ;;
            48|63|64|94|95|140) m="memory (ECC) error , the card's memory is failing; 95/140 need a reset, repeats mean a replacement" ;;
            79) m="GPU HAS FALLEN OFF THE BUS , the PCIe link dropped: power delivery (PSU, the 8-pin, a riser), PCIe power management (pcie_aspm), heat, or a dying card. No reset reaches it; a cold reboot brings it back if the hardware is sound"; XID79=1 ;;
            109) m="context switch timeout , the card stopped answering; a reset usually clears it" ;;
            119|120) m="GSP firmware stopped answering (RPC timeout) , the driver waits on it forever; a reset under it usually makes the wait bail" ;;
            *) m="see NVIDIA's Xid table" ;;
        esac
        say "Xid $code: $m"
    done
fi
aer=$(kmsg | grep -E 'AER:|pcieport.*(error|Error)|DPC' | tail -3)
[ -n "$aer" ] && { say "PCIe errors around it:"; echo "$aer" | sed 's/^/      /' | cut -c1-160; }
lock=$(kmsg | grep -E 'soft lockup|hard LOCKUP|rcu: INFO|hung task' | tail -3)
[ -n "$lock" ] && { say "lockups:"; echo "$lock" | sed 's/^/      /' | cut -c1-160; }
[ -z "$aer$lock" ] && say "no PCIe (AER) errors, no CPU lockups logged"

[ -z "$STUCK" ] && {
    echo
    if [ "$CARD_GONE" = 1 ] || [ "$CARD_ABSENT" = 1 ]; then
        echo "nothing is stuck, but there is NO CARD: it fell off the bus in this boot and nothing held it. Cold reboot."
        exit 1
    fi
    if [ "$XID79" = 1 ]; then echo "the card is back, and it fell off the bus in THIS boot too: the cause is still there (power, pcie_aspm, heat)."; exit 1; fi
    echo "clear: nothing stuck, the card answers. (gpu.sh says whether the model is on it.)"
    exit 0
}

# ---------------------------------------------------------------- 4. inside the stuck process
head_ "4. inside the stuck process"
SPIN_CPUS=""
for p in $STUCK; do
    # what it holds
    fds=$(ls -l /proc/$p/fd 2>/dev/null | grep -oE '/dev/nvidia[^ ]*' | sort | uniq -c | awk '{printf "%s×%s ", $2, $1}')
    say "pid $p holds: ${fds:-no /dev/nvidia* descriptors} · syscall: $(cut -d' ' -f1 /proc/$p/syscall 2>/dev/null || echo ?) · wchan: $(cat /proc/$p/wchan 2>/dev/null || echo ?)"
    # per thread: state, cpu, and how much of a CPU it burns in the kernel over two seconds
    declare -A t0=()
    for t in /proc/$p/task/*; do set -- $(statrest "$t/stat"); t0[${t##*/}]=${13:-0}; done
    sleep 2
    for t in /proc/$p/task/*; do
        tid=${t##*/}
        set -- $(statrest "$t/stat") || continue
        [ $# -lt 37 ] && continue
        st=$1; cpu=${37}; d=$(( ${13:-0} - ${t0[$tid]:-0} )); pct=$(( d * 100 / (2 * HZ) ))
        w=$(cat "$t/wchan" 2>/dev/null); sc=$(cut -d' ' -f1 "$t/syscall" 2>/dev/null)
        if [ "$pct" -ge 50 ]; then
            say "thread $tid: state $st, on cpu $cpu, ${pct}% of a core INSIDE THE KERNEL (stime) , spinning; wchan ${w:-?}, syscall ${sc:-?}"
            SPIN_CPUS="$SPIN_CPUS $cpu"
        else
            say "thread $tid: state $st, cpu $cpu, ${pct}% kernel time, wchan ${w:-?}, syscall ${sc:-?}"
        fi
        stk=$(cat "$t/stack" 2>/dev/null | head -5 | tr '\n' ' ' | cut -c1-200)
        [ -n "$stk" ] && say "    kernel stack: $stk"
    done
    unset t0
done
[ -n "$SPIN_CPUS" ] && say "an empty kernel stack on a spinning thread is expected: a task that is ON a CPU cannot be unwound from /proc; the NMI backtrace below can"

# the backtrace of every busy CPU, by NMI, into the kernel log: the one view of a spinning task.
if [ "$SYSRQ" = 1 ] && [ -w /proc/sysrq-trigger ] && [ -w /dev/kmsg ]; then
    old=$(cat /proc/sys/kernel/sysrq 2>/dev/null || echo 1)
    echo 1 > /proc/sys/kernel/sysrq
    echo "unwedge: backtrace request for $STUCK" > /dev/kmsg
    echo l > /proc/sysrq-trigger
    sleep 1
    echo "$old" > /proc/sys/kernel/sysrq
    dump=$(dmesg 2>/dev/null | awk '/unwedge: backtrace request for/{f=1} f')
    for cpu in $SPIN_CPUS; do
        blk=$(echo "$dump" | awk -v c="$cpu" '$0 ~ ("NMI backtrace for cpu " c "$"){f=1; next} /NMI backtrace for cpu/{f=0} f')
        say "cpu $cpu (NMI backtrace):"
        echo "$blk" | grep -E 'CPU:|RIP:|\+0x' | grep -vE '^\S+ Code:' | sed 's/^\[[^]]*\] */      /' | head -18 | cut -c1-150
        [ -z "$blk" ] && say "      (no backtrace came back for cpu $cpu; dmesg | grep -A30 'NMI backtrace for cpu $cpu')"
    done
    nvl=$(echo "$dump" | grep -c '\[nvidia\]')
    [ "$nvl" -gt 0 ] && say "$nvl frame(s) inside [nvidia]: that is where it spins"
else
    [ "$SYSRQ" = 1 ] && say "sysrq not available here (need /proc/sysrq-trigger and /dev/kmsg writable); on the box: echo 1 > /proc/sys/kernel/sysrq; echo l > /proc/sysrq-trigger; dmesg | tail -60"
fi

# ---------------------------------------------------------------- 5. verdict
head_ "5. verdict"
if [ "$CARD_GONE" = 1 ] || [ "$CARD_ABSENT" = 1 ] || [ "$XID79" = 1 ]; then
    cat <<EOF
  the card is OFF THE BUS and $STUCK spins in the driver reading a device that is not there.
  Nothing root can do ends the process: signals need it to leave the kernel (it never does),
  ptrace needs it to stop (it cannot), the module cannot be unloaded while a CPU executes it,
  nvidia-smi has no device to reset. It burns one core and keeps its memory until the kernel goes.
  The clean exit is a COLD reboot: poweroff, wait 30s, power on (a warm reboot often leaves a
  dropped card dropped). The gamble before that is --reset: a PCI remove + rescan of ${GPU:-the slot};
  if the link retrains, the driver binds a fresh device beside the zombie and there is a GPU
  again without a reboot. The remove itself can hang in the kernel. Either way, after the box is
  back, run this again: it says whether the card fell off in that boot too.
EOF
    VERDICT=gone
else
    cat <<EOF
  the card answers but $STUCK spins inside the driver on it. --reset tries, in order:
  nvidia-smi -r (refused while a process holds the card, harmless to try), a PCI function-level
  reset of $GPU (the driver's wait reads reset values and usually bails, the task then dies on
  its pending SIGKILL), then remove + rescan. Each is checked; if the task dies, the driver is
  reloaded and the card proven with nvidia-smi. If none works: reboot.
EOF
    VERDICT=wedged
fi
[ "$RESET" = 1 ] || { echo; echo "diagnosis only. --reset to act (stops the stack first)."; exit 1; }

# ---------------------------------------------------------------- 6. the levers
head_ "6. --reset"
ask() { local a; printf '  %s [y/N] ' "$1" > /dev/tty; read -r a < /dev/tty; [ "$a" = y ] || [ "$a" = Y ]; }
gone_all() { local p; for p in $STUCK; do alive "$p" && return 1; done; return 0; }
wait_gone() { local i; for i in $(seq 1 "$1"); do gone_all && return 0; sleep 1; done; return 1; }
# a sysfs write that may never return is done from a child, with a bound.
bounded_write() { # value path seconds
    ( echo "$1" > "$2" ) 2>/dev/null & local w=$! i
    for i in $(seq 1 "$3"); do kill -0 "$w" 2>/dev/null || { wait "$w" 2>/dev/null; return $?; }; sleep 1; done
    say "the write to $2 has not returned after $3s: it is stuck in the kernel too (a shell in state D); nothing more from here"
    return 124
}

if systemctl is-active --quiet ghost.secd 2>/dev/null; then
    ask "stop ghost.secd (locks the volume; the app unlocks it after) ?" || { say "not without the stack down: a reset crashes whatever is on the card"; exit 1; }
    systemctl stop --no-block ghost.secd
    cg=$(systemctl show -p ControlGroup --value ghost.secd 2>/dev/null)
    in_service() { # the service's processes other than the stuck ones (cgroup v2, else by name)
        if [ -r "/sys/fs/cgroup$cg/cgroup.procs" ]; then cat "/sys/fs/cgroup$cg/cgroup.procs"
        else pgrep -x 'ghost\.(secd|watchd|oracled|searchd|synthd|framed|poltergres|ctld)|llama-server|postgres|redis-server'; fi 2>/dev/null
    }
    for i in $(seq 1 90); do
        left=""
        for p in $(in_service); do case " $STUCK " in *" $p "*) ;; *) left="$left $p" ;; esac; done
        [ -z "$left" ] && break
        sleep 1
    done
    if [ -n "${left:-}" ]; then say "still in the service after 90s:$left , not going on with those on the card"; exit 2; fi
    say "stack down after ${i}s (systemd keeps waiting on the stuck pid; that is fine)"
fi
PERSIST=0
if systemctl is-active --quiet nvidia-persistenced 2>/dev/null; then systemctl stop nvidia-persistenced && PERSIST=1 && say "nvidia-persistenced stopped"; fi
others=""
for d in /proc/[0-9]*; do
    p=${d#/proc/}
    case " $STUCK $$ " in *" $p "*) continue ;; esac
    ls -l "$d/fd" 2>/dev/null | grep -q '/dev/nvidia' && others="$others $p($(cat "$d/comm" 2>/dev/null))"
done
if [ -n "$others" ] && [ "$FORCE" = 0 ]; then say "others hold the card:$others , stop them, or --force to crash them"; exit 2; fi

if [ "$VERDICT" = wedged ]; then
    if command -v nvidia-smi >/dev/null && ask "lever 1: nvidia-smi -r on $GPU ?"; then
        timeout 30 nvidia-smi -r -i "$GPU" 2>&1 | sed 's/^/      /' | head -5
        wait_gone 10 && say "the stuck process is gone"
    fi
    if ! gone_all && ask "lever 2: function-level reset, echo 1 > /sys/bus/pci/devices/$GPU/reset ?"; then
        bounded_write 1 "/sys/bus/pci/devices/$GPU/reset" 30 && say "reset returned"
        wait_gone 20 && say "the stuck process is gone"
    fi
fi
if ! gone_all && ask "lever 3: remove the device and rescan the bus (echo 1 > .../${GPU:-?}/remove; echo 1 > /sys/bus/pci/rescan) ?"; then
    if [ -n "$GPU" ] && [ -e "/sys/bus/pci/devices/$GPU" ]; then
        bounded_write 1 "/sys/bus/pci/devices/$GPU/remove" 30 && say "remove returned"
        sleep 2
    else
        say "no device to remove (it is not in /sys/bus/pci any more); rescan alone"
    fi
    bounded_write 1 /sys/bus/pci/rescan 30 && say "rescan returned"
    sleep 3
    [ -z "$GPU" ] && GPU=$(ls /sys/bus/pci/drivers/nvidia/ 2>/dev/null | grep -E '^[0-9a-f]{4}:' | head -1)
    if [ -n "$GPU" ] && [ -e "/sys/bus/pci/devices/$GPU" ]; then
        vend=$(od -An -tx1 -N2 "/sys/bus/pci/devices/$GPU/config" 2>/dev/null | tr -d ' ')
        [ "$vend" = ffff ] && say "the slot still answers nothing: the link did not retrain" || say "the device is back on the bus (vendor 0x${vend:2:2}${vend:0:2}) · driver: $(basename "$(readlink "/sys/bus/pci/devices/$GPU/driver" 2>/dev/null)" 2>/dev/null || echo none)"
    else
        say "the device did not come back on rescan: the link is down for good until the power drops"
    fi
    wait_gone 10 && say "the stuck process is gone"
fi

echo
if gone_all; then
    say "the stuck process is gone. reloading the driver:"
    for m in nvidia_uvm nvidia_drm nvidia_modeset nvidia_peermem nvidia; do rmmod "$m" 2>/dev/null && say "  unloaded $m"; done
    modprobe nvidia 2>&1 | sed 's/^/      /'; modprobe nvidia_uvm 2>&1 | sed 's/^/      /'
    [ "$PERSIST" = 1 ] && systemctl start nvidia-persistenced
    smi=$(timeout 30 nvidia-smi -L 2>&1); rc=$?
    if [ $rc = 0 ] && echo "$smi" | grep -q GPU; then
        say "the card is back: $(echo "$smi" | head -1)"
        say "start the stack: sudo ./tools/redeploy.sh (or systemctl start ghost.secd, then unlock from the app); then sudo ./tools/gpu.sh"
        exit 0
    fi
    say "the process is gone but the card is not answering ($(echo "$smi" | head -1 | cut -c1-100)): reboot, cold"
    exit 1
fi
say "still there. the driver will not give it back; the exit is the reboot:"
say "    sudo poweroff     # wait 30s, power on , not 'reboot': a dropped card needs the rail to drop"
if [ "$PERSIST" = 1 ]; then systemctl start nvidia-persistenced; fi
exit 1
