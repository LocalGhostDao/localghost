# FULLY AI GENERATED NOT YET REVIEWD
# ghost.voiced

Audio ingestion, archival, and transcription. Pulls from upstream audio sources (or accepts pushes from external agents), stores canonical copies in its own archive, runs a local transcription model against each recording, produces transcripts with speaker separation and timestamp markers where available, and pushes those transcripts through ghost.noted's inbox so the rest of the fleet sees them as text.

## Status

Phase 0. No code. This document describes the architecture as of April 2026. ghost.voiced ships in shade (v0.2) at the earliest, possibly later if wisp (v0.1) demands more focus on text and image paths first. The design is a sketch and will be revised substantially when implementation starts. Parts of the design that depend on local transcription model capability are explicitly hedged.

## Purpose

ghost.voiced is the audio layer of LocalGhost's personal archive. It has three jobs, same as ghost.noted and ghost.framed. Sync (pull upstream audio into the local archive, or accept pushes from external agents). Archival (keep the canonical copy of every audio recording that belongs in the memory layer). Transcription (run a local speech-to-text model against each recording and turn audio into structured text).

The output is text, not audio. The transcription runs once per recording, produces a transcript with speaker separation and timestamp markers where available, and that transcript flows through ghost.noted's inbox as any other text source. ghost.synthd and other downstream daemons never see the audio itself, they see the transcript. The audio stays in ghost.voiced's archive, available for the UI to play back when a user wants to hear the original, but downstream processing operates on the text.

The three-in-one framing matches ghost.noted and ghost.framed. Same justification, splitting sync from archive from processing adds coordination complexity without real gain. Keeping them together means the full pipeline from audio-arrives to transcript-emitted lives behind one clear boundary.

The architecture is bidirectional. The daemon can pull from upstream sources (filesystem watch on a voice memos folder, possibly cloud audio storage via export) or accept pushes from external agents (the mobile app streaming recordings, a browser extension, a CLI tool). Which direction applies depends on the source and the deployment.

## What this daemon is not

ghost.voiced is not the conversational interface to the memory layer. Earlier versions of some LocalGhost documents described voiced as "the voice that answers questions." That was wrong. Querying the memory layer is done by the app talking to ghost.synthd directly over a local API. That query path does not belong to a daemon, it is an interface that glues the app to ghost.synthd and, where useful, to a local LLM for natural-language phrasing of the response. The app layer owns it.

ghost.voiced is purely an ingestion daemon. Audio arrives, transcript leaves. No conversation, no query handling, no speech synthesis.

## Position in the fleet

ghost.voiced sits to the side of ghost.noted, parallel to ghost.framed. It has its own archive and its own processing pipeline, and feeds into the same text pipeline ghost.noted provides for everything else.

```
UPSTREAM AUDIO SOURCES
  |
  |  PULL mode (daemon polls upstream):
  |    Filesystem watcher on voice memos folder
  |    Filesystem watcher on podcast or recording folders
  |    Cloud audio storage via export (v0.3+)
  |
  |  PUSH mode (external agent sends to the daemon):
  |    Mobile app voice memo sync over encrypted tunnel
  |    Mobile app real-time voice stream (v0.3+)
  |    Browser extension sending audio from web recordings
  |    CLI tool or scripted import
  |
  v
ghost.voiced
  |
  |  step 1, sync loop or push endpoint brings audio in
  |  step 2, store canonical copy in local audio archive
  |  step 3, extract audio metadata (duration, sample rate, format)
  |  step 4, write source row to Postgres, enqueue transcription
  |  step 5, worker pulls from queue, calls local transcription model
  |  step 6, apply speaker separation if multiple speakers detected
  |  step 7, assemble structured transcript as text blob
  |  step 8, POST transcript to ghost.noted inbox with source metadata
  |
  v
ghost.noted (treats the transcript as text, links back to the audio source)
  |
  v
rest of the fleet (ghost.synthd, etc.)
```

The contract ghost.voiced maintains is that every audio recording handled by the daemon either produces a transcript that gets pushed to ghost.noted, or is recorded as skipped (too short, silent, unreadable format) for the audit trail. The transcript pushed to ghost.noted carries enough metadata that the link between the ghost.noted text source and the ghost.voiced audio source is bidirectional. ghost.noted's source row records the ghost.voiced source ID in its metadata. ghost.voiced's transcript row records the resulting ghost.noted source ID. Either daemon can walk to the other side of the link, and any downstream UI can start from a journal entry and retrieve the audio it came from, or start from a recording and retrieve all the entries derived from it.

