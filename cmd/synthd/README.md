# FULLY AI GENERATED NOT YET REVIEWD
# ghost.synthd

The memory layer. Consumes journal entries from ghost.noted, resolves mentions into canonical entities, clusters entries into memories, rolls memories into episodes at any time granularity, maintains the queue of user decisions the system cannot make on its own, and serves the app layer and ghost.cued with memories and episodes ranked for the moment.

## Status

Phase 0. No code. This document describes the architecture as of April 2026, before first commit. It will be revised after implementation starts. Many parts of this doc are committed to, a few parts are explicitly hedged, and a handful of open questions are named at the end rather than guessed at.

## Purpose

ghost.synthd is where personal memory is built. Journal entries arriving from ghost.noted are raw material. The daemon runs a slow overnight pass that resolves entity mentions, clusters entries into memories, rolls memories into episodes at multiple time granularities, and surfaces any decision it cannot make on its own to the user through the queue POST_09 described.

The daemon does not ingest. That is ghost.noted, ghost.framed, and ghost.voiced. The daemon does not serve queries directly to the user. The app layer owns the query interface and talks to ghost.synthd through a local API. The daemon does not surface memories in real time based on situation. That is ghost.cued, which reads from ghost.synthd's output.

ghost.synthd is the quiet engine that turns a flow of sentences into a structured personal history. Almost everything the user experiences as the memory layer comes from this daemon's output.

## The four-layer hierarchy

Raw input to journal entry to memory to episode. Four layers, each one an aggregation of the layer below, each one with its own semantics and lifecycle.

**Raw input** is the source. Text, image, audio. Lives in the ingestion daemons' archives (ghost.noted, ghost.framed, ghost.voiced). ghost.synthd does not store raw input, it references it through the cross-daemon source-link pattern.

**Journal entry** is the sentence. One observation, context resolved inline, produced by ghost.noted from a raw input. ghost.synthd consumes entries through ghost.noted's Redis stream. Entries are atomic, they do not get merged or edited by ghost.synthd. They can be marked superseded when a memory supersedes them in practice, but the underlying row is immutable.

**Memory** is the event. A cluster of related journal entries about the same event, theme, or observation. The Milan dinner, Nick's flying exam, the day Bogdan died, the mi300x announcement. Memories cluster by relatedness (shared entities, shared themes, semantic similarity), not by time. A memory can span entries that arrived weeks apart if they are about the same thing.

**Episode** is the period. A time-windowed aggregation of memories. Yesterday, last week, the Milan trip, 2024, the year Bogdan died. Episodes cluster by time and have a granularity property (day, week, month, year, multi-year, arbitrary window). An episode can also be a named period that does not fit a standard granularity (the Galápagos trip, the move to London).

**Entity** sits sideways. A canonical person, place, organisation, object, or concept, referenced by journal entries, memories, and episodes alike. Built from the raw mentions ghost.noted produces. Merged and maintained through the queue.

The aggregation is different at each level, and that is why four layers is the right count. Journal entries aggregate sentences from raw input. Memories aggregate entries by relatedness. Episodes aggregate memories by time. Entities are not aggregations at all, they are canonical references. Each layer does a different kind of work.

A journal entry can belong to more than one memory, and a memory can belong to more than one episode. The junction tables below support this. An entry about meeting Ionut at the airport in 2022 is in a "Galápagos trip" memory because that is where they were going, and in a "time with Ionut" memory because he is the entity that connects several moments across years. The entry itself is not duplicated, it is referenced by both memories. The same logic applies one level up. The Galápagos trip memory belongs to the "2022" episode and to the "named: Galápagos trip" episode that crosses months. Both references are valid, both are useful, the graph is not a tree.

## Position in the fleet

ghost.synthd subscribes to ghost.noted's event streams. It exposes a query API to the app layer and a fast-path API to ghost.cued. It writes the queue tables that the app layer reads to surface decisions to the user.

```
INGESTION DAEMONS
  ghost.noted   (text)
  ghost.framed  (images, push through noted)
  ghost.voiced  (audio, push through noted)
  |
  |  Redis streams (noted.entry.created, noted.entry.updated, ...)
  |
  v
ghost.synthd
  |
  |  overnight pass, slow clock:
  |    resolve entity mentions, propose merges
  |    cluster entries into memories
  |    roll memories into episodes at multiple granularities
  |    regenerate prose for memories and episodes with changed backing data
  |    surface borderline cases to the queue
  |
  |  query API, fast path:
  |    return ranked memories and episodes for a query or situation
  |    resolve audit-trail traversal
  |
  |  queue tables:
  |    proposals the user needs to confirm or reject
  |
  v
app layer (queries, queue UI, memory browser)
ghost.cued (situational retrieval, v0.2+)
ghost.watchd (health, pass scheduling status)
```

ghost.synthd does not emit its own Redis streams for downstream consumers. The app and ghost.cued pull from ghost.synthd's API and database. No event vocabulary at this layer.

## Responsibilities

**Subscribe to ingestion events.** ghost.synthd is a Redis streams consumer on the `noted.entry.*` and `noted.source.*` channels. The daemon processes events in a consumer group that guarantees at-least-once delivery. Entry-created events land in a staging area until the overnight pass promotes them. Entry-updated events reconcile against any memory that references the entry. Entry-deleted events cascade through memories and episodes that referenced them.

**Generate journal entry embeddings.** Every entry gets a vector embedding stored in pgvector. The embedding is what makes semantic clustering possible. Same model used for all entries in a given database epoch, so embeddings are comparable.

**Generate mention embeddings.** Every mention (verbatim string plus type hint) gets its own embedding. Mention embeddings are what the entity resolution pass uses to cluster mentions into entities.

**Maintain the entity registry.** Entities are first-class rows with their own embeddings. New entities are proposed when a mention does not match any existing entity with sufficient confidence. The proposal goes to the queue. User confirms or rejects. On confirmation, the entity is created and the mention is attached.

