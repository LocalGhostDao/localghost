// ghost-roadtiles , cut OpenStreetMap's roads into the map's tiles, outside a box.
//
//	ghost-roadtiles -out <dir> [-work <dir>] [-workers N] [-buffer-mb N] <file.osm.pbf>...
//	ghost-roadtiles -out <dir> -in <dir-of-pbfs>
//
// The same internal/roadtiles.Build that ghost.framed runs on a box (road-tiles). Pass the planet
// or Geofabrik's continent extracts; every file is read three times (the roads' node ids, those
// nodes, the roads) and the tiles are written whole at the end. Hours for a continent, most of it
// zlib; RAM about 2 GB plus the OS cache of the node file (see the package comment).
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/roadtiles"
)

func main() {
	out := flag.String("out", "", "tile directory to write (swapped in whole)")
	in := flag.String("in", "", "directory of .osm.pbf files (instead of naming them)")
	work := flag.String("work", "", "scratch directory (default <out>.work)")
	workers := flag.Int("workers", 0, "PBF decoding goroutines (0 = CPUs)")
	bufMB := flag.Int("buffer-mb", 512, "cell buffers held in memory before flushing")
	flag.Parse()
	files := flag.Args()
	if *in != "" {
		files = append(files, roadtiles.FindPBFs(*in)...)
	}
	if *out == "" || len(files) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ghost-roadtiles -out <dir> [-work <dir>] <file.osm.pbf>...   or   -in <dir>")
		os.Exit(2)
	}
	st, err := roadtiles.Build(files, *out, roadtiles.Options{
		Workers: *workers, Work: *work, BufferMB: *bufMB,
		Progress: func(s string) { fmt.Fprintf(os.Stderr, "  %s roads: %s\n", time.Now().Format("15:04:05"), s) },
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ghost-roadtiles:", err)
		os.Exit(1)
	}
	fmt.Println("  roads: " + st.String())
}
