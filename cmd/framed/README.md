# ghost.framed

Image ingestion, archival, and description. Pulls from upstream image sources (or accepts pushes from external agents), stores canonical copies in its own archive, runs a local vision model against each image, produces structured descriptions, and pushes those descriptions through ghost.noted's inbox endpoint so the rest of the fleet sees them as text.

## Status

Phase 0. No code. This document describes the architecture as of April 2026. ghost.framed ships in shade (v0.2), not wisp (v0.1), so this is a design sketch and will be revised substantially when implementation starts. Parts of the design that depend on vision-model capability are explicitly hedged because what a local vision model can reliably produce in mid-2026 is not something I am confident about yet.

## Purpose

ghost.framed is the image layer of LocalGhost's personal archive. It has three jobs wrapped in one daemon, sync (pull upstream images into the local archive, or accept pushes from external agents), archival (keep the canonical copy of every image that belongs in the memory layer), and description (run a local vision model against each image and turn visual content into structured text).

The output is text, not images. The vision model runs once per image, produces a structured description, and that description flows through ghost.noted's inbox as any other text source. ghost.synthd and ghost.cued never see the image itself, they see the description. The image stays in ghost.framed's archive, available for the UI to retrieve when a user wants to see the original, but downstream processing operates on the text.

The three-in-one framing matches ghost.noted. Splitting sync, archive, and processing into separate daemons would add coordination complexity for no real gain. Keeping them together means the full pipeline from image-arrives to description-emitted lives behind one clear boundary.

Like ghost.noted, the architecture is bidirectional. The daemon can pull from upstream sources (filesystem watch, cloud photo storage via export) or accept pushes from external agents (the mobile app, a browser extension, a CLI tool). Which direction applies depends on the source and the deployment.

## Position in the fleet

ghost.framed sits to the side of ghost.noted. It has its own archive and its own processing pipeline, and it feeds into the same text pipeline ghost.noted provides for everything else.

```
UPSTREAM IMAGE SOURCES
  |
  |  PULL mode (daemon polls upstream):
  |    Filesystem watcher on photo library folders
  |    Filesystem watcher on screenshots folder
  |    Cloud photo storage via export (v0.2+)
  |
  |  PUSH mode (external agent sends to the daemon):
  |    Mobile app camera roll sync over encrypted tunnel
  |    Browser extension sending saved images
  |    CLI tool or scripted import
  |
  v
ghost.framed
  |
  |  step 1, sync loop or push endpoint brings image in
  |  step 2, store canonical copy in local image archive
  |  step 3, extract EXIF metadata (timestamp, GPS, camera)
  |  step 4, write source row to Postgres, enqueue processing
  |  step 5, worker pulls from queue, calls local vision model
  |  step 6, run OCR if image contains text
  |  step 7, assemble structured description as text blob
  |  step 8, POST description to ghost.noted inbox with source metadata
  |
  v
ghost.noted (treats the description as text, links back to the image source)
  |
  v
rest of the fleet (ghost.synthd, etc.)
```

The contract ghost.framed maintains is that every image handled by the daemon either produces a description that gets pushed to ghost.noted, or is recorded as skipped (too small, unreadable, not an image) for the audit trail. The description pushed to ghost.noted carries enough metadata that the link between the ghost.noted text source and the ghost.framed image source is bidirectional. ghost.noted's source row records the ghost.framed source ID in its metadata. ghost.framed's description row records the resulting ghost.noted source ID. Either daemon can walk to the other side of the link, and any downstream UI can start from a journal entry and retrieve the image it came from, or start from an image and retrieve all the entries derived from it.

## Responsibilities

**Upstream sync, pull mode.** The daemon watches configured filesystem sources. Common image formats only (JPEG, PNG, HEIC, WebP). Subdirectory recursion is configurable per source. Cloud photo storage (v0.2+) would use a user-run export that writes into a watched folder, not a direct API integration, to avoid holding third-party credentials.

**Upstream sync, push mode.** Two endpoints for external agents. A general `/api/v1/framed/push` endpoint that accepts binary image bytes plus metadata, used by browser extensions and scripted imports. An authenticated streaming endpoint for the mobile app at `/api/v1/framed/push/mobile`, which uses the encrypted tunnel to push newly-captured photos as they happen.

