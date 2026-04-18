# ghost.tallyd

Structured-data ingestion, archival, and time-series aggregation. Imports bank exports, health logs, git commits, and other structured sources through a plugin system. Stores canonical copies in its own archive, parses them into time-series tables, exposes a query API for structured analysis, and pushes narrative summaries of notable events through ghost.noted's inbox so they become memories alongside everything else.

## Status

Phase 0. No code. This document describes the architecture as of April 2026. ghost.tallyd ships in v0.3, later than the other ingestion daemons because structured-data ingestion benefits from the memory layer being stable first, and because the plugin system that parses different source formats needs time to settle. The design is a sketch and will be revised substantially when implementation starts.

## Purpose

ghost.tallyd is the numbers layer of LocalGhost's personal archive. The text of your life lives in ghost.noted. The photos live in ghost.framed. The audio lives in ghost.voiced. The numbers, the spending, the steps, the commits, the minutes spent on your phone, live in ghost.tallyd.

The daemon has the same three-part shape as the other ingestion daemons (sync plus archive plus processing), with one addition. The other ingestion daemons produce text, and their output flows through ghost.noted into ghost.synthd as journal entries. ghost.tallyd produces two things. Structured time-series rows in its own Postgres tables, queryable directly. And narrative summary text for notable events, pushed through ghost.noted like any other text source. The same transaction that lands in tallyd's rows as a row (2026-03-14, 47.50, Napoli 1820, food_dining) can also land in noted's inbox as a sentence ("Spent £47 at Napoli 1820 on 14 March") when the event is narratively notable enough to surface.

Both paths matter. The structured path is how the app answers "how much did I spend on restaurants last year." The narrative path is how that specific memorable dinner at Napoli 1820 becomes a memory that ghost.synthd clusters alongside the journal entry you wrote about it and the photo ghost.framed described. Personal memory needs both kinds of fact, aggregate and specific, and ghost.tallyd produces both.

## Position in the fleet

ghost.tallyd sits alongside the other ingestion daemons at the edge of the fleet. It feeds ghost.synthd through two distinct paths.

```
UPSTREAM STRUCTURED SOURCES
  |
  |  PULL mode (daemon polls or watches):
  |    Filesystem watcher on a bank-exports folder
  |    Filesystem watcher on health-export folder
  |    git log from configured repos
  |    Cloud storage via export (v1.0+)
  |
  |  PUSH mode (external agent sends):
  |    Mobile app pushing health data over encrypted tunnel
  |    Browser extension pushing screen time
  |    CLI tool or scripted import
  |
  v
ghost.tallyd
  |
  |  step 1, sync loop or push endpoint brings export in
  |  step 2, store canonical copy in local archive
  |  step 3, plugin parses the export into structured rows
  |  step 4, rows land in tallyd's time-series tables
  |  step 5, notable-event detector flags rows worth narrating
  |  step 6, narrator generates summary text for notable events
  |  step 7, narrator pushes text to ghost.noted inbox
  |
  v                              v
ghost.noted                      ghost.synthd (direct API calls)
  (treats narratives as text)    (calls tallyd during episode regeneration)
  |
  v
ghost.synthd (consumes narrative entries)
```

Two paths into ghost.synthd. The narrative path goes through ghost.noted the same way ghost.framed and ghost.voiced output does, so notable events become journal entries and cluster into memories. The structured path is direct. During episode regeneration, ghost.synthd calls ghost.tallyd's query API to pull aggregates (total spending by category, step counts, commit counts) and folds those facts into episode prose.

The double-use is deliberate. A memorable dinner is a narrative memory (Path A) and also contributes to annual restaurant-spending aggregates (Path B). Both appear in an episode. A 2024 episode might read "You spent £47 at Napoli 1820 on 14 March, a dinner you wrote about three times. Your total restaurant spending for the year was £4,200 across 87 visits, up 31% from 2023." The specific detail comes from Path A, the aggregate comes from Path B, the episode prose weaves both into one view.

## Responsibilities

**Upstream sync, pull mode.** The daemon watches configured filesystem sources. A `bank-exports` folder where the user drops CSV exports. A `health-exports` folder. Configured git repositories where the daemon runs `git log` on a schedule. Cloud storage connectors (v1.0+) are deferred, consistent with the other ingestion daemons.

**Upstream sync, push mode.** Two endpoints for external agents. A general `/api/v1/tallyd/push` endpoint that accepts JSON or CSV payloads with metadata. An authenticated streaming endpoint for the mobile app at `/api/v1/tallyd/push/mobile` that uses the encrypted tunnel for health data, screen time, and location metrics the phone captures continuously.

**Archive management.** Every export that arrives is stored in `/var/lib/localghost/tallyd/archive/` under an internally organised layout, `<year>/<month>/<source-type>/<source-hash>.<ext>`. Source type segregation matters here because bank exports, health exports, and git logs have very different structures and keeping them separate in the archive makes browsing and backup easier.