**Propose entity merges.** When an existing entity has two candidate canonical labels (mentions of "my friend" start matching the same entity as mentions of "Ionut"), the overnight pass proposes a merge. The proposal goes to the queue. User confirms or rejects. On confirmation, the entities collapse, all references update, the embedding re-centres.

**Cluster journal entries into memories.** The overnight pass walks recent entries, finds clusters by semantic similarity and shared entity references, produces memories from confident clusters. Borderline clusters go to the queue for user confirmation. Entries not confidently clustered sit as entries without a memory until later passes find a cluster to put them in.

**Roll memories into episodes.** The overnight pass walks memories by time window and produces episode rows at configurable granularities. Day, week, month, year, and arbitrary named periods the user has defined. Each episode's prose is generated once and cached.

**Generate prose for memories and episodes.** A local LLM is called to produce the prose. For memories, the prose is a short narrative summarising what the memory is about. For episodes, the prose is a longer narrative summarising the time window. Prose is generated when the memory or episode is first produced, and regenerated when the underlying data has changed in a way that matters.

**Maintain the queue.** Every decision the daemon cannot make confidently surfaces here. Entity merge proposals. Entity creation proposals. Cluster confirmation for borderline memory candidates. Anchor promotion (elevating a memory to load-bearing status). Deletion cascades that need user approval. The queue is the single user-facing surface for every structural change to the memory graph.

**Serve query API to the app layer.** Queries arrive as structured requests. "Show me memories from last month." "Find memories mentioning Cristina and Milan." "Give me the episode for 2024." "Walk the audit trail from this episode down to journal entries." The API returns structured results with prose and references.

**Serve fast-path API to ghost.cued.** Situational retrieval. ghost.cued passes a situation description, ghost.synthd returns a shortlist of candidate memories or episodes ranked by semantic similarity to the situation plus recency, anchor status, and entity overlap. ghost.cued applies its own ranking on top. The fast path is separate from the app query API because latency budgets are different.

**Score and update decay.** Decay is not a per-row score that drifts. Decay is aggregation. An old memory still exists and is queryable, but it does not surface in default retrieval when the episode containing it is a more appropriate result. The overnight pass is what enforces this by regenerating episodes, which shifts the balance of what appears in default queries over time.

**Regenerate prose on change.** When backing data changes for a memory or an episode, the prose regenerates on the next pass. New entries land in a memory, the memory's prose updates. User confirms a merge that changes an entity label, memories and episodes that reference the entity regenerate their prose. Retention of old prose is not in the main schema, the previous version is appended to an audit log with the timestamp and the reason for regeneration.

**Respect user edits.** When the user edits a memory's prose, a memory's membership (adding or removing journal entries), or an episode's prose, the edit is sticky. Subsequent regeneration passes see a `user_edited` marker on the row and skip prose regeneration for that row. The user can explicitly unset the marker if they want the system to take over again.

## Non-responsibilities

**No ingestion.** ghost.synthd does not read raw source files or call extraction models. It consumes journal entries through ghost.noted's events.

**No query-interface UI.** The app layer owns the query interface. ghost.synthd exposes an API, the app calls it.

**No real-time situational retrieval.** ghost.cued does that, by reading from ghost.synthd's output.

**No speech or text generation beyond prose.** The local LLM is called for generating memory and episode prose. Every other inference need in the fleet belongs to a different daemon.

**No entity persistence outside its own schema.** Ghost.synthd owns the entity table and the entity embeddings. No other daemon stores entity identity, they reference by entity ID from the synthd schema.

**No user-facing file or audio playback.** The app layer serves raw input bytes from ghost.noted's, ghost.framed's, or ghost.voiced's archives when the user wants to see the original source. ghost.synthd only stores references.

**No hard-delete of past prose.** User edits stick, regeneration respects them, old prose goes to an audit log rather than being thrown away. The audit log is a discard area, not versioned history in the main schema.

## Data model

Eleven tables in Postgres 15+ with pgvector. The schema is a sketch and will evolve.

