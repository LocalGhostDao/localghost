package hw

// The DECLARATIVE schema and its convergence engine. The registry below is the single source of
// truth for what the database should look like; Converge introspects information_schema, diffs
// reality against the registry, and applies the difference. The rules, in order of importance:
//
//  1. NEVER destroy data. Missing things are created; mismatched types are migrated with an
//     explicit cast when postgres can do it; columns that exist in the DB but not in the registry
//     are DRIFT , logged loudly, left untouched. Dropping is a human decision, always.
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
