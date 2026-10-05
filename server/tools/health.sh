#!/usr/bin/env bash
# health.sh , one glance at whether the box is actually alive.
#
# For every daemon that has a control socket on the unlocked volume, this pings it, prints its status
# line, and tails its most recent log. It discovers services from the run dir rather than a baked-in
# list, so it always reflects what is really running (a daemon watchd has not started yet simply has no
# socket, and shows as DOWN). Run it on the box while UNLOCKED.
#
#   sudo ./tools/health.sh                 # all services, status + 5 log lines each
#   sudo ./tools/health.sh -n 20           # 20 log lines each
#   sudo ./tools/health.sh ghost.oracled   # just one service, more detail
#
# Exit status is non-zero if any expected daemon is down, so it is usable in a check.

set -u

MOUNT="${GHOST_MOUNT:-/var/lib/ghost/mnt/slot0}"
RUN_DIR="${GHOST_RUN_DIR:-$MOUNT/run}"
LOG_DIR="${GHOST_LOG_DIR:-$MOUNT/logs}"
# The repo's own build first (freshest), then the /opt copy redeploy installs, then PATH.
CLI="${GHOST_CLI:-./bin/ghost-cli}"
[ -x "$CLI" ] || CLI="/opt/localghost/bin/ghost-cli"
[ -x "$CLI" ] || CLI="$(command -v ghost-cli || echo ./bin/ghost-cli)"
LINES=5
ONLY=""

# ghost-cli prints a command's answer as INDENTED JSON (one field a line, '"key": value'). The
# detail lines below read single fields with sed, so the answer is folded back onto one line
# first ('"key":value'): read raw, every compact pattern here matched nothing and the oracled
# model line, synthd's outings and days and tallyd's feeds were silently missing.
cj() { "$CLI" "$@" 2>/dev/null | sed 's/^[[:space:]]*//' | tr -d '\n' | sed 's/": /":/g'; }

while [ $# -gt 0 ]; do
    case "$1" in
        -n) LINES="$2"; shift 2 ;;
        -n*) LINES="${1#-n}"; shift ;;
        ghost.*) ONLY="$1"; shift ;;
        *) echo "usage: $0 [-n LINES] [ghost.SERVICE]"; exit 2 ;;
    esac
done

# The canonical roster , the ten supervised daemons plus watchd. secd is checked separately (it lives
# on the UNENCRYPTED state dir, not the volume, because it runs before unlock). If a services.conf adds
# more, socket discovery below still catches them.
ROSTER="ghost.watchd ghost.oracled ghost.searchd ghost.framed ghost.noted ghost.cued ghost.synthd ghost.shadowd ghost.tallyd ghost.voiced"

green() { printf '\033[32m%s\033[0m' "$1"; }
red()   { printf '\033[31m%s\033[0m' "$1"; }
yellow() { printf '\033[33m%s\033[0m' "$1"; }
dim()   { printf '\033[2m%s\033[0m'  "$1"; }

# The volume is mounted inside ghost.secd's PRIVATE MOUNT NAMESPACE , a deliberate design choice: the
# host mount table never shows the decrypted volume, and other host processes cannot casually see it.
# From here (root), the way in is the kernel's own link: /proc/<secd>/root resolves into that
# namespace, files open through it and unix sockets connect through it. So when the run dir is not
# visible, every path this script touches is simply prefixed with that door , no nsenter, no
# re-exec, no copying the script or the CLI through /tmp. ghost-cli takes the same door on its own
# (internal/nsreach), so the plain `$CLI <svc> ping` calls below just work.
if [ ! -d "$RUN_DIR" ]; then
    SECD_PID="$(pidof ghost.secd || true)"
    SECD_PID="${SECD_PID%% *}"
    if [ -n "$SECD_PID" ] && [ -d "/proc/$SECD_PID/root$RUN_DIR" ]; then
        DOOR="/proc/$SECD_PID/root"
        MOUNT="$DOOR$MOUNT"; RUN_DIR="$DOOR$RUN_DIR"; LOG_DIR="$DOOR$LOG_DIR"
        export GHOST_RUN_DIR="$RUN_DIR" GHOST_LOG_DIR="$LOG_DIR"
        echo "(volume reached through ghost.secd's namespace: $DOOR)"
    else
        echo "run dir $RUN_DIR not present , is the box unlocked? (secd mounts the volume on unlock,"
        echo "inside its own mount namespace; run as root and this script reaches it through /proc)"
        exit 1
    fi