```sql
-- Vector dimension depends on the embedding model chosen at deployment.
-- Placeholder of 768 here, real value is a config decision.

-- One row per journal entry, consumed from ghost.noted events.
CREATE TABLE journal_entries (
  id                   UUID PRIMARY KEY,           -- Same ID as in ghost.noted
  noted_source_id      UUID NOT NULL,              -- Reference to ghost.noted.sources
  content              TEXT NOT NULL,
  embedding            VECTOR(768),
  source_created_at    TIMESTAMPTZ,
  happened_at          TIMESTAMPTZ,
  happened_at_accuracy TEXT,
  seen_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
  superseded_at        TIMESTAMPTZ,                -- Marked when memory ref becomes the primary view
  deleted_at           TIMESTAMPTZ,
  metadata             JSONB NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_entries_happened_at ON journal_entries(happened_at) WHERE deleted_at IS NULL;
CREATE INDEX idx_entries_seen_at ON journal_entries(seen_at) WHERE deleted_at IS NULL;
CREATE INDEX idx_entries_embedding ON journal_entries USING ivfflat (embedding vector_cosine_ops);

-- One row per mention, consumed from ghost.noted events.
CREATE TABLE mentions (
  id            UUID PRIMARY KEY,                  -- Same ID as in ghost.noted
  entry_id      UUID NOT NULL REFERENCES journal_entries(id) ON DELETE CASCADE,
  entity_id     UUID REFERENCES entities(id),      -- NULL until resolved
  mention_text  TEXT NOT NULL,
  mention_type  TEXT,
  embedding     VECTOR(768),
  span_start    INTEGER,
  span_end      INTEGER,
  confidence    NUMERIC(3, 2),                     -- Resolution confidence, 0 to 1
  deleted_at    TIMESTAMPTZ,
  resolved_at   TIMESTAMPTZ,                       -- When the resolution happened
  metadata      JSONB NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_mentions_entry_id ON mentions(entry_id);
CREATE INDEX idx_mentions_entity_id ON mentions(entity_id) WHERE entity_id IS NOT NULL;
CREATE INDEX idx_mentions_unresolved ON mentions(seen_at) WHERE entity_id IS NULL AND deleted_at IS NULL;
CREATE INDEX idx_mentions_embedding ON mentions USING ivfflat (embedding vector_cosine_ops);

-- One row per canonical entity.
CREATE TABLE entities (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  canonical_label   TEXT NOT NULL,                 -- Current best name
  entity_type       TEXT,                          -- Free-form, inferred from mentions
  embedding         VECTOR(768),                   -- Centroid, updated as mentions resolve
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  mention_count     INTEGER NOT NULL DEFAULT 0,    -- Denormalised for fast lookup
  first_mentioned   TIMESTAMPTZ,
  last_mentioned    TIMESTAMPTZ,
  user_edited       BOOLEAN NOT NULL DEFAULT FALSE,
  deleted_at        TIMESTAMPTZ,
  metadata          JSONB NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_entities_canonical_label ON entities(canonical_label) WHERE deleted_at IS NULL;
CREATE INDEX idx_entities_type ON entities(entity_type) WHERE deleted_at IS NULL;
CREATE INDEX idx_entities_embedding ON entities USING ivfflat (embedding vector_cosine_ops);

-- Alternative labels an entity has been referred to by.
CREATE TABLE entity_aliases (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id   UUID NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  alias_text  TEXT NOT NULL,
  first_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
  usage_count INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX idx_entity_aliases_entity_id ON entity_aliases(entity_id);
CREATE INDEX idx_entity_aliases_text ON entity_aliases(alias_text);

-- One row per memory, the event-level cluster.
CREATE TABLE memories (
  id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  prose              TEXT NOT NULL,                -- Current prose, generated or user-edited
  embedding          VECTOR(768),                  -- Embedding of the prose
  confidence         NUMERIC(3, 2),                -- How confident the clustering was
  is_anchor          BOOLEAN NOT NULL DEFAULT FALSE,
  user_edited        BOOLEAN NOT NULL DEFAULT FALSE,
  happened_at_start  TIMESTAMPTZ,                  -- Earliest happened_at of member entries
  happened_at_end    TIMESTAMPTZ,                  -- Latest happened_at of member entries
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_regenerated   TIMESTAMPTZ,                  -- When prose was last regenerated
  deleted_at         TIMESTAMPTZ,
  metadata           JSONB NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_memories_happened_at_start ON memories(happened_at_start) WHERE deleted_at IS NULL;
CREATE INDEX idx_memories_anchor ON memories(is_anchor) WHERE deleted_at IS NULL AND is_anchor = TRUE;
CREATE INDEX idx_memories_embedding ON memories USING ivfflat (embedding vector_cosine_ops);

-- Membership of journal entries in memories.
CREATE TABLE memory_entries (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  memory_id   UUID NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
  entry_id    UUID NOT NULL REFERENCES journal_entries(id) ON DELETE CASCADE,
  role        TEXT,                                -- 'primary', 'supporting', free-form
  added_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  added_by    TEXT NOT NULL DEFAULT 'system'       -- 'system' or 'user'
);

CREATE UNIQUE INDEX idx_memory_entries_memory_entry ON memory_entries(memory_id, entry_id);
CREATE INDEX idx_memory_entries_entry ON memory_entries(entry_id);

-- Entities referenced by a memory.
CREATE TABLE memory_entities (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  memory_id    UUID NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
  entity_id    UUID NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  mention_count INTEGER NOT NULL DEFAULT 1,
  relevance    NUMERIC(3, 2)                        -- How central this entity is to the memory
);

CREATE UNIQUE INDEX idx_memory_entities_memory_entity ON memory_entities(memory_id, entity_id);
CREATE INDEX idx_memory_entities_entity ON memory_entities(entity_id);

-- One row per episode, the time-windowed aggregation.
CREATE TABLE episodes (
  id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  window_start       TIMESTAMPTZ NOT NULL,
  window_end         TIMESTAMPTZ NOT NULL,
  granularity        TEXT NOT NULL,                -- 'day', 'week', 'month', 'year', 'named'
  name               TEXT,                         -- Optional name for 'named' episodes (e.g. 'Galápagos trip')
  prose              TEXT NOT NULL,
  embedding          VECTOR(768),
  memory_count       INTEGER NOT NULL DEFAULT 0,
  entry_count        INTEGER NOT NULL DEFAULT 0,
  user_edited        BOOLEAN NOT NULL DEFAULT FALSE,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_regenerated   TIMESTAMPTZ,
  deleted_at         TIMESTAMPTZ,
  metadata           JSONB NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_episodes_window_start ON episodes(window_start) WHERE deleted_at IS NULL;
CREATE INDEX idx_episodes_granularity ON episodes(granularity) WHERE deleted_at IS NULL;
CREATE INDEX idx_episodes_embedding ON episodes USING ivfflat (embedding vector_cosine_ops);

-- Membership of memories in episodes.
CREATE TABLE episode_memories (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  episode_id  UUID NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
  memory_id   UUID NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
  relevance   NUMERIC(3, 2)
);

CREATE UNIQUE INDEX idx_episode_memories_episode_memory ON episode_memories(episode_id, memory_id);
CREATE INDEX idx_episode_memories_memory ON episode_memories(memory_id);

-- The queue. Every decision the system cannot make alone lands here.
CREATE TABLE queue_items (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  item_type       TEXT NOT NULL,  -- 'entity_merge', 'entity_create', 'cluster_confirm',
                                  -- 'anchor_promote', 'deletion_cascade', 'prose_review'
  payload         JSONB NOT NULL, -- Type-specific data
  proposed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  resolved_at     TIMESTAMPTZ,
  resolution      TEXT,           -- 'confirmed', 'rejected', 'deferred'
  resolved_by     TEXT,           -- 'user', 'system_auto_accept', 'system_timeout'
  priority        INTEGER NOT NULL DEFAULT 5,
  metadata        JSONB NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_queue_open ON queue_items(priority DESC, proposed_at)
  WHERE resolved_at IS NULL;
CREATE INDEX idx_queue_by_type ON queue_items(item_type) WHERE resolved_at IS NULL;

-- Discarded prose, append-only audit log.
CREATE TABLE prose_history (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subject_type    TEXT NOT NULL,  -- 'memory', 'episode'
  subject_id      UUID NOT NULL,
  prose           TEXT NOT NULL,
  replaced_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  reason          TEXT NOT NULL   -- 'regenerated_after_update', 'user_edit', 'manual_trigger'
);

CREATE INDEX idx_prose_history_subject ON prose_history(subject_type, subject_id, replaced_at);

-- State of the overnight pass.
CREATE TABLE pass_runs (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at    TIMESTAMPTZ,
  status          TEXT NOT NULL,  -- 'running', 'completed', 'failed', 'interrupted'
  entries_processed INTEGER NOT NULL DEFAULT 0,
  memories_created  INTEGER NOT NULL DEFAULT 0,
  memories_updated  INTEGER NOT NULL DEFAULT 0,
  episodes_updated  INTEGER NOT NULL DEFAULT 0,
  queue_items_created INTEGER NOT NULL DEFAULT 0,
  last_error        TEXT,
  metadata          JSONB NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_pass_runs_started ON pass_runs(started_at);
```

