# FULLY AI GENERATED NOT YET REVIEWD
# ghost.voiced

Audio ingestion and transcription. Accepts encrypted audio chunks streamed from the LocalGhost mobile app over WiFi and a secure tunnel, decrypts them into a bounded RAM buffer, transcribes them with a local speech-to-text model, and hands the enriched transcript to ghost.noted. The daemon supports two paths with different retention semantics. Live-capture sessions are for ambient conversation and are ephemeral. Audio never lands on disk, the phone holds each encrypted chunk only until ghost.voiced confirms the journal entry exists, and the phone deletes it on confirm. Voice-memo sessions are for the user speaking to themselves. The audio is the user's own content and is kept on the NAS for playback and retrieval. The intake protocol is the same for both paths, the retention rules differ at the point they matter.

## Status

Phase 0. No code. This document describes the architecture as of April 2026. ghost.voiced targets shade (v0.2) for the full design described here, and has a planned retirement path as phone-side models mature (see [Why this daemon might retire](#why-this-daemon-might-retire)). The design is a sketch and will be revised substantially when implementation starts. Parts of the design that depend on local transcription model capability are explicitly hedged.

## Purpose

ghost.voiced is the audio layer of LocalGhost's personal memory pipeline. It has one job, executed with two retention policies depending on what the user is recording.

**One job.** Turn recorded sound into text the journal can use.

**Two retention policies.**

1. **Live-capture mode.** The user is recording ambient conversation (a meeting, a negotiation with a vendor, a check-in at a hotel desk). Audio of other people is in the recording. The audio is destroyed at the point it becomes a transcript, and the transcript is destroyed at the point it becomes a journal entry. Nothing survives except the user's own summary of what happened. This is the ephemeral path.

2. **Voice-memo mode.** The user is speaking to themselves (a driving note, a voice journal, a 2 AM idea). The audio is the user's own content and the user owns it. ghost.voiced archives the audio, produces a transcript, pushes the transcript to ghost.noted, and keeps the audio available for playback on request. This is the archival path.

**The mechanisms.** Accept encrypted chunks from the phone. Decrypt into a bounded RAM ring buffer. Transcribe chunk-by-chunk with a local model. Attach context (time, place, speakers where detectable). Hand the enriched transcript to ghost.noted. In live-capture mode, destroy the audio and transcript once the journal entry is committed. In voice-memo mode, reassemble the decrypted chunks into a voice memo file, write it to the memo archive, and keep it retrievable by the app.

Downstream daemons (ghost.noted, ghost.synthd, ghost.cued) see transcripts, not audio, regardless of mode. Audio retrieval in voice-memo mode is a user-facing feature exposed through the mobile and desktop apps, not a data source for the rest of the fleet.

This split is the central architectural commitment. Recordings of other people are what the law protects against and what the architecture refuses to keep. Recordings the user made of themselves are their property and the architecture treats them the way it treats any other user-owned content. The detailed reasoning for the ephemeral path lives in [What the Ghost Owes the People It Overhears](https://localghost.ai/hard-truths/overhears).

## Why this daemon might retire

ghost.voiced exists because in 2026 phones cannot run a continuous Whisper-large transcription pipeline without destroying their batteries, and the NAS can. That is a temporary state of affairs.

Apple Intelligence on iPhone 15 Pro and newer, Gemini Nano on Pixel 8 and newer, and Samsung's on-device Galaxy AI already run reasonable speech-to-text locally. The models are not yet Whisper-large quality and cannot sustain hours of continuous transcription, but they are improving on every release cycle. By the time LocalGhost is at v1.0 or v2.0, a phone will plausibly run transcription quality equivalent to what the NAS runs today, at usable battery cost.

The migration plan, sketched.

**Phase A (now, v0.2).** Phone captures, encrypts, forwards chunks to ghost.voiced on NAS. NAS transcribes. NAS pushes to ghost.noted. This README describes Phase A.

**Phase B (v0.3 or v0.4, when phone transcription gets good enough).** Phone transcribes locally using whatever on-device model the platform offers. Phone sends transcript (not audio) directly to ghost.noted on NAS. ghost.voiced shrinks to just the voice-memo archive path. The live-capture path moves into the mobile app, with ghost.voiced no longer on the critical path for live capture.

**Phase C (v1.0+, when phone extraction gets good enough).** Phone transcribes AND runs ghost.noted's extraction locally. Phone sends the finished journal entry directly to ghost.synthd on NAS for memory integration. ghost.noted shrinks too. The NAS is the durable memory layer and the sync target, but inference has migrated to the edge.

Voice memos keep an archive on the NAS through all phases. The phone is not durable enough to be the sole home for the user's own content (phones get lost, break, get replaced). The NAS is where backups happen. This is the same reason photos sync from the phone to ghost.framed's archive even though the phone is where they originated.

The principle that survives every phase is local-only inference. Local means the NAS today and increasingly the phone tomorrow, but never the cloud. As phones get capable enough, "local" expands to include them. ghost.voiced's job in 2026 is to make the privacy architecture work with the hardware that currently exists. The day phones can run the pipeline well on-device, ghost.voiced retires gracefully. That's a success condition, not a failure.

## What this daemon is not

**Not an archive of live-capture audio.** Live-capture audio (ambient conversation, anything that contains other people's voices as part of the intended recording) is not kept. Once the transcript has been handed to ghost.noted and the journal entry committed, the live-capture audio is unrecoverable. This is by design and is the load-bearing claim of the architecture for that path.

**Voice memos are different and do get archived.** Voice memos are user-initiated recordings of the user talking to themselves. They are the user's own content, like a photo the user took or a note the user wrote. ghost.voiced keeps them, versions them, makes them retrievable. The architectural commitment is about passive capture of other people, not about refusing to store content the user deliberately created for themselves.

**The voice memo path is not a loophole for live capture.** A user cannot tag a conversation with another person as "voice memo" to opt it into archival. Capture mode is set at session open, bound to the session ID, and recorded in the audit trail. Live-capture mode is what the app uses for the gesture-triggered conversation capture flow. Voice-memo mode is what the app uses for the dedicated voice-memo UI. The user's choice of which UI to invoke is their decision to make about their own content.

**Not the conversational interface to the memory layer.** The app talks to ghost.synthd directly for query. ghost.voiced is pure ingestion with a narrow playback surface for voice memos only.

**Not a batch transcription service.** Audio arrives in chunks as the phone records and gets transcribed as it arrives. There is no queue of hours-long recordings to process later.

**Not a speech-to-text API for other applications.** ghost.voiced serves the LocalGhost memory pipeline and the LocalGhost app layer. Its endpoints are authenticated and localhost-bound.

**Not a source of audio embeddings.** No "find recordings that sound like this one" capability, for either path.

**Not a voice-print identifier.** The daemon does not attempt to identify who a speaker is across sessions. Speakers within a single session may be separated if the model supports it. Cross-session identity resolution is ghost.synthd's problem and is handled from text, not audio.

**Not a speech synthesis service.** ghost.voiced consumes audio, never produces it.

## Position in the fleet

ghost.voiced sits to the side of ghost.noted, parallel to ghost.framed, with two intake paths sharing a common protocol.

```
PHONE (LocalGhost mobile app)
  |
  |  Capture (user gesture: back tap, action button, quick tile,
  |  or dedicated voice-memo UI)
  |  Encrypt in memory (XChaCha20-Poly1305, session key wrapped to NAS public key)
  |  Buffer in excluded-from-backup directory (iOS tmp/, Android no-backup cache)
  |  Forward over WiFi and a secure tunnel to NAS whenever connectivity exists
  |  Retry with idempotent chunk IDs until processing-complete confirm returns
  |  Delete local encrypted chunk on processing-complete confirm
  |
  v
NAS / ghost.voiced
  |
  |  POST /api/v1/voiced/session/open     (phone opens a session, declares capture_mode)
  |  POST /api/v1/voiced/push/chunk       (phone streams encrypted chunks)
  |  POST /api/v1/voiced/session/close    (phone signals the session is complete)
  |
  |  Common steps for both modes:
  |    1. Receive encrypted chunk, verify signature, dedupe on chunk ID
  |    2. Decrypt into RAM ring buffer (tmpfs, bounded seconds-to-minutes)
  |    3. Hand bytes to local transcription model as they arrive
  |    4. Enrich transcript segments with time, place, detected speakers
  |    5. POST assembled transcript to ghost.noted inbox
  |    6. On ghost.noted journal-entry-committed confirm, respond
  |       processing-complete to phone
  |
  |  Mode-specific steps at session close:
  |    live-capture mode:
  |      - Zero the RAM ring buffer
  |      - Do not write audio to disk
  |      - Destroy the assembled transcript
  |    voice-memo mode:
  |      - Reassemble decrypted chunks into a voice memo file
  |      - Write to /var/lib/localghost/voiced/memos/<year>/<month>/<hash>.<ext>
  |      - Keep the audio retrievable for playback
  |      - Transcript is also destroyed; the audio is the user asset
  |
  v
ghost.noted (treats transcript as text, produces journal entries)
  |
  v
rest of the fleet (ghost.synthd, ghost.cued, etc.)
```

The contract ghost.voiced maintains is the same for both modes at the protocol level. Every chunk accepted either becomes part of a committed journal entry or gets logged as skipped (silent, model error, aborted) in the session log. The modes diverge at session close in their handling of the audio. Live-capture destroys it. Voice-memo archives it.

If the daemon crashes mid-session, the phone re-sends on restart because processing-complete was never returned. For live-capture that means fresh transcription from chunks. For voice-memo that means fresh transcription plus fresh archive write, and idempotent dedupe ensures a resent chunk is not added twice to the reassembled file.

## Responsibilities

**Chunk intake, single protocol.** One endpoint, `POST /api/v1/voiced/push/chunk`, accepts encrypted audio chunks from the mobile app. The session open call declares the capture mode (`live` or `memo`), which determines the retention policy applied at session close. Both endpoints authenticate per-device with pinned TLS over the secure tunnel.

**Chunk decryption.** Each chunk is encrypted with a session key that was wrapped to the NAS public key at session start. ghost.voiced unwraps the session key on session-start, then decrypts subsequent chunks as they arrive. Decrypted audio lives only in the RAM ring buffer regardless of mode.

**Deduplication / idempotency.** Every chunk carries a content-hashed chunk ID (SHA-256 of encrypted bytes) plus a session ID. ghost.voiced keeps a short-lived dedupe table keyed on `(session_id, chunk_id)` in Redis with a TTL of a few hours. A chunk that arrives twice is processed once, and the second arrival returns the same processing-complete confirm as the first. This handles the case where the first confirm was lost in transit. For voice-memo mode, this also prevents resends from being appended twice to the archived file.

**Ring buffer management (both modes).** Audio lives in a tmpfs-backed ring buffer sized to seconds-to-minutes. Bytes are written in on chunk arrival, read out by the transcription worker, and the backing memory is zeroed as it is consumed. The overwrite is done by ghost.voiced directly, no separate cleanup daemon. This applies to both modes because the transcription pipeline is the same regardless of what the archive policy is at session close.

**Transcription.** The core processing step for both modes. Audio in the RAM buffer plus a structured parameter set goes to the local transcription model (Whisper family in 2026, or equivalent). The response is transcript text with timestamp markers per segment, and where the model supports it, speaker labels (SPEAKER_00, SPEAKER_01). ghost.voiced does not name speakers. Naming is entity-resolution work and lives in ghost.synthd.

**Chunk-aware stitching.** Because audio arrives in chunks, the transcription model may produce segment boundaries that do not line up with speech boundaries. The daemon carries a small overlap between consecutive chunks (a few hundred milliseconds) and stitches adjacent transcriptions, dropping repeated content at the overlap. The stitching strategy is recorded in transcript metadata.

**Session context enrichment.** When the phone opens a session, it sends context the NAS cannot infer. GPS location (if available and permitted), a user-supplied label if the capture was prompted with one, time zone. The NAS attaches this context to the transcript so ghost.noted's extraction produces entries like "at the Hotel Duomo" rather than "at an unknown location." Voice-memo sessions get the same enrichment so the memo metadata knows where it was recorded.

**Transcript assembly.** Once the session ends (phone signals session-close, or a timeout expires without new chunks), the daemon assembles the full transcript text from the stitched segments. Plain prose with speaker labels and relative timestamps. The assembled text is what ghost.noted sees.

**Push to ghost.noted.** The assembled transcript goes to ghost.noted's inbox endpoint. The push carries `source_hint` set to `ghost.voiced`, a ghost.voiced session ID, the session start time, the session duration, detected language, and speaker count. For voice-memo sessions, the push also includes a voice memo ID so the journal entry can link back to the memo. ghost.noted runs its extraction on the transcript. Once the journal entry is committed, ghost.noted returns its source ID. ghost.voiced stores `(session_id, noted_source_id)` in the session-log row.

**Processing-complete confirm back to phone.** After ghost.noted commits the journal entry, ghost.voiced sends processing-complete to the phone for every chunk in the session. Only then does the phone delete its local encrypted copies. Before that point, the chunks sit on the phone as the only surviving copy of the audio, encrypted, excluded from backup.

**Session close, live-capture mode.** The transcript is destroyed on the NAS after processing-complete has been sent to the phone. The RAM ring buffer is zeroed. Nothing audio-derived persists on the NAS except the session-log row (metadata only) and the journal entry in ghost.noted.

**Session close, voice-memo mode.** The decrypted chunks are reassembled in order into a voice memo file (format follows the source codec, typically Opus or AAC), written to the voice memo archive under `/var/lib/localghost/voiced/memos/<year>/<month>/<hash>.<ext>`, and a `voice_memos` row is written linking the session, the archive path, and the journal entry. The transcript is destroyed on the NAS (the archived audio plus the journal entry are what survives). Processing-complete is sent to the phone after the archive write is confirmed.

**Voice memo retrieval.** For voice-memo mode only, ghost.voiced exposes `GET /api/v1/voiced/memos/:id` for audio playback and `GET /api/v1/voiced/memos` for listing. Playback supports HTTP range requests so the app can seek. Authenticated, localhost-bound, served from the memo archive.

**Voice memo deletion.** The user deletes a voice memo through the app. ghost.voiced removes the file from the archive, marks the `voice_memos` row `deleted_at`, and cascades a deletion signal through ghost.noted so the linked journal entry is also removed.

**Session log.** For each session, ghost.voiced writes a row with session_id, capture_mode, start_time, duration, chunk_count, detected_language, speaker_count, transcription_model, prompt_version, noted_source_id, voice_memo_id (if applicable), and outcome (committed, skipped_silent, skipped_model_error, aborted). The session log is text-only metadata and contains no audio-derived content. It exists so ghost.watchd and the user have an audit trail of what the daemon did.

**Change tracking.** Live-capture sessions have nothing to re-sync and nothing to update. For voice memos, the user can re-import a memo file (corrected edit, different format); ghost.voiced detects the new content-hash, writes the new archive entry, re-runs transcription, pushes an updated transcript to ghost.noted, and marks the old memo row `superseded_at`. Same pattern ghost.framed uses for image updates.

**Deletion.** Live-capture session deletion cascades through ghost.noted and clears the session-log row. Voice-memo deletion removes the archive file, marks the memo row, and cascades the journal entry deletion through ghost.noted.

## Non-responsibilities

**No retention of live-capture audio.** See above, repeatedly. This is the central architectural commitment for the ephemeral path. The voice-memo path is separate and does retain audio because voice memos are the user's own content.

**No playback of live-capture audio.** The app cannot retrieve audio from a live-capture session because none exists. Playback is only offered for voice memos.

**No re-transcription of live-capture audio.** Cannot retranscribe what no longer exists. If the user later wants a better transcript of a conversation, they cannot rewind time. Voice memos can be re-transcribed because the audio persists.

**No real-time user-facing UI.** The daemon does not present an interactive transcription view during capture. Transcripts are assembled at session close and pushed to ghost.noted. The app can poll session status during capture if it wants to show the user that recording is in progress, but intermediate transcript contents are not exposed.

**No entity resolution.** Speakers are labelled by the model as SPEAKER_00, SPEAKER_01, etc. Resolving them to named people across sessions is ghost.synthd's work, operating on the text of journal entries.

**No journal entry extraction.** ghost.voiced produces a transcript. ghost.noted runs extraction on the transcript to produce journal entries.

**No voice-print identification.** Across sessions, speakers are not matched by voice. For live-capture audio the data doesn't exist to match against. For voice memos, the audio exists but the architecture does not derive voice prints from it.

**No audio embeddings.** No similarity search over audio, for either path.

**No speech synthesis.** The daemon consumes audio, never produces it.

**No outbound inference beyond transcription.** The transcription model call is the only inference ghost.voiced performs. Any other inference lives in a different daemon.

**No cloud transcription APIs.** Ever. Cloud transcription means sending audio to a third-party server. For live-capture that's a privacy catastrophe. For voice memos it would violate the local-first manifesto regardless.

**No passive always-on capture.** ghost.voiced only receives audio when a phone session is active. There is no wake-word hardware and no continuous background listening. Session starts require an explicit user gesture in the mobile app, in either mode.

## The link to ghost.noted

The link from ghost.voiced session to ghost.noted source is bidirectional at the ID level. ghost.voiced's session-log row stores the ghost.noted source ID returned by the inbox push. ghost.noted's source row stores the ghost.voiced session ID (and, for voice-memo sessions, the voice memo ID) in its metadata JSONB.

For live-capture sessions, the link enables ghost.noted to know (for the audit trail) that a given journal entry originated from a voice capture session rather than from a text note or an image caption. It does not enable audio retrieval because no audio exists to retrieve.

For voice-memo sessions, the link additionally resolves to the memo archive. ghost.noted can present a journal entry with a "play the original voice memo" action. ghost.voiced serves the audio through the playback endpoint, the app plays it back.

When a journal entry derived from a live-capture session is deleted, ghost.noted cascades the deletion through its normal flow. The ghost.voiced session-log row is left in place (it is retention-window-bounded metadata) but is marked `entry_deleted_at`.

When a journal entry derived from a voice-memo session is deleted, the voice memo itself is also deleted (the user's intent is usually to delete the memory in full). ghost.voiced removes the archive file, marks the `voice_memos` row, and the session-log row records the deletion. The user can choose to delete the journal entry while keeping the memo (treated as a journal-only action, the memo row stays, the archive file stays).

## Data model

Four tables. The sessions and session_state tables handle the intake protocol for both modes. The voice_memos and voice_memo_revisions tables handle the archival path. Chunk idempotency lives entirely in Redis and is not persisted to Postgres.

```sql
-- One row per capture session (live or memo).
CREATE TABLE sessions (
  id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  device_id             TEXT NOT NULL,              -- Phone/app instance identifier
  capture_mode          TEXT NOT NULL,              -- 'live' | 'memo'
  started_at            TIMESTAMPTZ NOT NULL,
  ended_at              TIMESTAMPTZ,
  duration_seconds      NUMERIC(10, 2),
  chunk_count           INTEGER NOT NULL DEFAULT 0,
  detected_language     TEXT,
  speaker_count         INTEGER,
  location_context      JSONB,                      -- {lat, lng, accuracy_m, source} or null
  user_label            TEXT,                       -- Optional label from capture gesture
  transcription_model   TEXT,
  prompt_version        TEXT,
  noted_source_id       UUID,                       -- ghost.noted source ID, once committed
  voice_memo_id         UUID,                       -- voice_memos row, if capture_mode = 'memo'
  outcome               TEXT NOT NULL DEFAULT 'in_progress',
                                                     -- 'in_progress' | 'committed'
                                                     -- | 'skipped_silent' | 'skipped_model_error'
                                                     -- | 'aborted'
  entry_deleted_at      TIMESTAMPTZ                  -- Set if ghost.noted deletes the derived entry
);

CREATE INDEX idx_sessions_device_started ON sessions(device_id, started_at DESC);
CREATE INDEX idx_sessions_outcome ON sessions(outcome);
CREATE INDEX idx_sessions_mode_started ON sessions(capture_mode, started_at DESC);
CREATE INDEX idx_sessions_noted_source_id ON sessions(noted_source_id);
CREATE INDEX idx_sessions_voice_memo_id ON sessions(voice_memo_id);

-- Per-session transcription state, used while a session is in progress.
-- Rows are deleted on session close.
CREATE TABLE session_state (
  session_id            UUID PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
  wrapped_session_key   BYTEA NOT NULL,              -- The session symmetric key, wrapped to NAS public key
  last_chunk_received   TIMESTAMPTZ,
  stitched_segments     JSONB,                       -- In-progress transcript segments before assembly
  memo_reassembly_path  TEXT                         -- Scratch path used during memo mode reassembly
);

-- One row per archived voice memo.
CREATE TABLE voice_memos (
  id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  session_id            UUID NOT NULL REFERENCES sessions(id),
  archive_path          TEXT NOT NULL,               -- Relative path within the memo archive
  audio_hash            BYTEA NOT NULL,              -- SHA-256 of the reassembled audio file
  mime_type             TEXT NOT NULL,               -- audio/opus, audio/aac, audio/mp4, etc.
  file_size             BIGINT NOT NULL,
  duration_seconds      NUMERIC(10, 2),
  sample_rate           INTEGER,
  channel_count         INTEGER,
  codec                 TEXT,
  captured_at           TIMESTAMPTZ NOT NULL,        -- When the user recorded it
  archived_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  noted_source_id       UUID,                        -- Journal entry the memo became
  deleted_at            TIMESTAMPTZ,
  audio_metadata        JSONB NOT NULL DEFAULT '{}',
  superseded_at         TIMESTAMPTZ                  -- Set if re-imported / replaced
);

CREATE INDEX idx_voice_memos_captured ON voice_memos(captured_at DESC);
CREATE INDEX idx_voice_memos_hash ON voice_memos(audio_hash);
CREATE INDEX idx_voice_memos_deleted ON voice_memos(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE UNIQUE INDEX idx_voice_memos_archive_path ON voice_memos(archive_path);

-- Revision history for voice memos that get re-imported.
CREATE TABLE voice_memo_revisions (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  voice_memo_id  UUID NOT NULL REFERENCES voice_memos(id) ON DELETE CASCADE,
  audio_hash     BYTEA NOT NULL,
  archive_path   TEXT NOT NULL,
  seen_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  change_type    TEXT NOT NULL                       -- 'created', 'updated', 'restored'
);

CREATE INDEX idx_voice_memo_revisions_memo ON voice_memo_revisions(voice_memo_id, seen_at);
```

**Why sessions unifies both modes.** The intake protocol is identical. Keeping a single `sessions` table with a `capture_mode` discriminator means both modes share chunk dedupe, session state, and audit trail machinery. Splitting into `live_sessions` and `memo_sessions` would duplicate most of the schema.

**Why `voice_memos` is separate from `sessions`.** A session is a protocol-level concept (a phone stream from open to close). A voice memo is a user-facing content object (the audio file with its own lifecycle). One session produces zero or one voice memos. A memo can survive re-imports as new sessions, each of which writes a new revision. The two have different cardinalities and different queries run against them.

**Why `voice_memo_revisions`.** If the user re-imports the same memo (edited in a different app, re-exported at a different bitrate), ghost.voiced detects the new audio hash and writes a new revision. The old revision is retained in the revisions table and optionally the old archive path is kept, so history is preserved without overwriting.

**Why the voice memo archive is on the filesystem, not in Postgres.** Audio files are large. Postgres is not the right tool. Filesystem storage gives standard backup tools the right shape to work with, and lets the app stream audio through the playback endpoint without loading whole files into Postgres memory.

**No transcripts table.** Transcripts are not retained in either mode. In live-capture mode this is the central commitment. In voice-memo mode the decision is different. The user already has the audio and can have a transcript regenerated on demand if they ever want one, so keeping a transcript as well would be redundant storage. The transcripts used by ghost.noted's extraction exist only in RAM during the session and are destroyed at session close.

**Why chunk idempotency lives in Redis only.** Chunks arrive at the rate the phone records. Postgres writes on every chunk are unnecessary overhead. Redis with a TTL-keyed hash keyed on `(session_id, chunk_id)` is the right tool and gives us what we need, a fast dedup check on every incoming chunk and automatic cleanup a few hours later. No durability backstop is needed. If Redis is down, chunk handling stops and the phone retries, which is the correct behaviour. If Redis loses state on restart (unlikely but possible on a fresh deploy), the worst case is a few chunks get re-processed, which is a short-lived annoyance, not a correctness issue.

## Interfaces

**No direct event emission.** Same as ghost.framed. ghost.voiced pushes to ghost.noted, which owns the event vocabulary.

**HTTP endpoints, all on localhost, under `/api/v1/voiced/`.** The per-daemon versioned path matches the convention across the fleet.

```
POST /api/v1/voiced/session/open            - open a new capture session (declares capture_mode)
POST /api/v1/voiced/push/chunk              - stream an encrypted chunk into an open session
POST /api/v1/voiced/session/close           - signal the phone has finished capturing this session

GET  /api/v1/voiced/sessions                - recent sessions (metadata, for the app's history UI)
GET  /api/v1/voiced/sessions/:id            - session metadata for one session

GET  /api/v1/voiced/memos                   - list voice memos (voice-memo path only)
GET  /api/v1/voiced/memos/:id               - voice memo metadata and transcript link
GET  /api/v1/voiced/memos/:id/audio         - stream audio bytes (range requests, authenticated)
DELETE /api/v1/voiced/memos/:id             - delete a voice memo (cascades journal entry deletion)

GET  /api/v1/voiced/admin/healthz           - liveness
GET  /api/v1/voiced/admin/readyz            - readiness, checks Postgres, Redis, transcription runtime, ring buffer, memo archive
GET  /api/v1/voiced/admin/config            - current daemon config (authenticated)
POST /api/v1/voiced/admin/config            - update daemon config (authenticated, triggers reload)
```

**Session open.** Phone sends `{device_id, capture_mode, started_at, wrapped_session_key, location_context?, user_label?}` where `capture_mode` is `live` or `memo`. NAS unwraps the session key, stores it in `session_state`, returns the `session_id`.

**Push chunk.** Phone sends `{session_id, chunk_id, sequence_number, encrypted_bytes}`. NAS verifies the chunk_id matches the SHA-256 of encrypted_bytes, checks dedupe, decrypts with the session key, writes into the RAM ring buffer, returns 202 Accepted. Processing happens asynchronously. For voice-memo mode, the decrypted chunk bytes are also tee'd into a scratch reassembly path on the NAS filesystem (separate from the memo archive, cleaned up on session close or failure).

**Session close.** Phone sends `{session_id}`. NAS drains the remaining ring buffer through the transcription model, stitches the final transcript, pushes to ghost.noted, waits for ghost.noted confirm. For live-capture mode, the NAS destroys the transcript and zeroes the ring buffer, then returns `{noted_source_id}`. For voice-memo mode, the NAS promotes the scratch reassembly file to the memo archive, writes the `voice_memos` row, destroys the transcript, and returns `{noted_source_id, voice_memo_id}`. The phone uses the response as the processing-complete signal and deletes all local chunks for the session.

**Voice memo playback.** `GET /api/v1/voiced/memos/:id/audio` streams the archived voice memo. Range requests are required for audio seeking in the app's playback UI. Authenticated, localhost-bound. The app reaches this endpoint through the same tunnel the capture path uses.

**Voice memo listing.** `GET /api/v1/voiced/memos` returns paginated memo metadata (id, captured_at, duration, user_label, noted_source_id). The app uses this for a voice memos list view. Supports filtering by date range and text search (search runs against the journal entry the memo produced, via the `noted_source_id`, not against the audio).

**Voice memo deletion.** `DELETE /api/v1/voiced/memos/:id` removes the archive file, marks the `voice_memos` row `deleted_at`, cascades a deletion signal through ghost.noted for the linked journal entry, returns 204.

**Database access.** Postgres read access on `sessions` and `voice_memos` is exposed to ghost.synthd, the app layer, and ghost.watchd through per-daemon roles. `session_state` and `voice_memo_revisions` are ghost.voiced private. ghost.voiced is the only daemon with write access to any of its tables. The memo archive filesystem is owned by ghost.voiced's process user. Chunk idempotency state lives in Redis and is ghost.voiced's responsibility to manage.

## The transcription parameters

Modern transcription models take structured inputs, not freeform prompts. The parameters ghost.voiced passes:

- Language: auto-detect on first chunk, lock to detected language for remaining chunks in the session
- Speaker separation: enabled when the model supports it
- Timestamp precision: per-segment. Per-word doubles output size for marginal gain in v0.2.
- Context prompt: any session-level context that would help domain vocabulary (e.g., a user-supplied label like "coffee with J.", if present)

**Multilingual.** Transcription produces output in the source language. ghost.voiced does not translate. The source-language transcript goes to ghost.noted with a `detected_language` metadata field, and ghost.noted's extraction handles translation through its normal multilingual path. Consistent with ghost.framed.

**Failure modes.** The model sometimes produces empty transcripts for audio it cannot recognise (music, silence, heavily accented speech the model does not handle well). The session outcome is recorded as `skipped_silent` or `skipped_model_error` and nothing is pushed to ghost.noted. Sometimes the model produces plausible-sounding transcripts that are wrong (hallucinated content on silent segments). This is harder to detect automatically. v0.2 accepts the transcript as produced and relies on the user noticing wrong journal entries in the ghost.noted UI.

## Transcript format

The transcript pushed to ghost.noted is plain prose with speaker labels and relative timestamps. ghost.noted's extraction treats it as text.

Multi-speaker:

```
[Session captured 2026-03-14 11:02, duration 4m 22s, Milan]
[Detected language: English. 2 speakers.]

SPEAKER_00 [0:00]: Okay so I was thinking about the architecture question
you raised last week, about whether the box should do its own sync or
whether the phone should push.

SPEAKER_01 [0:12]: Right, and my view has kind of shifted on that. I
think the box is the archive, the phone is the sensor, but sync direction
is flexible.

SPEAKER_00 [0:21]: That matches what I was thinking.
```

Single-speaker:

```
[Session captured 2026-04-02 18:47, duration 0m 38s, home]
[Detected language: English. Single speaker.]

Just realised the extraction prompt needs to handle the empty-entry case
explicitly, otherwise it will hallucinate entries from code files.
```

No markdown, no structured format ghost.noted has to parse. Plain text.

## v0.2 scope (shade)

**What definitely ships.**

- `POST /api/v1/voiced/push/chunk` intake from the mobile app
- Session open/close lifecycle with wrapped-key exchange and `capture_mode` selection
- Chunk decryption into RAM ring buffer (tmpfs-backed, bounded seconds-to-minutes)
- Dedupe via content-hashed chunk IDs with Redis-backed state
- Chunk-by-chunk transcription with a local Whisper-family model
- Chunk-aware stitching of segments across chunk boundaries
- Transcript assembly and push to ghost.noted
- Processing-complete confirm back to the phone for local chunk deletion
- Live-capture mode with full audio destruction at session close
- Voice-memo mode with archive writes, `voice_memos` row, playback endpoint
- Voice memo list / get / delete endpoints
- Voice memo revisions on re-import
- Session log with metadata and audit trail
- Admin endpoints (healthz, readyz, config)

**What probably ships.**

- Speaker separation where the model supports it. Depends on the chosen model's capabilities.
- Voice memo text search (backed by ghost.synthd's index of the derived journal entries).

**What does not ship in v0.2.**

- Voice-print speaker identification across sessions (neither mode)
- Audio embeddings or similarity search (neither mode)
- Music or non-speech content recognition beyond the skip path
- Long-form batch transcription of external audio archives (separate use case)
- Sync from the phone's built-in voice memos app (iOS Voice Memos, etc.). v0.2 scopes to memos captured through the LocalGhost app itself.

## v0.3+ roadmap

**v0.3.** Sync from phone's built-in voice memos app (iOS Voice Memos, Android equivalents). Gives the user a path to pull pre-existing memos into ghost.voiced's archive without re-recording them. Better speaker separation if the initial v0.2 model performed poorly. Possibly per-word timestamps for richer playback UI.

**v0.4 (phase B of retirement plan).** On-device transcription in the mobile app, when phone-side models reach usable quality and battery cost. Live-capture path moves into the app. Phone sends transcript (not audio) to ghost.noted directly. ghost.voiced shrinks to the voice-memo archive path only. See [Why this daemon might retire](#why-this-daemon-might-retire).

**v1.0+ (phase C of retirement plan).** On-device extraction in the mobile app, when phone-side models can run the ghost.noted extraction prompt at quality parity with the NAS. Phone sends finished journal entries directly to ghost.synthd. Both ghost.voiced and ghost.noted (for voice inputs) retire for the live path. Voice-memo archive remains on the NAS.

**v1.0+.** Voice-print speaker identification across voice-memo sessions, gated on a user-configured flag. Would let ghost.synthd cluster speakers across memos automatically. Privacy concerns worth their own design pass, especially around whether voice prints derived from voice memos are retained even after the source memo is deleted.

**v1.0+.** Voice memo editing UI (trim, split, merge) with revision tracking. Would let users clean up memos while keeping the history traceable.

**Never.** Cloud transcription APIs. Persistent archive of live-capture audio. Playback of live-capture audio. Voice-print identification for live-capture sessions (by definition there is no audio retained to derive prints from). These are architectural commitments, not optimisation targets.

## Open questions

**Ring buffer sizing.** Seconds to minutes is the intent. Concrete sizing depends on the rate the phone pushes and the rate the transcription model drains. If transcription falls behind the phone's push rate, the ring buffer fills and either the daemon applies back-pressure on the phone (which then buffers locally for longer) or the daemon drops chunks (which is worse). v0.2 uses back-pressure. The specific buffer-size-per-session value is a tuning parameter to settle with evidence.

**Handling disconnect mid-session.** If the phone uploads chunks 1-5 and then loses connectivity, the session sits open on the NAS waiting for more chunks. A timeout eventually fires (default 10 minutes of no new chunks) and the session closes with whatever chunks arrived, transcribed and pushed as a partial session. The phone still holds chunks 6-N encrypted. When the phone has connectivity again, it sees the session is closed, opens a new session, uploads the remaining chunks as a fresh session. For live-capture this is acceptable (two journal entries for one conversation, the user can merge them in ghost.noted). For voice-memo this is worse because the memo is split across two archive files, and the user probably wanted one. An alternative is to let memo sessions pause/resume with a longer timeout, at the cost of more complex state. Unresolved.

**User gesture for session open.** Architecturally, ghost.voiced does not care how the user triggers a session. The mobile app does. The app's gesture design (back tap, Action Button, quick settings tile, dedicated memo UI) determines how sessions are structured. Live-capture sessions might use the one-gesture-start, one-gesture-stop pattern. Voice-memo sessions probably use a dedicated recording UI with explicit start and stop buttons. These decisions live in the mobile app README, not here, but they affect the shape of session traffic ghost.voiced sees.

**Voice memo size limits.** A two-hour lecture recorded as a voice memo is a lot of audio to stream and transcribe in one session. Memory pressure on the ring buffer is real. v0.2 caps individual voice memo sessions at a conservative duration (probably 30 minutes, to be tuned) and the app shows a warning as the session approaches the cap. Raising the cap in v0.3+ requires evidence the transcription stack handles it.

**Voice memo format.** What format does ghost.voiced archive the memo in. Opus is efficient and lossless-for-voice at moderate bitrates, supported by all modern platforms. AAC is broadly supported. Keeping whatever the phone recorded is simplest but means heterogeneous archive contents. v0.2 likely keeps the phone's native format (iOS records to .m4a/AAC, Android platform choice). Transcoding to Opus for archive uniformity is a v0.3+ question.

**Confirmation semantics.** Current design is two-phase, with receipt (chunk arrived) followed by processing-complete (chunk has become a journal entry, or for memo mode, is also in the archive). The phone holds local copies until processing-complete. Whether this is the right trade-off, or whether there are cases where the phone should delete on receipt (phone storage pressure), is unresolved. Conservative default (processing-complete) is what v0.2 ships.

**Tunnel establishment.** How the phone and NAS authenticate the tunnel. The candidates are WireGuard with pre-shared keys distributed during first-run pairing, Tailscale with the NAS as a tailnet node, or a LocalGhost-specific pairing protocol. The choice affects UX (how the user adds a new device), security posture (key rotation, revocation), and operational burden. Unresolved in detail, v0.2 implementation will pick one and document the trade-off.

**Voice memo playback on the phone.** The phone has the memo's encrypted chunks locally until processing-complete. After that, the memo lives on the NAS. Does the phone cache a copy for offline playback, or always stream from the NAS when the user wants to hear it back. Streaming is simpler and keeps the phone's storage low. Caching is faster and works offline. v0.2 streams; a local cache is a v0.3+ enhancement if the evidence supports it.

**What counts as "audio" for the excluded-from-backup directory.** iOS `tmp/` and Android no-backup caches are the platform answers. But a jailbroken or rooted device might not respect the no-backup flags. The architecture is built around "the platform honours the no-backup flag." On hostile OS versions this assumption is weaker. Acknowledged limitation, not a v0.2 solve.

**Transcription model selection.** Whisper has multiple size tiers. Alternatives exist. The right choice depends on NAS compute. Default to something reasonable (probably `whisper-large-v3-turbo` or equivalent). Let the user configure. Settled in v0.2 implementation with evidence.

**Silent or music segments within a session.** Captured audio can include walking between rooms, background music, silence. The model produces noise or empty output. Whether to trim at the ghost.voiced layer or push through and let ghost.noted's extraction ignore it is unresolved. Current lean is minimal trimming at ghost.voiced (drop segments the model itself labels as no recognised content), more aggressive handling in ghost.synthd if needed.

**Audit log retention.** Session metadata retention defaults to 30 days for live-capture sessions, longer for voice-memo sessions (because the memo itself is archival and the session metadata is the audit trail for it). Whether 30 days is the right window for live-capture is a v0.2 configuration decision. The metadata contains no audio-derived content, only the fact that a session happened and what journal entry it produced, so retention is not the same privacy concern as audio or transcript retention.

## Rejected approaches

**Persistent archive of live-capture audio.** The most obvious alternative design and the one the original README draft was built around for everything. Rejected for the live-capture path because the whole architectural argument of the memory layer depends on ambient conversation audio not persisting. The audio exists long enough to be transcribed, and then it is gone.

**Playback of live-capture audio.** Related to the above. The UI cannot play back what does not exist, and rebuilding the live-capture path to support playback would unwind every property the design depends on.

**Unified retention policy across both modes.** The two alternatives considered were making both modes ephemeral (losing voice memos as user content) or making both modes archival (losing the legal argument for live capture). Rejected. The two use cases have different owners of the content and different privacy implications, and the architecture is stronger for recognising that instead of flattening it.

**Letting the user tag a live-capture session as voice-memo after the fact.** Considered and rejected. Capture mode is bound to the session at open, recorded in the audit log, and cannot be changed. The reason is that if mode could be switched post-facto, a user could start a conversation in live-capture mode (thinking "I won't keep this"), then halfway through decide to archive it, and now the other person's audio is on disk against the architecture's promise. Mode is committed at session open.

**Batch transcription of files users drop into a folder.** Rejected as a primary path. Voice memo imports go through the same chunked protocol as live capture, because that's the protocol the daemon implements. Dropping files into a filesystem watch folder is not supported. The `memos` endpoints exist for listing and retrieval of archived memos, not for intake of external audio archives.

**Cloud transcription APIs.** Better quality than local in 2026, usually. Sends audio to third-party servers. For live-capture this is a privacy catastrophe. For voice memos it would violate the local-first manifesto regardless. Not reopening.

**Doing transcription in the mobile app (for v0.2).** Would move the compute to the phone. Deferred for v0.2 because phone-side transcription models in 2026 are not yet at quality or battery parity with what a NAS can run. The NAS has the compute to run a good model continuously and phone batteries are not built for that. This is explicitly a temporary arrangement, not a permanent architectural commitment. See [Why this daemon might retire](#why-this-daemon-might-retire). Phase B of the retirement plan moves live-capture transcription into the app when evidence supports it.

**Transcription producing journal entries directly (for v0.2).** Simpler pipeline. Deferred for v0.2 because keeping extraction in ghost.noted means the fleet has one extraction pattern instead of several, and ghost.noted's extraction is tuned for the journal-entry shape regardless of the source of the text. Like the above, this is a temporary separation. Phase C of the retirement plan collapses the pipeline further, with the phone producing journal entries directly when phone-side extraction reaches quality parity.

**Emitting Redis stream events from ghost.voiced.** Alternative to pushing through ghost.noted. Rejected because it would double the event vocabulary. Push through ghost.noted, keep the downstream contract narrow.

**Always-on ambient capture from the phone.** Explicitly rejected, both modes. The whole point of the architecture is that capture is gesture-triggered. If the phone is not actively streaming a session, ghost.voiced has nothing to do.

**Keeping the transcript after processing (live-capture mode).** Considered. A short retention window (hours, not days) would let the user sanity-check the transcript before it disappears. Rejected because "a recording of what the person said" is the thing the legal architecture is built to not retain, even briefly at rest. Ephemerality at rest is stricter than ephemerality in flight. If the transcript exists on disk for even a few hours, it is subject to backup, accidental sync, and forensic recovery in a way a RAM-only transcript is not.

**Keeping the transcript alongside the voice memo (voice-memo mode).** Considered. The user might want to search the text of their voice memos. Rejected because the journal entry derived from the memo is already in ghost.noted and is searchable. Duplicating the transcript would be redundant storage with no added capability. If the user really wants the verbatim transcript, it can be regenerated from the archived audio.

**Cross-session voice-print identification in v0.2.** Would let ghost.synthd cluster speakers across sessions automatically. Rejected for v0.2 because it requires retaining a voice-print per speaker, which is a biometric identifier that creates obligations under GDPR Article 9 and similar laws in other jurisdictions. Worth its own design pass before it ships. One additional wrinkle makes this harder. Voice prints derived from live-capture audio would retain identifying data about third parties even after the audio itself is gone, which partially defeats the live-capture ephemerality argument. A v1.0+ design has to address that directly.

**Unified queue with ghost.framed or ghost.noted.** Shared queue across ingestion daemons. Rejected because transcription, image description, and text extraction have different resource characteristics (transcription is heavy GPU work with real-time-ish pacing, image description is heavy GPU work but batch, text extraction is cheap). Separate queues let each daemon tune its own rate.

## Implementation notes

Written in Go. Transcription model invocation goes through whatever inference runtime the fleet standardises on, probably a dedicated Whisper-family runtime rather than the general LLM runtime, for performance reasons.

Four goroutine groups run the daemon. The session manager handles opens, routes chunks to workers, and drives closes. The transcription worker pulls from the ring buffer, runs the model, emits segments, and hands them to the inbox pusher, which assembles transcripts and pushes them to ghost.noted with retry. The memo archiver, which only runs for `capture_mode='memo'` sessions, reassembles chunks from the scratch path into the memo archive file on successful session close.

The RAM ring buffer is a `chan []byte` backed by a fixed tmpfs-mounted scratch area. Writes are non-blocking with back-pressure to the phone endpoint. Reads are consumed by the transcription worker. On successful transcription of a segment, the worker zeroes the backing bytes explicitly before releasing the buffer entry back to the pool. Go does not zero memory on free by default, so the zeroing is explicit.

For voice-memo sessions, decrypted chunks are additionally written to a session-scoped scratch file on the NAS filesystem (separate path from the memo archive). This is the only case where decrypted audio lands on NAS disk, and it only happens for sessions where the user has explicitly opted into archival. The scratch file is promoted to the memo archive on successful session close and journal entry commit. On session failure or abort, the scratch file is deleted.

Cryptographic primitives. XChaCha20-Poly1305 for chunk encryption. The NAS's long-lived key pair is an X25519 keypair, used to wrap per-session symmetric keys. Session keys are generated on the phone and wrapped to the NAS public key at session open. Chunk IDs are SHA-256 of the encrypted bytes. The transport tunnel carries TLS with a certificate pinned on the phone at device pairing, so the full stack is encrypted twice, once at the chunk level (XChaCha20-Poly1305) and once at the transport level (TLS through the tunnel).

Idempotency. A Redis-backed dedup set keyed on `(session_id, chunk_id)` ensures re-sent chunks are recognised and not re-processed. A chunk that arrives after the session has already closed is accepted if the chunk_id is in the Redis set (the confirm-in-flight case) and rejected with `409 Conflict` otherwise. For voice-memo mode, the memo archiver uses the same dedup state to ensure a resent chunk is not appended twice to the reassembled file. TTL on the dedup keys is a few hours, long enough to cover the confirm-in-flight window and not so long that state grows unbounded.

Graceful shutdown. On SIGTERM, stop accepting new sessions, drain ring buffers for in-progress sessions (push partial transcripts to ghost.noted, promote any memo scratch files to the archive), send processing-complete, exit. Any session that cannot be drained is left with `outcome='aborted'`, the phone will re-send its chunks as a new session, and any memo scratch files are deleted.

Voice memo playback streaming. The `/api/v1/voiced/memos/:id/audio` handler opens the archived audio file and streams bytes to the client with HTTP range support. Implementation uses `http.ServeContent` on the file handle, which handles range requests and content-type detection. Authentication check happens before the file is opened.

Test strategy. Unit tests for the chunk dedupe logic, the ring buffer back-pressure, the session lifecycle state machine, the memo reassembly logic, and the cryptographic primitives. Integration tests against a small corpus of fixture audio with known content and known speaker counts, pushed through a fake phone client in both `live` and `memo` modes. Quality evaluation on real audio is manual and runs when the model or stitching strategy changes.

## Versioning

The transcription model identifier and parameter-set version are recorded on the session row (`transcription_model`, `prompt_version`). For voice-memo sessions, the model metadata travels with the archived memo, so re-transcription is possible. For live-capture sessions the metadata is audit trail only, because the source audio is gone.

A user-triggered bulk re-transcription in v1.0+ applies to voice memos only. It re-runs transcription against archived memo audio with a newer model, produces an updated transcript, pushes it to ghost.noted as an update on the existing source, and marks the old transcription as superseded in `voice_memo_revisions`. Live-capture entries stand as-is regardless of model improvements. Changing the model does not retroactively rewrite history that was built from audio that no longer exists.