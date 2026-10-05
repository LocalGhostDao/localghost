package hw

// The DECLARATIVE schema and its convergence engine. The registry below is the single source of
// truth for what the database should look like; Converge introspects information_schema, diffs
// reality against the registry, and applies the difference. The rules, in order of importance:
//
//  1. NEVER destroy data. Missing things are created; mismatched types are migrated with an
//     explicit cast when postgres can do it; columns that exist in the DB but not in the registry
//     are DRIFT , logged loudly, left untouched. Dropping is a human decision, always (made, it
//     goes in as a data migration below, named and dated).
//  2. Users just run the latest build. Unlock runs Converge; whatever the box was missing, it
//     gains; whatever changed shape, it migrates; the log says exactly what happened.
//  3. One-shot DATA migrations (backfills, rebuilds) are versioned in schema_migrations and run
//     exactly once per box, in order, after the structure converges.
//
// Adding a column: add it to the registry. Changing a type: change it in the registry (Converge
// attempts ALTER ... USING cast). Renaming: add the new column + a data migration that copies, and
// accept the old column as documented drift until a human drops it. The legacy SQL blob in
// datastore.go still runs first as belt-and-braces bootstrap; new work belongs HERE.

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// SchemaCol is one column's desired shape. Type is postgres DDL syntax; Default is the literal
// default expression (” means none). BIGSERIAL is only meaningful at table creation , a serial
// can never be retrofitted onto an existing table by this engine (and never needs to be: serial
// PKs exist from birth).
type SchemaCol struct {
	Name    string
	Type    string
	NotNull bool
	Default string
}

// SchemaTable is one table's desired shape. PK is the raw primary-key clause body ("hash" or
// "day, metric"); Unique is zero or more UNIQUE clause bodies; Indexes are FULL create statements
// (matched by index name , the engine creates the ones whose names are missing).
type SchemaTable struct {
	Name    string
	Cols    []SchemaCol
	PK      string
	Unique  []string
	Indexes []string
}