**Plugin system.** Each data source is a plugin. A plugin knows how to parse one specific format into tallyd's structured schema, how to detect notable events in that data, and how to generate narrative text for those events. v0.3 ships with three plugins, expected to grow to ten or more over v1.0+. The plugin interface is a Go package that exports a small set of functions (parse, detect_notable, narrate), and new plugins are added by dropping a Go package into the plugins directory and rebuilding.

**Structured parsing.** When an export arrives, the appropriate plugin parses it into rows. Bank CSVs become transactions (date, amount, merchant, category, account). Health exports become measurements (timestamp, metric, value, unit, source). Git logs become commits (sha, author, message, repo, timestamp, lines_added, lines_removed). Each plugin has its own tables, which keeps the schema specific to the source type rather than forcing a one-size-fits-all structure.

**Notable-event detection.** After parsing, the plugin flags rows that deserve narrative treatment. The rules are plugin-specific. For a bank plugin, a notable event might be any transaction over a threshold, any transaction with a merchant not seen before, any transaction tagged with a location that matches a user's named place. For a health plugin, a notable event might be a step count above a personal best, a new workout type, a heart-rate event. For a git plugin, a notable event might be a commit to a new repo, a commit on a day the user has not committed in a month, a merge of a long-running branch. The rules are conservative by default. Over-narrating produces noise in ghost.synthd, under-narrating loses memory-worthy events.

**Narrative generation.** For each notable event, the plugin generates short English prose describing the event in context. "Spent £47 at Napoli 1820 in Milan on 14 March 2026, first visit to this restaurant." "Walked 12,400 steps on 3 April 2026, first time crossing 12k since January." "Committed to localghost/cmd/synthd for the first time on 17 April 2026, opening commit on the memory layer." The prose is factual, dated, and anchored to the underlying structured row.

**Push to ghost.noted.** The narrative text goes to ghost.noted's inbox at `/api/v1/noted/inbox`. The push carries `source_hint` set to `ghost.tallyd`, the tallyd source ID, the tallyd archive path, and the relevant structured row ID so the bidirectional link resolves from either side. ghost.noted stores the tallyd source ID in its source metadata. ghost.noted returns its source ID, tallyd records it alongside the row that triggered the narrative. The link between the structured row and the journal entry it produced is queryable from both sides.

**Structured query API.** Beyond the narrative path, tallyd exposes a query API for structured analysis at `/api/v1/tallyd/query/...`. Time-series queries (spending by month, steps by day, commits by week), aggregate queries (total by category, top merchant, average heart rate), windowed queries (rolling averages, year-on-year comparisons). This is the path ghost.synthd uses during episode regeneration, and the path the app uses for dashboard-style views.

**Change tracking.** Structured data is mostly immutable (a past transaction does not change), but corrections happen. A bank restates a transaction, a health source recalculates a measurement. Tallyd treats these as updates and re-processes, which regenerates the narrative if the event was previously flagged notable.

**Deletion.** Deletion happens only from within LocalGhost. Same semantics as the other ingestion daemons. Deletion of a source cascades to the derived rows and, through ghost.noted, to any journal entries that were generated from notable events in that source.

## Non-responsibilities

**No cross-source correlation.** tallyd does not try to correlate spending with sleep, or commit frequency with mood. That is ghost.synthd's job, using tallyd's structured query API as one of its inputs.

**No statistical modelling.** tallyd aggregates and reports. It does not fit models, detect trends, or predict future values. Those are application-layer concerns.

**No entity resolution.** Merchants, repo names, health metrics all appear as strings or enums in tallyd's tables. ghost.synthd resolves "Napoli 1820" the merchant to the same entity as "Napoli 1820" the journal-entry mention.

**No query interface for the user.** The app layer queries tallyd. The user does not hit tallyd's API directly.

**No source-native inference.** The plugin rules for detecting notable events are heuristic code, not LLM calls. Keeping detection deterministic keeps the narrative path predictable. The narrative generation itself uses a local LLM (for the prose), but the decision of what to narrate does not.

**No upstream writes.** tallyd never writes back to the user's bank, health app, or git repo. Read-only ingestion across the board.

## The link to ghost.noted and ghost.synthd

The bidirectional link pattern established for ghost.framed and ghost.voiced applies here too. For each notable event that becomes a narrative, tallyd's row stores the ghost.noted source ID the narrative was assigned, and ghost.noted's source metadata stores the tallyd source and row IDs. Either daemon can walk to the other side of the link. A journal entry in ghost.noted about "the £47 dinner at Napoli 1820" points back to a specific row in tallyd's transactions table. Going the other way, tallyd's transaction row points forward to the ghost.noted source containing the journal entry derived from it.

