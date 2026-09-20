package search

// Tag categories. A flat tag list is a search surface; a categorised one is a SUMMARY: "people:
// two adults · places: beach, harbour · food: pastel de nata" is what a chat prompt wants to see
// about a matched set of photos, and what a gallery can group by. The taxonomy is small and
// fixed on purpose , eleven words the model can hold in its head and a person can predict.
//
// Two ways a tag gets its category, cheapest first: the LEXICON below (a few hundred of the tags
// a photo archive actually produces, resolved with no model at all), then the text model for
// whatever is left (CategorizePrompt, one small call per frame, background priority). A tag the
// person added keeps whatever category they gave it; a tag they removed is a tombstone and is
// never categorised.

import (
	"sort"
	"strings"
)

// Categories is the closed set, in the order a digest lists them.
var Categories = []string{"people", "place", "object", "activity", "food", "animal", "vehicle", "nature", "event", "text", "style"}

// Tag is one tag with its category (empty when not yet known).
type Tag struct {
	Name     string
	Category string
}

// IsCategory reports whether c is one of the closed set.
func IsCategory(c string) bool {
	for _, k := range Categories {
		if k == c {
			return true
		}
	}
	return false
}

// TagPrompt asks the model for categorised tags in one go. The category list is spelled out so
// the model never invents one; anything it invents anyway is dropped to empty and the lexicon or a
// later pass fills it.
const TagPrompt = `From this photo description, output 6-12 short lowercase tags: single words or two-word phrases, concrete things, places and activities only (no colours-as-tags, no counts, no sentences). Prefix each tag with its category from exactly this list: people, place, object, activity, food, animal, vehicle, nature, event, text, style. Format: category:tag, comma-separated, nothing else. Example: place:beach, people:child, food:ice cream, activity:swimming

`

// CategorizePrompt assigns categories to tags that already exist (the backfill for tags written
// before categories did).
const CategorizePrompt = `Assign each of these photo tags one category from exactly this list: people, place, object, activity, food, animal, vehicle, nature, event, text, style. Reply with ONLY comma-separated category:tag pairs, one per tag, in the same order.

Tags: `

// ParseTags normalises the model's comma list into tags: lowercase, trimmed, 2..24 chars, deduped,
// capped at 12; "category:tag" carries its category when the category is in the closed set, and a
// bare tag is looked up in the lexicon. Defensive by construction , a rambling model yields fewer
// tags, never garbage rows.
func ParseTags(raw string) []Tag {
	seen := map[string]bool{}
	var out []Tag
	for _, part := range strings.Split(raw, ",") {
		cat, name := "", strings.ToLower(strings.TrimSpace(part))
		if i := strings.IndexByte(name, ':'); i > 0 {
			c := strings.TrimSpace(name[:i])
			if IsCategory(c) {
				cat, name = c, strings.TrimSpace(name[i+1:])
			}
		}
		name = strings.Trim(name, ".:;\"'`")
		if len(name) < 2 || len(name) > 24 || strings.ContainsAny(name, "\n\t") || seen[name] {
			continue
		}
		if cat == "" {
			cat = Lexicon(name)
		}
		seen[name] = true
		out = append(out, Tag{Name: name, Category: cat})
		if len(out) == 12 {
			break
		}
	}
	return out
}

// TagNames is the bare list, for callers that only want the words.
func TagNames(tags []Tag) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		out = append(out, t.Name)
	}
	return out
}

// Lexicon returns the category of a tag the archive sees all the time, or empty when the model has
// to decide. The last word of a two-word tag decides ("wooden table" is an object, "red car" a
// vehicle), which is how English compounds work often enough to be worth it.
func Lexicon(tag string) string {
	if c, ok := lexicon[tag]; ok {
		return c
	}
	if i := strings.LastIndexByte(tag, ' '); i > 0 {
		if c, ok := lexicon[tag[i+1:]]; ok {
			return c
		}
	}
	if strings.HasSuffix(tag, "s") { // plurals: dogs, boats, mountains
		if c, ok := lexicon[strings.TrimSuffix(tag, "s")]; ok {
			return c
		}
	}
	return ""
}

var lexicon = map[string]string{}