**Archive management.** Every image that arrives is stored in `/var/lib/localghost/framed/archive/` under an internally organised layout, `<year>/<month>/<image-hash>.<ext>`. The hash is based on image bytes so identical images collapse to one archive entry regardless of source. A Postgres row maps each upstream URI to the archive path.

**Deduplication.** Content-hash dedup catches exact duplicates (the same photo saved twice, a screenshot taken twice of the same thing). Near-duplicate detection (two photos of the same scene taken seconds apart) is not attempted, ghost.synthd can cluster near-duplicate entries later based on descriptions.

**EXIF extraction.** For each image, pull standard EXIF fields at ingestion time. Timestamp, GPS coordinates if present, camera make and model, lens, orientation. No inference here, just reading the metadata the image carries. Timestamp becomes the source_created_at the description carries forward to ghost.noted. GPS becomes location metadata in the description.

**Vision model call.** The core processing step. The image plus a structured prompt go to the local vision model, which returns a structured description. The prompt asks for a caption, a list of visible entities, any notable details, and a confidence indicator. The response is constrained to a JSON schema so ghost.framed can parse it reliably.

**OCR.** If the image contains text (a screenshot, a sign, a menu, a whiteboard, a document photograph), OCR extracts it. The extracted text is included in the description as a separate field. v0.2 uses whatever local OCR runtime the fleet standardises on.

**Description assembly.** The daemon assembles a text blob from the caption, the entity list, the OCR text, and the EXIF metadata. The blob is structured but readable, so that ghost.noted's extraction can operate on it as naturally as any other text source.

**Push to ghost.noted.** The assembled description goes to ghost.noted's inbox endpoint. The push carries `source_hint` set to `ghost.framed`, the ghost.framed source ID, the ghost.framed archive path, the EXIF timestamp as the source_created_at, and any GPS coordinates in structured metadata. ghost.noted stores the ghost.framed source ID in its own source metadata, creating a bidirectional link. ghost.noted returns the ghost.noted source ID it assigned, and ghost.framed writes that ID into the `descriptions.noted_source_id` column so the link is resolvable from both sides. ghost.noted then runs its own extraction on the description and produces journal entries, which carry the link through the extraction by referencing the ghost.noted source they came from.

**Change tracking.** If an image is updated upstream (metadata changed, image rotated, annotations added), ghost.framed detects the hash change, pulls the new version into the archive as a revision, and re-processes. The new description replaces the old one in ghost.noted, which triggers ghost.noted's own update event.

**Deletion.** Deletion happens only from within LocalGhost. The user deletes an image through the UI, ghost.framed removes the image from its archive, emits a deletion signal to ghost.noted which cascades the corresponding source and entries, and records the deletion in its own audit log. Upstream deletions do not propagate, the archive retains the image and sync simply stops updating that source. This matches ghost.noted's deletion semantics.

## Non-responsibilities

**No entity resolution.** ghost.framed produces raw descriptions with visible entities as strings. ghost.synthd resolves entities across images and across sources.

**No journal entry extraction.** ghost.framed produces a description, not entries. ghost.noted runs its extraction on the description to produce entries.

**No face recognition naming.** The vision model can describe that a person is visible, sometimes with features. ghost.framed does not attempt to name faces. Naming is entity-resolution work and lives in ghost.synthd with the user-confirmation loop.

**No thumbnail generation.** Thumbnails are a UI concern. The archive holds full-resolution originals, the UI generates thumbnails as needed.

**No image search.** Queries against images go through the app layer, which talks to ghost.synthd directly and can filter on entries derived from images. ghost.framed itself does not answer queries.

**No image embeddings.** Would let ghost.framed answer "find images like this one" directly, but that use case is v1.0+ and the embedding work, if it happens, lives in ghost.synthd alongside text embeddings.

**No upstream writes.** ghost.framed never writes back to upstream. It does not modify photos in the user's library, never adds tags, never rotates, never renames files.

**No inference beyond description and OCR.** The vision model call and the OCR call exist to produce the description. Any other inference use is in a different daemon.

## The link to ghost.noted