The synthd-direct path is separate. During episode regeneration, ghost.synthd's overnight pass calls tallyd's structured query API with a time window and asks for aggregates. These aggregates flow into the episode's generated prose as facts (total, top, average, year-on-year) rather than as references to individual rows. The same episode might reference a single narrative memory (the dinner at Napoli 1820) through ghost.synthd's memory-to-journal-entry-to-noted-source chain, and also include an aggregate fact (your restaurant spending for the year was £4,200) that came from tallyd directly. Both are in the episode, both trace back to tallyd's underlying rows, through different paths.

When ghost.synthd writes episode prose, it records which tallyd queries fed the aggregate facts. This is the audit trail. A user asking "where does this number come from" can walk from the episode to the tallyd query that produced it, to the rows that backed the query.

## Data model

Seven tables plus per-plugin tables. The core tables handle sources, archives, plugins, and the notable-event queue. The per-plugin tables handle the structured time-series for each source type.

```sql
-- Core tallyd tables, present regardless of plugins.

CREATE TABLE tally_sources (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_type       TEXT NOT NULL,        -- 'bank_revolut', 'health_samsung', 'git_local', etc.
  plugin_version    TEXT NOT NULL,        -- Plugin version that parsed this source
  upstream_uri      TEXT NOT NULL,        -- File path, push submission ID, git repo path
  archive_path      TEXT NOT NULL,        -- Path in tallyd's archive
  content_hash      BYTEA NOT NULL,
  file_size         BIGINT NOT NULL,
  upstream_modified TIMESTAMPTZ,
  first_seen        TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_synced       TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at        TIMESTAMPTZ,
  parse_status      TEXT NOT NULL DEFAULT 'pending', -- 'pending', 'parsed', 'failed'
  parse_error       TEXT,
  rows_produced     INTEGER NOT NULL DEFAULT 0,
  metadata          JSONB NOT NULL DEFAULT '{}'
);

CREATE UNIQUE INDEX idx_tally_sources_type_uri ON tally_sources(source_type, upstream_uri);
CREATE INDEX idx_tally_sources_hash ON tally_sources(content_hash);
CREATE INDEX idx_tally_sources_deleted_at ON tally_sources(deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TABLE tally_source_revisions (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id      UUID NOT NULL REFERENCES tally_sources(id) ON DELETE CASCADE,
  content_hash   BYTEA NOT NULL,
  archive_path   TEXT NOT NULL,
  seen_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  change_type    TEXT NOT NULL
);

CREATE INDEX idx_tally_revisions_source ON tally_source_revisions(source_id, seen_at);

-- Sync state per configured upstream, matches pattern in other ingestion daemons.
CREATE TABLE sync_state (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_config   TEXT NOT NULL,
  last_run_at     TIMESTAMPTZ,
  last_success_at TIMESTAMPTZ,
  last_error      TEXT,
  next_run_at     TIMESTAMPTZ,
  status          TEXT NOT NULL DEFAULT 'idle'
);

CREATE UNIQUE INDEX idx_sync_state_source ON sync_state(source_config);

-- Parsing queue, separate from narrative queue for different failure domains.
CREATE TABLE parse_queue (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id       UUID NOT NULL REFERENCES tally_sources(id) ON DELETE CASCADE,
  enqueued_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  started_at      TIMESTAMPTZ,
  completed_at    TIMESTAMPTZ,
  attempts        INTEGER NOT NULL DEFAULT 0,
  last_error      TEXT,
  status          TEXT NOT NULL DEFAULT 'pending'
);

CREATE INDEX idx_parse_queue_status ON parse_queue(status, enqueued_at)
  WHERE status IN ('pending', 'failed');

-- Notable-event queue. Rows flagged by plugins as worth narrating.
CREATE TABLE notable_events (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id         UUID NOT NULL REFERENCES tally_sources(id) ON DELETE CASCADE,
  plugin_name       TEXT NOT NULL,
  row_table         TEXT NOT NULL,            -- Which plugin table the row is in
  row_id            UUID NOT NULL,            -- Row ID in the plugin table
  reason            TEXT NOT NULL,            -- Why the plugin flagged this as notable
  enqueued_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  narrated_at       TIMESTAMPTZ,              -- When narrative was generated
  noted_source_id   UUID,                     -- ghost.noted source this became
  status            TEXT NOT NULL DEFAULT 'pending' -- 'pending', 'narrated', 'pushed', 'failed', 'skipped'
);

CREATE INDEX idx_notable_status ON notable_events(status, enqueued_at);
CREATE INDEX idx_notable_row ON notable_events(row_table, row_id);
CREATE INDEX idx_notable_noted ON notable_events(noted_source_id) WHERE noted_source_id IS NOT NULL;

-- Plugin registry, records what plugins have been seen and their versions.
CREATE TABLE plugin_registry (
  name            TEXT PRIMARY KEY,
  version         TEXT NOT NULL,
  enabled         BOOLEAN NOT NULL DEFAULT true,
  first_seen      TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_run        TIMESTAMPTZ,
  metadata        JSONB NOT NULL DEFAULT '{}'
);

-- Per-plugin tables below. These are the structured time-series.
-- Each plugin creates and owns its own tables during a migration.

-- Example: bank_revolut plugin tables.
CREATE TABLE bank_revolut_transactions (
  id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id        UUID NOT NULL REFERENCES tally_sources(id) ON DELETE CASCADE,
  transaction_date DATE NOT NULL,
  amount           NUMERIC(14, 2) NOT NULL,  -- Signed, negative for debits
  currency         TEXT NOT NULL,
  merchant         TEXT,
  merchant_normalised TEXT,                   -- Lowercased, whitespace-collapsed
  category         TEXT,                      -- Revolut's category
  country          TEXT,
  city             TEXT,
  notes            TEXT,
  balance_after    NUMERIC(14, 2),
  original_row     JSONB NOT NULL             -- Full raw CSV row for audit
);

CREATE INDEX idx_revolut_date ON bank_revolut_transactions(transaction_date);
CREATE INDEX idx_revolut_merchant ON bank_revolut_transactions(merchant_normalised);
CREATE INDEX idx_revolut_category ON bank_revolut_transactions(category);

-- Example: health_samsung plugin tables.
CREATE TABLE health_samsung_measurements (
  id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id        UUID NOT NULL REFERENCES tally_sources(id) ON DELETE CASCADE,
  measured_at      TIMESTAMPTZ NOT NULL,
  metric           TEXT NOT NULL,             -- 'steps', 'heart_rate', 'distance_km', 'sleep_minutes'
  value            NUMERIC(14, 4) NOT NULL,
  unit             TEXT NOT NULL,
  source_device    TEXT,                      -- Watch model, phone model
  metadata         JSONB NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_samsung_metric_time ON health_samsung_measurements(metric, measured_at);
CREATE INDEX idx_samsung_time ON health_samsung_measurements(measured_at);

-- Example: git_local plugin tables.
CREATE TABLE git_local_commits (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id       UUID NOT NULL REFERENCES tally_sources(id) ON DELETE CASCADE,
  repo_path       TEXT NOT NULL,
  commit_sha      TEXT NOT NULL,
  author_name     TEXT,
  author_email    TEXT,
  message         TEXT,
  committed_at    TIMESTAMPTZ NOT NULL,
  lines_added     INTEGER,
  lines_removed   INTEGER,
  files_changed   INTEGER,
  branch_hint     TEXT
);

CREATE UNIQUE INDEX idx_git_repo_sha ON git_local_commits(repo_path, commit_sha);
CREATE INDEX idx_git_time ON git_local_commits(committed_at);
CREATE INDEX idx_git_author ON git_local_commits(author_email, committed_at);
```

