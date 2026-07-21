# LocalGhost TODO

Two sections, no mixing: what remains, then the build log. New done items go to the TOP
of DONE (reverse chronological); open items live in TO DO until they move.

## TO DO

- [ ] **7. Reprocess pass , RUN IT** (the command now exists, written 2026-07-15): framed walks
      the archive, re-inserts records (idempotent), re-derives previews (force=true also fixes
      pre-existing sideways portrait thumbs), re-notifies search, rebuilds GPS days. Run:
        ghost-cli ghost.framed reprocess force=true        (via ns.sh)
        ghost-cli ghost.searchd unpark                     (revive jobs dead at 5 attempts)
      then watch nvidia-smi while the caption backlog drains and "tags pending" resolves.
      Root cause of the idle GPU, diagnosed: the degraded window's photos were never ingested
      (no ingest, no caption job , the queue was honestly empty), searchd's rebuild cannot
      discover them (it regenerates derived state from originals, not from the archive), and
      any jobs that did burn 5 attempts against the broken DB were parked forever with no
      unpark lever. All three now have answers.

## Sync

- [ ] **9. Runtime bundle switchover** , bundle_db_runtime.sh --verify, then halt + unlock,
      confirm ps shows postgres from runtime/pgroot, decide on purging the OS packages. First
      real use of `halt`.
- [ ] **25b. Gallery grouping** (was 25 remainder) (UX, next session's opener): tags as tappable
      chips on tiles and in the detail dialog; place line under the date (the geocoded
      hierarchy); group/filter by place and by day; search box wired to /v1/frames/search;
      correct-orientation thumbs land via reprocess. The data all exists now , this is pure
      presentation.
- [ ] **26. PIN/key derivation review** (assessed 2026-07-15; deliberate, not urgent , needs a
      registry re-enrol migration, wrong thing to hot-fix). Findings: (a) PinKey doubles as the
      registry identifier AND the wrapping key, so registry.blob stores the PIN-derived half of
      the wrapping key in the clear , fix is domain separation (id = HKDF(PinKey,"id"), wrap =
      HKDF(PinKey,"wrap"), x/crypto/hkdf), blob then holds only a one-way derivative; (b) the
      registry is an OFFLINE PIN oracle at Argon2 speed (6 digits ≈ a day for box root),
      bypassing the TPM's rate limiting because Resolve is pure software , fix on the TPM tier is
      a TPM-resident HMAC mixed into the identifier so every guess transits the TPM dwell;
      software tier cannot have this and must say so. What is already RIGHT and must not
      regress: Argon2id 64MiB/t4, full-registry constant-time scan with fillers (no timing/count
      leak), wipe-PIN indistinguishability, Gate online rate limiting.

- [ ] **30. Memory system, next slices** , (a) DONE (item 34); (b) DONE 2026-07-15 as
      memoriesSource: retrieval lives in synthd's context-injection path (FIRST in source order ,
      what the box knows about the person outranks document search), keyword term-overlap over
      live memories, top 2 by hits then recency, source="memory" with an honest Why; the Index
      interface upgrade folds into (c) embeddings; (c) embeddings via the search layer's
      embedder into memories.emb, PGIndex upgrades to semantic; (d) frames as a memory source
      (places + dates + tags -> "was in Strathcona Park mid-July"); (e) cued gating policy.

- [ ] **31. ghost.shadowd, the real charter** (recorded 2026-07-15 from hard-truths/should-not-
      possess + dictator-brain + critic-worth-listening-to): the anti-possession daemon , a fleet
      of individually-tunable manipulation-pattern detectors (28-entry catalogue) plus the
      COLD-READ ARBITER (separate model never shaped by the user, scheduled reset to a published
      baseline against arbiter capture). Contract: NAMING IS THE ACTION , no blocking, no
      refusing, mute-not-disable, enforcement only for user-written Ulysses contracts. First
      tractable detectors read data the box already holds: interaction time + sessions past task
      completion (addiction by design), ghost-vs-human emotional-processing share (engagement
      loneliness), sunk-cost retrieval framing, topic-surface narrowing (filter bubble of one).
      Vlad ships no v1 without shadowd running.
- [ ] **32. Fleet gaps inventory** (per cmd READMEs, 2026-07-15): ghost.noted , first slice DONE
      (see 35), pullers/upload/mentions remain; ghost.voiced (audio -> transcripts ->
      noted; ephemeral live-capture vs kept voice-memos) , stub; ghost.tallyd (structured data ->
      time-series + narrative summaries -> noted) , stub; framed's README says descriptions push
      through noted's inbox , currently framed notifies searchd directly, reconcile when noted
      is real; synthd's entities/episodes/decision-queue layers , not started (distillation loop
      is the first slice only).

## Security / longer arc (deprioritized 2026-07-15 , UX first, per the operator)

- [ ] **15. Re-pair gating on FIDO2.**
- [ ] **16. Backup system** , weekly fulls + daily incremental diffs on the always-mounted HDD,
      asymmetric encryption (daily job never holds the key), folder unreachable by the app.
- [ ] **17. Box security threat model blog post** (separate from the Border Agent post).

## Done (this era)

## DONE

- [x] **73. Field triage two: the text lane thinks, the embedder pools** (2026-07-21): the
      reasoning disease's second organ , tags (and all one-shot TEXT inference) never got
      enable_thinking:false, only the multimodal caption path did; ~470 reasoning chars, no
      content, every tag job. Suppressed now; StreamChat keeps its deliberate <think> handling.
      Embeds: llama-server 500s on /v1/embeddings for nomic-style models without an explicit
      pooling mode , --pooling mean (+ -c 2048 -ub 1024) added to the spawn. Post-deploy:
      ghost.searchd revive refunds the attempts both storms burned. Meanwhile the log sang:
      day episodes 98+83 built, video previews growing, vector=true embedder=true.