## Responsibilities

**Upstream sync, pull mode.** The daemon watches configured filesystem sources. Supports common audio formats (M4A, MP3, WAV, FLAC, OGG). Subdirectory recursion is configurable per source. Cloud audio storage would use a user-run export that writes into a watched folder, not a direct API integration, consistent with the pattern in the other ingestion daemons.

**Upstream sync, push mode.** Two endpoints for external agents. A general `/api/v1/voiced/push` endpoint that accepts binary audio bytes plus metadata, used by browser extensions and scripted imports. An authenticated streaming endpoint for the mobile app at `/api/v1/voiced/push/mobile`, which uses the encrypted tunnel to push voice memos and eventually real-time recordings.

**Archive management.** Every audio file that arrives is stored in `/var/lib/localghost/voiced/archive/` under an internally organised layout, `<year>/<month>/<audio-hash>.<ext>`. Hash is on audio bytes so identical recordings collapse to one archive entry regardless of source. Postgres row maps each upstream URI to archive path.

**Deduplication.** Content-hash dedup catches exact duplicates. Near-duplicate detection (the same recording saved at different bitrates, the same voice memo uploaded twice from different devices) is not attempted. ghost.synthd can cluster near-duplicate entries later based on transcript content.

**Audio metadata extraction.** For each recording, pull standard audio metadata at ingestion time. Duration, sample rate, channel count, codec, file size, and any ID3 or equivalent tags the file carries. On recordings that include embedded timestamps (many mobile voice memo apps embed the recording start time), capture that as the source_created_at.

**Transcription.** The core processing step. The audio plus a structured prompt goes to the local transcription model (Whisper or equivalent in 2026). The response is the transcript text with timestamp markers for every segment, and where the model supports it, speaker labels (SPEAKER_00, SPEAKER_01, etc). ghost.voiced does not attempt to name the speakers, naming is entity-resolution work and lives in ghost.synthd through the queue.

**Chunking for long recordings.** Voice memos are usually short (seconds to a few minutes). Podcast recordings, lectures, and meeting audio can be hours. The transcription model has practical limits on how much audio it can process in one pass. For long recordings the worker chunks the audio into overlapping windows, transcribes each chunk, and stitches the results into one continuous transcript. The chunk boundaries are recorded so a future analysis can know where they fell.

**Speaker separation.** If multiple speakers are detected, the transcript uses speaker labels on each utterance. If the model cannot reliably separate speakers (single-channel audio, heavily overlapping speech, noisy recording), the transcript is produced as a single stream without labels. ghost.voiced does not fight the model's uncertainty, it records what the model produced and surfaces low-confidence speaker separation in the transcript metadata.

**Transcript assembly.** The daemon assembles a text blob from the transcript segments, speaker labels where present, and audio metadata. The blob is structured but readable English prose so ghost.noted's extraction treats it naturally.

**Push to ghost.noted.** The assembled transcript goes to ghost.noted's inbox endpoint. The push carries `source_hint` set to `ghost.voiced`, the ghost.voiced source ID, the ghost.voiced archive path, the source_created_at (from embedded audio timestamp or filesystem mtime), and any relevant metadata in structured form. ghost.noted stores the ghost.voiced source ID in its own source metadata. ghost.noted returns its source ID, ghost.voiced writes it into the `transcripts.noted_source_id` column. Link resolvable from both sides.

**Change tracking.** Audio files are typically immutable once created, so update events are rare. But they happen. A recording is re-exported at a different sample rate. A voice memo is trimmed. The embedded metadata is edited. When ghost.voiced detects a hash change, it pulls the new version into the archive, re-runs transcription, pushes the new transcript as an update to ghost.noted. Transcript rows gain a new entry with the old one marked `superseded_at`.

**Deletion.** Deletion happens only from within LocalGhost. The user deletes an audio recording through the UI, ghost.voiced removes the audio from its archive, emits a deletion signal to ghost.noted which cascades the corresponding source and entries. Upstream deletions do not propagate. Consistent with ghost.noted and ghost.framed.