**Why plain Postgres rather than TimescaleDB.** Time-series specialisation buys hypertables, continuous aggregates, automatic partitioning, and better compression for append-heavy workloads. The personal-scale volume tallyd handles is small enough that plain Postgres with conventional indexes is sufficient. A user with five years of bank statements produces tens of thousands of rows, not billions. Adding TimescaleDB is another runtime dependency the fleet does not need. If the scale argument ever changes (a user with decades of high-frequency health data from multiple sensors, say), TimescaleDB can be added later as an optional storage backend behind the same plugin interface.

**Why per-plugin tables rather than a generic key-value schema.** A single polymorphic table (columns like `metric_name` and `metric_value` holding any structured value) would handle arbitrary plugins without schema changes. Rejected because queries over financial data (sum by category, monthly totals, merchant analysis) are fundamentally different from queries over health data (daily step averages, heart-rate distribution, sleep stage analysis), and forcing them into one generic shape means every query has to cast and filter generically. Per-plugin tables let each source type have columns that match its data model, and the structured query API can expose source-specific endpoints that take advantage of the specific shape.

**Why a `notable_events` table rather than direct push on insert.** Narrative generation and the push to ghost.noted are separate from parsing. Parsing a file is a fast, deterministic operation. Narrative generation requires the LLM and can be slow. Separating the two means parsing can complete quickly (rows land in the time-series tables and are immediately queryable via the API path) while narrative generation runs as a background job at whatever rate the LLM allows. A table gives the queue durability across daemon restarts.

**Why `original_row` as JSONB on plugin tables.** Parsed rows are transformed interpretations of the raw export. Storing the original CSV row (or original JSON object from a health export) lets the user audit the parse if a column is wrong, lets a future plugin version re-parse the archived row with improved logic, and lets an external tool reach the original data without re-parsing from the archive.

**Why a `plugin_registry` table.** Plugins version independently. A user might be running the v1 Revolut plugin but have archived data parsed by the v0 plugin. The registry records which plugin version produced which rows (via the `plugin_version` column on `tally_sources`) so a future re-parse can decide whether to re-process with a newer version.