**Why entries keep the same ID as in ghost.noted.** Cross-daemon traceability. A user asking "why does this memory exist" walks from the memory to its journal entries, each of which references the same ID that exists in ghost.noted's database. No translation, no lookup, the same UUID flows from ingestion to archival to synthesis.

**Why mentions have their own embedding.** Mention resolution needs to cluster similar mentions together. Mention embedding captures the verbatim string plus the type hint, giving the resolution pass a vector it can compare against entity centroids.

**Why entities have their own embedding.** ghost.cued and the app layer need to rank memories by similarity to a query, and entity overlap is a strong ranking signal. Entity embedding as a first-class vector (rather than a centroid computed on the fly) makes query-time ranking fast. The centroid is updated when new mentions resolve.

**Why memories and episodes both have prose, embeddings, and a user_edited flag.** The three properties together support the audit-trail, the ranking, and the user-override requirements. Prose is what the user sees. Embedding is what retrieval uses for similarity. User_edited is what the regeneration pass checks before overwriting.

**Why memory_entries and episode_memories are junction tables.** Memories can contain many entries, entries can belong to many memories (a journal entry about meeting Ionut at the airport might be part of a "Galápagos trip" memory and a "time with Ionut" memory). Same for episodes. Junction tables handle the many-to-many cleanly.

**Why a single memories table rather than separate tables for confident and borderline clusters.** The borderline-cluster case lives in the queue as a proposal, not as a row in the memories table. Once a memory exists in the memories table, it has been confirmed (by the system at a high confidence threshold or by the user through the queue).

**Why prose_history is a discard log, not a version history.** You explicitly said you do not want versioning in the main tables. The main table holds one version. Every regeneration appends the old prose to the discard log with a timestamp and the reason. The user can see how prose has changed over time through this log, but the main table never carries the weight of multiple versions.

**Why pass_runs exists.** The overnight pass is a background process with real consequences. Knowing when it last ran, how long it took, how much work it did, and whether it failed is operationally important. ghost.watchd reads this table to surface pass health to the user.

## The overnight pass

The pass is the engine. It runs on a slow clock, typically overnight, but can be manually triggered through the admin endpoint. A full pass has eight stages, each producing results the next stage consumes.

**Stage 1, ingest events.** Pull from ghost.noted's Redis streams since the last pass. Write new journal entries and mentions into ghost.synthd's tables. Generate embeddings for entries and mentions. Mark entry-updated and entry-deleted events for later processing.

**Stage 2, resolve mentions.** For each unresolved mention, query the entities table for candidates whose embedding is similar to the mention's embedding and whose alias list overlaps. If a confident match exists, attach the mention to that entity. If no confident match exists, either create a candidate new entity and add a queue item asking the user to confirm, or defer the mention for the next pass if the system thinks a future mention might clarify it.

**Stage 3, propose entity merges.** Walk recently-updated entities. For each, find other entities whose embedding is now close enough to suggest they are the same. For each candidate pair, add a queue item proposing the merge. Do not auto-merge.

**Stage 4, cluster entries into memories.** Walk journal entries not yet assigned to a memory. Find clusters by embedding similarity and shared entity references. For high-confidence clusters, create memory rows and attach the entries. For borderline clusters, add queue items asking the user to confirm.

**Stage 5, assign entries to existing memories.** Walk new journal entries against existing memories. If a new entry is semantically close to a memory and shares its entities, propose attaching it. High-confidence attachments are automatic, borderline ones go to the queue.

**Stage 6, roll memories into episodes.** For each configured granularity (day, week, month, year), find memories whose time windows fall within an episode window that has changed since the last pass. Regenerate affected episodes. Named episodes (Galápagos trip, move to London) are regenerated when any of their member memories changes.

**Stage 7, regenerate prose.** For memories and episodes whose backing data has changed since the last regeneration, and whose `user_edited` flag is FALSE, call the local LLM to regenerate prose. Append the old prose to prose_history with reason 'regenerated_after_update'. Update last_regenerated.

**Stage 8, close out.** Write the pass_runs row. Emit a synthd.pass.completed event for ghost.watchd to pick up. Log counters for each stage.

The pass is resumable. If it is interrupted (the daemon is shut down mid-pass), the next run picks up from the last completed stage based on the pass_runs table. Each stage writes its own durability markers so a resume does not redo completed work.

## The queue