The bidirectional link between a ghost.framed image source and the ghost.noted text source derived from it is a real architectural commitment, not just a convenience. The daemons are separate, they have their own databases and their own archives, but the memory layer needs to move between them freely. A user viewing a journal entry should be able to tap through to the image that entry came from. A user viewing an image should be able to see every entry the system has derived from it, now and across re-processings.

The link is stored on both sides. ghost.framed's `descriptions.noted_source_id` points to the ghost.noted source row. ghost.noted's source row stores the ghost.framed source ID in its metadata JSONB under a known key. Either daemon can resolve the link without round-tripping through the other daemon's API, because the link is cached on both sides at push time. If one side's database is restored from backup without the other, the link remains intact as long as the UUIDs on each side do.

When an image is re-processed and produces a new description, the link updates but does not break. ghost.framed marks the old description `superseded_at`, pushes the new description, ghost.noted treats the push as an update to the existing source (keyed on the ghost.framed source ID in metadata), ghost.noted's source row keeps the same ID but its content changes. The link from ghost.framed's new description row points to the same ghost.noted source ID the old description pointed to. Journal entries derived from the old description are reconciled against the new description by ghost.noted's normal update flow.

When an image is deleted from ghost.framed's archive, ghost.framed pushes a deletion signal through ghost.noted's inbox that carries the ghost.framed source ID. ghost.noted finds the source row with that ID in metadata, cascades the deletion, emits events. The link drives the cascade rather than being collateral damage from it.

The same pattern will apply to ghost.voiced when it ships. Audio sources in ghost.voiced will link bidirectionally to the ghost.noted text sources containing their transcripts, and the ghost.noted metadata can hold multiple cross-daemon source IDs if a single text source somehow derives from multiple upstream modalities.

## Data model

Six tables. The archive filesystem layout sits alongside Postgres, with rows mapping source identifiers and upstream URIs to archive paths. Unlike ghost.noted, there is no full-text `current_content` column since the canonical content is the image bytes in the archive.

```sql
-- One row per canonical image source.
CREATE TABLE image_sources (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_type       TEXT NOT NULL,                -- 'fs_watch', 'mobile_push', 'browser_push', etc.
  upstream_uri      TEXT NOT NULL,                -- File path, mobile upload ID, etc.
  archive_path      TEXT NOT NULL,                -- Relative path within the framed archive
  image_hash        BYTEA NOT NULL,               -- SHA-256 of the image bytes
  mime_type         TEXT NOT NULL,                -- image/jpeg, image/png, etc.
  file_size         BIGINT NOT NULL,              -- Bytes on disk
  image_width       INTEGER,
  image_height      INTEGER,
  upstream_modified TIMESTAMPTZ,                  -- Upstream last-modified, for sync comparison
  first_seen        TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_synced       TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at        TIMESTAMPTZ,
  exif_metadata     JSONB NOT NULL DEFAULT '{}',  -- Raw EXIF as extracted
  skip_reason       TEXT                          -- 'too_small', 'corrupted', 'not_an_image', NULL if processed
);

CREATE UNIQUE INDEX idx_image_sources_type_uri ON image_sources(source_type, upstream_uri);
CREATE INDEX idx_image_sources_hash ON image_sources(image_hash);
CREATE INDEX idx_image_sources_deleted_at ON image_sources(deleted_at) WHERE deleted_at IS NOT NULL;

-- Additional upstream URIs that resolve to the same image.
CREATE TABLE image_source_aliases (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id    UUID NOT NULL REFERENCES image_sources(id) ON DELETE CASCADE,
  source_type  TEXT NOT NULL,
  upstream_uri TEXT NOT NULL,
  first_seen   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_image_aliases_type_uri ON image_source_aliases(source_type, upstream_uri);
CREATE INDEX idx_image_aliases_source_id ON image_source_aliases(source_id);

-- One row per revision of an image source. Insert-only.
CREATE TABLE image_source_revisions (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id      UUID NOT NULL REFERENCES image_sources(id) ON DELETE CASCADE,
  image_hash     BYTEA NOT NULL,
  archive_path   TEXT NOT NULL,
  seen_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  change_type    TEXT NOT NULL                    -- 'created', 'updated', 'restored'
);

CREATE INDEX idx_image_source_revisions_source_id ON image_source_revisions(source_id, seen_at);

-- One row per description produced for an image.
CREATE TABLE descriptions (
  id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id             UUID NOT NULL REFERENCES image_sources(id) ON DELETE CASCADE,
  noted_source_id       UUID,                     -- The ghost.noted source ID this description became
  caption               TEXT NOT NULL,            -- One or two sentences describing the scene
  structured_content    JSONB NOT NULL,           -- Full structured description (entities, details, etc.)
  ocr_text              TEXT,                     -- Extracted text from OCR, nullable
  ocr_language          TEXT,                     -- Detected language of OCR text
  model_identifier      TEXT NOT NULL,            -- Which vision model produced this
  prompt_version        TEXT NOT NULL,            -- Which prompt version
  produced_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  pushed_to_noted_at    TIMESTAMPTZ,              -- When the description was successfully pushed
  superseded_at         TIMESTAMPTZ               -- When a newer description replaced this one
);

CREATE INDEX idx_descriptions_source_id ON descriptions(source_id);
CREATE INDEX idx_descriptions_noted_source_id ON descriptions(noted_source_id);
CREATE INDEX idx_descriptions_superseded_at ON descriptions(superseded_at) WHERE superseded_at IS NULL;

-- Sync state per upstream source.
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

-- Queue for pending image processing.
CREATE TABLE processing_queue (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id       UUID NOT NULL REFERENCES image_sources(id) ON DELETE CASCADE,
  enqueued_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  started_at      TIMESTAMPTZ,
  completed_at    TIMESTAMPTZ,
  attempts        INTEGER NOT NULL DEFAULT 0,
  last_error      TEXT,
  status          TEXT NOT NULL DEFAULT 'pending',
  priority        INTEGER NOT NULL DEFAULT 5       -- Higher priority processed first (1=low, 10=high)
);

CREATE INDEX idx_processing_queue_status_priority ON processing_queue(status, priority DESC, enqueued_at)
  WHERE status IN ('pending', 'failed');
```