## The plugin interface

Each plugin is a Go package under `cmd/tallyd/plugins/<plugin-name>/`. The package exports a small number of functions the core tallyd daemon calls at the right moments.

```go
// Plugin interface, simplified.
type Plugin interface {
    Name() string
    Version() string

    // CanHandle returns true if this plugin knows how to parse the given source.
    // Called when a new source lands in the archive.
    CanHandle(source TallySource) bool

    // Parse reads the archived source and emits rows into the plugin's tables.
    // Returns the number of rows produced and any error.
    Parse(ctx context.Context, db *sql.DB, source TallySource) (int, error)

    // DetectNotable walks rows produced from this source and flags the ones
    // worth narrating. Writes rows to the notable_events table.
    DetectNotable(ctx context.Context, db *sql.DB, sourceID uuid.UUID) error

    // Narrate generates prose for a single notable event.
    // Called by the narrative worker after DetectNotable has queued events.
    Narrate(ctx context.Context, db *sql.DB, event NotableEvent) (string, error)

    // Migrate runs plugin-specific schema migrations.
    // Called once on daemon startup if the plugin's version has changed.
    Migrate(ctx context.Context, db *sql.DB) error

    // QueryHandlers exposes HTTP handlers for the plugin's structured query
    // endpoints under /api/v1/tallyd/query/<plugin-name>/...
    QueryHandlers() http.Handler
}
```

The interface is deliberately small. A plugin knows how to parse its source format, how to identify notable events, how to narrate them, and how to answer structured queries. Everything else (archive management, sync scheduling, queue management, pushing to ghost.noted) is handled by the core daemon.

A new plugin is added by dropping a Go package into the plugins directory, registering it in the plugin list, and rebuilding the daemon. There is no dynamic loading. Plugins are compiled in. The plugin system is a code-organisation pattern more than a runtime extension mechanism.

**Why compiled-in plugins rather than dynamic.** Dynamic loading (Go plugins, scripting languages, RPC plugins) adds runtime complexity and a security surface. Compiling plugins in means the daemon knows exactly what code is running and the user knows which plugins are present from the release notes. Adding a new plugin requires a rebuild, which is acceptable because plugin additions are infrequent events for a given user.

**Why plugin-owned query handlers.** Each plugin's structured data is different. Rather than forcing plugins to map their data into a generic query interface, each plugin exposes its own handlers under its own path. The core daemon mounts these at `/api/v1/tallyd/query/<plugin-name>/...` and gets out of the way. A Revolut plugin exposes transaction-shaped endpoints, a health plugin exposes measurement-shaped endpoints. Each is queried on its own terms.

## Interfaces

**HTTP endpoints under `/api/v1/tallyd/`.** Same per-daemon pattern as the other ingestion daemons.

```
POST /api/v1/tallyd/push                       - generic structured-data push
POST /api/v1/tallyd/push/mobile                - authenticated mobile streaming endpoint
GET  /api/v1/tallyd/sync                       - per-source sync state
POST /api/v1/tallyd/sync/:config/run           - manually trigger sync for a configured upstream
POST /api/v1/tallyd/reparse/:id                - manually trigger re-parse of an archived source
GET  /api/v1/tallyd/plugins                    - list registered plugins and versions

# Query endpoints, owned by each plugin
GET  /api/v1/tallyd/query/<plugin-name>/...    - structured queries, plugin-specific

# Admin
GET  /api/v1/tallyd/admin/healthz              - liveness
GET  /api/v1/tallyd/admin/readyz               - readiness, Postgres and narrator runtime
GET  /api/v1/tallyd/admin/config               - current daemon config (authenticated)
POST /api/v1/tallyd/admin/config               - update daemon config (authenticated, triggers reload)
```

The query endpoints are plugin-specific. Example query paths a plugin might expose:

```
# bank_revolut plugin
GET /api/v1/tallyd/query/bank_revolut/transactions?from=...&to=...
GET /api/v1/tallyd/query/bank_revolut/totals?group_by=category&window=year
GET /api/v1/tallyd/query/bank_revolut/merchants/top?window=year&limit=10

# health_samsung plugin
GET /api/v1/tallyd/query/health_samsung/daily?metric=steps&from=...&to=...
GET /api/v1/tallyd/query/health_samsung/distribution?metric=heart_rate&window=week
GET /api/v1/tallyd/query/health_samsung/milestones?metric=steps

# git_local plugin
GET /api/v1/tallyd/query/git_local/commits?repo=...&from=...&to=...
GET /api/v1/tallyd/query/git_local/activity?group_by=week
GET /api/v1/tallyd/query/git_local/repos?sort=commits
```

These are examples. Each plugin decides its own query shape. The core daemon does not constrain the shape beyond the URL prefix.