Every structural decision the system cannot make alone surfaces here. POST_09 described the queue as "a small moment of reflection on a day that is already over." That is the shape. The user opens the queue when they want to engage with it, it does not push notifications.

Queue item types:

**entity_create**: "I saw a new mention 'my neighbour Sarah'. Is this a new person or an alias of someone I already know?"

**entity_merge**: "The entity 'my friend' and the entity 'Ionut' look like the same person. Should I merge them?"

**cluster_confirm**: "I see these four journal entries about a dinner in Milan. Do they belong to the same memory?"

**anchor_promote**: "This memory has been referenced many times over the last year. Is it a core memory I should protect from aggregation?"

**deletion_cascade**: "You deleted the source 'journal-2024-03.md'. It had 47 entries that fed 12 memories. Should I delete those memories, or keep them?"

**prose_review**: "This memory's prose was regenerated because new entries landed. Here is the new version. Accept or revert?"

Queue items have a priority. The system sets a default priority based on item type (merges high, deletions high, prose reviews low). User interaction bumps priority of related items. Items not resolved within a configurable window stay in the queue but drop in priority.

The queue never auto-resolves items for the user. The only auto-resolution is system_timeout for items the user has explicitly marked "stop asking about this type of thing," which marks future instances as auto-dismissed without resolution. The user can always see dismissed items in a separate view.

## The queue as a user surface

The queue is not a notification system and not a task list. Both mental models are wrong. A notification system pushes at the user when a message arrives. A task list shows everything outstanding with equal urgency. Neither is what the memory layer needs.

The queue is a reflection surface. The user opens it when they have a moment. The items wait. When the user engages, they work through a few items at a time, then close it and do something else. Hours or days later, they open it again. The queue grows and shrinks on its own rhythm. POST_09 described this pattern through the Samsung Health daily mood prompt and the Google Photos "on this day" summary, both of which wait for the user rather than demanding attention. The queue follows that shape.

**How items are presented.** One item at a time, with a clear primary question and a small number of actions (confirm, reject, defer, stop asking). Not a dashboard of twelve outstanding decisions. The UI presents the highest-priority unresolved item, the user resolves it or defers it, the next item appears. The user can stop at any time.

**What information each item includes.** Enough context that the user can make the decision without having to dig. For an entity merge, the two entity labels, the memories each one appears in, a few example journal entries from each. For a cluster confirmation, the four candidate entries and a proposed memory title. For an anchor promotion, the memory prose and the system's reason for proposing. The right amount is enough for the user to decide in a few seconds without feeling rushed. Too much context becomes homework, too little forces the user to go hunting.

**How often the app surfaces the queue.** POST_09 committed to the queue being pull, not push. The app shows a small indicator (a badge, a count) when the queue has items. The user decides when to engage. The system does not notify, does not send pushes, does not escalate. A queue that is never engaged with grows until the user notices, which is the correct behaviour for a memory layer, not a bug.

**How items interact with each other.** When the user confirms an entity merge, related queue items update. Cluster confirmations that involved the old entity now reflect the merged one. Prose-review items that reference the merged entity may be invalidated. The queue is internally consistent at any moment, so the user does not resolve an item that is about to become irrelevant.

**What happens if the user ignores the queue for weeks.** Items age. Low-priority items like prose reviews drop off the surface (they stay in the queue but do not appear by default). High-priority items like merge proposals stay visible. The backlog is visible as a count but does not nag. If the user returns after a long absence, the queue is waiting, not screaming.

**What the queue does not do.** It does not present information the user could have surfaced themselves by querying. Those go through the app's query API. The queue is only for decisions the system actively needs from the user. A queue full of things the system wants to tell the user is a feed, and feeds are the failure mode LocalGhost exists to avoid.

## Query APIs

One API hierarchy under `/api/v1/synthd/`. The daemon name sits as the top resource under the versioned API, consistent with the rest of the LocalGhost fleet where every daemon exposes its own `/api/v1/<daemon>/...` surface. Resources hang off the daemon name. Memory, episode, entity, entry, situation, queue, search. Each has its own endpoints for lookup, filter, and the action it supports. Search is a resource like the others, its one endpoint orchestrates across the rest.

**The resource endpoints.**

```
# Memories
GET  /api/v1/synthd/memory/:uuid                     - one memory with prose and audit-trail pointers
GET  /api/v1/synthd/memory/:uuid/entries             - journal entries backing a memory
GET  /api/v1/synthd/memory/:uuid/similar             - memories semantically similar to this one
GET  /api/v1/synthd/memory/query?q=...               - natural-language search over memories only
GET  /api/v1/synthd/memory/window?start=...&end=...  - memories in a time window
GET  /api/v1/synthd/memory/entity/:entity-uuid       - memories referencing a specific entity

# Episodes
GET  /api/v1/synthd/episode/:uuid                    - one episode with prose
GET  /api/v1/synthd/episode/:uuid/memories           - memories inside an episode
GET  /api/v1/synthd/episode/query?q=...              - natural-language search over episodes only
GET  /api/v1/synthd/episode/window?start=...&end=... - episodes in a time window
GET  /api/v1/synthd/episode/granularity/:gran        - episodes at a given granularity
GET  /api/v1/synthd/episode/named                    - named episodes (Galápagos trip, etc)

# Entities
GET  /api/v1/synthd/entity/:uuid                     - one entity with metadata and mention count
GET  /api/v1/synthd/entity/:uuid/memories            - memories referencing the entity
GET  /api/v1/synthd/entity/:uuid/episodes            - episodes referencing the entity
GET  /api/v1/synthd/entity/query?q=...               - entity search by name or semantic
GET  /api/v1/synthd/entity/type/:type                - entities of a given type

# Journal entries (lookup only, entries are not the user-facing object)
GET  /api/v1/synthd/entry/:uuid                      - one journal entry with source pointer
GET  /api/v1/synthd/entry/:uuid/memories             - memories this entry belongs to

# Situation ranking for ghost.cued
POST /api/v1/synthd/situation/rank                   - rank memories and episodes against a situation
POST /api/v1/synthd/situation/rank/memory            - rank memories only
POST /api/v1/synthd/situation/rank/episode           - rank episodes only

# Queue
GET  /api/v1/synthd/queue                            - current queue items, ordered by priority
GET  /api/v1/synthd/queue/:uuid                      - one queue item with full context
POST /api/v1/synthd/queue/:uuid/resolve              - confirm, reject, or defer an item
GET  /api/v1/synthd/queue/dismissed                  - items the user has marked 'stop asking'

# Search
GET  /api/v1/synthd/search/query?q=...               - top-level search, returns mixed results
```