**Why images live in the archive on disk, not in Postgres.** Image files are large, Postgres is the wrong tool for large immutable binary data, and filesystem storage lets standard backup tools work on the archive directly. The Postgres row carries the metadata and a pointer to the filesystem location.

**Why a descriptions table with its own rows rather than a column on image_sources.** Descriptions change when the model or prompt changes, and the same image can produce multiple descriptions over time. Keeping each description as its own row with model and prompt metadata makes it possible to re-process an image with a newer model without losing the older description, and makes it possible to compare descriptions produced by different models if a comparison is ever useful.

**Why the priority column on processing_queue.** Vision inference is slow. A user's first-run sync against a ten-year photo library could queue tens of thousands of images. Processing them in pure FIFO order means recent photos wait behind old ones, and the user sees the system "working" without any visible progress on photos they care about. A priority column lets the daemon process recent photos first and work through the backlog over days or weeks. Default priority is 5, mobile push gets priority 8 (recent captures are what the user cares about), backlog gets priority 3.

**Why a sync_state table, same as ghost.noted.** Each upstream source config has its own cadence, error history, and next-run schedule. Tracking in Postgres survives restarts and feeds the admin endpoint.

**Why superseded_at rather than deleted_at on descriptions.** A description is replaced rather than deleted when the image is re-processed. The `superseded_at` marks when a newer description took over. Old descriptions are retained in case a future analysis wants to see how descriptions have changed, and the query `WHERE superseded_at IS NULL` returns the current description for each source.

## Interfaces

**No direct event emission.** Unlike ghost.noted, ghost.framed does not emit events on Redis streams. The output of ghost.framed is a description pushed to ghost.noted's inbox, and ghost.noted emits the events that downstream consumers subscribe to. This keeps the fleet's event vocabulary narrower (ghost.synthd only needs to know about ghost.noted events, not per-source-type events for every ingestion daemon).

**HTTP endpoints, all on localhost, under `/api/v1/framed/`.** The per-daemon versioned path matches the convention across the LocalGhost fleet. Public ingestion endpoints, operational endpoints, content serving, and admin endpoints all sit under the same prefix.