## Non-responsibilities

**No entity resolution.** ghost.voiced produces transcripts with raw speaker labels (SPEAKER_00, SPEAKER_01) and verbatim content. ghost.synthd resolves speakers to entities across recordings and across sources.

**No journal entry extraction.** ghost.voiced produces a transcript, not entries. ghost.noted runs its extraction on the transcript to produce entries.

**No speaker identification by voice.** The transcription model can sometimes produce speaker labels within a recording but does not attempt to identify who a speaker is across recordings. Voice-print identification is neither a v0.2 goal nor a v1.0 goal. Speakers are named through the same queue-and-merge flow ghost.synthd uses for any entity.

**No speech synthesis.** The daemon does not produce audio, only consumes it. If LocalGhost ever wants to speak to the user, that is a different subsystem.

**No real-time transcription in v0.2.** Recordings are transcribed after they arrive as files. Real-time transcription of live audio is harder, needs different model capabilities, and is deferred.

**No audio search.** Queries against audio-derived content go through the app talking to ghost.synthd, which can filter on entries derived from audio. ghost.voiced itself does not answer queries.

**No audio embeddings.** Would enable "find recordings that sound like this one." Not in scope. If it happens later, the embedding work lives in ghost.synthd alongside text and image embeddings.

**No upstream writes.** ghost.voiced never writes back to upstream. It does not modify recordings in the user's library, never transcodes them in place, never renames files.

**No inference beyond transcription.** The transcription model call is the only inference the daemon makes. Any other use of inference is in a different daemon.

## The link to ghost.noted

The bidirectional link between a ghost.voiced audio source and the ghost.noted text source derived from it is the same architectural commitment ghost.framed makes for images. ghost.voiced's `transcripts.noted_source_id` points to the ghost.noted source row. ghost.noted's source row stores the ghost.voiced source ID in its metadata JSONB under a known key. Either daemon can resolve the link without round-tripping through the other's API, because the link is cached on both sides at push time.

When an audio recording is re-processed (new model, new prompt, new transcription settings), the link updates but does not break. The old transcript is marked `superseded_at`, the new transcript gets pushed to ghost.noted as an update to the existing source keyed on the ghost.voiced source ID in metadata, journal entries are reconciled through ghost.noted's normal update flow.

When a recording is deleted from ghost.voiced's archive, ghost.voiced pushes a deletion signal through ghost.noted's inbox carrying the ghost.voiced source ID. ghost.noted finds the source row with that ID in metadata, cascades the deletion, emits events. Link drives the cascade.

## Data model

Six tables. Archive filesystem layout sits alongside Postgres, with rows mapping source identifiers and upstream URIs to archive paths. No full-text `current_content` column since the canonical content is the audio bytes in the archive.

