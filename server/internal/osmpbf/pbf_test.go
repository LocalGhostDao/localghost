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