```
POST /api/v1/framed/push                    - generic binary image push with metadata
POST /api/v1/framed/push/mobile             - authenticated streaming endpoint for the mobile app
GET  /api/v1/framed/queue                   - processing queue depth, priority distribution, failures
GET  /api/v1/framed/sync                    - per-source sync state
POST /api/v1/framed/sync/:config/run        - manually trigger a sync for a configured upstream
POST /api/v1/framed/reprocess/:id           - manually trigger re-processing of an archived image
GET  /api/v1/framed/image/:id               - serve image bytes from archive (authenticated, localhost only)

GET  /api/v1/framed/admin/healthz           - liveness
GET  /api/v1/framed/admin/readyz            - readiness, checks Postgres, vision runtime, archive filesystem
GET  /api/v1/framed/admin/config            - current daemon config (authenticated)
POST /api/v1/framed/admin/config            - update daemon config (authenticated, triggers reload)
```

The push endpoint accepts multipart form data with the image bytes and a JSON metadata part:

```json
{
  "source_hint": "browser-extension",
  "upstream_uri": "https://example.com/photo.jpg",
  "metadata": {}
}
```

The mobile push endpoint uses the encrypted tunnel established at pairing.

The `/api/v1/framed/image/:id` endpoint serves image bytes from the archive so the app UI can display them. It binds to localhost only and requires an auth token. This is the one place ghost.framed serves content rather than just ingesting it.

**Database access.** Postgres read access on image_sources and descriptions is exposed to ghost.synthd, the app layer, and ghost.watchd through per-daemon roles. ghost.framed is the only daemon with write access to its tables. The archive filesystem is owned by ghost.framed's process user.

## The vision prompt

The vision prompt is v0.2 work and what follows is direction of travel rather than a frozen spec.

The vision model receives the image along with any EXIF metadata ghost.framed can supply (source type, timestamp, GPS, camera). The prompt asks for a structured JSON response containing a caption (one or two sentences), a list of visible entities (people, objects, places, named things if recognisable), notable details (activity depicted, setting, mood if discernible), transcribed text if any is visible in the image, and a confidence indicator for whether the image is interpretable at all.

The prompt is constrained enough that parsing is reliable but loose enough that the model can choose what is notable. For a screenshot, the notable detail is the UI and the text. For a photograph, it is the people and the setting. For a whiteboard photograph, it is the diagram and the writing. The prompt does not try to be exhaustive, it tries to capture what a person would tell another person about the image.

**What the prompt does not try to do.** It does not name people by face. It does not identify places by landmark recognition (GPS is more reliable for that). It does not infer relationships between people. It does not guess at context the image does not show. Those are either entity-resolution tasks for ghost.synthd or things vision models get wrong often enough that v0.2 should not rely on them.

**Multilingual.** If the image contains text in a non-English language (signs, menus, foreign-language screenshots), the OCR extraction preserves the original text and marks the detected language. The vision model caption is produced in English to match ghost.noted's convention. OCR text keeps its source language and is annotated with the language code so ghost.synthd can reason about it separately.

**Failure modes.** The vision model sometimes returns invalid JSON. Sometimes it produces a caption unrelated to the image (hallucination). Sometimes it misses obvious entities. The worker retries with a simpler prompt on invalid JSON. Hallucinated or missing content is surfaced through ghost.synthd's queue, same way ghost.noted surfaces extraction failures.

## Description assembly

The description pushed to ghost.noted is a structured text blob. The format is readable English, because ghost.noted's extraction is a text-reading LLM and structured prose is what it handles best. A sample format:

```
[Image captured 2026-04-02 19:14 at 42.2531°N, 13.9036°E]
[Camera: iPhone 15 Pro, lens: main]

Caption: A plate of pasta with burrata and cherry tomatoes sits on a marble
table at a restaurant. A man is visible in the background working through
a large pizza.

Visible entities:
- A plate of pasta with burrata (foreground, centre)
- A man eating pizza (background, right)
- A marble table surface
- A window with evening light
- A wine glass with red wine (foreground, left)

Notable details: The setting is a restaurant interior, evening meal. The
pizza portion the background figure is working through is unusually large.

Visible text (OCR):
None detected.
```

The format is deliberately plain. No markdown, no special structure ghost.noted has to parse. ghost.noted's extraction treats it as text and produces journal entries from it the same way it produces entries from a markdown note. An entry like "We ate at a restaurant with marble tables where a man at the next table was working through a huge pizza" is what ghost.noted would extract from this description, which is the kind of thing the memory layer actually wants.

