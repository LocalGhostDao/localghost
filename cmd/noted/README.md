# ghost.noted

Text ingestion, archival, and journal-entry extraction. Pulls from upstream sources (or accepts pushes from external agents), stores canonical copies in its own archive, runs extraction against the archive, produces journal entries and raw mentions that ghost.synthd consumes.

## Status

Phase 0. No code. This document describes the architecture as of April 2026, before first commit. It will be revised after implementation starts, especially the extraction prompt, the sync scheduling, and the conflict-resolution behaviour, which are the parts most likely to need tuning against real data.

## Purpose

ghost.noted is the text layer of LocalGhost's personal archive. It has three jobs wrapped in one daemon, sync (pull upstream content into the local archive, or accept pushes from external agents), archival (keep the canonical copy of every text source the user has decided matters), and extraction (turn archive content into structured journal entries through a local LLM).

The three-in-one framing is deliberate. Splitting them into separate daemons would create coordination problems, the sync daemon would finish writing, the archive daemon would need to know, the extraction daemon would need to wait. Keeping them in one daemon means the whole pipeline from upstream-arrival to journal-entries-emitted lives behind a single clear boundary. Downstream daemons, especially ghost.synthd, only see the output (entries and mentions as events), not the internals.

The architecture is bidirectional. The daemon can pull from upstream sources on a schedule (IMAP polling, filesystem mirror, cloud storage sync) or accept pushes from external agents (the mobile app, a browser extension, a CLI tool). Which direction applies depends on the source type and the deployment. The daemon is agnostic about which path a piece of content arrived through, once it is in the archive the rest of the pipeline is identical.

## Position in the fleet

ghost.noted sits at the edge of the memory fleet. Upstream of it are the user's content sources. Downstream of it is ghost.synthd, which consumes journal entries and mentions.

```
UPSTREAM SOURCES
  |
  |  PULL mode (daemon polls upstream on schedule):
  |    IMAP server
  |    Filesystem watcher on user-specified folders
  |    Cloud storage (v0.2+)
  |
  |  PUSH mode (external agent sends to the daemon):
  |    Mobile app over encrypted tunnel
  |    Browser extension over local HTTP
  |    CLI tool, scripted export
  |
  v
ghost.noted
  |
  |  step 1, sync loop or push endpoint brings content in
  |  step 2, store canonical copy in local archive
  |  step 3, write source row to Postgres, enqueue extraction
  |  step 4, worker pulls from queue, calls local LLM
  |  step 5, LLM returns journal entries and raw mentions
  |  step 6, write entries and mentions to Postgres
  |  step 7, emit events on Redis streams
  |
  v
ghost.synthd (entity resolution, clustering, memories, queue)
ghost.watchd (health, counters, queue depth, archive size)
```

The contract ghost.noted maintains with ghost.synthd is that every journal entry has a stable ID, a source reference, a set of timestamps, and a list of raw mentions. ghost.synthd is responsible for everything that happens to entries after they arrive, including entity resolution, merging, clustering, decay, and promotion to anchor memories. ghost.noted does not retain any understanding of what ghost.synthd has done with its output.

## Responsibilities

**Upstream sync, pull mode.** The daemon polls or watches upstream sources on a configured schedule. IMAP uses IDLE where available with polling fallback. Local filesystems use inotify on Linux and FSEvents on macOS. Cloud storage (a v0.2 concern) would use the provider's change-notification API where one exists, polling the file list where not. Each pull comparison checks the upstream last-modified timestamp against what is recorded in the archive. If the upstream is newer, the daemon pulls the new version.

**Upstream sync, push mode.** The daemon exposes two endpoints for external agents. The first is a general `/api/v1/noted/inbox` endpoint that accepts JSON-wrapped text with metadata (used by browser extensions, CLI tools, anything that pushes occasional content). The second is an authenticated streaming endpoint for the mobile app at `/api/v1/noted/push/mobile`, which stays connected over the encrypted tunnel and pushes content as it is captured. Both paths land in the same archive.

**Archive management.** Every piece of content that arrives is stored in a local archive under `/var/lib/localghost/noted/archive/`. The layout is internally organised, not a mirror of the upstream structure, because the upstream structure is an implementation detail of wherever the content came from. Files are stored at `<year>/<month>/<source-hash>.ext` with a Postgres row mapping source identifier and upstream URI to archive path.