// schemaRegistry , every table the main database owns, transcribed from the living schema.
var schemaRegistry = []SchemaTable{
	{Name: "settings", PK: "key", Cols: []SchemaCol{
		{"key", "TEXT", true, ""},
		{"value", "TEXT", true, ""},
	}},
	{Name: "notification_mute", PK: "scope", Cols: []SchemaCol{
		{"scope", "TEXT", true, ""},
		{"muted_until", "TIMESTAMPTZ", true, ""},
	}},
	{Name: "notifications", PK: "id", Cols: []SchemaCol{
		{"id", "BIGSERIAL", true, ""},
		{"service", "TEXT", true, ""},
		{"kind", "TEXT", true, "'message'"},
		{"title", "TEXT", true, "''"},
		{"body", "TEXT", true, "''"},
		{"seen", "BOOLEAN", true, "FALSE"},
		{"options", "TEXT", true, "''"},
		{"answer", "TEXT", true, "''"},
		{"answered", "TIMESTAMPTZ", false, ""},
		{"created", "TIMESTAMPTZ", true, "now()"},
		// where a tap takes the phone: map:<day>, memories:<id>, memories:near, news, status
		{"link", "TEXT", true, "''"},
	}, Indexes: []string{
		"CREATE INDEX IF NOT EXISTS notifications_id_desc ON notifications (id DESC)",
	}},
	{Name: "frames", PK: "hash", Cols: []SchemaCol{
		// PROVENANCE , which phone this arrived from (device key = sha256 of its client cert).
		// Empty means unknown: pre-provenance rows and local imports, never a guess. When photos
		// eventually live ONLY on the box, this column is how an archive still knows whose life
		// each frame came from.
		{"device", "TEXT", true, "''"},
		{"hash", "TEXT", true, ""},
		{"taken_at", "BIGINT", true, "0"},
		{"lat", "DOUBLE PRECISION", true, "0"},
		{"lon", "DOUBLE PRECISION", true, "0"},
		{"has_gps", "BOOLEAN", true, "FALSE"},
		{"archive_path", "TEXT", true, ""},
		{"preview_path", "TEXT", true, "''"},
		{"thumb_path", "TEXT", true, "''"},
		{"bytes", "BIGINT", true, "0"},
		{"source", "TEXT", true, "''"},
		{"received_at", "BIGINT", true, "0"},
		{"kind", "TEXT", true, "'unknown'"},
		{"mime", "TEXT", true, "''"},
		{"taken_src", "TEXT", true, "'mtime'"},
		{"place", "TEXT", true, "''"},
		{"description", "TEXT", true, "''"},
		{"display_name", "TEXT", true, "''"},
		// PIPELINE VERSION , which framed pipeline last converged this row (framed.PipelineVersion).
		// 0 is "before anyone counted". A row below the running version is re-read from its
		// original at start; that is how a parser fix reaches photos archived years earlier
		// without anyone writing a script.
		{"pipe_ver", "INT", true, "0"},
		// WHEN the description landed (unix seconds; 0 = not yet). The stock-take's rate and
		// ETA come from this: how many were described in the last hour says how long the rest
		// will take, with no counter anyone has to keep in memory.
		{"described_at", "BIGINT", true, "0"},
	}, Indexes: []string{
		"CREATE INDEX IF NOT EXISTS frames_taken_at ON frames (taken_at)",
		"CREATE INDEX IF NOT EXISTS frames_pipe_ver ON frames (pipe_ver)",
		"CREATE INDEX IF NOT EXISTS frames_described_at ON frames (described_at) WHERE described_at > 0",
		"CREATE INDEX IF NOT EXISTS frames_kind ON frames (kind)",
		// The map's LOD queries bbox-filter on lat/lon every pan tick; at a two-person archive
		// (40k+) that is a full scan per gesture without this. Partial: only GPS rows belong.
		"CREATE INDEX IF NOT EXISTS frames_gps ON frames (lat, lon) WHERE has_gps",
	}},
	{Name: "geo_points", PK: "geonameid", Cols: []SchemaCol{
		{"geonameid", "BIGINT", true, ""},
		{"name", "TEXT", true, ""},
		{"lat", "DOUBLE PRECISION", true, ""},
		{"lon", "DOUBLE PRECISION", true, ""},
		{"kind", "CHAR(1)", true, ""},
		{"fcode", "TEXT", true, "''"},
		{"country", "TEXT", true, "''"},
		{"admin1", "TEXT", true, "''"},
		{"admin2", "TEXT", true, "''"},
		{"population", "BIGINT", true, "0"},
		{"rank", "BIGINT", true, "0"},
	}, Indexes: []string{
		"CREATE INDEX IF NOT EXISTS geo_points_lat ON geo_points (lat)",
		"CREATE INDEX IF NOT EXISTS geo_points_lon ON geo_points (lon)",
		"CREATE INDEX IF NOT EXISTS geo_points_rank ON geo_points (rank DESC) WHERE rank > 0",
	}},
	{Name: "geo_names", PK: "code", Cols: []SchemaCol{
		{"code", "TEXT", true, ""},
		{"name", "TEXT", true, ""},
	}},
	// THE WEATHER OF THE WORLD'S LARGER PLACES, pulled by the box once a day as one fixed list
	// (internal/weather): the forecast where the person is comes from this table by the trail,
	// a named place by the box's GeoNames, and no weather service learns where anyone is. One
	// row a place; the forecast is the JSON of weather.Forecast, replaced at every pull.
	{Name: "weather_places", PK: "geonameid", Cols: []SchemaCol{
		{"geonameid", "BIGINT", true, ""},
		{"name", "TEXT", true, ""},
		{"country", "TEXT", true, "''"},
		{"lat", "DOUBLE PRECISION", true, ""},
		{"lon", "DOUBLE PRECISION", true, ""},
		{"population", "BIGINT", true, "0"},
		{"fetched_at", "BIGINT", true, "0"},
		{"forecast", "TEXT", true, "''"},
	}, Indexes: []string{
		"CREATE INDEX IF NOT EXISTS weather_places_lat ON weather_places (lat)",
		"CREATE INDEX IF NOT EXISTS weather_places_lon ON weather_places (lon)",
	}},
	// A phone is identified by its CLIENT CERTIFICATE (deviceKey = sha256 of the cert nginx
	// verified, first 8 bytes), never by a serial or IMEI: hardware IDs are permission-gated,
	// survive factory resets, and correlate across apps , the tracking primitive this project
	// refuses. This table adds the human half: a name the person chose and the model string the
	// phone volunteered, so the devices screen says "wife's S24" instead of "phone a3f9c2".
	{Name: "device_names", PK: "device", Cols: []SchemaCol{
		{"device", "TEXT", true, ""},
		{"name", "TEXT", true, "''"},
		{"model", "TEXT", true, "''"},
		{"first_seen", "BIGINT", true, "0"},
		// The CONTINUITY key: a hash of the phone's app-scoped Android ID, which survives an
		// uninstall/reinstall (and every debug rebuild) while a certificate does not. Not a
		// serial: since Android 8 this value is per app-signing-key, so it cannot correlate the
		// person across apps , it only answers "is this the same phone that was here before".
		{"stable_id", "TEXT", true, "''"},
	}},
	{Name: "sync_cursors", PK: "device, kind", Cols: []SchemaCol{
		{"device", "TEXT", true, ""},
		{"kind", "TEXT", true, ""},
		{"ts", "BIGINT", true, "0"},
		{"id", "BIGINT", true, "0"},
		// When this device last reported progress , the honest "last sync" for the devices
		// screen, which until now displayed invented numbers.
		{"updated_at", "BIGINT", true, "0"},
	}},
	{Name: "chats", PK: "id", Cols: []SchemaCol{
		{"id", "BIGSERIAL", true, ""},
		{"title", "TEXT", true, "''"},
		{"created_at", "BIGINT", true, ""},
		{"updated_at", "BIGINT", true, ""},
	}},
	{Name: "chat_messages", PK: "id", Cols: []SchemaCol{
		{"id", "BIGSERIAL", true, ""},
		{"chat_id", "BIGINT", true, ""},
		{"role", "TEXT", true, ""},
		{"content", "TEXT", true, ""},
		{"ts", "BIGINT", true, ""},
		// an answer's thinking, the web sources it drew on (JSON, [{n,title,url,kind,fetched}]),
		// and whether it is still being written (writing | done | stopped): the box keeps writing
		// an answer after the phone goes away, and a reopened chat shows it whole (synthd answers.go)
		{"reasoning", "TEXT", true, "''"},
		{"sources", "TEXT", true, "''"},
		{"state", "TEXT", true, "'done'"},
	}, Indexes: []string{
		"CREATE INDEX IF NOT EXISTS chat_messages_chat ON chat_messages (chat_id, id)",
	}},
	{Name: "memories", PK: "id", Cols: []SchemaCol{
		{"id", "BIGSERIAL", true, ""},
		{"title", "TEXT", true, ""},
		{"body", "TEXT", true, ""},
		{"kind", "TEXT", true, "'distilled'"},
		{"source_chat", "BIGINT", false, ""},
		{"created_at", "BIGINT", true, ""},
		{"updated_at", "BIGINT", true, ""},
		{"user_edited", "BOOLEAN", true, "FALSE"},
		{"tombstoned", "BOOLEAN", true, "FALSE"},
		{"emb", "JSONB", false, ""},
		{"source_ref", "TEXT", true, "''"},
		// META , structured detail beside the prose, for memories a machine assembled from data
		// (kind='outing': photos, days, place, tags, cover frames, distance). NULL for the rest.
		{"meta", "JSONB", false, ""},
	}, Indexes: []string{
		"CREATE INDEX IF NOT EXISTS memories_source ON memories (source_chat)",
	}},
	{Name: "journal_entries", PK: "id", Unique: []string{"source, ref"}, Cols: []SchemaCol{
		{"id", "BIGSERIAL", true, ""},
		{"source", "TEXT", true, ""},
		{"ref", "TEXT", true, ""},
		{"ts", "BIGINT", true, ""},
		{"title", "TEXT", true, ""},
		{"body", "TEXT", true, ""},
		{"created_at", "BIGINT", true, ""},
		{"distilled", "BOOLEAN", true, "FALSE"},
	}, Indexes: []string{
		"CREATE INDEX IF NOT EXISTS journal_ts ON journal_entries (ts)",
		"CREATE INDEX IF NOT EXISTS journal_undistilled ON journal_entries (ts DESC) WHERE NOT distilled",
	}},
	// VOICE NOTES , the person's own recordings (at the daily check-in, or any time), kept: the
	// audio is theirs, like a photo. ghost.voiced archives the WAV under path (relative to the
	// mount), transcribes it with whisper.cpp when the box has a speech model, and writes the
	// transcript to the journal (source ghost.voiced, ref voice:<id>). status: pending (waiting for
	// the engine or its turn), done, failed (MaxTries used; error says why). id is the phone's
	// (32 hex), so an upload retried after a lost answer is the same note.
	{Name: "voice_notes", PK: "id", Cols: []SchemaCol{
		{"id", "TEXT", true, ""},
		{"kind", "TEXT", true, "'journal'"}, // checkin | journal
		{"day", "TEXT", true, "''"},         // the phone's day it was recorded on, YYYY-MM-DD
		{"taken_at", "BIGINT", true, ""},    // recording start, unix seconds
		{"duration_ms", "BIGINT", true, "0"},
		{"bytes", "BIGINT", true, "0"},
		{"sha256", "TEXT", true, "''"},
		{"path", "TEXT", true, "''"},
		{"device", "TEXT", true, "''"},
		{"status", "TEXT", true, "'pending'"},
		{"tries", "INTEGER", true, "0"},
		{"error", "TEXT", true, "''"},
		{"transcript", "TEXT", true, "''"},
		{"lang", "TEXT", true, "''"},
		{"model", "TEXT", true, "''"},
		{"received_at", "BIGINT", true, "0"},
		{"transcribed_at", "BIGINT", true, "0"},
	}, Indexes: []string{
		"CREATE INDEX IF NOT EXISTS voice_notes_taken ON voice_notes (taken_at DESC)",
		"CREATE INDEX IF NOT EXISTS voice_notes_pending ON voice_notes (taken_at DESC) WHERE status = 'pending'",
	}},
	{Name: "health_metrics", PK: "day, metric", Cols: []SchemaCol{
		{"day", "TEXT", true, ""},
		{"metric", "TEXT", true, ""},
		{"value", "DOUBLE PRECISION", true, ""},
	}},
	{Name: "health_samples", PK: "metric, ts", Cols: []SchemaCol{
		{"metric", "TEXT", true, ""},
		{"ts", "BIGINT", true, ""},
		{"value", "DOUBLE PRECISION", true, ""},
	}},
	{Name: "reports", PK: "day", Cols: []SchemaCol{
		{"day", "TEXT", true, ""},
		{"generated_at", "BIGINT", true, ""},
		{"body", "JSONB", true, ""},
	}},
	// DAY SUMMARIES , one row per day the box knows anything about, built by ghost.synthd once
	// the day is over and again whenever the day's inputs change (captions landing, a late sync,
	// a route told on new streets). facts is the sheet the summary was written from (photos,
	// places, tags, captions, the route, health, notes, the outing it belongs to); template is
	// the deterministic sentence; summary is the model's prose when it passed the grounding
	// check, else the template; written_by says which. "On this day" reads these , no model at
	// request time , and the memories feed carries a projection of the days with signal.
	{Name: "day_summaries", PK: "day", Cols: []SchemaCol{
		{"day", "TEXT", true, ""},
		{"built_at", "BIGINT", true, ""},
		{"version", "INTEGER", true, "1"},
		{"signature", "TEXT", true, "''"},
		{"title", "TEXT", true, "''"},
		{"template", "TEXT", true, "''"},
		{"summary", "TEXT", true, "''"},
		{"written_by", "TEXT", true, "'template'"},
		{"model_at", "BIGINT", true, "0"},
		{"prose_tries", "INTEGER", true, "0"},
		{"tries_sig", "TEXT", true, "''"},
		{"facts", "JSONB", false, ""},
	}, Indexes: []string{
		"CREATE INDEX IF NOT EXISTS day_summaries_mmdd ON day_summaries (substr(day, 6, 5))",
	}},
	// WIKIPEDIA, on the box, in Postgres. ghost.synthd imports the mirror's ZIM file (the English
	// Wikipedia without pictures) once, article by article: the title, the lead as plain text, the
	// rest as plain text with its section headings, and every redirect's title pointing at its
	// article; then the file goes (internal/wiki Store). A lookup is case-blind on the title, by
	// prefix (a pattern index), by likeness when pg_trgm is installed (GIN trigram indexes), and
	// by full-text search over the title and the lead (a GIN tsvector index): the lookup indexes
	// are the store's (wiki.Store.EnsureIndexes), made once the import is done and dropped while
	// one runs, since keeping five indexes current through twenty million inserts is most of the
	// import's time. idx is the entry's place in the file it came from, which the import resumes
	// by. About seven million articles and twelve million redirects.
	{Name: "wiki_articles", PK: "idx", Cols: []SchemaCol{
		{"idx", "INTEGER", true, ""},
		{"title", "TEXT", true, ""},
		{"title_lc", "TEXT", true, ""},
		{"lead", "TEXT", true, "''"},
		{"body", "TEXT", true, "''"},
		{"disamb", "BOOLEAN", true, "FALSE"},
	}},
	{Name: "wiki_redirects", Cols: []SchemaCol{
		{"title_lc", "TEXT", true, ""},
		{"idx", "INTEGER", true, ""},
	}, Unique: []string{"title_lc, idx"}},
	// NEWS , the publications the phone fetches for the box (ghost.synthd is the single writer):
	// the list itself with each feed's health, every entry seen, the stories they add up to (one
	// story for the same event across outlets, with the model's grounded summary), and the
	// digests posted. The box never fetches a page; the phone fetched the feed bytes.
	{Name: "news_feeds", PK: "id", Cols: []SchemaCol{
		{"id", "TEXT", true, ""},
		{"name", "TEXT", true, "''"},
		{"url", "TEXT", true, ""},
		{"enabled", "BOOLEAN", true, "true"},
		{"added_at", "BIGINT", true, "0"},
		{"last_fetch", "BIGINT", true, "0"},
		{"last_ok", "BIGINT", true, "0"},
		{"last_status", "TEXT", true, "''"},
		{"last_items", "INTEGER", true, "0"},
		{"failures", "INTEGER", true, "0"},
	}},
	{Name: "news_items", PK: "id", Cols: []SchemaCol{
		{"id", "BIGSERIAL", true, ""},
		{"feed_id", "TEXT", true, ""},
		{"guid", "TEXT", true, ""},
		{"link", "TEXT", true, "''"},
		{"title", "TEXT", true, ""},
		{"summary", "TEXT", true, "''"},
		{"published", "BIGINT", true, "0"},
		{"fetched", "BIGINT", true, "0"},
		{"story_id", "BIGINT", true, "0"},
		// THE ARTICLE ITSELF, read from its page for a story the box summarises: the paragraphs the
		// page serves anyone (body, held only until the story's summary is written, a day at most),
		// when it was read (body_at, 0 = not yet), and how it went (body_status: ok; paywalled, the
		// page says so and only its free part was there; short, the page gave little text; or the
		// HTTP status).
		{"body", "TEXT", true, "''"},
		{"body_at", "BIGINT", true, "0"},
		{"body_status", "TEXT", true, "''"},
	}, Unique: []string{"feed_id, guid"}, Indexes: []string{
		"CREATE INDEX IF NOT EXISTS news_items_published ON news_items (published DESC)",
		"CREATE INDEX IF NOT EXISTS news_items_story ON news_items (story_id)",
		"CREATE INDEX IF NOT EXISTS news_items_fts ON news_items USING gin (to_tsvector('english', title || ' ' || summary))",
	}},
	{Name: "news_stories", PK: "id", Cols: []SchemaCol{
		{"id", "BIGSERIAL", true, ""},
		{"first_seen", "BIGINT", true, "0"},
		{"last_seen", "BIGINT", true, "0"},
		{"title", "TEXT", true, ""},
		{"tokens", "TEXT", true, "''"},
		{"sources", "INTEGER", true, "1"},
		{"summary", "TEXT", true, "''"},
		{"written_by", "TEXT", true, "''"},
		{"model_at", "BIGINT", true, "0"},
		{"tries", "INTEGER", true, "0"},
		{"digested", "BIGINT", true, "0"},
	}, Indexes: []string{
		"CREATE INDEX IF NOT EXISTS news_stories_seen ON news_stories (last_seen DESC)",
	}},
	{Name: "news_digests", PK: "id", Cols: []SchemaCol{
		{"id", "BIGSERIAL", true, ""},
		{"at", "BIGINT", true, "0"},
		{"kind", "TEXT", true, "''"},
		{"body", "TEXT", true, "''"},
		{"story_ids", "TEXT", true, "''"},
	}},
	// RATES , the market numbers fetched for the box (by the phone on Wi-Fi, else by ghost.tallyd
	// itself; tallyd writes): the ECB's reference rates by day (one euro in each currency, back to
	// 1999 once the history is in), each venue's quote as taken, the index made from them per
	// symbol, the daily candles and the daily index, and the top 100 by market cap, kept a week.
	{Name: "fx_rates", PK: "day, code", Cols: []SchemaCol{
		{"day", "TEXT", true, ""},
		{"code", "TEXT", true, ""},
		{"rate", "DOUBLE PRECISION", true, ""},
	}},
	{Name: "crypto_quotes", PK: "ts, exchange, base, quote", Cols: []SchemaCol{
		{"ts", "BIGINT", true, ""},
		{"exchange", "TEXT", true, ""},
		{"base", "TEXT", true, ""},
		{"quote", "TEXT", true, ""},
		{"price", "DOUBLE PRECISION", true, ""},
		{"volume", "DOUBLE PRECISION", true, "0"},
		{"quote_ts", "BIGINT", true, "0"},
	}},
	// the box's USD price of each symbol it follows, and how it was made (venues in, venues out)
	{Name: "crypto_index", PK: "ts, symbol", Cols: []SchemaCol{
		{"ts", "BIGINT", true, ""},
		{"symbol", "TEXT", true, ""},
		{"price", "DOUBLE PRECISION", true, ""},
		{"n", "INTEGER", true, "0"},
		{"spread", "DOUBLE PRECISION", true, "0"},
		{"used", "TEXT", true, "''"},
		{"dropped", "TEXT", true, "''"},
		{"markets", "INTEGER", true, "0"}, // markets blended (a venue can have several)
		{"paths", "TEXT", true, "''"},     // the quote currencies converted from: USD,USDT,BTC…
	}, Indexes: []string{
		"CREATE INDEX IF NOT EXISTS crypto_index_symbol ON crypto_index (symbol, ts DESC)",
	}},
	// a coin as Coinbase's list describes it: what it is, its colour, its site and white paper
	// (rewritten with the list every hour), for the coin's own page
	{Name: "coin_info", PK: "symbol", Cols: []SchemaCol{
		{"symbol", "TEXT", true, ""},
		{"name", "TEXT", true, "''"},
		{"description", "TEXT", true, "''"},
		{"color", "TEXT", true, "''"},
		{"website", "TEXT", true, "''"},
		{"whitepaper", "TEXT", true, "''"},
		{"updated_at", "BIGINT", true, "0"},
		// what the box wrote about the coin from what it read (synthd coindesc.go): the text, when,
		// and from which sources ("Wikipedia, Coinbase, solana.com"); written_at alone marks a try
		// that found too little, tried again after three days
		{"written", "TEXT", true, "''"},
		{"written_at", "BIGINT", true, "0"},
		{"written_from", "TEXT", true, "''"},
	}},
	// the days: each venue's daily candle as it keeps it, and the box's daily USD close per symbol
	// (the venues' closes, USDT folded with the day's USDT/USD close), built back through the years
	{Name: "crypto_daily", PK: "day, symbol, exchange, quote", Cols: []SchemaCol{
		{"day", "TEXT", true, ""},
		{"symbol", "TEXT", true, ""},
		{"exchange", "TEXT", true, ""},
		{"quote", "TEXT", true, ""},
		{"open", "DOUBLE PRECISION", true, "0"},
		{"high", "DOUBLE PRECISION", true, "0"},
		{"low", "DOUBLE PRECISION", true, "0"},
		{"close", "DOUBLE PRECISION", true, ""},
		{"volume", "DOUBLE PRECISION", true, "0"},
	}},
	{Name: "crypto_daily_index", PK: "day, symbol", Cols: []SchemaCol{
		{"day", "TEXT", true, ""},
		{"symbol", "TEXT", true, ""},
		{"close", "DOUBLE PRECISION", true, ""},
		{"n", "INTEGER", true, "0"},
	}},
	// one row per coin per day from the rank list (the newest snapshot of the day): the price, the
	// market cap and the day's dollar volume across all exchanges, which is what the market index's
	// monthly weights are averaged from
	{Name: "coin_daily", PK: "day, symbol", Cols: []SchemaCol{
		{"day", "TEXT", true, ""},
		{"symbol", "TEXT", true, ""},
		{"coin_id", "TEXT", true, "''"},
		{"rank", "INTEGER", true, "0"},
		{"price_usd", "DOUBLE PRECISION", true, "0"},
		{"market_cap", "DOUBLE PRECISION", true, "0"},
		{"volume_usd", "DOUBLE PRECISION", true, "0"},
		{"source", "TEXT", true, "''"},
	}},
	// THE MARKET INDEX: the constituents and weights set at each month's start (the fifty largest by
	// market cap, weighted by the previous month's average daily dollar volume), and the chained
	// daily value
	{Name: "crypto_market_weights", PK: "month, symbol", Cols: []SchemaCol{
		{"month", "TEXT", true, ""},
		{"symbol", "TEXT", true, ""},
		{"rank", "INTEGER", true, "0"},
		{"weight", "DOUBLE PRECISION", true, ""},
		{"avg_volume_usd", "DOUBLE PRECISION", true, "0"},
		{"base_price", "DOUBLE PRECISION", true, ""},
	}},
	{Name: "crypto_market_index", PK: "day", Cols: []SchemaCol{
		{"day", "TEXT", true, ""},
		{"value", "DOUBLE PRECISION", true, ""},
		{"priced", "INTEGER", true, "0"},
		{"missing", "TEXT", true, "''"},
	}},
	// THE SERIES: a price every minute for the last week and every hour for the last thirty days.
	// crypto_bars is each venue's own candles (the history fetched back when the box starts),
	// crypto_series the box's price per symbol (source live: the minute's index from the tickers;
	// minutes: an hour rolled up from the minutes; venues: folded from the venues' candles where the
	// box was not yet watching), crypto_market_series the market index the same way.
	{Name: "crypto_bars", PK: "res, exchange, base, quote, ts", Cols: []SchemaCol{
		{"res", "TEXT", true, ""},
		{"ts", "BIGINT", true, ""},
		{"exchange", "TEXT", true, ""},
		{"base", "TEXT", true, ""},
		{"quote", "TEXT", true, ""},
		{"open", "DOUBLE PRECISION", true, "0"},
		{"high", "DOUBLE PRECISION", true, "0"},
		{"low", "DOUBLE PRECISION", true, "0"},
		{"close", "DOUBLE PRECISION", true, ""},
		{"volume", "DOUBLE PRECISION", true, "0"},
	}, Indexes: []string{
		"CREATE INDEX IF NOT EXISTS crypto_bars_base ON crypto_bars (res, base, ts)",
	}},
	{Name: "crypto_series", PK: "res, symbol, ts", Cols: []SchemaCol{
		{"res", "TEXT", true, ""},
		{"ts", "BIGINT", true, ""},
		{"symbol", "TEXT", true, ""},
		{"open", "DOUBLE PRECISION", true, "0"},
		{"high", "DOUBLE PRECISION", true, "0"},
		{"low", "DOUBLE PRECISION", true, "0"},
		{"close", "DOUBLE PRECISION", true, ""},
		{"n", "INTEGER", true, "0"},
		{"source", "TEXT", true, "''"},
	}, Indexes: []string{
		"CREATE INDEX IF NOT EXISTS crypto_series_ts ON crypto_series (res, ts)",
	}},
	{Name: "crypto_market_series", PK: "res, ts", Cols: []SchemaCol{
		{"res", "TEXT", true, ""},
		{"ts", "BIGINT", true, ""},
		{"open", "DOUBLE PRECISION", true, "0"},
		{"high", "DOUBLE PRECISION", true, "0"},
		{"low", "DOUBLE PRECISION", true, "0"},
		{"close", "DOUBLE PRECISION", true, ""},
		{"priced", "INTEGER", true, "0"},
		{"source", "TEXT", true, "''"},
	}},
	// THE FETCH LOG: one row per address fetched for the box (by the box or by the phone), and one
	// per minute's tick. Box Status reads it back as success rates, latencies and runs of failures
	// per source and per exchange (internal/feedstat, internal/monitor). Kept two days.
	{Name: "fetch_log", PK: "id", Cols: []SchemaCol{
		{"id", "BIGSERIAL", true, ""},
		{"ts", "BIGINT", true, ""},
		{"source", "TEXT", true, ""},
		{"kind", "TEXT", true, ""},
		{"by_who", "TEXT", true, "''"},
		{"status", "INTEGER", true, "0"},
		{"ok", "BOOLEAN", true, "FALSE"},
		{"took_ms", "INTEGER", true, "0"},
		{"bytes", "INTEGER", true, "0"},
		{"items", "INTEGER", true, "0"},
		{"error", "TEXT", true, "''"},
	}, Indexes: []string{
		"CREATE INDEX IF NOT EXISTS fetch_log_kind ON fetch_log (kind, ts DESC)",
		"CREATE INDEX IF NOT EXISTS fetch_log_source ON fetch_log (source, ts DESC)",
	}},
	{Name: "coin_ranks", PK: "ts, rank", Cols: []SchemaCol{
		{"ts", "BIGINT", true, ""},
		{"rank", "INTEGER", true, ""},
		{"coin_id", "TEXT", true, ""},
		{"symbol", "TEXT", true, ""},
		{"name", "TEXT", true, "''"},
		{"price_usd", "DOUBLE PRECISION", true, "0"},
		{"market_cap", "DOUBLE PRECISION", true, "0"},
		{"volume_24h", "DOUBLE PRECISION", true, "0"},
		{"change_24h", "DOUBLE PRECISION", true, "0"},
		{"source", "TEXT", true, "''"},
		{"supply", "DOUBLE PRECISION", true, "0"}, // circulating, in coins (Coinbase's list)
	}},
	{Name: "frame_tags", PK: "hash, tag", Cols: []SchemaCol{
		{"hash", "TEXT", true, ""},
		{"tag", "TEXT", true, ""},
		{"source", "TEXT", true, "'model'"},
		{"created_at", "BIGINT", true, ""},
		// CATEGORY , one of search.Categories (people, place, object, activity, food, animal,
		// vehicle, nature, event, text, style); '' = not yet assigned. Grouping tags by category
		// is what turns a matched photo set into a prompt-sized digest ("places: beach, harbour ·
		// food: pastel de nata") instead of a flat word list.
		{"category", "TEXT", true, "''"},
	}, Indexes: []string{
		"CREATE INDEX IF NOT EXISTS frame_tags_tag ON frame_tags (tag)",
		"CREATE INDEX IF NOT EXISTS frame_tags_hash ON frame_tags (hash)",
		"CREATE INDEX IF NOT EXISTS frame_tags_category ON frame_tags (category)",
	}},
	{Name: "location_points", PK: "ts, source", Cols: []SchemaCol{
		{"ts", "BIGINT", true, ""},
		{"lat", "DOUBLE PRECISION", true, ""},
		{"lon", "DOUBLE PRECISION", true, ""},
		{"source", "TEXT", true, "'watch'"},
		// how the phone took the point: w the quarter-hour fix, p another app's fix, a the app
		// opening; '' from before, and from sources that do not say
		{"via", "TEXT", true, "''"},
	}},
	// TRAIL DECISIONS , a stretch of the trail the person was asked about ("were you there?",
	// framed/questions.go) and said yes to: never asked about again. A "no" leaves no row: the
	// points are deleted from location_points.
	{Name: "trail_kept", PK: "ts_from, ts_to", Cols: []SchemaCol{
		{"ts_from", "BIGINT", true, ""},
		{"ts_to", "BIGINT", true, ""},
		{"at", "BIGINT", true, "0"},
	}},
	// DAEMON STATE , what a daemon wants the status screens to know about work in flight, as
	// one JSON value per key, written by that daemon only (rule 1: single writer). framed's
	// "converge" key is the stock-take's live progress; the phone reads it through hw. A
	// restart leaves the last value, so "last checked 3 minutes ago, all at the latest stage"
	// survives the daemon that said it.
	{Name: "daemon_state", PK: "daemon, key", Cols: []SchemaCol{
		{"daemon", "TEXT", true, ""},
		{"key", "TEXT", true, ""},
		{"value", "TEXT", true, "'{}'"},
		{"updated_at", "BIGINT", true, "0"},
	}},
}