## v0.2 scope (shade)

Two image sources for v0.2. More can join later, same principle as ghost.noted: start with the pattern that works and expand once the extraction pipeline stabilises.

**What definitely ships in v0.2.**

- Filesystem watcher on user-configured photo library folders, pull mode.
- Filesystem watcher on a screenshots folder, pull mode with priority-low default.
- Local `/api/v1/framed/push` endpoint for any external agent.
- EXIF extraction, vision model call, OCR pipeline against configured sources.
- Own image archive with revision tracking.
- Description push to ghost.noted with appropriate metadata.

**What probably ships in v0.2.**

- Mobile camera roll push endpoint. Depends on the mobile app's v0.2 readiness.

**What probably does not ship in v0.2.**

- Cloud photo storage sync via export scripts. Easy to add, but the user has to run the export, and v0.2 should not require that setup step.

**What definitely does not ship in v0.2.**

- Video keyframe extraction.
- Near-duplicate clustering.
- Image embeddings.
- Handwriting OCR beyond what the general OCR engine handles.
- Face clustering, face recognition, named-person attribution.

## v1.0+ roadmap

**v1.0.** Video keyframe extraction. Pull keyframes from short videos and run the vision pipeline on each. Still not full video understanding, because that needs a model class that is expensive to run locally and the use case is thin.

**v1.0+.** Near-duplicate detection and clustering. Helpful for "twenty burst-mode shots of the same scene," lets ghost.synthd decay all but the best one. Requires image similarity which probably means an image embedding index.

**v1.0+.** Image embeddings for similarity search. Enables queries like "show me photos like this one." Embedding work probably lives in ghost.synthd rather than ghost.framed, since ghost.synthd already owns the text-embedding index and would benefit from a consistent embedding pattern across modalities.

**v1.0+.** Better handling of document photographs, where the user photographs a page of handwriting or a printed document and expects the text to flow through as if they had typed it. Needs handwriting OCR or a layout-aware document model.

**Never.** Cloud vision APIs. Quality trade-off is real in 2026. Privacy trade-off is not acceptable. The quality gap will close as local vision models improve.

## Open questions