**Deduplication.** If the same raw content arrives through multiple paths (an email saved as a markdown file, the same paragraph pasted twice, a source synced from two places), the archive detects the duplicate using a content hash. The content is stored once in the archive, and a `source_aliases` row records the additional upstream reference. Extraction runs once, the entries are produced once.

**Change tracking.** When upstream has a newer version of a source, the daemon pulls the new content, stores it in the archive under a new revision marker, diffs against the previous version, and triggers re-extraction. The old archive file is retained for audit (configurable retention), the new file becomes the current version. Updates are updates, not delete-then-create, so the source identity and its downstream event stream preserve continuity.

**Extraction queueing.** Source changes do not trigger extraction synchronously. The daemon writes the source change to an `extraction_queue` table in Postgres and returns. A worker loop pulls from the queue and runs extraction at the rate the local box can handle. The queue is durable across restarts, so a crash mid-extraction loses no work. The queue also serves as natural back-pressure when a large sync (initial IMAP import, first-time folder mirror) arrives.

**Extraction.** The worker loads a queued source from the archive, calls the local LLM with the extraction prompt, and parses the response. The response is a list of journal entries, each with content, timestamps where available, and a list of raw mentions. Parsing failures are handled by retrying with a simpler prompt, and after three retries the source is marked as extraction-failed and surfaced in ghost.synthd's queue for the user to handle manually.

**Entry reconciliation on update.** When a source changes and re-extraction runs, the worker reconciles the new entries against the old ones. Any old entry not present in the new extraction is marked deleted. Any new entry not present in the old extraction is marked created. Entries present in both are checked for content change and marked updated where the content differs. The reconciliation is conservative, it trusts the new extraction and does not try to preserve old entries that the LLM no longer produces.