// dataMigrations , one-shot rebuilds, run exactly once per box, in version order, AFTER structure
// converges. Version numbers are forever; append, never renumber. Keep each migration idempotent
// anyway (belt and braces , a crash between Run and the version insert re-runs it).
var dataMigrations = []struct {
	Version int
	Name    string
	Run     func(db *poltergres.ReadWrite) error
}{
	{1, "baseline", func(db *poltergres.ReadWrite) error { return nil }},
	// 2 Oct 2026, Vlad's call: what the removed features left behind goes. The paper sign-ins'
	// table (news_logins, empty since they went) and the "unreadable" mark on frames (damaged
	// photos are moved to frames/damaged instead).
	{2, "drop the paper sign-ins and the unreadable mark", func(db *poltergres.ReadWrite) error {
		if err := db.Exec("DROP TABLE IF EXISTS news_logins"); err != nil {
			return err
		}
		return db.Exec("ALTER TABLE frames DROP COLUMN IF EXISTS unreadable")
	}},
	// 2 Oct 2026: "calories" was Health Connect's resting estimate, the same 1,564 kcal every day
	// (Samsung Health shares no total), not a measurement; the phone now sends active kcal. The
	// rows go, and the "N kcal." each health day's journal line carried.
	{3, "drop the resting calorie estimate", func(db *poltergres.ReadWrite) error {
		if err := db.Exec("DELETE FROM health_metrics WHERE metric = 'calories'"); err != nil {
			return err
		}
		return db.Exec(`UPDATE journal_entries SET body = btrim(regexp_replace(body, '\s*[0-9]+ kcal\.', '', 'g'))
			WHERE source = 'ghost.tallyd' AND ref LIKE 'health:%' AND body ~ '[0-9]+ kcal\.'`)
	}},
	// 2 Oct 2026: the weekly notifications (framed's week in frames, shadowd's observation) keyed
	// their week by the day of the month and came again every day; one of each text is kept.
	{4, "drop the repeated weekly notifications", func(db *poltergres.ReadWrite) error {
		return db.Exec(`DELETE FROM notifications a USING notifications b
			WHERE a.service IN ('ghost.framed', 'ghost.shadowd') AND a.kind IN ('highlight', 'observation')
			AND b.service = a.service AND b.kind = a.kind AND b.title = a.title AND b.body = a.body AND a.id < b.id`)
	}},
}

