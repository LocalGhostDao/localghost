// ghost-landtiles , cut OpenStreetMap's land polygons into the map's one-degree tiles, outside a box.
//
//	ghost-landtiles <land_polygons.shp> <out-dir>
//
// The same internal/landtiles.Build that ghost.framed runs on a box (geo-tiles), for the mirror:
// mirror/publish.sh (in the web repo, LocalGhostDao/web) cuts the coastline once on the localghost.ai
// server and every box downloads the finished tiles instead of a several-hundred-MB shapefile and a
// 2 GB build. out-dir is written whole (built beside it, swapped in), with index.bin and one .lgt per
// coast cell.
package main

import (
	"fmt"
	"os"

	"github.com/LocalGhostDao/localghost/server/internal/landtiles"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: ghost-landtiles <land_polygons.shp> <out-dir>")
		os.Exit(2)
	}
	shp, out := os.Args[1], os.Args[2]
	st, err := landtiles.Build(shp, out, func(p string) { fmt.Fprintln(os.Stderr, "  tiles: "+p) })
	if err != nil {
		fmt.Fprintln(os.Stderr, "ghost-landtiles:", err)
		os.Exit(1)
	}
	fmt.Println("  tiles: " + st.String())
}