**Deletion.** Deletion happens only from within LocalGhost. The user deletes a journal entry through the UI, ghost.noted deletes the entry, cascades to mentions, emits events. The user can also delete a source, which cascades to all entries derived from it. Deletion at the upstream (an email deleted on the server, a file removed from the user's folder) does not automatically delete from the archive. The archive retains the copy and the sync simply stops updating that source. This is deliberate, the archive is the user's personal history, and forgetting has to be an explicit act.

**Source metadata capture.** Non-inferred metadata captured at ingestion. Timestamps from the file or message headers. Author from email From field. Source URL for fetched content. Upstream last-modified for sync comparison. No entity extraction at this layer, that happens in the extraction step. No classification, no summarisation.

## Non-responsibilities

**No entity resolution.** ghost.noted produces raw mentions as verbatim strings. ghost.synthd decides whether two mentions refer to the same entity.

**No embedding.** Vector work is ghost.synthd's concern.

**No clustering.** Pattern detection across entries is ghost.synthd's concern.

**No search.** Queries against entries or memories go through the app layer, which talks to ghost.synthd directly.

**No cross-source correlation at ingestion.** Connecting "this email mentions the same project as that markdown note" is ghost.synthd's job.

**No user-facing UI.** The daemon exposes an admin HTTP endpoint for configuration and the inbox push endpoint, nothing else. User-facing interaction happens through the app layer.

**No upstream writes.** IMAP sync is read-only, the daemon never marks messages read, never moves them, never deletes them. Cloud storage sync (v0.2) will also be read-only. The user's upstream remains untouched.

**No inference beyond extraction.** The local LLM call exists to produce journal entries. Any other use of inference is in a different daemon.

## Cross-daemon source links

Some text sources ghost.noted stores do not originate from text. A description of a photograph arrives from ghost.framed. A transcript of a voice memo arrives from ghost.voiced. These are text as far as ghost.noted is concerned, the extraction pipeline treats them identically to any other source, but they carry a reference back to the upstream daemon so the link between text and its non-text origin stays resolvable.

When a push arrives with a `source_hint` naming another daemon in the fleet (`ghost.framed`, `ghost.voiced`, etc.), the metadata carries the upstream source ID in a known key. ghost.noted stores that ID in `sources.metadata` and returns its own source ID in the push response. The upstream daemon records the ghost.noted source ID in its own state so the link is cached on both sides. This lets either daemon resolve the link without a query through the other's API.

When the upstream daemon pushes an update (the image was re-processed, the transcript was regenerated against a newer model), the push carries the same upstream source ID. ghost.noted looks up the existing source by that ID in metadata, updates the content, triggers re-extraction, and emits the normal update events. The ghost.noted source ID is stable across the update, and all downstream references (clusters in ghost.synthd, mentions, anchors) survive.

When the upstream daemon pushes a deletion signal (the image was deleted from the ghost.framed archive), ghost.noted cascades the deletion through its normal path. The link drives the cascade rather than being collateral from it.

This pattern keeps ghost.noted as the single text-processing layer in the fleet regardless of how the text arrived. The cost is that every non-text daemon has to speak ghost.noted's inbox protocol and track ghost.noted source IDs. The benefit is that ghost.synthd, the app's query layer, and every other downstream consumer only deal with one event vocabulary and one source schema.

## Data model

Seven tables, all Postgres 15+. The archive filesystem layout sits alongside Postgres, with rows mapping source identifiers to archive paths. Entity resolution lives in ghost.synthd, so the canonical entities table is not in this schema.

```sql
-- One row per canonical source the archive has ever seen.
CREATE TABLE sources (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_type       TEXT NOT NULL,                -- 'imap', 'fs_watch', 'inbox', 'mobile_push', etc.
  upstream_uri      TEXT NOT NULL,                -- file path, IMAP UID, inbox submission ID
  archive_path      TEXT NOT NULL,                -- Relative path within the noted archive
  content_hash      BYTEA NOT NULL,               -- SHA-256 of the current content
  current_content   TEXT,                         -- Cached current content for fast reads (nullable for large sources)
  title             TEXT,                         -- Extracted title or first line
  upstream_modified TIMESTAMPTZ,                  -- Upstream last-modified, for sync comparison
  first_seen        TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_synced       TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at        TIMESTAMPTZ,                  -- Soft delete from within LocalGhost
  metadata          JSONB NOT NULL DEFAULT '{}'
);

CREATE UNIQUE INDEX idx_sources_type_uri ON sources(source_type, upstream_uri);
CREATE INDEX idx_sources_content_hash ON sources(content_hash);
CREATE INDEX idx_sources_deleted_at ON sources(deleted_at) WHERE deleted_at IS NOT NULL;

-- Additional upstream URIs that resolve to the same source (dedup).
CREATE TABLE source_aliases (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id    UUID NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  source_type  TEXT NOT NULL,
  upstream_uri TEXT NOT NULL,
  first_seen   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_aliases_type_uri ON source_aliases(source_type, upstream_uri);
CREATE INDEX idx_aliases_source_id ON source_aliases(source_id);

-- One row per revision of a source. Insert-only, retained for audit.
CREATE TABLE source_revisions (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id      UUID NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  content        TEXT NOT NULL,
  content_hash   BYTEA NOT NULL,
  archive_path   TEXT NOT NULL,                   -- Path to the archived revision file
  seen_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  change_type    TEXT NOT NULL                    -- 'created', 'updated', 'restored'
);

CREATE INDEX idx_source_revisions_source_id_seen_at ON source_revisions(source_id, seen_at);

-- One row per journal entry. Produced by the LLM from a source.
CREATE TABLE journal_entries (
  id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id            UUID NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  content              TEXT NOT NULL,             -- The self-contained observation, usually a sentence
  content_hash         BYTEA NOT NULL,            -- For dedup on re-extraction
  source_created_at    TIMESTAMPTZ,               -- From the source file/message headers
  source_seen_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  happened_at          TIMESTAMPTZ,               -- Event time extracted from content, nullable
  happened_at_accuracy TEXT,                      -- 'exact', 'day', 'month', 'year', 'unknown'
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at           TIMESTAMPTZ,
  metadata             JSONB NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_entries_source_id ON journal_entries(source_id);
CREATE INDEX idx_entries_happened_at ON journal_entries(happened_at) WHERE deleted_at IS NULL;
CREATE INDEX idx_entries_updated_at ON journal_entries(updated_at) WHERE deleted_at IS NULL;
CREATE INDEX idx_entries_deleted_at ON journal_entries(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX idx_entries_content_hash ON journal_entries(source_id, content_hash);

-- One row per raw mention inside a journal entry.
CREATE TABLE mentions (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  entry_id      UUID NOT NULL REFERENCES journal_entries(id) ON DELETE CASCADE,
  mention_text  TEXT NOT NULL,                    -- The verbatim string as it appears
  mention_type  TEXT,                             -- Free-form type hint from the LLM
  span_start    INTEGER,
  span_end      INTEGER,
  deleted_at    TIMESTAMPTZ,
  metadata      JSONB NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_mentions_entry_id ON mentions(entry_id);
CREATE INDEX idx_mentions_text ON mentions(mention_text) WHERE deleted_at IS NULL;
CREATE INDEX idx_mentions_type ON mentions(mention_type) WHERE deleted_at IS NULL;

-- Sync state per upstream source.
CREATE TABLE sync_state (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_config   TEXT NOT NULL,                  -- Named reference to a configured upstream (e.g. 'imap_main', 'folder_journal')
  last_run_at     TIMESTAMPTZ,
  last_success_at TIMESTAMPTZ,
  last_error      TEXT,
  next_run_at     TIMESTAMPTZ,
  status          TEXT NOT NULL DEFAULT 'idle'    -- 'idle', 'running', 'error'
);

CREATE UNIQUE INDEX idx_sync_state_source ON sync_state(source_config);

-- Queue for pending extraction work.
CREATE TABLE extraction_queue (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id      UUID NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  enqueued_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  started_at     TIMESTAMPTZ,
  completed_at   TIMESTAMPTZ,
  attempts       INTEGER NOT NULL DEFAULT 0,
  last_error     TEXT,
  status         TEXT NOT NULL DEFAULT 'pending'
);

CREATE INDEX idx_queue_status_enqueued ON extraction_queue(status, enqueued_at)
  WHERE status IN ('pending', 'failed');
```

**Why separate sources from source_revisions from source_aliases.** Three different concerns. The `sources` table holds the canonical current state of each source. The `source_revisions` table is an insert-only audit log of how the content has changed over time. The `source_aliases` table records alternative upstream references that resolve to the same canonical source. Splitting them keeps each table focused on one role, and the most common read path (get the current content of a source) touches only `sources`.

**Why the archive is on the filesystem rather than in Postgres.** Text content is small enough that Postgres could hold it in a bytea column, but the archive will grow large over time and relational storage is the wrong tool for large, immutable, rarely-queried blobs. Filesystem storage lets standard backup tools work on the archive directly, lets the user inspect the archive with standard commands if they need to, and keeps Postgres focused on the relational work it is good at. The `current_content` column on `sources` caches a copy for fast reads without forcing a filesystem round-trip for every query, but the filesystem is the source of truth.

**Why three timestamps per entry.** The source has an `upstream_modified` timestamp from the upstream system and a `first_seen` timestamp from when ghost.noted first encountered it. The entry adds a `happened_at` that the LLM extracts from the content, nullable, with an accuracy marker. ghost.synthd's job is to aggregate approximate timestamps into more accurate ones over time.

**Why soft-delete everywhere.** Hard deletes lose audit trail and break any downstream reference that pointed at the row before it was deleted. Soft deletes let ghost.synthd drop entries from active indexes while ghost.noted retains the audit. Retention policy for soft-deleted rows is a separate concern, handled by a sweep process outside this daemon.

**Why mention_type is free-form rather than enumerated.** An earlier schema had a fixed enumeration. I dropped it because an enumeration forces the LLM to fit real-world mentions into a small set of categories that are often wrong. A restaurant is a place but it is also an organisation. An emotion is a concept but it deserves its own treatment. ghost.synthd uses vector similarity on `mention_text` and `mention_type` together to cluster mentions into entities, and an open vocabulary produces better clustering than a forced one.

**Why a Postgres queue table for extraction, and Redis streams for external events.** Two different queues at play. The internal extraction queue is ghost.noted talking to itself. The external event stream is ghost.noted talking to the rest of the fleet. The first needs strict transactional coupling with the source write, single-consumer (the extraction worker inside ghost.noted), and durability across restarts. Postgres is the right tool for that. The second needs multi-consumer delivery, replay from any offset, and consumer groups. Redis streams are the right tool for that. The two queues are separate and that is deliberate.

**Why a sync_state table.** Each upstream source config (one IMAP account, one watched folder, one cloud storage mount) has its own sync cadence, its own error history, and its own next-run schedule. Tracking this state in Postgres rather than in memory means a daemon restart does not lose sync scheduling, and the admin endpoint can show the user the current state of every configured sync.

## Interfaces

**Events published to Redis streams.**

Events go to Redis streams rather than pub/sub. Streams give multi-consumer delivery with consumer groups, replay from any offset, and at-least-once semantics. ghost.noted writes the source and the extraction_queue row in the same Postgres transaction, commits, and then emits to the stream. If the stream emit fails after commit, the daemon retries from the queue state on restart, so no event is lost and downstream sees at-least-once delivery.

```
noted.entry.created  { entry_id, source_id, content, happened_at, mentions }
noted.entry.updated  { entry_id, source_id, old_content, new_content, mentions }
noted.entry.deleted  { entry_id, source_id, reason }
noted.source.ingested { source_id, source_type, entry_count, is_update }
noted.source.deleted { source_id, cascaded_entry_ids }
noted.extraction.failed { source_id, attempts, last_error }
```

Event payloads include enough context that downstream daemons do not usually need to round-trip to Postgres. Stream names are the event types, each one a separate stream, so consumers can subscribe to only the events they care about.

**HTTP endpoints, all on localhost, under `/api/v1/noted/`.** The per-daemon versioned path matches the convention across the LocalGhost fleet. Public ingestion endpoints (inbox, push), operational endpoints (sync, queue, reextract), and admin endpoints (healthz, readyz, config) all sit under the same prefix.

```
POST /api/v1/noted/inbox                    - generic push, accepts JSON-wrapped text
POST /api/v1/noted/push/mobile              - authenticated streaming endpoint for the mobile app
GET  /api/v1/noted/queue                    - extraction queue depth and recent failures
GET  /api/v1/noted/sync                     - per-source sync state and next run time
POST /api/v1/noted/sync/:config/run         - manually trigger a sync run for a configured upstream
POST /api/v1/noted/reextract/:id            - manually trigger re-extraction of an archived source

GET  /api/v1/noted/admin/healthz            - liveness
GET  /api/v1/noted/admin/readyz             - readiness, checks Postgres, Redis, LLM runtime, archive filesystem
GET  /api/v1/noted/admin/config             - current daemon config (authenticated)
POST /api/v1/noted/admin/config             - update daemon config (authenticated, triggers reload)
```

The inbox endpoint accepts:

```json
{
  "content": "...",
  "title": "...",
  "source_hint": "browser-extension",
  "upstream_uri": "https://example.com/article",
  "metadata": {}
}
```

The mobile push endpoint accepts the same shape but uses the encrypted tunnel rather than localhost HTTP, and the authentication is the per-device token established at pairing.

**Database access.** Postgres read access on sources, journal_entries, and mentions is exposed to ghost.synthd, the app layer, and ghost.watchd through per-daemon roles. ghost.noted is the only daemon with write access to those tables. The archive filesystem is owned by ghost.noted's process user.

## The extraction prompt

The extraction is the heart of the daemon and the part most likely to change in the first month of running. What follows is the v0.1 starting point.

The extraction call gets the full archived source content, source metadata (timestamps, title, source type), and returns a structured JSON response. The prompt asks the model for a list of journal entries, where each entry is a single observation that stands on its own. Anaphoric references ("he said it was fine") are resolved inline by the extraction to name the entity where the source makes it obvious ("Ionut said it was fine"). Temporal references ("yesterday", "last week") are resolved against the source timestamp where the source timestamp is known.

For each entry the extraction produces the entry content as a self-contained sentence, the `happened_at` timestamp if extractable with an accuracy marker, and a list of mentions each with verbatim string, rough type hint, and character span.

The prompt handles the "no entries" case explicitly. If the content is structural (a code file, a shell script, a log file), the extraction returns zero entries and ghost.noted records the source as processed-but-empty.

**What the prompt does not try to do.** It does not summarise the source. It does not categorise or tag. It does not resolve mentions against known entities. It does not decide whether an entry is important. All of those are ghost.synthd's jobs.

**Multilingual content.** The extraction prompt translates non-English content into English before producing entries. The raw source content stays in its original language in the archive, the extracted entries are English. This is a v0.1 simplification that trades some fidelity on multilingual content for a much simpler downstream. v0.2 is the point to revisit.

**Failure modes the prompt has to handle.** The model sometimes returns invalid JSON, sometimes hallucinates entries not in the source, sometimes misses entries that should have been extracted, sometimes fuses two entries that should have been separate. The worker retries with a simpler prompt on invalid JSON. Hallucinated and missed entries are surfaced in ghost.synthd's queue for user correction. Fused or split entries are also corrected in the queue.

## v0.1 scope (wisp)

v0.1 starts small. Not every source type ships in the first release, and whether the daemon pulls or pushes for a given source is deliberately left flexible so the first-commit-through-first-release path can respond to what actually works on the box.

**What definitely ships in v0.1.**

- Filesystem watcher on a user-configured markdown folder, pull mode.
- Local inbox endpoint on localhost for any external agent that can make an HTTP request.
- The extraction pipeline against whichever sources are configured.
- The archive, revision tracking, and deletion semantics.
- The Redis streams event output for ghost.synthd.

**What probably ships in v0.1.**

- IMAP account sync, pull mode, read-only. Whether this ships in v0.1 or v0.2 depends on how long the filesystem watcher and extraction pipeline take to stabilise. If they are clean by month two, IMAP joins v0.1.

**What probably does not ship in v0.1.**

- Mobile push endpoint. The mobile app is v0.2 work, and the push endpoint lands when the mobile app is ready to use it.
- Cloud storage sync (Dropbox, Google Drive, iCloud via export). v0.2 at the earliest.

**What definitely does not ship in v0.1.**

- Slack exports, Discord exports, RSS feeds, browser history, git commit messages. These are all v0.2+ and their specific paths are decided when the work to build them starts.

## v0.2+ roadmap

**v0.2 (shade) additions.** Mobile app push endpoint. IMAP if not already in v0.1. RSS feed polling. Browser extension push (the extension POSTs to the inbox endpoint). Cloud storage sync where a user-run export script writes into the watched folder.

**v0.3.** Slack and Discord export batch ingestion. Git commit message sync from configured repos. Possibly a better extraction model if the v0.1 baseline proves insufficient.

**v1.0.** Voice transcript ingestion via ghost.voiced pushing to the inbox. Screenshot text ingestion via ghost.framed pushing to the inbox. These are late because the producing daemons need to mature first.

**Never.** Direct cloud-service connectors that require the daemon to hold long-lived OAuth credentials. Those go through a user-run export script that writes files the watcher tracks, or through a browser extension that POSTs to the inbox. The daemon does not hold third-party credentials, because credential storage enlarges the threat surface documented in the [honeypot post](https://www.localghost.ai/hard-truths/honeypot).

## Open questions

**How aggressive should extraction be on structured content.** A bank statement CSV has rows that could each be journal entries. A chat log has hundreds of short messages that could each be entries. Over-extracting produces noise. Under-extracting loses information. The current plan is that the v0.1 prompt treats structural content conservatively and ghost.synthd's queue surfaces cases where the user wants finer extraction. The right default will be clearer after running against real data.

**How should happened_at be extracted for content without clear temporal markers.** An entry like "I have been thinking about this for a while" has no clear happened_at. The current plan leaves it null and lets ghost.synthd fill in over time by correlating with other entries. Whether that works in practice is unknown.

**How to handle extraction model changes.** When the extraction model is swapped, the existing entries were produced by the old model and are no longer consistent with what the new model would produce. The v0.1 answer is to leave old entries as they are, let new sources go through the new model, and document the drift. A user-triggered bulk re-extraction is a v0.2 capability.

**Anchor-protected entries on re-extraction.** The current re-extraction reconciles entries by content match. An entry the user has promoted to an anchor in ghost.synthd could be lost if a minor edit to the source causes the LLM to produce slightly different entries. A safer pattern is to surface anchor-entry-at-risk cases in the queue. v0.2 concern.

**IMAP IDLE reliability.** Server support is uneven. Fallback polling is mandatory. How aggressive the polling should be depends on how the daemon is deployed. Starting with five minutes and tuning from evidence.

**Archive retention for deleted content.** Soft-deleted sources keep their archive files until a retention policy says otherwise. Forever is probably wrong, thirty days is probably too short. The honest answer is "until the user configures, forever" with the retention policy being a v0.2 decision.

**Conflict resolution on simultaneous upstream and local changes.** If the user edits a source locally in the archive (unusual, not the intended workflow) and the upstream also changes, the sync loop has to pick a winner. v0.1 treats upstream as authoritative and local edits as a bug. v0.2 may need a conflict-resolution UI if users ever edit archive content directly.

**Extraction failures at scale.** A source that cannot be extracted after three retries is marked failed. The volume of failures could be noisy if a particular file type reliably fails. Some failure-grouping mechanism will be needed in v0.2, grouping similar failures so the user dismisses them in batch.

## Rejected approaches

**Raw content as the memory.** Earlier design treated raw content as the memory itself and skipped LLM extraction. Rejected because the POST_09 memory model requires atomic observations that can be clustered, anchored, and reasoned about individually. A journal file of twenty paragraphs is twenty or more memories, not one.

**Entity resolution in ghost.noted.** Simpler design would have extraction resolve entities during the LLM call. Rejected because reliable entity resolution requires cross-source context that ghost.noted does not have, and because resolution is inherently cumulative work better suited to ghost.synthd's overnight pass.

**External archive on upstream storage (no local copy).** Earlier design referenced content in the user's existing folders and IMAP without copying. Rejected once LocalGhost's identity as a personal archive became clear. The archive has to be local and authoritative, because if the upstream disappears, the memory layer should not disappear with it.

**Archive as a flat mirror of upstream structure.** Simpler to implement, easier to debug. Rejected because upstream structures vary (IMAP folder hierarchies, filesystem trees, cloud storage paths) and mirroring them means the archive layout changes every time a new source type is added. Internal organisation by year, month, and hash gives a stable layout that any source type can use.

**Filesystem as database (no Postgres).** Simpler stack, fewer dependencies, file-level inspection straightforward. Rejected because the relational queries ghost.synthd needs are slow against a flat file tree and fast against a Postgres index. Postgres is also the standard in the rest of the fleet.

**Synchronous extraction.** Earlier draft had extraction running on the same call path as sync. Rejected because queues are the right pattern even at low throughput, durability of a queue table is worth the small complexity cost, and the queue gives back-pressure when a large sync arrives.

**Redis streams for the internal extraction queue.** Briefly considered unifying the internal queue and external event stream on Redis streams. Rejected because the internal queue has a single consumer inside the same daemon and wants to be in the same transactional boundary as the source write. Postgres-as-queue for internal work, Redis streams for external events.

**LISTEN/NOTIFY instead of Redis streams for external events.** Would remove Redis from the dependency list. Rejected because LISTEN/NOTIFY has an 8KB payload limit and ghost.noted events routinely exceed that. Redis streams also give replay and consumer-group semantics that LISTEN/NOTIFY does not.

**Upstream-authoritative deletion.** Earlier draft had upstream deletions cascading to the archive. Rejected because the archive is meant to be a personal history that retains things the user might forget to keep, and propagating upstream deletes would defeat the point. Deletions from the archive happen only through LocalGhost's own UI.

## Implementation notes

Written in Go. Single binary, runtime dependencies are Postgres, Redis, and the local LLM runtime. Config via YAML with sane defaults. Structured JSON logs to stderr, configurable.

The daemon runs three goroutine groups. Sync workers handle pulls from upstream (one per configured source) and accept pushes on the HTTP endpoints. Archive writers handle the actual archive filesystem operations and the Postgres transaction that registers each source change. Extraction workers pull from the queue and run the LLM pipeline.

Graceful shutdown matters. SIGTERM stops accepting new sync work but finishes in-flight writes. The extraction worker finishes the current job, commits, and stops pulling. Queued-but-not-started work waits for the next daemon start. Data loss on shutdown is unacceptable.

Test strategy. Unit tests for extraction response parsing, reconciliation logic, and queue state machine. Integration tests that spin up Postgres, Redis, and a mock LLM in containers, run the daemon against fixture sources, and verify archive state and event emission. Property-based tests on reconciliation and revision tracking. End-to-end tests against a real local LLM are a v0.2 concern.

Archive filesystem operations are fsync'd. A crash mid-archive-write should leave the archive in a consistent state where either the file is fully written and the Postgres row points at it, or the file is absent and no Postgres row exists. Partial archive writes are unacceptable.

## Versioning

The daemon version tracks the overall LocalGhost release. The event schema uses an explicit `schema_version` field and consumers must handle at least the two most recent versions. The database schema uses standard migrations. The extraction prompt is versioned separately in its own file, with the prompt version recorded in `journal_entries.metadata` so analysis can distinguish entries produced by different prompt-model combinations.