// normalizeType maps registry DDL types to information_schema.columns.data_type values.
func normalizeType(ddl string) string {
	switch strings.ToUpper(strings.TrimSpace(ddl)) {
	case "BIGINT", "BIGSERIAL":
		return "bigint"
	case "TEXT":
		return "text"
	case "DOUBLE PRECISION":
		return "double precision"
	case "BOOLEAN":
		return "boolean"
	case "JSONB":
		return "jsonb"
	case "TIMESTAMPTZ":
		return "timestamp with time zone"
	case "INT", "INTEGER", "SERIAL":
		return "integer"
	default:
		if strings.HasPrefix(strings.ToUpper(ddl), "CHAR") {
			return "character"
		}
		return strings.ToLower(ddl)
	}
}

func (c SchemaCol) ddl() string {
	s := c.Name + " " + c.Type
	if c.NotNull {
		s += " NOT NULL"
	}
	if c.Default != "" {
		s += " DEFAULT " + c.Default
	}
	return s
}

func (t SchemaTable) createDDL() string {
	parts := make([]string, 0, len(t.Cols)+2)
	for _, c := range t.Cols {
		parts = append(parts, c.ddl())
	}
	if t.PK != "" {
		parts = append(parts, "PRIMARY KEY ("+t.PK+")")
	}
	for _, u := range t.Unique {
		parts = append(parts, "UNIQUE ("+u+")")
	}
	return "CREATE TABLE IF NOT EXISTS " + t.Name + " (" + strings.Join(parts, ", ") + ")"
}