func init() {
	add := func(cat string, words ...string) {
		for _, w := range words {
			lexicon[w] = cat
		}
	}
	add("people", "person", "people", "man", "woman", "child", "kid", "baby", "toddler", "boy", "girl", "adult", "family", "friends",
		"group", "crowd", "couple", "selfie", "portrait", "face", "hand", "hands", "smile", "man walking", "woman walking",
		"pedestrian", "tourist", "player", "runner", "cyclist", "swimmer", "diver", "worker", "waiter", "chef", "musician")
	add("place", "beach", "harbour", "harbor", "marina", "port", "pier", "dock", "quay", "street", "road", "alley", "square",
		"plaza", "market", "shop", "store", "cafe", "café", "restaurant", "bar", "pub", "hotel", "room", "bedroom", "kitchen",
		"bathroom", "living room", "office", "garden", "backyard", "park", "playground", "school", "church", "cathedral",
		"temple", "mosque", "museum", "gallery", "library", "station", "airport", "terminal", "platform", "bridge", "tunnel",
		"castle", "palace", "ruins", "monument", "tower", "skyline", "city", "town", "village", "countryside", "farm",
		"vineyard", "stadium", "arena", "gym", "pool", "swimming pool", "spa", "balcony", "terrace", "rooftop", "courtyard",
		"lobby", "hallway", "stairs", "staircase", "elevator", "parking", "garage", "driveway", "pavement", "sidewalk",
		"promenade", "boardwalk", "lighthouse", "campsite", "cabin", "cottage", "house", "home", "apartment", "building",
		"skyscraper", "warehouse", "factory", "hospital", "clinic", "pharmacy", "bakery", "butcher", "supermarket", "mall",
		"cinema", "theatre", "theater", "concert hall", "zoo", "aquarium", "amusement park", "fairground", "cemetery", "chapel",
		"beach bar", "ski resort", "slope", "trail", "path", "footpath", "viewpoint", "summit", "peak", "lookout")
	add("object", "table", "chair", "bench", "sofa", "couch", "bed", "lamp", "candle", "book", "books", "magazine", "newspaper",
		"phone", "laptop", "computer", "screen", "keyboard", "camera", "lens", "tripod", "bag", "backpack", "suitcase",
		"luggage", "umbrella", "hat", "cap", "sunglasses", "glasses", "watch", "jewellery", "ring", "necklace", "shoes",
		"boots", "sandals", "jacket", "coat", "dress", "shirt", "t-shirt", "jeans", "scarf", "gloves", "helmet", "towel",
		"blanket", "pillow", "cushion", "mirror", "window", "door", "gate", "fence", "wall", "roof", "sign", "signpost",
		"poster", "painting", "sculpture", "statue", "vase", "flower pot", "plant pot", "bottle", "glass", "cup", "mug",
		"plate", "bowl", "fork", "knife", "spoon", "pan", "pot", "kettle", "teapot", "tray", "basket", "box", "crate",
		"barrel", "bucket", "rope", "net", "anchor", "sail", "mast", "oar", "paddle", "surfboard", "skateboard", "ball",
		"football", "basketball", "tennis racket", "racket", "bat", "golf club", "ski", "skis", "snowboard", "kite",
		"balloon", "balloons", "toy", "toys", "doll", "lego", "puzzle", "guitar", "piano", "drum", "violin", "microphone",
		"speaker", "headphones", "television", "tv", "remote", "clock", "calendar", "map", "ticket", "passport", "money",
		"coins", "card", "gift", "present", "wrapping", "ribbon", "flag", "banner", "tent", "sleeping bag", "torch",
		"flashlight", "lantern", "grill", "barbecue", "fire pit", "fireplace", "radiator", "fan", "air conditioner",
		"shopping bag", "trolley", "cart", "pram", "stroller", "wheelchair", "ladder", "tool", "tools", "hammer",
		"drill", "saw", "paint", "brush", "pen", "pencil", "notebook", "envelope", "letter", "package", "parcel")
	add("activity", "walking", "running", "jogging", "hiking", "climbing", "cycling", "biking", "swimming", "surfing",
		"sailing", "rowing", "kayaking", "paddling", "fishing", "diving", "snorkelling", "snorkeling", "skiing",
		"snowboarding", "skating", "sledding", "camping", "picnic", "barbecuing", "cooking", "baking", "eating", "dining",
		"drinking", "toasting", "shopping", "reading", "writing", "drawing", "painting class", "playing", "dancing",
		"singing", "concert", "gig", "performing", "watching", "sightseeing", "touring", "travelling", "traveling",
		"driving", "flying", "boarding", "waiting", "queueing", "sleeping", "napping", "resting", "sunbathing",
		"lying down", "sitting", "standing", "posing", "photographing", "filming", "gardening", "planting", "harvesting",
		"cleaning", "working", "meeting", "presenting", "teaching", "studying", "exercising", "yoga", "stretching",
		"football match", "tennis match", "match", "game", "race", "marathon", "workout", "training", "boxing",
		"golf", "tennis", "football", "basketball", "volleyball", "cricket", "rugby", "skateboarding", "bowling",
		"karaoke", "boat trip", "road trip", "tour", "walk", "hike", "ride", "swim", "dive", "sail")
	add("food", "food", "meal", "breakfast", "brunch", "lunch", "dinner", "snack", "dessert", "cake", "birthday cake",
		"cupcake", "cookie", "biscuit", "bread", "toast", "sandwich", "burger", "hamburger", "pizza", "pasta", "noodles",
		"ramen", "sushi", "rice", "curry", "soup", "salad", "steak", "chicken", "fish dish", "seafood", "prawns", "shrimp",
		"oysters", "mussels", "cheese", "eggs", "omelette", "pancakes", "waffles", "croissant", "pastry", "pastel de nata",
		"tart", "pie", "ice cream", "gelato", "chocolate", "candy", "sweets", "fruit", "apple", "banana", "orange",
		"strawberries", "berries", "grapes", "watermelon", "vegetables", "tomatoes", "avocado", "olives", "nuts",
		"coffee", "espresso", "cappuccino", "latte", "tea", "juice", "smoothie", "water bottle", "wine", "red wine",
		"white wine", "beer", "cocktail", "champagne", "whisky", "gin", "drinks", "tapas", "mezze", "kebab", "taco",
		"tacos", "burrito", "dumplings", "dim sum", "pho", "paella", "risotto", "lasagne", "bbq", "plate of food")
	add("animal", "dog", "puppy", "cat", "kitten", "bird", "seagull", "pigeon", "duck", "swan", "goose", "chicken hen",
		"rooster", "horse", "pony", "donkey", "cow", "sheep", "goat", "pig", "deer", "fox", "rabbit", "squirrel",
		"monkey", "elephant", "giraffe", "lion", "tiger", "bear", "wolf", "camel", "fish", "dolphin", "whale", "seal",
		"turtle", "lizard", "snake", "frog", "butterfly", "bee", "insect", "spider", "crab", "jellyfish", "starfish",
		"parrot", "owl", "eagle", "flamingo", "peacock", "penguin", "pet", "wildlife")
	add("vehicle", "car", "taxi", "bus", "coach", "tram", "train", "metro", "subway", "railway", "bicycle", "bike",
		"motorbike", "motorcycle", "scooter", "moped", "van", "truck", "lorry", "tractor", "boat", "ship", "ferry",
		"yacht", "sailboat", "sailing boat", "speedboat", "fishing boat", "canoe", "kayak", "cruise ship", "plane",
		"airplane", "aeroplane", "aircraft", "helicopter", "jet", "hot air balloon", "cable car", "gondola", "rickshaw",
		"tuk tuk", "ambulance", "fire engine", "police car", "camper", "campervan", "caravan", "rv", "trailer")
	add("nature", "sea", "ocean", "wave", "waves", "surf", "tide", "coast", "coastline", "cliff", "cliffs", "rock",
		"rocks", "boulder", "sand", "dune", "dunes", "island", "bay", "cove", "lagoon", "lake", "pond", "river",
		"stream", "creek", "waterfall", "canyon", "valley", "hill", "hills", "mountain", "mountains", "glacier", "snow",
		"ice", "forest", "woods", "woodland", "jungle", "rainforest", "tree", "trees", "palm tree", "pine", "oak",
		"flower", "flowers", "rose", "tulip", "sunflower", "lavender", "cactus", "grass", "meadow", "field", "fields",
		"wheat", "vineyard rows", "desert", "sky", "clouds", "cloud", "sunset", "sunrise", "sun", "moon", "stars",
		"night sky", "milky way", "rainbow", "fog", "mist", "rain", "storm", "lightning", "wind", "leaves", "autumn leaves",
		"blossom", "cherry blossom", "moss", "fern", "seaweed", "coral", "reef", "volcano", "lava", "cave", "spring")
	add("event", "birthday", "party", "wedding", "reception", "ceremony", "graduation", "christening", "baptism",
		"funeral", "anniversary", "christmas", "new year", "easter", "halloween", "thanksgiving", "festival", "carnival",
		"parade", "fireworks", "celebration", "toast", "conference", "exhibition", "fair", "show", "performance",
		"holiday", "vacation", "trip", "reunion", "date night", "picnic day", "sports day", "market day", "protest",
		"demonstration", "ceremony hall", "award", "competition", "tournament", "final", "opening", "launch")
	add("text", "sign text", "menu", "receipt", "label", "logo", "brand", "price tag", "timetable", "schedule", "scoreboard",
		"screenshot", "document", "page", "handwriting", "note", "graffiti", "billboard", "advert", "advertisement",
		"street sign", "road sign", "shop sign", "nameplate", "plaque", "inscription", "text", "writing", "letters",
		"numbers", "licence plate", "license plate", "number plate", "qr code", "barcode")
	add("style", "black and white", "monochrome", "sepia", "vintage", "retro", "film", "grainy", "blurry", "blurred",
		"bokeh", "long exposure", "macro", "close-up", "closeup", "wide angle", "panorama", "aerial", "drone shot",
		"top-down", "flat lay", "silhouette", "backlit", "golden hour", "blue hour", "night shot", "low light",
		"high contrast", "hdr", "overexposed", "underexposed", "candid", "posed", "landscape", "portrait orientation",
		"symmetry", "reflection", "shadow", "shadows", "minimal", "minimalist", "colourful", "colorful", "pastel",
		"neon", "warm tones", "cool tones", "moody", "bright", "dark", "soft light", "harsh light", "screenshot style")
	// A few tags live in two categories in English; the last add wins, so the more useful one is
	// listed last: "picnic" is an activity people search for, "trip" an event.
	add("activity", "picnic")
	add("event", "trip")
}

// SortedCategories orders a map's keys in taxonomy order, unknown ones last.
func SortedCategories(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	rank := func(k string) int {
		for i, c := range Categories {
			if c == k {
				return i
			}
		}
		return len(Categories)
	}
	sort.SliceStable(keys, func(i, j int) bool { return rank(keys[i]) < rank(keys[j]) })
	return keys
}