```sql
-- One row per canonical audio source.
CREATE TABLE audio_sources (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_type       TEXT NOT NULL,              -- 'fs_watch', 'mobile_push', 'browser_push'
  upstream_uri      TEXT NOT NULL,              -- File path, mobile upload ID, etc.
  archive_path      TEXT NOT NULL,              -- Relative path within the voiced archive
  audio_hash        BYTEA NOT NULL,             -- SHA-256 of the audio bytes
  mime_type         TEXT NOT NULL,              -- audio/mp4, audio/mpeg, audio/wav, etc.
  file_size         BIGINT NOT NULL,
  duration_seconds  NUMERIC(10, 2),             -- Duration to 10ms precision
  sample_rate       INTEGER,
  channel_count     INTEGER,
  codec             TEXT,
  upstream_modified TIMESTAMPTZ,
  first_seen        TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_synced       TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at        TIMESTAMPTZ,
  audio_metadata    JSONB NOT NULL DEFAULT '{}',
  skip_reason       TEXT                        -- 'too_short', 'silent', 'unsupported_format', etc.
);

CREATE UNIQUE INDEX idx_audio_sources_type_uri ON audio_sources(source_type, upstream_uri);
CREATE INDEX idx_audio_sources_hash ON audio_sources(audio_hash);
CREATE INDEX idx_audio_sources_deleted_at ON audio_sources(deleted_at) WHERE deleted_at IS NOT NULL;

-- Additional upstream URIs that resolve to the same audio.
CREATE TABLE audio_source_aliases (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id    UUID NOT NULL REFERENCES audio_sources(id) ON DELETE CASCADE,
  source_type  TEXT NOT NULL,
  upstream_uri TEXT NOT NULL,
  first_seen   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_audio_aliases_type_uri ON audio_source_aliases(source_type, upstream_uri);
CREATE INDEX idx_audio_aliases_source_id ON audio_source_aliases(source_id);

-- One row per revision of an audio source. Insert-only.
CREATE TABLE audio_source_revisions (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id      UUID NOT NULL REFERENCES audio_sources(id) ON DELETE CASCADE,
  audio_hash     BYTEA NOT NULL,
  archive_path   TEXT NOT NULL,
  seen_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  change_type    TEXT NOT NULL                  -- 'created', 'updated', 'restored'
);

CREATE INDEX idx_audio_source_revisions_source_id ON audio_source_revisions(source_id, seen_at);

-- One row per transcript produced for a recording.
CREATE TABLE transcripts (
  id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id             UUID NOT NULL REFERENCES audio_sources(id) ON DELETE CASCADE,
  noted_source_id       UUID,                   -- ghost.noted source ID the transcript became
  transcript_text       TEXT NOT NULL,          -- Assembled prose with speaker labels and timestamps
  segments              JSONB NOT NULL,         -- Structured segments: start_ms, end_ms, speaker, text
  speaker_count         INTEGER,                -- Speakers detected in this recording
  detected_language     TEXT,                   -- ISO language code from the model
  chunking_strategy     TEXT,                   -- 'single_pass', 'windowed_5min', 'windowed_10min'
  model_identifier      TEXT NOT NULL,
  prompt_version        TEXT NOT NULL,
  produced_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  pushed_to_noted_at    TIMESTAMPTZ,
  superseded_at         TIMESTAMPTZ
);

CREATE INDEX idx_transcripts_source_id ON transcripts(source_id);
CREATE INDEX idx_transcripts_noted_source_id ON transcripts(noted_source_id);
CREATE INDEX idx_transcripts_superseded_at ON transcripts(superseded_at) WHERE superseded_at IS NULL;

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

-- Queue for pending transcription work.
CREATE TABLE transcription_queue (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id       UUID NOT NULL REFERENCES audio_sources(id) ON DELETE CASCADE,
  enqueued_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  started_at      TIMESTAMPTZ,
  completed_at    TIMESTAMPTZ,
  attempts        INTEGER NOT NULL DEFAULT 0,
  last_error      TEXT,
  status          TEXT NOT NULL DEFAULT 'pending',
  priority        INTEGER NOT NULL DEFAULT 5
);

CREATE INDEX idx_transcription_queue_status_priority ON transcription_queue(status, priority DESC, enqueued_at)
  WHERE status IN ('pending', 'failed');
```

**Why audio lives in the archive filesystem, not in Postgres.** Audio files are large. Postgres is not the right tool. Filesystem storage gives standard backup tools the right shape to work with, and lets the app play audio back directly from the archive path.

**Why a transcripts table with its own rows.** Transcription models improve over time, prompts change, chunking strategies evolve. The same recording can produce multiple transcripts. Keeping each transcript as its own row with model and prompt metadata makes re-processing possible without losing older transcripts, and makes it possible to compare transcripts across models if useful.

**Why the segments column is JSONB.** Each transcript is internally a list of segments (start_ms, end_ms, speaker label, text). This is structured data but not something downstream queries against directly in SQL, the assembled `transcript_text` is what ghost.noted sees. JSONB keeps the structured form available for UI playback (highlight the current segment in the transcript while the audio plays) without forcing a normalised schema that would create one row per segment, which would explode row counts for long recordings.

**Why chunking_strategy is recorded.** Long recordings get chunked. The chunking strategy affects how segments align and where the worker made decisions about transcription boundaries. Recording the strategy lets a future analysis know whether a stitched transcript used single-pass, five-minute, or ten-minute windows.

**Why the priority column on the queue.** Same reason as ghost.framed. Transcription is slow, a first-run sync against a long archive of voice memos could queue hundreds of hours of audio. Priority lets the daemon process recent recordings first (default 5), mobile push gets priority 8, backlog gets priority 3. Recent audio is what the user cares about.