**No direct event emission.** ghost.tallyd does not emit Redis streams, same pattern as ghost.framed and ghost.voiced. Narrative events flow through ghost.noted's inbox, noted emits the events downstream consumers subscribe to.

**Database access.** Postgres read access to tallyd's tables is exposed to ghost.synthd, the app layer, and ghost.watchd through per-daemon roles. ghost.synthd queries the structured tables during episode regeneration via the HTTP API rather than direct SQL, keeping the coupling clean. The app layer may query either way depending on performance needs.

## The notable-event detector

The detector is per-plugin code. It runs after a source has been parsed, walks the rows produced by the parse, and flags any that should become narratives. The rules are heuristic and plugin-specific. v0.3 starts conservative and adds rules over time as the ghost.synthd queue surfaces cases where the user says "this should have been a memory" or "this should not have been a memory."

**Bank plugin heuristics.** Transactions above a configurable threshold. Transactions with a merchant not seen in the previous six months. Transactions tagged with a country or city the user has not visited recently. Transactions with categories the user has marked interesting in config. Transactions on days with unusually high or low total spending.

**Health plugin heuristics.** Measurements that break a personal best (steps, distance, heart-rate peak). New workout types. Measurements that complete a milestone (first 10k steps day, first marathon distance, first 100 workouts of a type). Measurements that mark an unusually long gap since the last measurement of that type.

**Git plugin heuristics.** First commit to a new repo. First commit after a gap of a configurable length. Commits with messages matching configured keywords (release, launch, ship). Merges of branches whose name contains configured keywords (feature, bugfix).

The heuristics are deliberately simple. Complex rules belong in ghost.synthd, which has the full memory graph to work with. Tallyd's detector fires when a row is notable on its own, before any other context is considered.

## The narrator

The narrator is per-plugin code too, but it follows a common pattern. Take a notable event and the row that triggered it, look up any context the plugin wants to include (previous visit count for a merchant, previous personal best for a metric, previous commit to this repo), call a local LLM with a structured prompt, get back one or two sentences of prose, store them, push them to ghost.noted.

The prompt is short and bounded. The narrator does not try to philosophise, editorialise, or speculate. It states the fact with enough context that the resulting sentence is useful to ghost.noted's extraction pipeline. "Spent £47 at Napoli 1820 in Milan on 14 March 2026, first visit to this restaurant" is the shape. No "the warm Italian evening," no "an unforgettable dinner," no emotional freight the underlying data does not support.

When the narrative is pushed to ghost.noted, it includes metadata so the bidirectional link is fully resolvable. Source hint set to `ghost.tallyd`. The tallyd source ID. The plugin name. The row table and row ID. The notable-event ID. The generated prose. ghost.noted stores all of this in its source metadata JSONB, returns its source ID, tallyd records it on the notable-event row.

## The structured query path from ghost.synthd

When ghost.synthd's overnight pass regenerates an episode, it needs two kinds of facts. Narrative memories (which come through the normal ghost.noted-to-ghost.synthd pipeline) and structured aggregates. The structured aggregates come from tallyd through direct API calls.

For a 2024 year-level episode, ghost.synthd's regeneration step calls tallyd's query API with the year window. For each plugin registered, ghost.synthd asks a small set of standard aggregate questions. What is the totals-by-category for financial sources. What is the top-metric-for-the-window for health sources. What is the commits-by-repo for developer sources. The plugin returns structured results. ghost.synthd folds the facts into the episode prose.

The API shape for this is deliberately plugin-agnostic at the synthd-facing level. synthd does not know about Revolut or Samsung Health or git by name. It asks each plugin for its "summary for window X" and the plugin decides what to return. This keeps synthd decoupled from the specifics of each data source.

**The audit trail.** When synthd writes an episode and includes a fact from tallyd ("Your top merchant for the year was Napoli 1820 with 12 visits"), synthd records which tallyd query produced the fact. The user can walk from the episode prose to the tallyd query to the rows that backed the query, the same way they can walk from an episode to a memory to a journal entry to a raw source.

## The summary layer

Per-source tables are the base. Queries that live inside a single source type are clean, fast, and expressive. Revolut spending by category, Samsung Health steps by day, git commits by repo. Each plugin knows its data and can answer its own questions well.

Cross-source queries are a different problem. "Total spending in 2024 across every financial source" means combining Revolut transactions, Monzo transactions, exchange trades, and on-chain wallet activity. "Average steps across every fitness tracker I have used" means combining Samsung Health and Garmin and Apple Health. "Net worth over time" means combining every financial-position source with price history. These queries do not sit cleanly inside any one plugin because they span plugins by construction.