- [x] **72. ffmpeg joins the volume** (2026-07-21): tools/bundle_ffmpeg.sh , same philosophy as
      the DB bundle (binary + full ldd closure onto the encrypted volume, --verify runs the
      bundled copy with ONLY bundled libs, then the OS package is removable). framed prefers the
      volume's copy (SetFFmpeg, LD_LIBRARY_PATH at the bundle's lib dir), PATH only as fallback.
      Docker considered and rejected: the setup IS the container. Map verdict from the field: DB
      aggregation healthy (3308 points, 29 world cells) , the fix is the item-65 APK, pending
      rebuild.

- [x] **71. Videos become citizens** (2026-07-21): frame-grab thumbnails , ffmpeg (best effort:
      absent = play glyph as before, present = grab at t=1s falling back to t=0, fed through the
      SAME makePreviews path so preview + thumb + upright all apply) at archive AND at reprocess
      (the 161 existing videos gain thumbs on the next pass). Playback: VideoPlayer.kt , the box
      speaks mTLS + bearer which no stock player can, so bytes come through the authenticated
      channel into cache and play as a local file (VideoView, zero new deps); the cache file dies
      with the dialog , the phone stays a window, not a second archive. Gallery routes video taps
      to the player, photo taps to the pinch viewer.

- [x] **70. pgvector path + idempotent image reimport** (2026-07-21): pgvector needs no code ,
      the bundle already carries vector.so when Debian's package is present and the schema
      self-upgrades on CREATE EXTENSION success; documented the activation (install pkg,
      re-bundle, restart) + the embed-model half (searchd spawns its own llama-server child from
      embedBin/embedModelPath in svcconf; until a gguf lands, vector mode sits ready, embed lane
      empty). ghost-cli ghost.searchd reingest: enqueues captions for exactly the image originals
      with no chunks and no queued job , a healthy library queues zero, a restored one queues the
      gap. Disaster flow documented: unseal | tar -x, reprocess, reingest, wait.

- [x] **69. Backups + episode backfill** (2026-07-21): BACKUPS live , framed seals its own tree
      nightly (03:00, Sunday full / else incremental via mtime watermark, four fulls kept, prune
      follows) into /var/lib/ghost/backup with an ASYMMETRIC seal (X25519 ephemeral + HKDF-SHA256
      + ChaCha20-Poly1305 chunked STREAM, final-chunk marker so truncation fails auth): the box
      holds only backup.pub and can write what it can never read. OPT-IN BY KEY , no key, no
      backups, one log line. cmd/ghost.restore: keygen (private key printed once, stays offline)
      + unseal (key on stdin never argv, tar to stdout). Manual: ghost-cli ghost.framed backup.
      App-unreachable by construction. EPISODE BACKFILL , "the last photo might not be from
      today": a watermark walks 120 historical days per pass from now-90 back to the earliest
      frame, so imported libraries and quiet seasons get their episodes too, bounded work per
      pass, all of history eventually.

- [x] **68. Day episodes + morning reflections (30d + 30e)** (2026-07-21): THE LOOP CLOSES ,
      live a day, the box remembers it, one morning it hands it back. synthd episodePass builds
      one memory per day (kind='episode', last 90 days, every pass) DETERMINISTICALLY from what
      the box knows: photos + where (framed), steps + sleep (tallyd), how you said you felt
      (check-in) , "14 photo(s) around Tofino. 12840 steps. 7h 20m sleep. You said you felt calm,
      salty." No model call: a memory of a day should exist the day it happened, not when a GPU
      gets around to it. User edits and tombstones permanently outrank regeneration. cued
      reflectionLoop: mornings 8-11, once daily , this day ONE YEAR AGO if an episode exists,
      else a random episode >30d old, else SILENCE (a young archive earns quiet mornings, not
      filler). Notification kind='reflection', tap lands in MEMORIES. Drill-ins: synthd counts
      episodes, cued shows episodes available + last reflection day.

