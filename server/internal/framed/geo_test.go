package framed

import "testing"

// The kinds geo-import keeps: populated places, parks, physical features, and now the spots a
// taste can point at. A code the interests do not name is still dropped.
func TestGeoKindKeepsSpots(t *testing.T) {
	cases := []struct {
		class, code string
		want        byte
	}{
		{"P", "PPL", 'P'}, {"P", "PPLA", 'P'},
		{"L", "PRK", 'K'}, {"L", "RESN", 'K'},
		{"H", "LK", 'F'}, {"T", "PK", 'F'}, {"R", "TRL", 'F'},
		{"T", "BCH", 'S'}, {"H", "HBR", 'S'}, {"S", "CSTL", 'S'}, {"S", "MSTY", 'S'}, {"S", "MUS", 'S'}, {"S", "CAVE", 'S'}, {"L", "VIN", 'S'},
		{"S", "AIRP", 0}, {"A", "ADM1", 'A'}, {"A", "ADM2", 0}, {"S", "BLDG", 0},
	}
	for _, c := range cases {
		if got := geoKind(c.class, c.code); got != c.want {
			t.Errorf("geoKind(%s, %s) = %q, want %q", c.class, c.code, got, c.want)
		}
	}
}

func TestLabelRankOrdersTheMapsNames(t *testing.T) {
	romania := labelRank('A', "PCLI", 19_000_000)
	bucharest := labelRank('P', "PPLC", 1_800_000)
	clujRegion := labelRank('A', "ADM1", 700_000)
	cluj := labelRank('P', "PPLA", 320_000)
	town := labelRank('P', "PPL", 12_000)
	village := labelRank('P', "PPL", 0)
	if !(romania > bucharest && bucharest > clujRegion && clujRegion > cluj && cluj > town && town > village && village == 1) {
		t.Fatalf("order: %d %d %d %d %d %d", romania, bucharest, clujRegion, cluj, town, village)
	}
	// the capital outranks a bigger city that is not one (Istanbul 15M vs Ankara 5M: Ankara first)
	if labelRank('P', "PPLC", 5_000_000) <= labelRank('P', "PPL", 15_000_000) {
		t.Fatal("a capital must outrank a bigger plain city")
	}
	for _, code := range []string{"PPLX", "PPLQ", "PPLW", "PPLH"} {
		if labelRank('P', code, 1_000_000) != 0 {
			t.Errorf("%s is not a label", code)
		}
	}
	if labelRank('A', "ADM2", 1_000_000) != 0 || labelRank('S', "BCH", 5) != 0 {
		t.Fatal("only countries, regions and populated places are labels")
	}
	if geoKind("A", "PCLI") != 'A' || geoKind("A", "ADM1") != 'A' || geoKind("A", "ADM2") != 0 {
		t.Fatal("geoKind: countries and first-level regions are kind A, nothing else in class A")
	}
}
