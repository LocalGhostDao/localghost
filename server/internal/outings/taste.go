package outings

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// TasteRow is one tag's presence across the archive: on how many distinct days with photos it
// appears, and in how many photos.
type TasteRow struct {
	Tag      string
	Category string
	Days     int
	Photos   int
}

// Like is a tag the person keeps photographing, with its share of photo days.
type Like struct {
	Tag      string  `json:"tag"`
	Category string  `json:"category,omitempty"`
	Share    float64 `json:"share"` // days with this tag / days with any photo
	Days     int     `json:"days"`
	Photos   int     `json:"photos"`
}

// InterestScore is one of the fixed interests (beaches, harbours, peaks ...) with the weight the
// person's tags give it.
type InterestScore struct {
	Name   string   `json:"name"`
	Weight float64  `json:"weight"` // the summed shares of the likes that feed it
	Tags   []string `json:"tags"`   // which of the person's tags fed it
}

// Taste is what the archive says the person likes to photograph.
type Taste struct {
	Days      int             `json:"days"`   // days with at least one photo
	Photos    int             `json:"photos"` // photos with a time
	Likes     []Like          `json:"likes"`
	Interests []InterestScore `json:"interests"`
	Summary   string          `json:"summary"`
	BuiltAt   int64           `json:"builtAt"`
}

const (
	minLikeDays    = 2
	maxLikes       = 30
	maxPerCategory = 6
)

// BuildTaste ranks tags by the share of photo days they appear on , a burst of five hundred
// beach photos on one day counts as one day, the same as a single photo of a boat on another , so
// the taste is what the person keeps coming back to, not what they shot most once. Text and style
// tags are left out (a caption's "black and white" is not a thing one likes), each category is
// capped so the list stays varied, and the interests are the likes folded onto the fixed list.
func BuildTaste(rows []TasteRow, totalDays, totalPhotos int, builtAt int64) Taste {
	t := Taste{Days: totalDays, Photos: totalPhotos, BuiltAt: builtAt}
	if totalDays <= 0 {
		t.Summary = "no photos with a date yet"
		return t
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Days != rows[j].Days {
			return rows[i].Days > rows[j].Days
		}
		if rows[i].Photos != rows[j].Photos {
			return rows[i].Photos > rows[j].Photos
		}
		return rows[i].Tag < rows[j].Tag
	})
	perCat := map[string]int{}
	for _, r := range rows {
		if r.Days < minLikeDays || r.Category == "text" || r.Category == "style" || r.Tag == "" {
			continue
		}
		if perCat[r.Category] >= maxPerCategory {
			continue
		}
		perCat[r.Category]++
		t.Likes = append(t.Likes, Like{Tag: r.Tag, Category: r.Category, Share: float64(r.Days) / float64(totalDays), Days: r.Days, Photos: r.Photos})
		if len(t.Likes) >= maxLikes {
			break
		}
	}
	t.Interests = interestsOf(t.Likes)
	t.Summary = summary(t)
	return t
}

func summary(t Taste) string {
	if len(t.Likes) == 0 {
		return "not enough tagged photos yet to say what you like"
	}
	var top []string
	for i, l := range t.Likes {
		if i == 3 {
			break
		}
		top = append(top, l.Tag)
	}
	s := "You photograph " + joinAnd(top) + " most"
	s += fmt.Sprintf(" , %s on %d%% of your days with a camera out", t.Likes[0].Tag, int(math.Round(t.Likes[0].Share*100)))
	if len(t.Likes) > 3 {
		var next []string
		for _, l := range t.Likes[3:] {
			if len(next) == 3 {
				break
			}
			next = append(next, l.Tag)
		}
		s += " , then " + joinAnd(next)
	}
	s += "."
	if len(t.Interests) > 0 {
		var names []string
		for i, in := range t.Interests {
			if i == 3 {
				break
			}
			names = append(names, in.Name)
		}
		s += " Places to your taste: " + joinAnd(names) + "."
	}
	return s
}

// Interest is a kind of place, the tags that show a liking for it, and the GeoNames feature codes
// that ARE it. The tags are the plain words a caption model uses; the codes are what geo-import
// keeps (kind S, K, F, and P for villages).
type Interest struct {
	Name   string
	Tags   []string
	FCodes []string
}