- [x] **67. Backlog pass: shadowd wakes + revive + operator UX** (2026-07-21): SHADOWD's first
      real detector (31 begun) , interaction-time trend: counts the person's OWN messages (never
      content-analysed), a week doubling the prior past a 60-message floor earns ONE factual
      observation per ISO week ("not a problem , just a fact you own"); quiet weeks earn silence.
      Drill-in shows live numbers (7d / prior 7d / days talked). ghost-cli gains `searchd revive`
      (paved path for tonight's raw-psql resurrection , resets exhausted jobs, reports the count).
      framed reprocess announces its actual start in the log (vs queued behind a sync drain).
      Map tap-dot-to-photo satisfied by the item-54 viewer , moved out of TO DO.

- [x] **66b. The 1564.50 confession** (2026-07-21): the phantom was CALORIES , Samsung Health
      synthesizes a BMR-based TotalCaloriesBurned for ANY queried day (1564.50 kcal, identical,
      every day since 2006, data or none). Positive, so the zero-guard was blind to it. Rule
      shipped: a constant is not a measurement , calories only land on days where another metric
      was actually measured; a day whose only content is the provider's guess about resting
      metabolism is an empty day. Repair: DELETE all-calories rows, re-sync walks to the real
      data boundary.
- [x] **66. Field report triage** (2026-07-21): (a) HEALTH PHANTOMS , empty aggregate buckets
      return non-null ZEROS for some metric types (Duration especially), writing one zero metric
      per day back to 2006: 7,305 phantom health days, the six-empty-months stop never fired, the
      walk ran to its 20-year cap, and synthd inherited 7,306 distill-queue entries. Zero is not
      data: only positive values land, days exist only when something real did. (b) Drill-in
      LAYOUT , long values squeezed into the row leftover wrapped one letter per line; >24 chars
      now renders as a full-width paragraph. (c) searchd drill-in breaks jobs out BY KIND (the
      5642 mystery); framed newest capture formatted as a date. (d) The mush legacy repair
      (delete reasoning-era image chunks + phantom health rows + re-enqueue captions for every
      image original) shipped as operator SQL , the box re-describes its whole library properly.

- [x] **65. Map skew shield actually fires + EXIF-upright viewer** (2026-07-21): the shield never
      triggered because getJson on a 503 returns an EMPTY OBJECT, not an exception , framesGeoLod
      came back empty-list (an answer) instead of null (unknown), and the map trusted a box that
      had never heard of the endpoint. Absent points key now = null; and an empty WORLD view with
      3306 geotagged photos is treated as skew too (a genuinely empty local view stays a real
      answer). Viewer: BitmapFactory ignores EXIF orientation, so originals arrived sideways
      (thumbs were pre-uprighted box-side, masking it); framework ExifInterface + one Matrix now
      uprights rotation and mirroring, with an OOM guard (a sideways photo beats a crash).

- [x] **64. Model gate + attempt refunds + framed pipeline visibility** (2026-07-21): searchd's
      model-dependent lanes (caption, tag) now REST behind a 20s hold the moment a "no backend"
      lands , no more machine-gunning a warming oracled. The deeper find: warmup fast-fails were
      CONSUMING job attempts (five storms = a photo permanently uncaptioned , "captions
      exhausted"); UnclaimJob refunds the attempt since it never reached the model. One log line
      per storm, not one per job. FRAMED's drill-in now shows the whole enrichment pipeline:
      archived -> geotagged -> placed -> named -> described -> tagged, plus caption queue and
      captions exhausted , the operator watches tagging happen in numbers.

- [x] **63. QR detection round three** (2026-07-21): (a) STRIDE-2 scanning , a finder is >=7
      modules tall so every-2nd-line scanning loses nothing decodable and halves the cost, which
      is what lets sticky + probe both run every frame; a NEAR-MISS (1-2 finders seen) flips the
      next frame to stride-1 precision , the extra rows are exactly where the missing finder
      hides. (b) TWO-OF-THREE RESCUE , the most common near-miss: two finders fix scale and
      orientation up to six candidate spots (right-angle completions about each + the diagonal
      hypotheses); a tight (3-module) window at each is searched with the lenient matcher, one
      hit completes the triple, the decoder judges. Detection now degrades gracefully instead of
      binarily: 3 finders decode, 2 finders usually still decode, 1 finder sharpens the next
      frame.

- [x] **62. Map skew shield + original-quality viewer** (2026-07-21): the blank map was VERSION
      SKEW , new APK calling /v1/frames/geo/lod + /newest against a secd that predates them, every
      call 503, zero cells. The redeploy fixes it, and the app now survives it forever: LOD null
      falls back to the old full-fetch endpoint (dots beat blankness), missing /newest falls back
      to fit-all camera. Viewer upgraded ORIGINAL-first: /v1/frames/original streams the untouched
      archive bytes mime-typed (phone decodes jpeg/heic/png natively); preview then thumb are
      fallbacks for undecodable originals, and a quality tag names any downgrade , no silent webp.

- [x] **61. Health correctness + check-in depth** (2026-07-21): DOUBLE-COUNTING fixed , when
      watch AND phone both write steps, raw record-summing counted both; daily totals (steps,
      distance, calories, floors, exercise) now come from the AGGREGATE API which dedupes across
      data origins with Health Connect's source priority; raw summing survives only as a named
      fallback for older providers. Sleep OVERLAP MERGE , two origins recording the same night no
      longer invent extra sleep (intervals merged before bucketing by wake day). Check-in grew
      memory: "yesterday you felt calm, tired" continuity line, a [ + past check-ins ] strip
      (/v1/checkins parses the journal entries back into day/feelings rows), memory list gained
      a filter box anda provenance line ("yours" / "distilled from your days" + date).

- [x] **60. Real notifications + tap-through + QR far-scan** (2026-07-21): the two FAKES in
      pollPending are dead , the app now reads /v1/notifications (per-device push cursor, offered
      once). Two real producers: framed's WEEKLY HIGHLIGHT (best day of the last 7 by geotagged
      photo count, once per ISO week, factual not engagement bait , "Thursday was the big one ,
      34 photos around Strathcona") and secd's evening CHECK-IN ask (19-21h, once, SILENT if the
      person already checked in , no streaks, no guilt). Tapping any notification opens the app;
      AFTER the security gate MainShell navigates to it (NOTIFICATIONS; the check-in ask lands on
      MEMORIES). QR: small-module leniency in matches11311 , sub-2.5px arms get 0.85 tolerance
      and a 1.7x centre floor (integer runs make far-QR ratios inherently ragged).

- [x] **59. Debug mode + tok/s + per-daemon drill-ins** (2026-07-21): global DEBUG MODE flag ,
      Settings > DEVELOPER > "set app in debug mode" (tap toggles, more diagnostics attach here);
      chat tok/s meter (13c done) , timed from FIRST answer token so thinking does not dilute the
      rate, chars/4 approximation, shown under the composer only in debug. Per-daemon screens
      (44 done) reached from Box Status rows as designed: the stats dialog now opens with a
      DRILL-IN section fed by /v1/daemon/summary , framed (archived/photos/videos/geotagged/
      placed/named/described/track points/geo places), noted (journal by source, awaiting
      distillation), synthd (memories live/yours/tombstoned, queue, reports), searchd (caption
      jobs pending/exhausted, chunks, tags), tallyd (health days/rows/samples/earliest),
      shadowd + oracled (charter/role lines). All flat SELECTs from each daemon's own tables.
      TODO restructured into TO DO / DONE sections , no more mixed reading.

- [x] **1. Markdown rendering in chat** , stdlib only. Range-based renderer, streaming-safe by
      construction (all parsing in remember/runCatching, plain-text fallback, fuzzed over 3485
      streaming prefixes). Includes SelectionContainer + per-message [ copy ]. Landed 2026-07-15.
- [x] **2. Expandable thinking** , reasoning text accumulates on the Message (not just a count),
      renders collapsed behind a +/− "thinking… (n)" toggle, STREAMS while expanded, doubles as
      the pre-answer indicator, stays readable after the answer. Landed 2026-07-15.
- [x] **3. Auto-scroll during streaming** , follow-tail keyed on CONTENT growth (tail text +
      reasoning lengths, not just message count); user drag away from the bottom stops following,
      returning to the bottom resumes. Landed 2026-07-15.
- [x] **4. Keyboard inset bug** , the shell pads systemBars bottom AND ChatScreen added full
      imePadding, while the IME inset spans the nav bar's space; fixed with ime.exclude(
      navigationBars) so the layers sum to exactly keyboard height. Landed 2026-07-15.