## Interfaces

**No direct event emission.** Same as ghost.framed. ghost.voiced pushes transcripts to ghost.noted, which emits the events downstream consumers subscribe to. One event vocabulary in the fleet.

**HTTP endpoints, all on localhost, under `/api/v1/voiced/`.** The per-daemon versioned path matches the convention across the LocalGhost fleet. Public ingestion endpoints, operational endpoints, content serving, and admin endpoints all sit under the same prefix.

```
POST /api/v1/voiced/push                    - generic binary audio push with metadata
POST /api/v1/voiced/push/mobile             - authenticated streaming endpoint for mobile voice memos
GET  /api/v1/voiced/queue                   - transcription queue depth, priority distribution, failures
GET  /api/v1/voiced/sync                    - per-source sync state
POST /api/v1/voiced/sync/:config/run        - manually trigger sync for a configured upstream
POST /api/v1/voiced/retranscribe/:id        - manually trigger re-transcription of an archived recording
GET  /api/v1/voiced/audio/:id               - serve audio bytes from archive (authenticated, localhost only, supports range requests)

GET  /api/v1/voiced/admin/healthz           - liveness
GET  /api/v1/voiced/admin/readyz            - readiness, checks Postgres, transcription runtime, archive filesystem
GET  /api/v1/voiced/admin/config            - current daemon config (authenticated)
POST /api/v1/voiced/admin/config            - update daemon config (authenticated, triggers reload)
```

The push endpoint accepts multipart form data with the audio bytes and a JSON metadata part:

```json
{
  "source_hint": "browser-extension",
  "upstream_uri": "webrec://example.com/session-1234",
  "metadata": {}
}
```

The mobile push endpoint uses the encrypted tunnel. Same pattern as ghost.framed.

The `/api/v1/voiced/audio/:id` endpoint serves audio bytes so the app UI can play recordings back. Range requests are required (for audio seeking in the playback UI). Localhost only, authenticated.

**Database access.** Postgres read access on audio_sources and transcripts is exposed to ghost.synthd, the app layer (for playback metadata), and ghost.watchd through per-daemon roles. ghost.voiced is the only daemon with write access to its tables. The archive filesystem is owned by ghost.voiced's process user.

## The transcription prompt

Unlike ghost.framed's vision prompt, the transcription model does not use a freeform natural-language prompt. Modern transcription models (Whisper family in 2026) take structured inputs: audio, target language (or auto-detect), optional context prompts for domain-specific vocabulary, and model-specific parameters for speaker separation.

The parameters ghost.voiced passes to the transcription call:

- Language: auto-detect on first pass, lock to detected language for subsequent chunks in a long recording
- Speaker separation: enabled when the model supports it
- Timestamp precision: per-segment, not per-word (per-word doubles output size for marginal gain in v0.2)
- Context prompt: any known source-type context (e.g., "voice memo about a project" vs "meeting recording") if the source type implies a useful context

**Multilingual.** Transcription produces output in the source language where possible. Unlike ghost.noted and ghost.framed, ghost.voiced does not translate. The source-language transcript gets pushed to ghost.noted with a `detected_language` metadata field, and ghost.noted's extraction handles the translation through its normal multilingual path. This keeps the transcription layer focused on faithful audio-to-text and pushes language handling to the place it already happens.

**Failure modes.** The model sometimes produces empty transcripts for audio it cannot recognise (music, silence, heavily accented speech the model does not handle well). The worker records `skip_reason` on the source and does not push anything to ghost.noted. Sometimes the model produces plausible-sounding transcripts that are wrong (hallucinated content on silent segments). The latter is harder to detect automatically, and v0.2 surfaces low-confidence transcripts through ghost.synthd's queue.

## Transcript assembly

The transcript pushed to ghost.noted is a structured text blob. Format is readable prose because ghost.noted's extraction is a text-reading LLM and prose is what it handles best. A sample format for a multi-speaker recording:

