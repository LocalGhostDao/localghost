// ghost-heights , the elevation tiles into packs, and a look inside one.
//
//	ghost-heights pack <tiles dir> <out dir>    one pack per 30-degree block from the GeoTIFFs in
//	                                            the folder (GLO-90_N30_W030.heights, the tiles
//	                                            sorted, the same bytes from the same tiles)
//	ghost-heights list <pack>                   the tiles in a pack
//	ghost-heights check <pack>                  every tile of a pack read by the box's own reader
//
// For the mirror's publish (the web repository): the Copernicus tiles come down once, this packs
// them, the set elevation lists some sixty files instead of twenty-six thousand. A box fetches the
// blocks that cover the part of the world it asked for (tools/fetch_geo.sh), and reads a tile out
// of its pack (internal/dem). Setup tool: in the release bundle, not installed on a box.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/LocalGhostDao/localghost/server/internal/dem"
)

func main() {
	if len(os.Args) < 3 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "pack":
		if len(os.Args) != 4 {
			usage()
		}
		err = pack(os.Args[2], os.Args[3])
	case "list":
		err = list(os.Args[2])
	case "check":
		err = check(os.Args[2])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ghost-heights:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: ghost-heights pack <tiles dir> <out dir> | list <pack> | check <pack>")
	os.Exit(2)
}

func pack(src, out string) error {
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	blocks := map[[2]int][]string{}
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(n), ".tif") {
			continue
		}
		lat, lon, ok := dem.Corner(n)
		if !ok {
			fmt.Fprintf(os.Stderr, "  %s: no corner in the name, left out\n", n)
			continue
		}
		bl, bo := dem.BlockOf(lat, lon)
		blocks[[2]int{bl, bo}] = append(blocks[[2]int{bl, bo}], filepath.Join(src, n))
	}
	if len(blocks) == 0 {
		return fmt.Errorf("no tiles in %s", src)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	keys := make([][2]int, 0, len(blocks))
	for k := range blocks {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		name := dem.PackName(k[0], k[1])
		dst := filepath.Join(out, name)
		f, err := os.Create(dst + ".part")
		if err != nil {
			return err
		}
		n, werr := dem.WritePack(f, blocks[k])
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(dst + ".part")
			return fmt.Errorf("%s: %w", name, werr)
		}
		if err := os.Rename(dst+".part", dst); err != nil {
			return err
		}
		st, _ := os.Stat(dst)
		fmt.Printf("%s  %d tiles  %.1f MB\n", name, n, float64(st.Size())/1e6)
	}
	return nil
}

func open(path string) (*os.File, []dem.PackEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	idx, err := dem.ReadPackIndex(f, st.Size())
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, idx, nil
}

func list(path string) error {
	f, idx, err := open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, e := range idx {
		fmt.Printf("%4d %5d  %12d  %s\n", e.Lat, e.Lon, e.Len, e.Name)
	}
	fmt.Printf("%d tiles\n", len(idx))
	return nil
}

func check(path string) error {
	f, idx, err := open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	bad := 0
	for _, e := range idx {
		if _, err := dem.ReadTile(io.NewSectionReader(f, e.Off, e.Len), e.Len); err != nil {
			fmt.Printf("  %s: %v\n", e.Name, err)
			bad++
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d of %d tiles did not read", bad, len(idx))
	}
	fmt.Printf("%d tiles, every one read\n", len(idx))
	return nil
}
