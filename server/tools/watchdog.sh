#!/usr/bin/env bash
# watchdog.sh , arm the board's hardware watchdog, so a kernel that hard-locks resets the box by
# itself within a minute instead of sitting dark until someone gets home (2026-09-23: eight hours).
# systemd feeds /dev/watchdog every few seconds while it runs; when the kernel stops scheduling ,
# a driver that took the wrong lock, a hard lockup with interrupts off , the feeding stops and the
# chip resets the machine. Nothing here touches the internet; the box boots locked and the app
# unlocks it as after any reboot.
#
#   sudo ./tools/watchdog.sh          # status: the device, its driver, whether systemd feeds it
#   sudo ./tools/watchdog.sh --arm    # RuntimeWatchdogSec=60 in a systemd drop-in, load the driver
#                                     # if none is, daemon-reexec, verify
#
# Hardware first (iTCO_wdt on Intel boards, sp5100_tco on AMD; loaded by the kernel on most boards
# already). softdog , a kernel timer, no chip , is the fallback: it catches a wedged userspace and
# most oopses that hold locks, not a true hard lockup with interrupts off. A BIOS can disable the
# Intel one (the "no reboot" bit); wdctl says so, and then it is a BIOS setting, not this script.
#
# Exit: 0 armed (or would be), 1 not armed, 2 could not tell.

set -u
_stamp() { while IFS= read -r _l; do printf '%(%H:%M:%S)T %s\n' -1 "$_l"; done; }
exec > >(_stamp) 2>&1
_stamp_pid=$!
trap 'exec 1>&- 2>&-; wait "$_stamp_pid" 2>/dev/null || true' EXIT

ARM=0
case "${1:-}" in
    --arm) ARM=1 ;;
    -h|--help) sed -n '2,/^set -u/p' "$0" | sed '$d'; exit 0 ;;
    "") ;;
    *) echo "unknown flag $1 (see --help)"; exit 2 ;;
esac
if [ "$(id -u)" != 0 ]; then echo "run as root: sudo $0 $*"; exit 2; fi
say() { echo "  $*"; }

echo "watchdog $(date -u '+%Y-%m-%d %H:%M:%S UTC') on $(hostname)"
echo
echo "=== the device ==="
devs=$(ls /dev/watchdog* 2>/dev/null | tr '\n' ' ')
say "devices: ${devs:-none}"
mods=$(lsmod 2>/dev/null | awk '/^(iTCO_wdt|sp5100_tco|softdog|.*_wdt|.*wdt.*)/{printf "%s ", $1}')
say "watchdog modules loaded: ${mods:-none}"
if command -v wdctl >/dev/null && [ -e /dev/watchdog ]; then
    say "wdctl:"; wdctl 2>&1 | head -12 | sed 's/^/      /'
fi
vendor=$(awk -F: '/^vendor_id/{gsub(/ /,"",$2); print $2; exit}' /proc/cpuinfo 2>/dev/null)
say "cpu vendor: ${vendor:-?} (Intel boards: iTCO_wdt · AMD boards: sp5100_tco)"
dm=$(dmesg 2>/dev/null | grep -iE 'watchdog|iTCO|sp5100|softdog' | grep -viE 'NMI watchdog|soft lockup' | tail -3)
[ -n "$dm" ] && { say "kernel log:"; echo "$dm" | sed 's/^/      /' | cut -c1-140; }

echo
echo "=== systemd ==="
rt=$(systemctl show -p RuntimeWatchdogUSec --value 2>/dev/null)
rb=$(systemctl show -p RebootWatchdogUSec --value 2>/dev/null)
say "RuntimeWatchdogSec=${rt:-?} · RebootWatchdogSec=${rb:-?}"
armed=0
if [ -n "$rt" ] && [ "$rt" != 0 ] && [ -e /dev/watchdog ]; then
    armed=1
    say "ARMED: systemd feeds /dev/watchdog; a hard lockup resets the box within $rt"
else
    say "not armed: $([ -e /dev/watchdog ] && echo 'the device is there but systemd does not feed it' || echo 'no /dev/watchdog')"
fi
[ "$ARM" = 1 ] || { echo; [ "$armed" = 1 ] && exit 0; echo "  --arm to arm it (a drop-in under /etc/systemd/system.conf.d, no reboot needed)"; exit 1; }

echo
echo "=== --arm ==="
if [ ! -e /dev/watchdog ]; then
    # Load a driver: the board's chip first, softdog only when no chip answers.
    tried=""
    for m in $([ "$vendor" = GenuineIntel ] && echo iTCO_wdt sp5100_tco || echo sp5100_tco iTCO_wdt); do
        modprobe "$m" 2>/dev/null && tried="$tried $m"
        [ -e /dev/watchdog ] && { say "loaded $m: /dev/watchdog is there"; echo "$m" > /etc/modules-load.d/watchdog.conf; break; }
        rmmod "$m" 2>/dev/null || true
    done
    if [ ! -e /dev/watchdog ]; then
        say "no hardware watchdog answered (tried:${tried:-none}); falling back to softdog , a kernel timer, no chip:"
        say "  it catches a wedged userspace and most oopses that hold locks, NOT a hard lockup with interrupts off"
        modprobe softdog 2>/dev/null && echo softdog > /etc/modules-load.d/watchdog.conf
        [ -e /dev/watchdog ] || { say "softdog did not load either; nothing to arm"; exit 1; }
    fi
fi
mkdir -p /etc/systemd/system.conf.d
cat > /etc/systemd/system.conf.d/watchdog.conf <<'CONF'
# localghost tools/watchdog.sh: systemd feeds the hardware watchdog; a kernel that stops
# scheduling is reset by the chip within a minute, and a reboot that hangs is cut after ten.
[Manager]
RuntimeWatchdogSec=60
RebootWatchdogSec=10min
CONF
say "wrote /etc/systemd/system.conf.d/watchdog.conf (RuntimeWatchdogSec=60, RebootWatchdogSec=10min)"
systemctl daemon-reexec
sleep 1
rt=$(systemctl show -p RuntimeWatchdogUSec --value 2>/dev/null)
if [ -n "$rt" ] && [ "$rt" != 0 ] && [ -e /dev/watchdog ]; then
    say "ARMED: RuntimeWatchdogSec=$rt, systemd feeds /dev/watchdog ($(lsmod | awk '/^(iTCO_wdt|sp5100_tco|softdog)/{print $1}' | tr '\n' ' '))"
    command -v wdctl >/dev/null && wdctl 2>&1 | grep -E 'Identity|Timeout|nowayout' | sed 's/^/      /'
    say "to prove it without waiting for a real freeze:  echo c > /proc/sysrq-trigger  (crashes the kernel on purpose; the box must come back within ~2 min , only with someone home the first time)"
    exit 0
fi
say "not armed after the reexec: RuntimeWatchdogUSec=$rt, /dev/watchdog $([ -e /dev/watchdog ] && echo present || echo absent). If wdctl says the BIOS disabled it, that is a BIOS setting."
exit 1