```
[Recording captured 2026-03-14 11:02, duration 4m 22s]
[Detected language: English. 2 speakers.]

SPEAKER_00 [0:00]: Okay so I was thinking about the architecture question
you raised last week, about whether the box should do its own sync or
whether the phone should push.

SPEAKER_01 [0:12]: Right, and my view has kind of shifted on that. I
think the box is the archive, the phone is the sensor, but sync direction
is flexible.

SPEAKER_00 [0:21]: That matches what I was thinking. It lets us ship
a small first version where the phone pushes, and we can add pulling
from cloud storage later without rewriting the core.
```

For a single-speaker voice memo, the format is simpler:

```
[Recording captured 2026-04-02 18:47, duration 0m 38s]
[Detected language: English. Single speaker.]

Just realised the extraction prompt needs to handle the empty-entry case
explicitly, otherwise it will hallucinate entries from code files. Add
that to the v0.1 checklist.
```

The format is plain. No markdown, no special structure ghost.noted has to parse. ghost.noted's extraction treats it as text and produces journal entries the same way it extracts from a markdown note.

## v0.2 scope (shade)

Two audio sources for v0.2. More can join later, same principle as the other ingestion daemons.

**What definitely ships in v0.2.**

- Filesystem watcher on a user-configured voice memos folder, pull mode.
- Local `/api/v1/voiced/push` endpoint for any external agent.
- Audio metadata extraction, transcription pipeline with speaker separation where supported, chunking for long recordings.
- Own audio archive with revision tracking.
- Transcript push to ghost.noted with appropriate metadata and bidirectional link.

**What probably ships in v0.2.**

- Mobile voice memo push endpoint. Depends on mobile app v0.2 readiness.

**What probably does not ship in v0.2.**

- Real-time audio streaming from the mobile app (the mobile app captures, the audio file syncs afterwards, no live stream).
- Podcast or long-form content ingestion (technically works, but long recordings take significant compute and v0.2 should not prioritise the scale case).

**What definitely does not ship in v0.2.**

- Real-time transcription of live audio.
- Voice-print speaker identification across recordings.
- Audio embeddings.
- Music recognition (the daemon skips recordings identified as music via the skip_reason path).

## v0.3+ roadmap

**v0.3.** Cloud audio storage via export scripts (rsync from cloud backup, iCloud voice memos exported). Podcast ingestion at scale if the transcription pipeline stabilises.

**v1.0.** Real-time transcription from live mobile audio. Different model class, different architecture (streaming rather than batch), significant v1.0 work. Not committing to it in this doc.

**v1.0+.** Voice-print speaker identification. Would let ghost.synthd cluster speakers across recordings without user confirmation, which is both useful and a privacy concern worth its own design pass.

**v1.0+.** Audio embeddings for similarity search. Unlikely to add enough value to justify the inference cost unless there is a specific use case.

**Never.** Cloud transcription APIs. Local Whisper-family models in 2026 are good enough for v0.2 on most hardware. The privacy trade-off of sending audio to cloud services is not acceptable.

## Open questions

**How detailed should speaker labels be.** v0.2 uses SPEAKER_00, SPEAKER_01 style labels produced by the transcription model. ghost.synthd resolves these to named entities over time. But within a single recording, the model sometimes produces label drift (SPEAKER_00 early in the recording becomes SPEAKER_02 later because a gap confused the model). How aggressive to be about correcting label drift at the ghost.voiced layer versus leaving it for ghost.synthd to handle is an open question. Current lean is leave it to ghost.synthd.

**How to handle very long recordings.** A three-hour meeting or podcast is technically handleable but the transcription cost is high and the storage for the transcript is also high. v0.2 processes these same as any other recording, but the queue priority defaults to low and the user can flag specific long recordings as high-priority. Whether this is good enough or whether long recordings need a different UX flow is unknown until evidence arrives.

**What counts as a "transcribe" trigger for ambient audio.** If the mobile app ever captures ambient audio (for situational context in ghost.cued), not every minute of it should go through full transcription. The current architecture assumes audio arrives as discrete recordings, not as a continuous stream. If that assumption changes, ghost.voiced needs a different intake path and the question of when to transcribe becomes a real design problem. This is v1.0+ work and out of scope for now.

**Transcription model selection.** Whisper has multiple size tiers, and there are alternatives. The right choice depends on the box's compute. Default to something reasonable and let the user configure. Specific default is a v0.2 decision.