fi

# Discover any extra sockets not in the roster (hand-added daemons), so nothing is missed.
# *.stream sockets are EXCLUDED by name: they are streamsock endpoints (unix-socket HTTP for token
# streaming), not control sockets , they will never answer a ctlsock ping, so including them reads
# as two permanently-STALE daemons and poisons the DEGRADED count on a perfectly healthy box.
EXTRA=""
for sock in "$RUN_DIR"/*.sock; do
    [ -e "$sock" ] || continue
    name="$(basename "$sock" .sock)"
    case "$name" in
        *.stream) continue ;;
    esac
    case " $ROSTER ghost.secd " in
        *" $name "*) : ;;
        *) EXTRA="$EXTRA $name" ;;
    esac
done

CHECK="$ROSTER$EXTRA"
[ -n "$ONLY" ] && CHECK="$ONLY"

# The clock, once, at the top: a health readout pasted into a chat an hour later still says when
# it was true, and lines up with the daemon logs it tails (each of which carries its own time).
printf 'health as of %s on %s\n' "$(date '+%Y-%m-%d %H:%M:%S %Z')" "$(hostname)"

down=0
total=0
for svc in $CHECK; do
    total=$((total + 1))
    sock="$RUN_DIR/$svc.sock"
    printf '\n=== %s ===\n' "$svc"

    if [ ! -S "$sock" ]; then
        printf '  %s   (no control socket at %s)\n' "$(red DOWN)" "$sock"
        down=$((down + 1))
    else
        # ping first , cheapest liveness check; then status for the detail line.
        if "$CLI" "$svc" ping >/dev/null 2>&1; then
            printf '  %s   ' "$(green UP)"
            # the daemon's own health, as its /health says it (status carries it since 29 Sep 2026):
            # ok, or DEGRADED with the one line of why (a model not running, notes waiting ...)
            st="$("$CLI" "$svc" status 2>/dev/null | tr -d '\n')"
            hc="$(printf '%s' "$st" | sed -n 's/.*"code": *\([0-9]\).*/\1/p')"
            hd="$(printf '%s' "$st" | sed -n 's/.*"detail": *"\([^"]*\)".*/\1/p')"
            case "$hc" in
                0) echo "ok${hd:+ , $hd}" ;;
                1) echo "$(yellow DEGRADED) ${hd}" ;;
                2) echo "$(red FAILING) ${hd}" ;;
                *) echo "(no health in its status: a build from before 29 Sep 2026)" ;;
            esac
            if [ "$svc" = "ghost.oracled" ]; then
                # The GPU question, from oracled itself (tools/gpu.sh has the whole picture).
                m=$(cj ghost.oracled models)
                v=$(echo "$m" | sed -n 's/.*"verdict":"\([^"]*\)".*/\1/p' | head -1)
                sp=$(echo "$m" | sed -n 's/.*"speed":"\([^"]*\)".*/\1/p' | head -1)
                [ -n "$v" ] && printf '  model %s\n' "$v"
                [ -n "$sp" ] && printf '  %s\n' "$sp"
                # Which llama.cpp the engine was built from (setup_llama.sh records the mirror's
                # tarball; a git checkout here means a box set up before the mirror , rerun setup_llama.sh)
                ld=/opt/localghost/llama.cpp
                if [ -f "$ld/.mirror-src" ]; then
                    printf '  engine: %s (commit %.12s, from the mirror)\n' "$(cat "$ld/.mirror-src")" "$(cat "$ld/.mirror-commit" 2>/dev/null)"
                elif [ -d "$ld/.git" ]; then
                    printf '  engine: a git checkout of llama.cpp, not the mirror'\''s pinned source (sudo ./tools/setup_llama.sh --build-only)\n'
                fi
                # The model the box offers PHONES (tools/phone_model.sh): read from the system area,
                # which is on the host, not in the volume.
                pm="${GHOST_STATE_DIR:-/var/lib/ghost}/models/catalog.json"
                if [ -f "$pm" ]; then
                    pn=$(sed -n 's/.*"name": *"\([^"]*\)".*/\1/p' "$pm" | head -1)
                    ps=$(sed -n 's/.*"sizeBytes": *\([0-9]*\).*/\1/p' "$pm" | head -1)
                    printf '  phone model offered: %s (%s MB)\n' "${pn:-?}" "$(( ${ps:-0} / 1000000 ))"
                else
                    printf '  phone model offered: none (sudo ./tools/phone_model.sh)\n'
                fi
            fi
            if [ "$svc" = "ghost.voiced" ]; then
                # The voice notes: how many wait, and the speech engine (whisper.cpp + a ggml model on
                # the volume; none until the mirror's sets whisper and speech are fetched)
                "$CLI" ghost.voiced voice 2>/dev/null | head -2 | sed 's/^/  /'
            fi
            if [ "$svc" = "ghost.framed" ]; then
                # What the MAP can draw from this box: the Natural Earth cuts under geo/, and the
                # coast tiles under landtiles/ (index.bin is what the phone asks for first; 64,804
                # bytes or it is refused). No tiles = the map keeps the 10m coast when zoomed in.
                cuts=$(ls "$MOUNT"/geo/world*.geojson 2>/dev/null | xargs -n1 basename 2>/dev/null | tr '\n' ' ')
                printf '  map base: %s\n' "${cuts:-none (fetch_geo.sh)}"
                if [ -s "$MOUNT/landtiles/index.bin" ]; then
                    ib=$(stat -c %s "$MOUNT/landtiles/index.bin" 2>/dev/null)
                    nt=$(ls "$MOUNT"/landtiles/*.lgt 2>/dev/null | wc -l)
                    sz=$(du -sh "$MOUNT/landtiles" 2>/dev/null | cut -f1)
                    if [ "$ib" = 64804 ]; then
                        printf '  map coast: %s tiles, %s, index ok\n' "$nt" "$sz"
                    else
                        printf '  map coast: index.bin is %s bytes, want 64804 , the phone refuses it; re-cut (fetch_geo.sh)\n' "$ib"
                    fi
                else
                    printf '  map coast: no tiles (tools/fetch_geo.sh <mount>/geo fetches the polygons and cuts them)\n'
                fi
                if [ -s "$MOUNT/roadtiles/index.bin" ]; then
                    nm=$(ls "$MOUNT"/roadtiles/1 2>/dev/null | wc -l); nf=$(ls "$MOUNT"/roadtiles/0 2>/dev/null | wc -l)
                    printf '  map roads: %s major + %s fine tiles, %s\n' "$nm" "$nf" "$(du -sh "$MOUNT/roadtiles" 2>/dev/null | cut -f1)"
                else
                    np=$(ls "$MOUNT"/geo/roads/*.osm.pbf 2>/dev/null | wc -l)
                    if [ "$np" -gt 0 ]; then
                        printf '  map roads: %s extract(s) under geo/roads, no tiles yet (framed cuts them: ghost-cli ghost.framed road-tiles; watch its log)\n' "$np"
                    else
                        printf '  map roads: none (GHOST_GEO_ROADS=all tools/fetch_geo.sh <mount>/geo fetches the continents)\n'
                    fi
                fi
                # The time zones: the grid the trail's newest point is looked up in, and the zone it named.
                if [ -s "$MOUNT/geo/tz/grid.bin" ]; then
                    printf '  time zones: grid built (%s); local_tz %s\n' "$(du -sh "$MOUNT/geo/tz/grid.bin" 2>/dev/null | cut -f1)" "$(cj ghost.framed setting key=local_tz | sed -n 's/.*"value":"\([^"]*\)".*/\1/p' | head -1)"
                elif ls "$MOUNT"/geo/tz/*.json >/dev/null 2>&1; then
                    printf '  time zones: file present, no grid yet (ghost-cli ghost.framed tz-grid; watch its log)\n'
                else
                    printf '  time zones: none (tools/fetch_geo.sh <mount>/geo fetches the tz set) , days are the box clock'"'"'s days\n'
                fi
                # The heights: the elevation tiles the days are drawn over (ghost-cli ghost.framed elevation).
                ntiles=$(ls "$MOUNT"/geo/elevation/*.tif 2>/dev/null | wc -l)
                if [ "$ntiles" -gt 0 ]; then
                    printf '  heights: %s elevation tiles (%s), each day drawn with its climb\n' "$ntiles" "$(du -sh "$MOUNT/geo/elevation" 2>/dev/null | cut -f1)"
                else
                    printf '  heights: none (sudo GHOST_GEO_ELEVATION=all ./tools/update.sh maps fetches them)\n'
                fi
            fi
            if [ "$svc" = "ghost.tallyd" ]; then
                # The data the box pulls in, one line per feed, as Box Status shows it
                # (ghost-cli ghost.tallyd feeds has the detail rows).
                f=$(cj ghost.tallyd feeds)
                fsum=$(echo "$f" | sed -n 's/.*"summary":"\([^"]*\)".*/\1/p' | head -1)
                if [ -n "$fsum" ]; then
                    printf '  feeds: %s\n' "$fsum"
                    # a JSON string here may hold an escaped quote (a Go error names its URL in
                    # quotes: Get "https://...": ...), so a value is ([^"\\]|\\.)*, not [^"]*
                    S='([^"\\]|\\.)*'
                    echo "$f" | grep -oE "\"title\":\"$S\",\"state\":\"$S\",\"line\":\"$S\"" \
                        | sed -E "s/\"title\":\"($S)\",\"state\":\"($S)\",\"line\":\"($S)\"/    \\3 · \\1 · \\5/; s/\\\\\"/\"/g"
                    # and every detail row that is not well: which exchange, which feed, and why
                    echo "$f" | grep -oE "\"k\":\"$S\",\"v\":\"$S\",\"state\":\"(flaky|late|failing)\"" \
                        | sed -E "s/\"k\":\"($S)\",\"v\":\"($S)\",\"state\":\"([a-z]*)\"/      ! \\1 (\\5): \\3/; s/\\\\\"/\"/g"
                fi
            fi
            if [ "$svc" = "ghost.synthd" ]; then
                # The memories made from the photos, and the taste , built without the model.
                o=$(cj ghost.synthd outings)
                n=$(echo "$o" | sed -n 's/.*"outings":\([0-9]*\).*/\1/p' | head -1)
                tr_=$(echo "$o" | sed -n 's/.*"trips":\([0-9]*\).*/\1/p' | head -1)
                ts=$(echo "$o" | sed -n 's/.*"taste":"\([^"]*\)".*/\1/p' | head -1)
                [ -n "$n" ] && printf '  outings %s (%s trips)\n' "$n" "${tr_:-0}"
                [ -n "$ts" ] && printf '  taste: %s\n' "$(echo "$ts" | cut -c1-140)"
                # The days, prebuilt: one summary a day, the model's where it passed the check.
                d=$(cj ghost.synthd days)
                dn=$(echo "$d" | sed -n 's/.*"days":\([0-9]*\).*/\1/p' | head -1)
                dm=$(echo "$d" | sed -n 's/.*"byModel":\([0-9]*\).*/\1/p' | head -1)
                dl=$(echo "$d" | sed -n 's/.*"oldest":"\([^"]*\)".*/\1/p' | head -1)
                dw=$(echo "$d" | sed -n 's/.*"backfillAt":"\([^"]*\)".*/\1/p' | head -1)
                [ -n "$dn" ] && printf '  day summaries %s (%s by the model), back to %s, backfill at %s\n' "$dn" "${dm:-0}" "${dl:-?}" "${dw:-start}"
                # The box's own Wikipedia, in the database once the file is imported (ghost-cli
                # ghost.synthd wiki q=… searches it, idx=… reads one article).
                wk=$(cj ghost.synthd wiki)
                ws=$(echo "$wk" | sed -n 's/.*"state":"\([^"]*\)".*/\1/p' | head -1)
                wn=$(echo "$wk" | sed -n 's/.*"edition":"\([^"]*\)".*/\1/p' | head -1)
                wa=$(echo "$wk" | sed -n 's/.*"articles":\([0-9]*\).*/\1/p' | head -1)
                wr=$(echo "$wk" | sed -n 's/.*"redirects":\([0-9]*\).*/\1/p' | head -1)
                wi=$(echo "$wk" | sed -n 's/.*"imported":\([0-9]*\).*/\1/p' | head -1)
                wt=$(echo "$wk" | sed -n 's/.*"entries":\([0-9]*\).*/\1/p' | head -1)
                case "$ws" in
                    ready) printf '  wikipedia: %s, %s articles and %s redirects in the database\n' "$wn" "${wa:-?}" "${wr:-?}" ;;
                    importing) printf '  wikipedia: importing, %s of %s entries read, %s articles in so far (Box Status shows it; an hour or two)\n' "${wi:-0}" "${wt:-?}" "${wa:-0}" ;;
                    downloading) printf '  wikipedia: downloading the file (update.sh), imported into the database once it is here\n' ;;
                    failed) printf '  wikipedia: the import stopped: %s (tried again every minute)\n' "$(echo "$wk" | sed -n 's/.*"error":"\([^"]*\)".*/\1/p' | head -1)" ;;
                    *) printf '  wikipedia: none (sudo ./tools/update.sh wiki fetches it, about 50 GB; imported, then the file goes)\n' ;;
                esac
            fi
        else
            printf '  %s   (socket present but not answering ping , wedged or mid-restart)\n' "$(red STALE)"
            down=$((down + 1))
        fi
    fi

    # Most recent log lines for this service. Logs are <dir>/<name>-YYYY-MM-DD.log; today's is the one
    # without .gz. Fall back to the newest matching file if today's is absent.
    latest="$(ls -1t "$LOG_DIR/$svc-"*.log 2>/dev/null | head -1)"
    if [ -n "$latest" ]; then
        printf '  %s\n' "$(dim "last $LINES log lines ($(basename "$latest")):")"
        tail -n "$LINES" "$latest" 2>/dev/null | sed 's/^/    /'
    else
        printf '  %s\n' "$(dim "no log file yet in $LOG_DIR")"
    fi
done

# secd separately , its socket is on the unencrypted state dir, reachable even pre-unlock.
# secd resolves its own state-dir socket (ghost-cli special-cases it), so call it plainly , no flags.
printf '\n=== ghost.secd (state dir) ===\n'
if "$CLI" ghost.secd ping >/dev/null 2>&1; then
    printf '  %s   ' "$(green UP)"
    # its status is indented JSON: the health code and line, like the daemons above
    st="$(cj ghost.secd status)"
    hc="$(printf '%s' "$st" | sed -n 's/.*"code":\([0-9]\).*/\1/p')"
    hd="$(printf '%s' "$st" | sed -n 's/.*"detail":"\([^"]*\)".*/\1/p')"
    case "$hc" in
        0) echo "ok${hd:+ , $hd}" ;;
        1) echo "$(yellow DEGRADED) ${hd}" ;;
        2) echo "$(red FAILING) ${hd}" ;;
        *) echo "(its status carries no health line)" ;;
    esac
else
    printf '  %s   (secd is the root daemon , if this is down the box is locked or crashed)\n' "$(red DOWN)"
fi

printf '\n----------------------------------------\n'
if [ "$down" -eq 0 ]; then
    printf '%s  %d/%d supervised daemons up  (%s)\n' "$(green ALL UP)" "$total" "$total" "$(date +%H:%M:%S)"
    exit 0
else
    printf '%s  %d of %d supervised daemons down , see the DOWN/STALE lines above  (%s)\n' "$(red DEGRADED)" "$down" "$total" "$(date +%H:%M:%S)"
    exit 1
fi