**How detailed should descriptions be.** A one-sentence caption is too little (ghost.noted's extraction cannot pull useful entries from it). A paragraph of detail is probably enough. Two paragraphs might be too much. The right length depends on what ghost.noted's extraction does with descriptions, which will only be clear after running both daemons against real photo libraries. Expect the length to change multiple times in v0.2.

**What to do with photos the user would not want indexed.** Some photos do not belong in a searchable memory layer. Intimate images, financial screenshots, health photos. The user needs a way to exclude folders or specific images. The v0.2 answer is a `.localghost-skip` file in any directory ghost.framed should ignore. Per-image opt-out and pattern-based rules are v1.0+.

**Screenshot detection versus photo detection.** Treating screenshots differently requires distinguishing them, and the OS makes this surprisingly hard. No EXIF GPS, specific dimensions matching screen resolution, and coming from a screenshots folder are all partial signals but none is reliable. The v0.2 answer is that the user configures which folders are photos and which are screenshots. The daemon trusts the configuration.

**Vision model selection.** 2026 has several local vision models that could do the v0.2 job, but they vary in speed, quality, and resource cost. The right model depends on the user's hardware. Default to a reasonable model and let the user swap it in config. The specific default is a v0.2 decision based on what works on the reference hardware tier.

**First-run strategy for large archives.** A user with ten years of photos has tens of thousands of images. Processing all of them takes days. The daemon needs a sensible first-run strategy. Current plan is to process recent photos first (last year) at priority 5, work backwards through the archive at priority 3, show progress in the UI, let the user prioritise specific date ranges or folders.

**OCR runtime choice.** Tesseract is the baseline. Newer options are better. The choice depends on what runs acceptably on the local hardware. v0.2 decision.

**How description updates interact with ghost.noted.** When an image re-processes and produces a new description, ghost.framed pushes the new description to ghost.noted. ghost.noted sees this as an update to an existing source. The question is whether ghost.framed also marks the previous description as superseded in its own database before or after the push. Current plan is "before, so ghost.framed's state is always consistent with what ghost.noted thinks is current." If the push fails, ghost.framed retries from the superseded state.

**Archive retention for deleted images.** Same question as ghost.noted. Deleted images in the archive can be hard-deleted immediately, soft-deleted with a retention window, or retained indefinitely. Soft-delete with a default long retention is the plan, actual policy is v0.2 configuration.

## Rejected approaches

**Running the vision model and producing journal entries directly.** Simpler pipeline would have ghost.framed produce entries itself, skipping ghost.noted. Rejected because keeping all extraction in ghost.noted means the fleet has one extraction pattern rather than two, and any improvement to the extraction prompt or model benefits all sources at once. ghost.framed produces text, ghost.noted turns text into entries.

**Images in the user's photo library without a local copy.** The previous design. Rejected once LocalGhost's identity as a personal archive became clear. If the upstream photo library disappears (drive unplugged, files moved, photos deleted on the phone), the memory layer should not disappear with it. The archive has to be local and authoritative.

**Archive as a mirror of upstream structure.** Easier to implement. Rejected because upstream structures vary across source types, and mirroring them means the archive layout changes every time a new source type is added. Internal organisation by year, month, and hash gives a stable layout.

**Emitting Redis stream events from ghost.framed.** The alternative to pushing through ghost.noted. Rejected because it would double the event vocabulary downstream has to handle. Every downstream consumer would need to subscribe to both ghost.noted events and ghost.framed events and handle them as separate source types. Pushing through ghost.noted keeps the downstream contract narrow.

**Image embeddings in v0.2.** Would enable similarity queries earlier. Rejected because image embeddings are another inference cost on top of the vision model and OCR, and the use case is not in scope until v1.0+.

**Cloud vision APIs.** Better quality than local in 2026. Violates the manifesto. The v0.2 quality cost is real and the v0.2 user should know that. Privacy trade-off does not get reopened.

**Unified queue with ghost.noted.** One shared extraction queue across all ingestion daemons. Rejected because image processing has very different resource characteristics (slower, more memory, benefits from GPU batching if available) than text extraction. Separate queues let each daemon tune its own rate.

**Upstream-authoritative deletion.** Earlier draft had upstream deletions cascading to the archive. Rejected because the archive is a personal history that retains things the user might forget to keep. Deletions happen through LocalGhost's UI, not from the outside.

## Implementation notes

Written in Go. Vision inference goes through whatever inference runtime the fleet standardises on. OCR through the chosen OCR runtime.

The daemon runs three goroutine groups, same pattern as ghost.noted. Sync workers handle filesystem watches, the mobile sync endpoint, and the push endpoint. Archive writers handle the actual filesystem operations and the Postgres transactions. Processing workers pull from the queue, run vision and OCR, assemble descriptions, and push to ghost.noted.

Vision inference is slow enough that v0.2 runs one image at a time in the processing worker. Concurrent inference requires either multiple model instances (memory cost) or shared model queueing (complexity cost). v0.2 picks the simplest version and tunes from evidence.

OCR runs only when the vision model indicates text is present or when the source is configured as screenshots (where text is expected). Running OCR on every photo wastes compute.

The push to ghost.noted is over localhost HTTP. Failure handling retries with exponential backoff. If ghost.noted is down, descriptions queue in a separate `noted_push_queue` table (same pattern as `processing_queue`) and retry when ghost.noted comes back.

Archive filesystem operations are fsync'd. A crash mid-archive-write leaves the archive in a consistent state, either the file is fully written and the Postgres row points at it, or neither exists.

Graceful shutdown, same rules as ghost.noted. Finish in-flight processing, commit state, exit cleanly. Queued work survives restart.

Test strategy is lighter than ghost.noted because much of the quality evaluation is subjective. Unit tests for EXIF parsing, description assembly, queue state, push retry logic. Integration tests against a small fixture image set with known properties. Quality evaluation against a real photo library is manual and happens when the prompt or model changes.

## Versioning

The vision prompt and the OCR runtime version are recorded in `descriptions.model_identifier` and `descriptions.prompt_version` so future analysis can distinguish descriptions produced by different combinations. A user-triggered bulk re-processing flow in v1.0+ lets the user rebuild descriptions from a newer model.