// ConvergeSchema diffs the live database against the registry and applies the difference. Returns
// a human-readable summary of everything it did (and everything it refused to do).
func ConvergeSchema(db *poltergres.ReadWrite, lg *slog.Logger) (string, error) {
	var report []string
	note := func(f string, a ...any) {
		line := fmt.Sprintf(f, a...)
		report = append(report, line)
		lg.Info("schema converge: "+line, "fn", "ConvergeSchema")
	}

	// live tables
	trows, err := db.Query(
		"SELECT table_name FROM information_schema.tables WHERE table_schema = 'public'")
	if err != nil {
		return "", fmt.Errorf("introspect tables: %w", err)
	}
	live := map[string]bool{}
	for _, v := range trows.Vals {
		if len(v) > 0 && v[0] != nil {
			live[*v[0]] = true
		}
	}

	for _, t := range schemaRegistry {
		if !live[t.Name] {
			if err := db.Exec(t.createDDL()); err != nil {
				return strings.Join(report, "; "), fmt.Errorf("create %s: %w", t.Name, err)
			}
			for _, ix := range t.Indexes {
				if err := db.Exec(ix); err != nil {
					lg.Warn("index create failed (table is up, index deferred)",
						"fn", "ConvergeSchema", "table", t.Name, "err", err)
				}
			}
			note("created table %s (%d columns)", t.Name, len(t.Cols))
			continue
		}
		// live columns for this table
		crows, err := db.Query(
			"SELECT column_name, data_type, is_nullable FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1",
			t.Name)
		if err != nil {
			return strings.Join(report, "; "), fmt.Errorf("introspect %s: %w", t.Name, err)
		}
		liveCols := map[string]string{} // name -> data_type
		for _, v := range crows.Vals {
			if len(v) >= 2 && v[0] != nil && v[1] != nil {
				liveCols[*v[0]] = *v[1]
			}
		}
		declared := map[string]bool{}
		for _, c := range t.Cols {
			declared[c.Name] = true
			liveType, exists := liveCols[c.Name]
			if !exists {
				if c.Type == "BIGSERIAL" {
					// A serial can only be born with its table; if it is somehow missing the
					// table needs human eyes, not an automatic ALTER.
					note("REFUSED: %s.%s is a missing BIGSERIAL , needs a human", t.Name, c.Name)
					continue
				}
				if err := db.Exec("ALTER TABLE " + t.Name + " ADD COLUMN " + c.ddl()); err != nil {
					return strings.Join(report, "; "), fmt.Errorf("add %s.%s: %w", t.Name, c.Name, err)
				}
				note("added column %s.%s %s", t.Name, c.Name, c.Type)
				continue
			}
			want := normalizeType(c.Type)
			if liveType != want {
				// Type migration: postgres decides castability. Failure is logged and LEFT , the
				// old shape keeps working; destroying data to satisfy a registry is not a thing
				// this engine does.
				if err := db.Exec(fmt.Sprintf(
					"ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s::%s",
					t.Name, c.Name, c.Type, c.Name, c.Type)); err != nil {
					note("TYPE MISMATCH left in place: %s.%s is %s, registry wants %s (cast failed: %v)",
						t.Name, c.Name, liveType, want, err)
				} else {
					note("migrated type %s.%s: %s -> %s", t.Name, c.Name, liveType, want)
				}
			}
		}
		// drift: live columns the registry does not know , loudly named, never touched
		for name := range liveCols {
			if !declared[name] {
				note("DRIFT: %s.%s exists in the DB but not in the registry (left untouched)", t.Name, name)
			}
		}
		// indexes by name
		irows, err := db.Query("SELECT indexname FROM pg_indexes WHERE schemaname = 'public' AND tablename = $1", t.Name)
		if err == nil {
			liveIdx := map[string]bool{}
			for _, v := range irows.Vals {
				if len(v) > 0 && v[0] != nil {
					liveIdx[*v[0]] = true
				}
			}
			for _, ix := range t.Indexes {
				name := indexName(ix)
				if name != "" && !liveIdx[name] {
					if err := db.Exec(ix); err != nil {
						lg.Warn("index create failed", "fn", "ConvergeSchema", "index", name, "err", err)
					} else {
						note("created index %s", name)
					}
				}
			}
		}
	}

	// data migrations , once per box, in order
	if err := db.Exec(
		"CREATE TABLE IF NOT EXISTS schema_migrations (version BIGINT PRIMARY KEY, name TEXT NOT NULL, applied_at BIGINT NOT NULL)"); err != nil {
		return strings.Join(report, "; "), fmt.Errorf("migrations table: %w", err)
	}
	mrows, err := db.Query("SELECT version FROM schema_migrations")
	if err != nil {
		return strings.Join(report, "; "), fmt.Errorf("read migrations: %w", err)
	}
	applied := map[string]bool{}
	for _, v := range mrows.Vals {
		if len(v) > 0 && v[0] != nil {
			applied[*v[0]] = true
		}
	}
	for _, m := range dataMigrations {
		key := fmt.Sprintf("%d", m.Version)
		if applied[key] {
			continue
		}
		if err := m.Run(db); err != nil {
			return strings.Join(report, "; "), fmt.Errorf("data migration %d (%s): %w", m.Version, m.Name, err)
		}
		if err := db.Exec(
			"INSERT INTO schema_migrations (version, name, applied_at) VALUES ($1, $2, (extract(epoch from now())*1000)::bigint) ON CONFLICT (version) DO NOTHING",
			m.Version, m.Name); err != nil {
			return strings.Join(report, "; "), fmt.Errorf("record migration %d: %w", m.Version, err)
		}
		note("ran data migration %d (%s)", m.Version, m.Name)
	}

	if len(report) == 0 {
		return "schema already converged (no changes)", nil
	}
	return strings.Join(report, "; "), nil
}

// indexName pulls the name out of a CREATE INDEX IF NOT EXISTS statement.
func indexName(create string) string {
	fields := strings.Fields(create)
	for i, f := range fields {
		if strings.EqualFold(f, "EXISTS") && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}