// Interests is the fixed list, in the order the summary names them when tied.
var Interests = []Interest{
	{"beaches", []string{"beach", "beaches", "sand", "sea", "seaside", "coast", "coastline", "shore", "swimming", "snorkeling", "snorkelling", "waves", "ocean"}, []string{"BCH", "BCHS", "COVE"}},
	{"harbours", []string{"harbour", "harbor", "boat", "boats", "marina", "sailing", "sailboat", "yacht", "port", "fishing boat", "ferry", "ship", "pier", "jetty"}, []string{"HBR", "MAR", "ANCH", "PRT", "PIER"}},
	{"islands and capes", []string{"island", "islands", "cape", "cliff", "cliffs", "lighthouse", "headland", "rocks"}, []string{"ISL", "ISLS", "CAPE", "CLF", "LTHSE", "PT"}},
	{"peaks and trails", []string{"mountain", "mountains", "hiking", "hike", "summit", "peak", "valley", "view", "viewpoint", "trail", "path", "ridge", "hill", "hills"}, []string{"PK", "MT", "MTS", "VLC", "TRL", "PASS", "RDGE", "HLL"}},
	{"lakes and waterfalls", []string{"lake", "waterfall", "river", "stream", "spring", "pond", "gorge", "canyon"}, []string{"LK", "FLLS", "SPNG", "GLCR", "RVN", "GRGE", "CNYN"}},
	{"parks and nature", []string{"park", "forest", "trees", "tree", "woods", "nature", "garden", "gardens", "wildlife", "birds", "bird", "flowers", "meadow"}, []string{"PRK", "RESN", "RESF", "RESW", "GDN", "ZOO", "FRST"}},
	{"castles and ruins", []string{"castle", "ruins", "ruin", "fortress", "fort", "archaeological site", "ancient", "temple", "amphitheatre", "amphitheater", "monument", "tower", "walls", "columns", "statue"}, []string{"CSTL", "RUIN", "FT", "ANS", "HSTS", "AMTH", "MNMT", "TOWR", "PAL", "PYR", "TMPL"}},
	{"monasteries and shrines", []string{"church", "monastery", "chapel", "cathedral", "mosque", "shrine", "icon", "bell tower"}, []string{"MSTY", "MSQE", "SHRN", "CH"}},
	{"museums", []string{"museum", "gallery", "art", "exhibition", "sculpture", "painting"}, []string{"MUS"}},
	{"caves", []string{"cave", "grotto", "caves"}, []string{"CAVE"}},
	{"hot springs and spas", []string{"hot spring", "thermal", "spa", "bath", "baths"}, []string{"SPNT", "SPA"}},
	{"food and markets", []string{"restaurant", "taverna", "cafe", "coffee", "wine", "vineyard", "market", "food", "meal", "dinner", "lunch", "bakery"}, []string{"REST", "MKT", "VIN"}},
	{"old towns and villages", []string{"old town", "village", "street", "alley", "architecture", "square", "town", "houses", "doorway", "balcony"}, []string{"PPL"}},
}

// interestsOf folds the likes onto the interests: an interest's weight is the summed share of the
// likes whose tag it lists (a tag can feed one interest only, the first that lists it).
func interestsOf(likes []Like) []InterestScore {
	var out []InterestScore
	for _, in := range Interests {
		sc := InterestScore{Name: in.Name}
		for _, l := range likes {
			if in.hasTag(l.Tag) {
				sc.Weight += l.Share
				sc.Tags = append(sc.Tags, l.Tag)
			}
		}
		if sc.Weight > 0 {
			out = append(out, sc)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Weight > out[j].Weight })
	return out
}

func (in Interest) hasTag(tag string) bool {
	t := strings.ToLower(strings.TrimSpace(tag))
	for _, x := range in.Tags {
		if x == t {
			return true
		}
	}
	return false
}

// InterestFor is the interest that lists this feature code, or nil.
func InterestFor(fcode string) *Interest {
	for i := range Interests {
		for _, c := range Interests[i].FCodes {
			if c == fcode {
				return &Interests[i]
			}
		}
	}
	return nil
}

// KindName is the plain word for a GeoNames feature code the box keeps.
func KindName(fcode string) string {
	if n, ok := kindNames[fcode]; ok {
		return n
	}
	return strings.ToLower(fcode)
}

var kindNames = map[string]string{
	"BCH": "beach", "BCHS": "beaches", "COVE": "cove", "HBR": "harbour", "MAR": "marina", "ANCH": "anchorage", "PRT": "port", "PIER": "pier",
	"ISL": "island", "ISLS": "islands", "CAPE": "cape", "CLF": "cliffs", "LTHSE": "lighthouse", "PT": "point",
	"PK": "peak", "MT": "mountain", "MTS": "mountains", "VLC": "volcano", "TRL": "trail", "PASS": "pass", "RDGE": "ridge", "HLL": "hill",
	"LK": "lake", "FLLS": "waterfall", "SPNG": "spring", "SPNT": "hot spring", "GLCR": "glacier", "RVN": "ravine", "GRGE": "gorge", "CNYN": "canyon", "BAY": "bay",
	"PRK": "park", "RESN": "nature reserve", "RESF": "forest reserve", "RESW": "wildlife reserve", "GDN": "garden", "ZOO": "zoo", "FRST": "forest",
	"CSTL": "castle", "RUIN": "ruins", "FT": "fort", "ANS": "archaeological site", "HSTS": "historical site", "AMTH": "amphitheatre", "MNMT": "monument", "TOWR": "tower", "PAL": "palace", "PYR": "pyramid", "TMPL": "temple",
	"MSTY": "monastery", "MSQE": "mosque", "SHRN": "shrine", "CH": "church", "MUS": "museum", "CAVE": "cave", "SPA": "spa",
	"REST": "restaurant", "MKT": "market", "VIN": "vineyard", "PPL": "village",
}

