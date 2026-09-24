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
		{"S", "AIRP", 0}, {"A", "ADM1", 0}, {"S", "BLDG", 0},
	}
	for _, c := range cases {
		if got := geoKind(c.class, c.code); got != c.want {
			t.Errorf("geoKind(%s, %s) = %q, want %q", c.class, c.code, got, c.want)
		}
	}
}
