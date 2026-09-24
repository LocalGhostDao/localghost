#!/bin/sh
# fetch_geo.sh <dest-dir> , downloads the public geodata the box geocodes and draws maps with:
# GeoNames (CC-BY: allCountries + admin1/admin2 code names + countryInfo), the Natural Earth
# countries GeoJSON (public domain) and OpenStreetMap's coastline (ODbL), from the localghost.ai
# mirror when it answers and from each upstream otherwise. ~400MB compressed, one-time, at SETUP , before any
# personal data exists, so the only thing revealed is "this IP provisioned a box once", the same
# class of disclosure as the apt installs setup already performs. NEVER run this against personal
# coordinates or from a running box's context; the whole point of on-box geocoding is that photo
# coordinates never touch the network.
#
# Best-effort per file: a miss is a loud note, not a failure , the box works without geo data, and
# `ghost-cli ghost.framed geo-import` picks up whatever the operator drops in later.
set -u
# GHOST_GEO_REFRESH=1 re-downloads everything even if present , the UPDATE path: GeoNames ships
# daily dumps; refresh then `ghost-cli ghost.framed geo-import` (upserts) then `reprocess` if you
# want existing frames re-resolved against the newer names.
DEST="${1:?usage: fetch_geo.sh <dest-dir>}"
FORCE="${GHOST_GEO_REFRESH:-}"
mkdir -p "$DEST"
GN="https://download.geonames.org/export/dump"
# 10m, not 110m: at 110m Vancouver Island is a twelve-vertex cartoon and photo dots sit "in the
# ocean" next to a coastline that is the thing that is wrong. 24MB buys real fjords.
NE="https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_10m_admin_0_countries.geojson"

get() { # get <url> <outfile>
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL --retry 2 -o "$2" "$1"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -O "$2" "$1"
    else
        echo "  note: neither curl nor wget present , skipping $(basename "$2")"
        return 1
    fi
}

# THE MIRROR FIRST. https://localghost.ai/mirror carries these same files , and the coastline already
# cut into the map's tiles, so this box skips a several-hundred-MB download and a 2 GB build , under
# a sha256 manifest signed by the same gpg key as the releases. tools/mirror_fetch.sh checks the
# signature against tools/mirror-key.asc (in this repo), then every file's hash, before anything lands
# here; a web host that was broken into can make it fail, never make it install something else.
# Whatever the mirror does not deliver, the upstream downloads below still fetch.
# GHOST_MIRROR=<url> points at another copy (a LAN mirror); GHOST_MIRROR=off skips it.
HERE="$(cd "$(dirname "$0")" && pwd)"
FETCH="$HERE/mirror_fetch.sh"
TILES="${GHOST_TILES_DIR:-$(dirname "$DEST")/landtiles}"
MIRROR_GEO=0
MIRROR_TILES=0
geo_missing() {
    for f in admin1CodesASCII.txt admin2Codes.txt countryInfo.txt allCountries.txt world.geojson world-50m.geojson world-110m.geojson; do
        [ -s "$DEST/$f" ] || return 0
    done
    return 1
}
rc=0
if [ "${GHOST_MIRROR:-}" = off ]; then
    echo "  geo: mirror off (GHOST_MIRROR=off) , straight to the upstreams"
    rc=3