**Chunking strategy for long recordings.** Windowed transcription with overlap is the standard approach. How big the windows should be, how much overlap, how to stitch segments at chunk boundaries, all of these are tunable parameters the v0.2 implementation will need to settle with evidence.

**Silent or music segments within a recording.** A voice memo with five seconds of throat-clearing at the start, or a podcast with music intros. The transcription model may produce noise or empty output for these segments. Whether to trim them from the transcript at the ghost.voiced layer or leave them for ghost.noted's extraction to handle is unknown. Current lean is minimal trimming at ghost.voiced (just drop segments with no recognised content), more aggressive handling in ghost.synthd if it becomes necessary.

**Archive retention for deleted audio.** Same question as ghost.noted and ghost.framed. Soft-delete with a default long retention is the plan. Actual policy is v0.2 configuration.

## Rejected approaches

**Transcription producing journal entries directly.** Simpler pipeline would have ghost.voiced produce entries itself, skipping ghost.noted. Rejected because keeping extraction in ghost.noted means the fleet has one extraction pattern rather than three. ghost.voiced produces text, ghost.noted turns text into entries. Same argument as ghost.framed.

**Audio in the user's filesystem without a local copy.** Rejected. Consistent with the other ingestion daemons. The archive has to be local and authoritative, because if the upstream disappears, the memory layer should not disappear with it.

**Translating all transcripts to English at the voiced layer.** Considered for consistency with ghost.framed (which produces English captions) and ghost.noted (which translates at extraction). Rejected because transcription is already a lossy step and layering translation on top of it introduces two places the meaning can degrade. Push the source-language transcript to ghost.noted and let the extraction handle the translation where it already happens.

**Emitting Redis stream events from ghost.voiced.** Alternative to pushing through ghost.noted. Rejected for the same reason as in ghost.framed, it would double the event vocabulary. Push through ghost.noted, keep the downstream contract narrow.

**Being the conversational query interface.** Earlier LocalGhost documents described voiced as the daemon that answers questions. Rejected, that is an app-layer concern, not a daemon. The app talks to ghost.synthd directly over a local API and can invoke a local LLM for natural-language phrasing. No daemon sits in that path.

**Real-time transcription in v0.2.** Would enable new use cases around live audio. Rejected because streaming transcription is a different architectural commitment, needs different model capability, and the v0.2 user's audio content is almost entirely discrete recordings. Batch transcription on files is sufficient.

**Cloud transcription APIs.** Better quality than local in 2026, usually. Violates the manifesto. Not reopening.

**Unified queue with ghost.framed or ghost.noted.** Shared queue across ingestion daemons. Rejected because audio transcription, image description, and text extraction have different resource characteristics (transcription benefits from GPU, image description benefits from GPU, text extraction is cheaper and mostly CPU). Separate queues let each daemon tune its own rate.

## Implementation notes

Written in Go. Transcription model invocation goes through whatever inference runtime the fleet standardises on (possibly a dedicated Whisper runtime rather than the general LLM runtime, depending on what performs better).

Three goroutine groups, same pattern as ghost.noted and ghost.framed. Sync workers, archive writers, transcription workers. Transcription runs one recording at a time in v0.2, concurrency is a v0.3+ optimisation if the evidence supports it.

For long recordings, the transcription worker chunks internally. Each chunk call is logged with its start time, end time, and model output. If a crash happens mid-chunk, the worker resumes from the last completed chunk boundary on restart.

Audio playback through the `/api/v1/voiced/audio/:id` endpoint supports HTTP range requests, which is what browsers and native audio players expect for seeking. The implementation streams audio bytes from the archive file without loading the whole file into memory.

Graceful shutdown, same rules as the other ingestion daemons. Finish in-flight work, commit state, exit cleanly. Queued work survives restart.

Test strategy. Unit tests for audio metadata extraction, queue state, chunking logic, push retry. Integration tests against a small set of fixture audio files with known content and known speaker counts. Quality evaluation against real recordings is manual, runs when the model or chunking strategy changes.

## Versioning

The transcription model identifier and the prompt (or parameter set, since transcription models use structured inputs rather than freeform prompts) are recorded in `transcripts.model_identifier` and `transcripts.prompt_version`. Future analysis can distinguish transcripts produced by different combinations. A user-triggered bulk re-transcription flow in v1.0+ lets the user rebuild transcripts from a newer model.