Responses are JSON. Every resource response includes its own UUID, the prose (for memories and episodes), structured metadata, and audit-trail pointers (for memories and episodes, a list of the UUIDs one level down). The app walks the pointers when the user asks for more detail.

The situation ranking endpoint deserves its own note. The request body carries time, location, calendar context, activity hint, and any other situational signals ghost.cued has. ghost.synthd applies a heuristic ranker (semantic similarity to the situation, entity overlap with calendar context, recency, anchor boost) and returns the shortlist. Latency budget here is tighter than the other resource endpoints, single-digit milliseconds, because ghost.cued calls this continuously while the user is moving through their day.

**Search as a resource.** The search endpoint is called when the app does not know what kind of thing the user is looking for. Takes a free-text query, figures out which resource-specific query endpoints to call, aggregates and ranks the results, returns a single response with mixed resource types. The endpoint is orchestration, not a separate retrieval engine. It embeds the query once, calls `memory/query`, `episode/query`, and `entity/query` in parallel, merges the results, applies cross-resource ranking (a memory scored against an entity against an episode is not trivial, but a single-pass rank on similarity plus recency plus anchor status gets close enough), returns a flat ranked list with each item tagged by type.

Response shape for the search endpoint:

```json
{
  "query": "Milan 2012",
  "results": [
    {"type": "memory", "uuid": "...", "score": 0.91, "preview": "..."},
    {"type": "episode", "uuid": "...", "score": 0.84, "preview": "..."},
    {"type": "entity", "uuid": "...", "score": 0.77, "preview": "..."},
    {"type": "memory", "uuid": "...", "score": 0.72, "preview": "..."}
  ],
  "truncated": false
}
```

The app uses the previews for initial rendering. When the user clicks a result, the app hits the resource-specific endpoint (`/api/v1/synthd/memory/:uuid`, etc) to get the full object. This keeps the search response small and the full-detail load lazy.

**Why the per-daemon prefix.** Every LocalGhost daemon exposes its own `/api/v1/<daemon>/...` surface. ghost.noted has `/api/v1/noted/inbox`, ghost.framed has `/api/v1/framed/push`, ghost.synthd has `/api/v1/synthd/memory/...`. The daemon name in the path is routing clarity. An app layer call, a log line, a network trace, all show which daemon they touched. Versioning happens per-daemon too, ghost.synthd can move to v2 without affecting other daemons' surfaces.

**Why search sits as a resource rather than its own hierarchy.** A separate top-level like `/api/search/v1/` was considered and rejected. Search is one more thing the daemon knows how to do, and treating it as a resource alongside memory, episode, entity keeps the URL scheme consistent. Future orchestration-style resources (timeline, recommendations, whatever) sit alongside search at the same level without a second top-level prefix. The search endpoint is implemented as a thin layer that calls the resource-specific query endpoints, shares no retrieval code with them, and can be replaced entirely if the orchestration approach changes.

**Admin endpoints under `/api/v1/synthd/admin/`.** Operational endpoints sit alongside the resource endpoints under the same per-daemon path. Admin is a resource the daemon exposes like any other.

```
GET  /api/v1/synthd/admin/healthz                    - liveness
GET  /api/v1/synthd/admin/readyz                     - readiness, Postgres and embedding runtime
GET  /api/v1/synthd/admin/passes                     - pass_runs history, last N runs
POST /api/v1/synthd/admin/passes/trigger             - manually trigger an overnight pass (authenticated)
GET  /api/v1/synthd/admin/config                     - current daemon config (authenticated)
POST /api/v1/synthd/admin/config                     - update config (authenticated, triggers reload)
```

**Database access.** Postgres read access to the tables in this schema is exposed to the app layer and ghost.watchd through per-daemon roles. ghost.synthd is the only daemon with write access. The app never writes to ghost.synthd's tables directly, all writes go through the API.

## Where RAG lives and where it does not

The ghost.cued post argued that retrieval is not enough for personal memory, and specifically that the deep-thinking RAG pattern is the wrong centre of gravity for a memory layer. That claim is easy to misread. It does not mean LocalGhost avoids RAG. It means RAG is one tool among several inside ghost.synthd, used for the paths where it is the right answer, and deliberately not used for the paths where it is not.

There are four retrieval paths ghost.synthd supports, and they divide into two camps.

**Paths that use RAG.** Path one is the user typing a natural-language query through the app. The app calls `GET /api/v1/synthd/search/query` when the user types into a generic search box, or `GET /api/v1/synthd/memory/query`, `GET /api/v1/synthd/episode/query`, `GET /api/v1/synthd/entity/query` when the user is searching within a specific view. ghost.synthd embeds the query, runs vector search against the relevant index, reranks by recency, entity overlap, and anchor status, and returns ranked results. This is RAG. It is classic RAG, running inside synthd, because a natural-language query over a personal memory index is exactly what RAG was built for.

Path three is ghost.cued's situational retrieval through `POST /api/v1/synthd/situation/rank`. ghost.cued passes a structured situation description (location, calendar context, activity hint). ghost.synthd builds a query vector from the situation, runs vector search, combines with entity overlap against calendar attendees, returns a ranked shortlist. This is RAG-adjacent because the input is not a natural-language string but a structured situation. The shape of the retrieval is the same, the shape of the input is not.