The answer is a summary layer that reads from the per-source tables and produces unified views. The summary layer is deliberately not in v0.3 because it requires decisions about normalisation (what counts as a "category" across Revolut and Binance, what counts as a "step" across Samsung and Garmin) that are better made with real data across real sources than guessed at upfront. v0.3 ships per-source tables and per-source queries, v1.0+ adds the summary layer when the cross-source patterns are clear.

Where the summary layer lives is its own design question. Three candidates, each with trade-offs.

One, inside tallyd as an aggregator plugin that reads from other plugins' tables and writes to unified rollup tables. Keeps all structured data in one daemon. Cost is that the aggregator has to know every plugin's schema, which couples it to every other plugin.

Two, inside ghost.synthd as part of the overnight pass, with the cross-source rollups stored in synthd's own tables and referenced during episode regeneration. Puts the rollups near where they are consumed. Cost is that synthd grows responsibilities it does not currently have.

Three, computed on demand by the app layer, with no stored summary tables at all. The app queries tallyd's per-source endpoints and combines the results at query time. Simplest, but every query pays the computation cost and the aggregation logic lives client-side.

The v1.0+ answer will probably be option one for financial data (where a unified portfolio view is a real user-facing concept) and option three for less structured cases (where ad-hoc cross-source combination suffices). The v0.3 position is that the summary layer is not yet built, and the architecture does not commit to one of the three options until a concrete need arises.

The consequence for v0.3 is that a user wanting a "total spending across all sources" answer needs to combine the per-source queries themselves. This is acceptable because v0.3 ships with three plugins, only one of which is financial (bank_revolut), so cross-source financial queries are not yet a real use case. The summary layer becomes necessary when the second financial plugin lands.

## v0.3 scope

Three plugins ship in v0.3 to prove the pattern. The choice is deliberate, one financial source, one biometric source, one developer signal. This spans the three main categories tallyd will handle long-term and lets the plugin system and the narrative path get exercised against genuinely different data shapes.

**Plugin 1, bank_revolut.** Parses Revolut CSV exports. Transactions become rows, notable-event rules fire for over-threshold transactions, new merchants, and foreign-country spending. Narratives get pushed to ghost.noted. The structured query API exposes transactions, totals, merchants, and category analysis endpoints.

**Plugin 2, health_samsung.** Parses Samsung Health export ZIP files. Measurements become rows, notable-event rules fire for personal bests, new workout types, and milestone events. The structured query API exposes daily and weekly aggregates, distributions, and milestone lists.

**Plugin 3, git_local.** Walks configured git repositories and runs `git log` with the machine-readable format. Commits become rows, notable-event rules fire for new repos, post-gap commits, and release-marked commits. The structured query API exposes commit history, activity-over-time, and repo summaries.

Three plugins is enough to validate the interface, the archive layout, the narrative path, and the structured query path all at once. More plugins follow in v1.0+ driven by user demand and what the ghost.synthd queue surfaces as gaps.

## v1.0+ roadmap

**v1.0.** Expand plugin set. Additional bank plugins (Monzo, HSBC, any bank whose export format the user needs). Apple Health alongside Samsung Health. Screen-time data from iOS and Android. Fitness tracker integrations (Strava export, Garmin export).