- [x] **18. Skip the app fingerprint after a recent device unlock** , gate key rebound with a
      10s auth-validity window (lockscreen unlock opens it); silent cipher path goes straight to
      the box PIN, prompt only outside the window. Landed 2026-07-15.
- [x] **19. Lock-screen links styled as buttons** ([ brackets ] + underline). Landed 2026-07-15.
- [x] **21. Scanner big-code detection** , module-proportional cluster radius (fixed 14px was
      shattering a frame-filling finder into a stack of fragment clusters) and triple dimension
      bounds widened 17..68 (the old 60 cap rejected every v11 = 61-module enrol code at the
      geometry stage). Landed 2026-07-15.
- [x] **20. Scanner assembly burst** , 100ms sampling while capturing the rotating enrol frames
      (bounded burst, thermal tuning kept for indefinite hunting). Landed 2026-07-15.

## App/box , chat continuity

- [x] **5. Chat disappeared after unlock** , currentChatId was process-memory only; now persisted
      (adoption, explicit open, new-chat reset) and restored after a successful unlock when the
      screen is empty and not incognito. Also fixed in passing: newConversation never reset
      currentChatId, so a "new" chat's first message APPENDED to the previous chat on the box.
      Landed 2026-07-15.

## Frames pipeline

- [x] **6. Portrait photos render rotated** , exif package extracts Orientation (0x0112); framed
      applies the transform to derived previews/thumbs AFTER downscale (commutes, and rotating
      1600px beats rotating 12MP). Originals untouched. Landed 2026-07-15. Existing wrong thumbs
      need the item-7 reprocess , which it turns out DOES NOT EXIST yet (the pipeline comment
      "regenerated by the reprocess command" is aspirational); item 7 now includes writing it.