**Paths that do not use RAG.** Path two is structured browsing. The user asks the app to show memories from March 2024, or memories referencing Cristina, or memories inside the Galápagos trip episode. The app calls `GET /api/v1/synthd/memory/window` with filters, or `GET /api/v1/synthd/entity/:uuid/memories`, or `GET /api/v1/synthd/episode/:uuid/memories`. These are relational queries over time columns and junction tables. Vector search has no role. Forcing RAG here would be slower, less accurate, and less auditable than plain SQL.

Path four is audit-trail traversal. The user asks "where did this come from" on a memory or an episode. The app walks the junction tables (episode to memories, memory to entries, entry to noted source, noted source to raw archive) through direct foreign-key joins. There is no query, no ranking, no relevance. The user is asking for provenance, and provenance is a graph walk.

**What this means for the daemon's architecture.** The vector index on memories, episodes, and entities is one of several indexes ghost.synthd maintains. It is not the primary access pattern, it is one access pattern among several. The schema is built for relational queries first (time indexes, junction table indexes, entity ID indexes) and vector queries second (ivfflat indexes on embeddings). A query hitting the relational indexes returns in milliseconds with deterministic results. A query hitting the vector indexes returns in milliseconds with ranked results.

**What RAG does not do in synthd.** RAG does not decide whether to surface a memory at a particular moment. That is ghost.cued, which takes synthd's ranked shortlist as one input and applies its own situational ranking on top. RAG does not produce the memory prose. The prose generation pass is separate, runs overnight, and uses the full memory data rather than a query-time retrieval. RAG does not handle deletion cascades, queue proposals, or the four-layer aggregation pipeline. All of those are relational-graph work.

**The deliberate trade-off.** A system built entirely around RAG would be simpler to describe but worse in practice. Personal memory is mostly not a query-shaped problem. Most of what the user wants to see is "my memories from yesterday," "my memories of that trip," "everything I have written about Cristina this year," and those are relational queries. The vector path exists for the cases where the user does not know what to ask for directly, and for ghost.cued's situational retrieval where structured input produces a semantic match. Keeping vector search as one tool rather than the organising principle is what lets the daemon be fast at the common paths.

## The bidirectional source link

Every journal entry carries the noted_source_id it came from. When the user asks "where did this memory come from," ghost.synthd walks the memory, its entries, the entries' noted_source_ids, and hands off to ghost.noted's API to resolve the source back to a raw input (text, image via framed, audio via voiced).

The audit trail is end-to-end. Memory to entries to ghost.noted sources to ghost.framed or ghost.voiced source (where applicable) to the raw archive file. Every step stores the reference to the step below. No layer throws away its backing evidence.

## v0.1 scope (wisp)

Ghost.synthd is in v0.1. Without it, there is no memory layer. But v0.1 ships a reduced version that works with ghost.noted only, since ghost.framed and ghost.voiced are v0.2.

**What definitely ships in v0.1.**

Entity resolution over text-derived mentions. Memory clustering over text-derived journal entries. Day-level and week-level episodes. Year-level episodes. Named episodes on user request. Entity merges and cluster confirmations through the queue. Prose generation for memories and episodes. Audit-trail traversal from memory down to noted source. App query API. Regeneration pass.

**What probably does not ship in v0.1.**

Ghost.cued fast-path API. Not because synthd cannot expose it, but because ghost.cued itself is v0.2.

Anchor promotion through the queue. The mechanism is in the schema. The queue type is defined. Whether the overnight pass actively proposes anchors or waits for the user to request them is a v0.2 decision.

Named episodes auto-detected. Day, week, month, and year episodes are produced by the overnight pass automatically. Named episodes like "Galápagos trip" probably require the user to define them explicitly in v0.1. Auto-detecting named episodes from journal data is a v0.2 refinement.

Multi-year episodes. Technically possible in v0.1 but not a priority. A user might have one year of data when v0.1 ships. Multi-year aggregation is more meaningful at v0.2 and beyond.

**What definitely does not ship in v0.1.**

Integration with ghost.framed or ghost.voiced derived entries. Those daemons are v0.2.

Cross-language entity resolution. The entity resolution pass assumes English entity labels because ghost.noted translates at extraction.

## v0.2+ roadmap

**v0.2 (shade).** Full integration with ghost.framed and ghost.voiced derived entries. Ghost.cued fast-path API. Anchor promotion in the queue. Auto-detection of named episodes. Retention configuration for old journal entries (archive-vs-surface policy).

**v0.3.** Cross-episode patterns (things the user does across years). Memory-to-memory similarity graph for "related memory" retrieval. Better handling of edit-reconciliation when a user edits a journal entry or a memory.

**v1.0.** Episode-level prose generation with the user's voice (captured from their own writing style). Multi-year rollup for decade-scale views. Ghost.shadowd integration for adversarial scoring.

**v1.0+.** Memory-graph visualisation endpoints (the app layer can draw a graph of entities and memories). Semantic search over entities across their memory participation.

**Never.** Cloud inference for prose generation. Local only, consistent with the manifesto. The quality trade-off is real and the user accepts it.

## Open questions

**Confidence thresholds for auto-clustering.** Memory cluster confidence, entity merge confidence, entity creation confidence all need specific thresholds. v0.1 picks reasonable starting values and tunes from evidence. The right thresholds depend on the user's data and will probably differ per user.

**Embedding model selection.** Same question as ghost.noted's extraction model, and the answer is the same. Use whatever runs acceptably on the box's hardware. The specific default is a v0.1 deployment decision.

**Prose generation quality bar.** A local LLM generating prose for every memory and episode produces variable quality. Some generated prose will be bad. Some will be better than what the user would write. Some will be confidently wrong. The queue prose_review item type exists to let the user catch the bad cases, but v0.1 cannot guarantee consistent quality. The user has to be prepared to edit or ignore some of the generated prose.