// SpotCodes is every feature code the interests name that geo-import does not already keep as a
// park (K), a physical feature (F) or a populated place (P): the S kind.
func SpotCodes() []string {
	skip := map[string]bool{"PRK": true, "RESN": true, "RESF": true, "RESW": true, "FLLS": true, "LK": true, "BAY": true, "GLCR": true, "PK": true, "MT": true, "VLC": true, "TRL": true, "PPL": true}
	var out []string
	seen := map[string]bool{}
	for _, in := range Interests {
		for _, c := range in.FCodes {
			if !skip[c] && !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Spot is one GeoNames point near the person.
type Spot struct {
	Name    string  `json:"name"`
	FCode   string  `json:"fcode"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	Country string  `json:"country,omitempty"`
	Admin1  string  `json:"admin1,omitempty"`
}

// Suggestion is a spot the taste says is worth a look, with the reason.
type Suggestion struct {
	Spot
	Kind       string  `json:"kind"`
	Interest   string  `json:"interest"`
	DistanceKm float64 `json:"distanceKm"`
	Bearing    string  `json:"bearing"`
	Score      float64 `json:"score"`
	Why        string  `json:"why"`
	BeenThere  int     `json:"beenThere"` // photos within a kilometre of it
}

const (
	maxSuggestions = 20
	maxPerInterest = 4
)

// Rank scores the spots within radiusKm of (lat, lon) against the taste: the interest's weight,
// discounted by distance (the square root, so a good spot twice as far is still worth naming) and
// halved when the person has photographed there before (a memory is not a discovery, but it is
// still a place they liked). At most a few per interest, so a coast does not answer with twenty
// beaches. photosNear says how many of the person's photos lie within a kilometre of a point.
func Rank(spots []Spot, taste Taste, lat, lon, radiusKm float64, photosNear func(lat, lon float64) int) []Suggestion {
	if radiusKm <= 0 {
		radiusKm = 15
	}
	weight := map[string]float64{}
	for _, in := range taste.Interests {
		weight[in.Name] = in.Weight
	}
	var out []Suggestion
	for _, s := range spots {
		in := InterestFor(s.FCode)
		if in == nil {
			continue
		}
		w := weight[in.Name]
		if w <= 0 {
			continue
		}
		d := distKm(lat, lon, s.Lat, s.Lon)
		if d > radiusKm {
			continue
		}
		been := 0
		if photosNear != nil {
			been = photosNear(s.Lat, s.Lon)
		}
		score := w * math.Sqrt(1-d/radiusKm)
		if been > 0 {
			score *= 0.5
		}
		sg := Suggestion{Spot: s, Kind: KindName(s.FCode), Interest: in.Name, DistanceKm: math.Round(d*10) / 10,
			Bearing: bearing(lat, lon, s.Lat, s.Lon), Score: score, BeenThere: been}
		sg.Why = why(in.Name, taste, been)
		out = append(out, sg)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].DistanceKm < out[j].DistanceKm
	})
	per := map[string]int{}
	kept := out[:0]
	for _, s := range out {
		if per[s.Interest] >= maxPerInterest {
			continue
		}
		per[s.Interest]++
		kept = append(kept, s)
		if len(kept) == maxSuggestions {
			break
		}
	}
	return kept
}

func why(interest string, taste Taste, been int) string {
	var tags []string
	var share float64
	for _, in := range taste.Interests {
		if in.Name == interest {
			tags = in.Tags
			share = in.Weight
			break
		}
	}
	s := interest
	if len(tags) > 0 {
		if len(tags) > 3 {
			tags = tags[:3]
		}
		s = fmt.Sprintf("you photograph %s (%d%% of your days)", joinAnd(tags), int(math.Round(math.Min(share, 1)*100)))
	}
	if been > 0 {
		s += fmt.Sprintf(" · you have %d photo%s within a kilometre", been, plural(been))
	} else {
		s += " · new to you"
	}
	return s
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// bearing is the eight-point compass direction from (lat1, lon1) to (lat2, lon2).
func bearing(lat1, lon1, lat2, lon2 float64) string {
	la1, la2 := lat1*math.Pi/180, lat2*math.Pi/180
	dlo := (lon2 - lon1) * math.Pi / 180
	y := math.Sin(dlo) * math.Cos(la2)
	x := math.Cos(la1)*math.Sin(la2) - math.Sin(la1)*math.Cos(la2)*math.Cos(dlo)
	deg := math.Mod(math.Atan2(y, x)*180/math.Pi+360, 360)
	dirs := []string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"}
	return dirs[int(math.Round(deg/45))%8]
}