- [x] **8. Sync screen counters** , the worker was already sequential (photos then videos); the
      three-numbers-racing screen was stale Activity state (worker only published kind+done/total,
      so last run's photo bar froze mid-fill while this run painted videos) and the byte line was
      dead in worker mode (never fed; the 9510.6MB total was a legacy-run leftover). Worker now
      owns and publishes the COMPLETE set , both kinds + bytes/speed/eta , every update (bytes
      throttled to 2/sec); the observer just paints; terminal states clear the meter. The tail
      sums the same two bars, so all numbers agree by construction. Landed 2026-07-15.

## Server

- [x] **10. Drawer recents box-backed + chat rename/delete** , the old drawer source was a STUB
      (delay + unused params, backed by nothing); recents now map /v1/chats, selection is chatId
      adoption, refresh on unlock/adoption/rename/delete. New endpoints: POST /v1/chats/rename
      (person's title outranks derived, permanently) and POST /v1/chats/delete (real deletion,
      messages then chat), both appears-down disciplined. Inline pencil-rename in CHATS rows.
      Landed 2026-07-15.
- [x] **23. Location search + on-box reverse geocoding + self-drawn map** , BUILT end to end
      2026-07-15 (design below stands as the record). Landed: internal/geo (GeoNames TSV +
      0.5° grid + haversine; filters to populated/parks/falls/peaks/trails so RAM stays sane);
      frames.place column with backfilling insert (fills empty, never clobbers); framed geocodes
      at archive AND reprocess; /v1/frames/geo, /v1/frames/search (place+name+tags, AND per
      term), /v1/geo/world; app MAP screen , Web Mercator Canvas, pinch/pan, graticule, optional
      Natural Earth landmass, photo dots, tap shows the place string. REVISED same night to
      DB-BACKED geocoding (geo_points/geo_names in postgres): the RAM grid forced filtering the
      dataset to what fit in memory; postgres takes the FULL allCountries (millions of rows,
      suburbs and villages, nearest point typically within a few km) plus admin2 names. The
      radii are honesty caps, not precision. TO ACTIVATE: mkdir <volume>/geo via ns.sh, drop
      allCountries.txt (or cities+features subsets) + admin1CodesASCII.txt + admin2Codes.txt +
      countryInfo.txt (+ world.geojson for landmass), then:
        ghost-cli ghost.framed geo-import     (batched, idempotent by geonameid, watch the log)
        ghost-cli ghost.framed reprocess      (backfills place on every geotagged frame)
      No framed restart needed , the resolver wires itself when the import completes.
      NEW PROVISIONS get all of this AUTOMATICALLY: setup fetches the public datasets straight
      onto the encrypted volume (tools/fetch_geo.sh , privacy-clean at setup, before personal
      data exists; GHOST_NO_GEO=1 skips) and imports them through the same framed.Store code
      path while the provisioned Postgres is up , a brand-new box geocodes from photo one. Remaining polish: day-track polylines on the map, tap-dot ->
      gallery, cluster counts at low zoom. Original design (kept for the record , designed 2026-07-15,
      multi-session). NO phone Geocoder and NO tile servers , both ship coordinates/viewports to
      third parties, disqualified by the threat model, not by dependency pedantry. Plan, in
      dependency order: (a) internal/geo , GeoNames TSV parser + lat/lon grid index + hierarchy
      assembly (continent/country/admin/nearest locality/nearest park/nearest feature , GeoNames
      carries parks and waterfalls, so "Canada / BC / Strathcona Provincial Park / Upper Myra
      Falls" is achievable offline); (b) place columns on frames, geocoded at ingest/reprocess,
      fed into search FTS so "waterfall vancouver island" works alongside tags; (c) /v1/frames/
      search (tags + place terms + optional lat/lon radius) and /v1/frames/geo (points for map);
      (d) map = Natural Earth public-domain vectors served from the box, drawn on a Compose
      Canvas (Web Mercator), day-track GeoJSON (rebuildDay already produces it) as polylines,
      photo dots clustering by zoom, tap opens gallery. Outline world, your data on it, fully
      offline , no street level, and we say so.
- [x] **11. Redis save policy** , `save 3600 1 60 20`: hourly snapshot for a quiet box, minute
      snapshots only under heavy write load (sync bursts, caption runs). Landed 2026-07-15.
      Verify live with `ghost-cli ghost.* / redis-cli CONFIG GET save` after next cold start.
- [x] **12. Graceful shutdown on lock and redeploy** , landed 2026-07-15: (a) killProc grace
      5s→15s so SIGTERMed daemons finish in-flight items before the SIGKILL floor; (b) redis
      lock-path shutdown was NOSAVE , explicitly discarding up to an hour of state at every
      lock , now SHUTDOWN SAVE (item 14 folded in); (c) pg_ctl -m fast -w was already the clean
      checkpoint; (d) redeploy.sh takes GHOST_PIN and rides halt (ordered teardown, redis saved,
      pg checkpointed) before the binary swap, with a loud fallback warning when no PIN.
- [x] **13a. Service transition alerts** , watchd writes to the notifications table (the feed the
      app already renders) on service-failed and service-recovered, rate-limited 5min/service so
      a flapper is one alert not a feed; lazy rw connection that self-heals across pg restarts.
      **13b. Nobody-watches-watchd** , /v1/services/summary now reports stale+age when the
      freshest sample is >35s old (the one condition the stats cannot self-report); Box Status
      shows a warning banner naming the rows below as the past, not the present. Landed
      2026-07-15.
- [x] **14. Redis persistence at lock** , folded into item 12 (shutdown save). Landed 2026-07-15.

- [x] **22. Setup provisions the volume interior** , at format time (PIN in hand, fresh mount):
      frames/preview/thumb trees owned by the service user; llama-server seeded when built; the
      DB runtime bundled onto the volume (best-effort); Postgres FULLY initialised via the real
      hw.DataStore machinery (initdb, roles, database, ownership, app+search schema, grants,
      peer-line hba) and stopped clean. First unlock STARTS things; convergence becomes repair,
      not deferred setup. DB init failure is fatal at the setup terminal, where it belongs.
      Landed 2026-07-15. Audit notes: services.conf + binary seed already existed; the gaps were
      the DB, the bundle, llama, and the frames tree.

- [x] **24. secd supervises watchd** , secd is the gateway and the only process positioned to
      notice its child die; a mid-session watchd crash used to orphan the cohort until the next
      unlock. Now: Wait on the child, deliberate stops (lock/halt/shutdown) exempt via a
      stopping flag, unexpected death logged loudly + respawned (bounded 15s cadence) + cohort
      re-issued. Named limit: an ADOPTED watchd has no proc to Wait on , covered by the
      staleness flag + unlock convergence. secd itself is systemd's job (Restart=). Landed
      2026-07-15.
- [x] **25. Gallery polish** , place hierarchy line in the detail dialog; location+tags search box
      wired to /v1/frames/search (debounced, returns to paged archive when cleared); place through
      FrameRow -> GalleryFrame; tag chips + add/remove already existed. Landed 2026-07-15.
      Remaining: tappable tag chips that seed a search, day/place grouping headers.
- [x] **27. Thinking panel actually shows** , ROOT CAUSE: the model reasons IN-BAND (prompt
      injection, no native reasoning_content channel), so reasoning arrived as answer tokens and
      the app's r-event toggle had nothing to show. Fix: think prompts now request explicit
      <think></think> delimiters, and ghost.synthd splits tokens inside the block into r-events
      (streaming state machine, carries partial tags across SSE seams, verified across every
      3-char boundary) , only the post-</think> answer is persisted. Landed 2026-07-15.
- [x] **28. Chats history polish** , box-chat rows gained two-tap delete (reuses the /v1/chats/
      delete path) beside the inline rename. Landed 2026-07-15.
- [x] **29. Memory system, foundation** (landed 2026-07-15; CORRECTED same night , the memory
      layer is ghost.SYNTHD per its charter, hard-truths/how-memory-gets-made; the distillation
      loop was briefly misfiled in shadowd and now lives in synthd's main). Division of labour, corrected: ghost.SYNTHD owns memory end to end (journal entries ->
      entities -> memories -> episodes; today's first real source is saved chats, distilled
      through oracled, since ghost.noted is a stub), ghost.cued GATES surfacing, ghost.SHADOWD
      is the ANTI-POSSESSION daemon (detector fleet + cold-read arbiter) and touches none of
      this. Landed: memories table
      (provenance via source_chat, user_edited, tombstoned, emb reserved for the embedding
      pass); synthd runs the distillation , every 10 min it distills finished (quiet 10 min, >= 2
      messages), undistilled, non-incognito chats through oracled into TITLE | body memory rows,
      bounded 5 chats/pass, model-down chats retried later, nothing-worth-remembering chats
      marked with a tombstoned sentinel so they are never re-summarized; /v1/memories +
      /v1/memories/delete on secd. Sovereignty is structural: tombstones are never resurrected
      (chats with ANY memory rows are never re-distilled), user_edited never overwritten,
      incognito is invisible by inheritance (never reaches the chats table).
- [x] **33. Journal-entry architecture** (landed 2026-07-15): each ingester writes its OWN diary
      , journal_entries table, idempotent by (source, ref), distilled flag as synthd's high-water
      mark. framed writes entries at archive AND reprocess with what it knows then (kind, when,
      geocoded place); captions/tags enrich the memory at distillation, not the entry. voiced/
      noted/tallyd write to the same table when they become real. synthd is the ONLY consumer.
      READMEs updated (framed, synthd; shadowd's created with its charter).
- [x] **34. Memories editable from the app** (landed 2026-07-15): /v1/memories/add (kind=user,
      user_edited from birth) and /v1/memories/edit (marks user_edited , re-distillation may
      never overwrite); MemoriesScreen off its stub onto the real endpoints , list, inline add,
      inline edit, two-tap delete, "yours" vs "distilled" labelling.
- [x] **43. Full-spectrum health + samples store + HEALTH screen** (landed 2026-07-15): app
      pulls heart rate (daily avg/min/max + raw series THINNED to 5-min buckets), distance,
      calories, floors, weight alongside steps/sleep/exercise; health_samples table for the
      high-res series (upsert by metric+ts); tallyd ingests samples + daily journal now carries
      distance/kcal/avg-HR-with-peak; /v1/health/stats serves 30-day daily series per metric;
      HEALTH in the drawer (♥) , the FIRST per-daemon screen: per-metric bar strips scaled to
      own range, latest + min/avg/max, stats not judgements. Manifest gains the read perms.
- [x] **58. Health from the beginning of time** (2026-07-21): (a) PAGINATION , Health Connect
      pages at ~1000 records and the reader took only page one, silently dropping the rest even
      for 7 days; the token loop now drains every page. (b) SYNC FULL HISTORY walks back month by
      month (own upload chunk each , memory + the box's cap honoured at any density), stops after
      6 empty months or 20 years, live progress line. (c) Box side: upload cap 256KB -> 1MB per
      chunk; tallyd samples batch 500/statement (a million HR points is an import, not a career);
      synthd bulk-flips tallyd entries older than 60 days , the model reads recent health, the
      deep past waits for day-episodes (30d). Re-syncs upsert, so overlap is free.
- [x] **57. Health sync button dead** (2026-07-21): Health Connect REFUSES to show its
      permission sheet unless the manifest declares ACTION_SHOW_PERMISSIONS_RATIONALE (+ the
      Android 14 VIEW_PERMISSION_USAGE/HEALTH_PERMISSIONS activity-alias) , launch() was a
      silent no-op, the button did literally nothing. Both declared; every button branch now
      reports (sheet opening, granted n/8, refused-with-reason) so silence is impossible.
- [x] **56. QR detection regression fixed** (2026-07-21): the perf pass that rotated ONE
      binarisation bias per frame (to stop the stalled analyser) regressed detection 5x , a code
      resolving under only one bias got a shot every fifth frame. Repair: STICKY bias + rotating
      probe , the bias that last produced finder candidates runs EVERY frame, a second pass keeps
      probing for better; partial finder visibility does not count as a miss (bias right, hand
      moved); 12 straight true misses drops the sticky. Worst case 2 passes/frame, common case 1,
      detection back to all-biases quality.
- [x] **55. Declarative schema convergence** (2026-07-21): the registry (schemadef.go) is the
      single source of truth; ConvergeSchema introspects information_schema at unlock, diffs, and
      applies , creates missing tables/columns/indexes, migrates types via ALTER USING cast where
      postgres allows, REFUSES destructive acts (drift columns loudly logged, never dropped;
      missing BIGSERIAL flagged for a human). Versioned one-shot data migrations in
      schema_migrations, run once per box in order. Converge failure is loud but non-fatal ,
      the bootstrap blob already ran, yesterday's schema keeps working. Users run the latest
      build, unlock, read the summary line. New schema work goes in the REGISTRY; the blob is
      frozen bootstrap.
- [x] **54. Map LOD + image viewer + descriptions** (2026-07-21): MAP opens on the NEWEST photo
      zoomed close (answers "where was I last" before "where have I been"); four-tier
      level-of-detail feed /v1/frames/geo/lod , postgres GROUPs by grid cell (1deg / 0.1 / 0.01 /
      raw), viewport+zoom drive the tier, debounced 220ms, so a continent view ships hundreds of
      rows not the whole archive; zoom cap raised 2000 -> 250000 so 100m clumps resolve into
      individual photos; cluster tap dives in, single-photo tap opens. FULL-SCREEN VIEWER
      (ImageViewer.kt) , box preview JPEG, pinch 1-12x, double-tap 3x, drag to pan, caption
      underneath; reachable from gallery detail and from map dots. DESCRIPTIONS: frames.description
      holds the caption SCENE section (2-4 sentences), shown in gallery detail; display_name cut
      to date + 2 tags , "a name is a label, not a summary". Captions now claim NEWEST-FIRST
      (ORDER BY id DESC) so today gets named before 2019. Naming stays in searchd (hybrid: framed
      does fast metadata at archive, searchd enriches async) per the operator's call.
- [x] **53. Offline map + clusters + the mmproj hunt** (2026-07-20, live-fire): vision was dead
      because the projector on the volume was named "mmpr" AND truncated at 175MB of ~800 , a
      July 11 copy died mid-transfer and every caption since returned <20 chars from a model
      that could not see; fix = clean download under the expected name + oracled respawn, the
      retry loop becomes the recovery. World map now CACHED on the phone (filesDir + ETag
      mtime+size; 304 = zero bytes; box unreachable = map still draws , landmass only, never
      photos or locations). MAP clusters at low zoom: 48px grid buckets, count labels, density-
      triggered (>250 dots on screen) so sparse archives never cluster. fetch_geo upgraded to
      10m coastline (110m drew Vancouver Island as a twelve-vertex cartoon , the dots were
      right, the shoreline was lying). Remaining polish: tap-dot -> gallery.
- [x] **52. First-build fixes** (2026-07-15): a backticked command name inside a SQL comment
      TERMINATED the schema's Go raw string ("unexpected name ghost" , my own documentation
      broke the build); backticks stripped from schema comments and audited. Unused "io" dropped
      from timeline.go. redeploy.sh now PROMPTS silently for the PIN (env vars leak into history
      and ps; a silent read leaks nowhere; Enter skips to the plain-restart path).
- [x] **51. Smoothness pass** (2026-07-15): animateContentSize on every expanding surface ,
      thinking panel, check-in card, OTD years, memory rows, health cards , expansion glides
      instead of jump-cutting; thinking toggle tap target enlarged; gallery search keyboard
      shows the SEARCH action; MEMORIES header counts ("N memories · M yours"); HEALTH metrics
      ordered by what people care about (steps, sleep, exercise... weight) instead of
      alphabetical accident.
- [x] **50. Final pass** (2026-07-15): blind guesses proven against source , pipeline fields are
      store/log as the journal block assumed; BoxHttp.postJson(ctx, path, body) exists;
      LifeContext is top-level in net (import matches MainShell's). docs/TONIGHT.md added , the
      deploy + test runbook in dependency order. 40 items closed this session.
- [x] **49. Second sweep, both sides** (landed 2026-07-15): (a) LazyColumn KEY COLLISION ,
      MemoriesScreen's OTD cards keyed by year (2025) share a key space with memory rows keyed
      by id; the day a memory id hits a year number, Compose crashes "key was already used";
      keys namespaced (otd-/mem-; ChatsScreen was already local-/box-). (b) check-in prefill
      applied substringAfterLast to the JOINED places string , "Canada / BC and Canada / Van"
      collapsed to "Van", first place vanished; shortening now per-element inside joinToString.
      Verified clean: conditionally-patched imports all landed (frames_http, chats_http),
      ctlsock.NewClientTimeout + Call(cmd, args) exist as assumed, synthd imports oracle, one
      distillLoop only, HEALTH in drawer + route, runtime wildcard covers mutableStateListOf.
- [x] **48. Weird-scenario bug hunt** (landed 2026-07-15). Three real ones: (a) NASTY ,
      stopWatchd defer-cleared watchStopping, racing superviseWatchd's Wait into respawning
      watchd on a LOCKED box; the flag now stays set from deliberate stop until the next spawn.
      (b) GPU-EATER , a full-archive reprocess journals 30k+ per-photo entries and the distiller
      fed each through oracled at 8/pass (weeks of NONE); framed's per-photo entries now flip
      distilled in bulk with no model call , day-level episodes (30d) will read frames directly.
      (c) SILENT LOSS , noted's pure content-hash ref swallowed intentional duplicate jots
      ("gym" twice = one entry); plain-text refs now fold in mtime, .eml keeps the pure hash so
      re-dropped emails stay deduped. Known-and-accepted, documented here: OTD groups by UTC
      day, so late-evening local photos can land on the neighbouring day , revisit if it grates.
- [x] **47. Geo lifecycle audit** (landed 2026-07-15): REAL BUG fixed , geoNearest's LIMIT 400
      was unordered, so dense areas (central London) could exclude the true nearest; candidates
      now ORDER BY approximate squared-degree distance (cos-corrected) in SQL, exact haversine
      still picks in Go. Windows now ascend CLIPPED to the cap (the old fixed list ran a 33km
      scan for a 2.5km feature cap). UPDATE path made real: points import upserts by geonameid
      (was DO NOTHING = "update" meant "append"; deletions linger, named limit);
      GHOST_GEO_REFRESH=1 on fetch_geo.sh re-downloads; frames.place refreshes only via
      reprocess BY DESIGN. Lifecycle documented in DATA.md.
- [x] **46. Permission UX + data-model audit** (landed 2026-07-15): every access row on SYNC now
      carries a RATIONALE (what the grant feeds, what denial costs) and is TAP-TO-FIX (re-request
      or the system-settings deep link for permanent denials); health shows per-type n/8 with
      partial explicitly fine ("sync ships what is granted and names what it skipped"); one line
      states the contract , every grant feeds only your box. Data model: docs/DATA.md , the ERD,
      the per-screen access-path table (all 0-join), and the six rules (single writer per table,
      denormalized display fields, soft refs no FKs, tombstones over deletes, idempotent natural
      keys, two-query pattern for one-to-many). Two real gaps fixed: partial index on
      journal_entries WHERE NOT distilled (synthd's forever-poll stays O(undistilled)) and
      frame_tags(hash) for the batched tag lookup.
- [x] **45. Health errors treated nicely + Google Timeline ingestion** (landed 2026-07-15):
      HealthSync isolates EVERY record type , a denied permission or flaky provider skips that
      type by name (surfaced as "skipped: heart rate, weight"), partial data honestly labelled
      beats all-or-nothing; SyncScreen shows the full result. framed gained
      <mount>/framed/gps-inbox: Google Takeout Records.json (STREAMING decoder , the file can be
      500MB, never loaded whole) and the newer on-device Timeline export (semanticSegments,
      geo: points) both parsed; points batch-insert 5k as source google-timeline; touched days
      rebuilt once each (progress logged every 200); one journal line per import records the
      span; rejects to gps-inbox/rejected. Polled every 5 min + ghost-cli ghost.framed
      gps-import to skip the wait. YEARS of map history from one Takeout drop.
- [x] **42. Health via tallyd + data-driven check-in** (landed 2026-07-15): app reads the
      phone's Health Connect store (where Samsung Health writes) , steps, sleep sessions
      (bucketed to the WAKE day), exercise , last 7 days, only network hop is phone->box; POST
      /v1/health drops the batch in tallyd's inbox; tallyd's FIRST REAL SLICE upserts
      health_metrics + journals each day once (sleep and movement reach the distiller).
      day/summary gains health + SUGGESTED feelings , transparent stated heuristics (short sleep
      -> tired; movement -> energised; park/trail/falls -> calm; 8h+ -> calm), offered FIRST
      with a dot marker, never preselected: the box proposes, the person disposes. Check-in why
      prefills sleep/steps/exercise. SYNC screen: CONNECT HEALTH -> system permission sheet ->
      SYNC HEALTH (7 DAYS). Gradle: androidx.health.connect:connect-client alpha07 , adjust the
      version if the build objects.
- [x] **41. Daily check-in + day summary + smarter OTD** (landed 2026-07-15): /v1/day/summary
      (frames + journal for the PHONE's day bounds , the box does not guess timezones); MEMORIES
      check-in card , 12 feeling chips (pick up to 3), why PREFILLED from the day (photos,
      places, note titles; short place names via substringAfterLast), editable, lands in the
      journal via /v1/notes so feelings become memories with full sovereignty; once daily,
      collapses to a checkmark. OTD smarter: photos hour-SPREAD across the day (the first-12 cut
      showed twelve frames of breakfast and none of the summit), weekday fed to the narrative.
- [x] **40. On This Day** (landed 2026-07-15, app+backend): synthd composes the retrospective ,
      frames grouped by year for today's month-day (photos capped 12/yr, distinct places),
      journal-entry titles per year, a 2-3 sentence model narrative per year (best-effort, cap 5
      model calls; a miss leaves the year factual) , cached in the reports table per MM-DD,
      regenerated past 20h. /v1/onthisday proxies via ctlsock; MEMORIES gained the [ + on this
      day ] card , loads on TAP (first build of a day narrates and takes a minute; cached is
      instant), renders narrative + places + thumb strip + notes per year, newest first.
- [x] **39. Day tracks on the map** (landed 2026-07-15, app+backend): /v1/geo/days (which
      tracks exist , empty list on a young box, not appears-down) + /v1/geo/day?d= (one day's
      GeoJSON exactly as framed's RebuildDay wrote it; strict date parse IS the traversal
      guard). App draws the last 14 days' LineStrings as dim polylines UNDER the photo dots ,
      movement under the moments. Remaining map polish: tap-dot -> gallery, cluster counts.
- [x] **37. Feed the journal from the phone** (landed 2026-07-15, app+backend): POST /v1/notes ,
      secd writes into noted's inbox (run-user owned so the ingest can read it), noted picks it
      up next tick, one path no special cases. App: ACTION_SEND text/plain share target
      (singleTask + onNewIntent; shares queue until the gate passes and flush after unlock with
      a toast), plus [ + jot a note ] on MEMORIES , jots go to the JOURNAL, synthd decides
      durability, distinct from [ + add a memory ] which is sovereign immediately.
- [x] **38. Prompt-first foreground auth** (landed 2026-07-15): returning to the foreground on
      the gate fires authentication IMMEDIATELY , silent pass inside the 10s device-unlock
      window, fingerprint sheet otherwise, device PIN/pattern sheet for people without
      biometrics (DEVICE_CREDENTIAL already allowed). The once-per-process flag re-arms on
      onStop, so every foregrounding prompts; the gate screen is only ever SEEN after a cancel,
      where UNLOCK is the retry.
- [x] **36. Chats flow through noted; synthd reads ONE source** (landed 2026-07-15): noted
      journals finished conversations (quiet 10 min, >=2 messages, ref chat:<id>, transcript
      capped , the chats table keeps every byte); synthd's distillation reads ONLY
      journal_entries WHERE NOT distilled (framed's photos, noted's texts AND chats, voiced/
      tallyd when real), flips distilled as the sentinel (flipped on NONE, left for retry on
      model failure); memories carry source_ref (+ source_chat parsed from chat: refs). The
      pipeline is now exactly the docs: ingesters journal -> synthd distills -> person governs.
- [x] **35. ghost.noted, first slice** (landed 2026-07-15): the inbox. <mount>/noted/inbox polled
      every 30s; .eml parsed with stdlib net/mail (Subject/Date/From honored, headers stripped),
      everything else ingested as plain text (first line titles it); journal entry idempotent by
      content hash; canonical copy content-addressed in noted/archive; rejects to inbox/rejected
      with reason logged, never deleted, never looped. Remaining for noted: IMAP/upstream
      pullers, secd upload endpoint for the app share sheet, mentions extraction.
- [x] Convergent unlock , mounted-but-dead is repairable; Warm gates only Unseal/Mount.
- [x] EnsureSchema bootstrap , pg_hba peer line for the run user; owner role, service roles,
      database, and ownership converged as superuser; grants (public + search) as owner.
- [x] phash column moved to the partitioned parent (ADD COLUMN on a partition is illegal).
- [x] `halt` command , maintenance stop, everything down, volume stays mounted, PIN-opaque.
- [x] Adopted-watchd support , idempotent start-cohort, socket-driven shutdown for orphans.
- [x] Binary ingest via temp+rename (no ETXTBSY against running binaries).
- [x] Spool ownership heal for legacy root-owned frames.
- [x] health.sh: /tmp-staged namespace entry; .stream sockets excluded from the roster.
- [x] bundle_db_runtime.sh: auto-nsenter + refuses to write to an unmounted path.
- [x] Blocking MODEL unlock stage , READY completes last, app lands on a box whose chat answers.