**Decay and episode rollup timing.** The overnight pass rolls memories into episodes at multiple granularities. But when should an entry at a daily granularity stop appearing in default retrieval and surface only through the day episode? Same question at other granularities. Current thinking is that entries always remain queryable but stop appearing in default views after a configurable window. The window is a user preference that defaults to seven days.

**Reconciliation when a user edits a journal entry.** Ghost.noted handles edits through re-extraction and cascade. When ghost.synthd sees an entry-updated event, it has to decide whether to keep the entry in the memory it currently belongs to, split it into a new memory, or leave it as it is. The conservative approach is leave it as it is unless the edit is large enough to change the clustering. The threshold for "large enough" is not specified yet.

**What happens to orphan memories.** If all the journal entries in a memory get deleted (cascading from a source deletion), the memory has no backing evidence. Current plan is to mark the memory deleted and add it to the deletion_cascade queue for the user to confirm. Alternative is to keep the memory with its prose but remove the entry references, which preserves the memory as a user artifact even when the backing evidence is gone. The first is simpler. The second is more in line with the "user's personal history" framing. Undecided.

**Prose quality when the underlying data is thin.** A memory with two journal entries both saying "had dinner with Cristina" does not give the prose generator much to work with. The generated prose will be thin. Whether to always generate prose or to defer generation until enough underlying data exists is a v0.1 decision. Current lean is always generate, because a thin prose is still more useful than no prose, and the user can edit it.

**How to handle system-generated prose drift.** Regeneration after a data change will produce slightly different prose even for the same underlying entries. The user might have gotten used to a specific wording. Whether to regenerate aggressively (always produce the latest) or conservatively (only regenerate when the data change is substantial) is a tuning decision.

## Rejected approaches

**Memories as materialised views of the entries table.** Simpler in theory. Rejected because the clustering is expensive and the query patterns need fast access to memories without recomputation. A real table with real indexes is the right answer.

**Episodes as materialised views of the memories table.** Same reasoning. Episodes have their own prose, their own embedding, their own metadata. Treating them as views loses the ability to cache and edit at the episode level.

**One table for memories and episodes with a "layer" column.** The financial-data parallel would suggest this (all OHLC in one table with a granularity column). Rejected because memories cluster by theme and episodes cluster by time. They are different kinds of aggregation, and forcing them into one table obscures that. The junction tables (memory_entries, episode_memories) also make it clear that episodes reference memories, not the other way around.

**Prose versioning in the main tables.** Keep every historical version of prose in a history table attached to the memory row. Rejected because you said no versioning in the main schema. Discard log handles audit, main tables hold one version.

**Auto-merging entities at high confidence.** The daemon could auto-merge entities whose embeddings are extremely close. Rejected because merges are consequential and reversibility is hard. High-confidence merges still go through the queue. The user's time is the cost, and the cost is worth paying for correctness.

**Auto-promoting memories to anchors based on usage.** The daemon could observe which memories the user references often and promote them to anchors automatically. Rejected for v0.1 because the signal is noisy and the anchor concept is user-owned (the user decides what is load-bearing, not the system's usage metrics).

**No queue, all decisions automatic.** The daemon could try to make every decision on its own and present results. Rejected because POST_09 committed to the queue as the central pattern, and personal memory is the kind of domain where user-in-the-loop is not a bug but a feature.

**Decay as a drifting score on the memory row.** Alternative to the aggregation-as-decay model. Rejected because a drifting score means retrieval behaviour changes silently over time and the user has no visible reason for why a memory stopped surfacing. Aggregation-as-decay is explicit, the episode exists, the user can see why the memory is no longer in the default view.

**Vector search on journal entries only, memories built at query time.** Cheap storage, expensive queries. Rejected because query latency matters for the app and for ghost.cued, and because memories are user-facing objects that deserve their own identity and edit history.

## Implementation notes

Written in Go. Redis streams consumer with consumer groups for the noted event subscription. pgvector for all vector operations. Local LLM runtime for prose generation, shared with the rest of the fleet.

The overnight pass runs as a scheduled job. Cron or systemd timer, triggered once per day by default, configurable. Manual trigger through the admin endpoint for testing.

The pass is idempotent at the stage level. Each stage can be rerun without corrupting state. If stage 4 succeeds but stage 5 fails, the next run skips to stage 5 based on the pass_runs progress markers.

Prose generation is rate-limited. A single pass that regenerates hundreds of memories would stall on inference. The pass processes regeneration in batches of 10-20 memories, commits results, yields to other work, resumes. Total pass duration for a steady-state user is typically minutes, not hours.

Embedding generation is similarly batched. Entry embeddings generated at ingestion time (stage 1) batch across all new entries. Mention embeddings batch the same way. Entity centroid updates are computed incrementally rather than from scratch.

Database access is through a connection pool. Read-heavy queries (the app API) use a read-replica-style pattern if the deployment grows enough to need it. v0.1 runs single-database.

The queue is read and written by multiple components. The overnight pass writes proposals. The app layer reads and resolves them. Ghost.synthd's regeneration pass sometimes reads resolved items to act on user decisions. Careful transaction handling matters here, and the resolution column is what gates whether a proposal has been acted on.

Graceful shutdown respects in-flight stages. SIGTERM completes the current stage's active batch, commits, and exits. Queued stages wait for next start.

Test strategy. Unit tests for the clustering, resolution, and ranking logic. Integration tests with a seeded Postgres and a mocked LLM runtime. Property-based tests on reconciliation when events arrive out of order. End-to-end tests against a real local LLM are v0.2 work. Quality evaluation of prose generation is inherently manual.

## Versioning

The daemon version tracks the overall LocalGhost release. The database schema uses standard migrations. Embeddings are versioned by model identifier, stored in a system config row, and a model change triggers a full re-embedding pass (expensive, infrequent, typically one per major version). Prose generation prompts are versioned in their own files with the version recorded in memories.metadata and episodes.metadata.