elif [ -n "$FORCE" ] || geo_missing; then
    STAGE="$DEST/.mirror-dl"
    sh "$FETCH" geo "$STAGE"
    rc=$?
    # whatever arrived verified goes in (a file that failed is only a hidden .part, left to resume)
    for f in "$STAGE"/*; do
        [ -f "$f" ] || continue
        case "$f" in
            *.zip) command -v unzip >/dev/null 2>&1 && unzip -q -o "$f" -d "$DEST" && rm -f "$f" ;;
            *)     mv -f "$f" "$DEST/" ;;
        esac
    done
    case "$rc" in
        0) MIRROR_GEO=1
           rm -rf "$STAGE"
           echo "  geo: GeoNames + Natural Earth from the mirror, signature and hashes checked (terms beside them)" ;;
        3) rm -rf "$STAGE"
           echo "  geo: the mirror has nothing for this box (the line above says why) , straight to the upstreams" ;;
        *) echo "  geo: the mirror did not deliver all of it , the upstreams below fetch what is missing" ;;
    esac
fi
if [ "$rc" != 3 ] && [ -z "${GHOST_GEO_NO_OSM:-}" ] && { [ -n "$FORCE" ] || [ ! -s "$TILES/index.bin" ]; }; then
    TST="$TILES.mirror-dl"
    if sh "$FETCH" landtiles "$TST" && [ -s "$TST/landtiles.tar.gz" ]; then
        # unpacked beside the live tiles and swapped in whole: secd serves the old set or the new one
        rm -rf "$TILES.new" && mkdir -p "$TILES.new"
        if tar -xzf "$TST/landtiles.tar.gz" -C "$TILES.new" && [ -s "$TILES.new/index.bin" ]; then
            cp "$TST"/TERMS-*.txt "$TST"/NOTICE.txt "$TILES.new/" 2>/dev/null || true
            rm -rf "$TILES.old"
            [ -d "$TILES" ] && mv "$TILES" "$TILES.old"
            mv "$TILES.new" "$TILES" && rm -rf "$TILES.old" "$TST"
            MIRROR_TILES=1
            echo "  geo: the coastline tiles from the mirror, checked, in $TILES (no shapefile, no build)"
        else
            rm -rf "$TILES.new"
            echo "  note: the mirror's tile archive did not unpack , the upstream shapefile below instead"
        fi
    fi
elif [ -z "$FORCE" ] && [ -s "$TILES/index.bin" ]; then
    MIRROR_TILES=1   # tiles already here: the shapefile is only needed to cut them
fi

if [ "$MIRROR_GEO" = 0 ]; then
    for f in admin1CodesASCII.txt admin2Codes.txt countryInfo.txt; do
        if [ -s "$DEST/$f" ] && [ -z "$FORCE" ]; then
            echo "  geo: $f already present (GHOST_GEO_REFRESH=1 to re-fetch)"
        elif get "$GN/$f" "$DEST/$f"; then
            echo "  geo: fetched $f"
        else
            echo "  note: could not fetch $f , place names will use raw codes until it is provided"
        fi
    done

    if [ -s "$DEST/allCountries.txt" ] && [ -z "$FORCE" ]; then
        echo "  geo: allCountries.txt already present (GHOST_GEO_REFRESH=1 to re-fetch)"
    elif command -v unzip >/dev/null 2>&1 && get "$GN/allCountries.zip" "$DEST/allCountries.zip"; then
        unzip -q -o "$DEST/allCountries.zip" -d "$DEST" && rm -f "$DEST/allCountries.zip"
        echo "  geo: fetched + unpacked allCountries.txt ($(du -h "$DEST/allCountries.txt" 2>/dev/null | cut -f1))"
    else
        echo "  note: could not fetch allCountries.zip (or unzip missing) , geocoding stays off until"
        echo "        the operator drops GeoNames TSVs in $DEST and runs geo-import"
    fi

    if [ -s "$DEST/world.geojson" ] && [ -z "$FORCE" ]; then
        echo "  geo: world.geojson already present"
    elif get "$NE" "$DEST/world.geojson"; then
        echo "  geo: fetched Natural Earth world.geojson"
    else
        echo "  note: could not fetch world.geojson , the MAP draws graticule + dots without landmass"
    fi

    # THE COARSE CUTS. The 10m file is the truth for a zoomed-in coastline and 24MB of truth is the wrong
    # thing to hand a phone before it can draw anything. Natural Earth publishes the same countries at
    # 110m (~800KB) and 50m (~4.5MB); the box serves whatever world*.geojson it has
    # (/v1/geo/world/index), the app opens on the smallest and refines with the largest once it is
    # cached. Existing boxes: drop these two files in <volume>/geo through ns.sh, nothing to restart.
    for res in 110m 50m; do
        url="https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_${res}_admin_0_countries.geojson"
        if [ -s "$DEST/world-$res.geojson" ] && [ -z "$FORCE" ]; then
            echo "  geo: world-$res.geojson already present"
        elif get "$url" "$DEST/world-$res.geojson"; then
            echo "  geo: fetched Natural Earth world-$res.geojson"
        else
            echo "  note: could not fetch world-$res.geojson , the map opens on the full-detail file (slower first draw)"
        fi
    done
fi

# THE COASTLINE AT FULL DETAIL. Natural Earth's 10m file is the base for the world and the
# continents; zoomed in on an island it is a smudge (Paxos is a handful of vertices). OpenStreetMap's
# land polygons draw every cove. The box cuts them into one-degree tiles (ghost.framed geo-tiles, run
# by itself when this file is newer than the tiles) and the phone fetches only the tiles under its
# viewport. Several hundred MB, once, at setup like everything here; ODbL: the map credits
# "© OpenStreetMap contributors" wherever it draws them. GHOST_GEO_NO_OSM=1 skips it.
OSM="https://osmdata.openstreetmap.de/download/land-polygons-complete-4326.zip"
if [ "$MIRROR_TILES" = 1 ]; then
    : # the tiles are here, cut; the shapefile is only needed to cut them
elif [ -n "${GHOST_GEO_NO_OSM:-}" ]; then
    echo "  geo: OpenStreetMap land polygons skipped (GHOST_GEO_NO_OSM set) , the map keeps the 10m coast"
elif [ -s "$DEST/land-polygons-complete-4326/land_polygons.shp" ] && [ -z "$FORCE" ]; then
    echo "  geo: OpenStreetMap land polygons already present"
elif command -v unzip >/dev/null 2>&1 && get "$OSM" "$DEST/land-polygons-complete-4326.zip"; then
    unzip -q -o "$DEST/land-polygons-complete-4326.zip" -d "$DEST" && rm -f "$DEST/land-polygons-complete-4326.zip"
    echo "  geo: fetched + unpacked OpenStreetMap land polygons ($(du -sh "$DEST/land-polygons-complete-4326" 2>/dev/null | cut -f1))"
else
    echo "  note: could not fetch the OpenStreetMap land polygons , the map keeps the 10m coast when zoomed in"
fi

