package osmpbf

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type testBlock = EncBlock

func TestReadsNodesWaysAndTagsInOrder(t *testing.T) {
	blocks := []testBlock{
		{Nodes: []TestNode{{1, 44.4268, 26.1025}, {2, 44.4270, 26.1030}, {3, 44.4300, 26.1100}}},
		{Nodes: []TestNode{{10, -33.8688, 151.2093}, {11, -33.8690, 151.2100}}},
		{Ways: []TestWay{
			{ID: 100, Tags: map[string]string{"highway": "primary", "name": "Bulevardul Unirii"}, Refs: []int64{1, 2, 3}},
			{ID: 101, Tags: map[string]string{"building": "yes"}, Refs: []int64{1, 2}},
			{ID: 102, Tags: map[string]string{"highway": "residential"}, Refs: []int64{10, 11}},
		}},
	}
	p := filepath.Join(t.TempDir(), "t.osm.pbf")
	os.WriteFile(p, Encode(blocks...), 0o644)
	h, err := ReadHeader(p)
	if err != nil || h.WritingProgram != "osmpbf_test" {
		t.Fatalf("header: %+v %v", h, err)
	}
	var got []*Block
	err = Scan(p, 3, func(b *Block) error { got = append(got, b); return nil }, nil)
	if err != nil || len(got) != 3 {
		t.Fatalf("scan: %v, %d blocks", err, len(got))
	}
	if len(got[0].NodeIDs) != 3 || got[0].NodeIDs[2] != 3 || got[1].NodeIDs[0] != 10 {
		t.Fatalf("nodes out of order: %v %v", got[0].NodeIDs, got[1].NodeIDs)
	}
	// nanodegrees: 44.4268° → 44426800000
	if got[0].NodeLat[0] != 44426800000 || got[0].NodeLon[0] != 26102500000 || got[1].NodeLat[0] != -33868800000 {
		t.Fatalf("coords: %v %v %v", got[0].NodeLat[0], got[0].NodeLon[0], got[1].NodeLat[0])
	}
	ws := got[2].Ways
	if len(ws) != 3 || ws[0].ID != 100 || ws[0].Tags["highway"] != "primary" || ws[0].Tags["name"] != "Bulevardul Unirii" ||
		len(ws[0].Refs) != 3 || ws[0].Refs[2] != 3 || ws[2].Refs[1] != 11 || ws[1].Tags["building"] != "yes" {
		t.Fatalf("ways: %+v", ws)
	}
	// an error from fn stops the scan
	calls := 0
	err = Scan(p, 2, func(*Block) error { calls++; return errors.New("enough") }, nil)
	if err == nil || err.Error() != "enough" || calls != 1 {
		t.Fatalf("stop: %v after %d", err, calls)
	}
}

func TestRejectsWhatItIsNot(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.pbf")
	os.WriteFile(p, []byte("this is not a pbf file at all, not even close"), 0o644)
	if _, err := ReadHeader(p); err == nil {
		t.Fatal("garbage read as a header")
	}
	if err := Scan(p, 1, func(*Block) error { return nil }, nil); err == nil {
		t.Fatal("garbage scanned")
	}
}

// Relations are read with their tags and members, and ScanTail finds them at the end of the file
// without walking the blocks before them.
func TestRelationsAndTail(t *testing.T) {
	blocks := []testBlock{
		{Nodes: []TestNode{{1, 0.1, 0.1}, {2, 0.1, 0.2}, {3, 0.2, 0.2}, {4, 0.2, 0.1}}},
		{Ways: []TestWay{{ID: 10, Refs: []int64{1, 2, 3}}, {ID: 11, Refs: []int64{3, 4, 1}}}},
		{Relations: []TestRelation{{ID: 500, Tags: map[string]string{"type": "multipolygon", "natural": "water", "name": "Lacul"},
			Members: []Member{{1, 10, "outer"}, {1, 11, "outer"}, {0, 1, ""}}}}},
	}
	p := filepath.Join(t.TempDir(), "r.osm.pbf")
	os.WriteFile(p, Encode(blocks...), 0o644)
	var rels []Relation
	if err := Scan(p, 2, func(b *Block) error { rels = append(rels, b.Relations...); return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].ID != 500 || rels[0].Tags["name"] != "Lacul" || len(rels[0].Members) != 3 ||
		rels[0].Members[1].Ref != 11 || rels[0].Members[1].Role != "outer" || rels[0].Members[1].Type != 1 || rels[0].Members[2].Type != 0 {
		t.Fatalf("relations: %+v", rels)
	}
	var seen []int // relations per block, from the end, until a block with ways
	err := ScanTail(p, 1, func(b *Block) (bool, error) {
		seen = append(seen, len(b.Relations))
		return len(b.Ways) == 0 && len(b.NodeIDs) == 0, nil
	})
	if err != nil || len(seen) != 2 || seen[0] != 1 || seen[1] != 0 {
		t.Fatalf("tail: %v %v", seen, err)
	}
}