**v1.0+.** Cloud storage connectors where a user-run export writes into a watched folder. This is the equivalent of the cloud-storage-via-export pattern in ghost.noted and ghost.framed. Direct third-party API connectors are deliberately deferred because they require credential storage, which enlarges the threat surface documented in the [honeypot post](https://www.localghost.ai/hard-truths/honeypot).

**v1.0+.** Cross-plugin notable-event detection. An event that is not notable in its own plugin might be notable in combination with another plugin's data. A £500 transaction and a trip to a new country on the same day is a memory. Requires coordination across plugins, which is deferred until the single-plugin rules are well understood.

**v1.5+.** User-configurable notable-event rules. The user can define their own rules ("flag transactions over £200 at merchants with 'coffee' in the name") without writing Go code. A small rule language, probably something like the BPM-style predicate grammar used in some monitoring systems.

**Never.** Cloud-hosted structured analysis. Same position as the rest of the fleet. Local inference, local aggregation, local storage.

## Open questions

**Threshold tuning.** The notable-event thresholds in v0.3 are educated guesses. Real usage will produce events that should not have been flagged and miss events that should have been. The overnight-pass queue in ghost.synthd is how the user surfaces these cases, and the plugin rules should be adjustable based on what the user says. Whether the adjustment happens through config (per-plugin thresholds the user tunes) or through learned-from-feedback weights is a v1.0 decision.

**Narrative voice.** The v0.3 narrator produces factual, dated sentences. A future version could produce prose that matches the user's own writing voice, learned from ghost.noted's journal entries. This would make tallyd-derived memories blend more naturally with user-written ones. Deferred because v0.3 should establish the pattern first, and because voice-imitation is an inference problem that needs its own design.

**Multi-currency handling.** The bank plugin stores `amount` and `currency` separately. Queries that aggregate across currencies need exchange-rate handling. v0.3 answers "total spending in GBP" by filtering on GBP rows. Cross-currency aggregation is a v1.0 concern.

**Duplicate detection across sources.** A transaction might appear in both a Revolut export and a PDF bank statement if the user has imports running for both. The content-hash dedup catches identical exports but not different-export-same-transaction. Plugin-specific rules are needed, and the v0.3 answer is "configure one source per account type, avoid importing the same underlying data twice."

**Historical backfill for new plugins.** When a user adds a new plugin, the archived exports from before the plugin existed need to be re-processed. The current plan is that adding a plugin triggers a re-parse pass over the archive. Whether the re-parse runs automatically or waits for manual trigger is undecided.

**Query API rate limits.** ghost.synthd calls tallyd during episode regeneration. The overnight pass could produce a burst of queries when regenerating many episodes. Rate limiting inside tallyd would throttle synthd, which is the wrong shape. The answer is probably that synthd batches its tallyd queries per plugin per time window and tallyd serves the batched query efficiently. Exact design is a v0.3 implementation decision.

## Rejected approaches

**One generic schema for all structured data.** A single polymorphic table with columns like `metric_name`, `metric_value_text`, `metric_value_numeric`, `metric_timestamp`. Rejected because queries over each data type are fundamentally different, and forcing them into a generic shape costs query expressiveness and performance for every downstream consumer.

**TimescaleDB as the storage backend.** Considered for time-series specialisation. Rejected for v0.3 because the personal-scale volume does not require it and adding another runtime dependency has cost. Reconsidered when the scale argument changes.

**Narrative generation synchronous with parsing.** Simpler pipeline. Rejected because parsing should complete quickly so the structured API path is immediately available, while narrative generation involves LLM calls that can be slow. Keeping them asynchronous lets the structured path work even when the narrator is backed up.

**Emitting Redis stream events from tallyd.** Alternative to pushing through ghost.noted. Rejected because it would double the event vocabulary in the fleet. The pattern established by ghost.framed and ghost.voiced is push-through-noted, and tallyd follows it for consistency.

**LLM-based notable-event detection.** Ask the model which rows are notable. Rejected for v0.3 because the rules are specific enough that code expresses them more cheaply, and because LLM calls on every parsed row adds compute cost that does not earn its keep. The narrator uses the LLM for prose generation, not for the decision to narrate.

**Dynamic plugin loading.** Runtime extension via Go plugins or scripting. Rejected for the reasons named in the plugin interface section. Compile-in is simpler, safer, and adequate for the add-rate of new plugins.

**Direct SQL access from ghost.synthd to tallyd's tables.** Would let synthd query without the HTTP round-trip. Rejected because the HTTP API is the right abstraction boundary. Direct SQL coupling means a schema change in tallyd breaks synthd's overnight pass. An HTTP API is stable across schema migrations and lets each plugin own its query shape.

**Cross-daemon correlation inside tallyd.** Considered having tallyd correlate its own data with ghost.noted journal entries (e.g., tag transactions with nearby journal entries). Rejected because cross-source correlation is ghost.synthd's job. Tallyd stays focused on structured ingestion and query.

## Implementation notes

Written in Go. Plugins are Go packages compiled into the daemon binary. Runtime dependencies are Postgres and the local LLM runtime for narrative generation. No Redis dependency directly (events flow through ghost.noted's Redis, not tallyd's).

Three goroutine groups, matching the other ingestion daemons. Sync workers handle filesystem watches and push endpoints. Parse workers run plugin parse functions on queued sources. Narrator workers run plugin detect+narrate+push functions on queued notable events.

Parsing is deterministic and fast. A year of Revolut CSV completes in under a second of SQL insert work. Health exports can be larger (Samsung Health exports can be hundreds of MB uncompressed) and are streamed rather than loaded entirely into memory.

Narrative generation is rate-limited at the LLM layer, shared across the fleet. Bursts of notable events (a monthly bank export lands with fifteen notable transactions) are handled by the narrator worker pulling from the queue at the LLM's sustainable rate.

Graceful shutdown, same rules as the other ingestion daemons. Finish in-flight work, commit state, exit cleanly. Queued work survives restart.

Test strategy. Unit tests for plugin parse functions against fixture export files. Integration tests for the full path from source arrival to ghost.noted push, using a fake ghost.noted inbox. Property-based tests on the reconciliation when a source is re-parsed with updated plugin logic. Quality evaluation of narrative prose is manual, same as in ghost.framed and ghost.voiced.

## Versioning

The daemon version tracks the overall LocalGhost release. Plugin versions are independent. Each plugin records its version on every row it produces (via the `plugin_version` column on `tally_sources`), so a future re-parse can decide whether to re-process older rows with newer plugin logic. Schema migrations are per-plugin, run on startup when the plugin's version has changed.