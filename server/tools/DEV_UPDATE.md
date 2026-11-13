# The engineering journal

What changed on the box and in the app, why, and what was tested, newest at the end. A new entry
goes at the end, under `## <day> <month> <year> , <what>`.

The current month is kept whole. Each month before it is one line a day at most, folded by hand
when the next month's first entry is written. Nothing is thrown away by that: the entries as
they were written are in this file's history, and the tree at a release tag holds the journal to
that day. `git show v0.0.8:server/tools/DEV_UPDATE.md` is everything to 13 November 2026 in
full, about 6,900 lines. Entries before 4 October 2026 carried no date; their day here is the
day they were committed.

The file began on 9 July 2026 as a guide to compiling and deploying. The deploy paths it
described are in the header of `tools/release.sh`; day to day a box is brought current with
`tools/redeploy.sh` (`tools/README.md`).

## July 2026
- 9 July , the journal's first commit: the deploy model (secd alone on the system disk, watchd owns the daemons, no pkill); logs and ghost-cli; oracled, synthd, framed and searchd; the Postgres and Redis clients (poltergres, apparedis); the `off` command; the multi-frame, rotating enrolment QR

## September 2026
- 19 September , the enrolment QR as ten easy frames, then any K of K+M (LGQR2) with a smaller link and a hardened decoder; the archive's stock-take (pipeline versions) and its progress on the phone (/v1/pipeline); the phone's own location trail
- 20 September , the lock-screen card (from a snapshot, then a live update); why a halt took 31 s; the trail every quarter hour, synced without duplicates; tags with categories and the photo digest; the phone searches the web and the box never does (a plan, tools, numbered sources)
- 21 September , a clock on every log line; the 44-second llama-server traced to a 60-day-old orphan that survives SIGKILL until a reboot, and the card that fell off the bus; twelve enrolment codes, any eight; the trail on the map by day; phrases hidden until a trip asks; the widget's own look
- 23 September , trail glitches: the spike to the mainland and back
- 24 September , one GPU probe for the whole box (the RmInitAdapter drumbeat was ours); the freeze under unwedge.sh --reset, and the watchdog; "failed at mounting store" after a power cycle; memories from the photos; chat end to end; the map with sea, land and coast; setup pulls from the signed mirror
- 25 September , the mirror goes live with the site key pinned; the captions that stopped on an unexplained "http 400" (3,240 parked); the first health after a redeploy; the map's coast checked end to end; the model weights pinned by hash; an app pass (scanner, torch, map chatter, slow photos, trails)
- 26 September , names on the map from the GeoNames already on the box; streets from OpenStreetMap's roads for the whole world, cut on the box and fetched by the phone a cell at a time
- 27 September , the day as a route along the streets; a smarter web search; memories written from a fact sheet and checked against it; one prebuilt summary a day; the map opens where you are; the phone's own model, built from the mirror's llama.cpp tarball; setup takes from the mirror and nowhere else
- 28 September , tools/update.sh: the box's data from the mirror in one command
- 29 September , the model that did not come back after the engine update (`--mlock`) and the unlock that deadlocked; a lock that could not unmount under uploads; Box Status freezing the database; the full re-upload explained; the check-in with a voice note transcribed on the box; the unlock screen's bar
- 30 September , the phone's own model, pinned to the box's llama.cpp; an answer survives closing the app; captions come back, every image fitted first; a server release from the phone without root; the phone's TLS checked by secd itself; a user of their own for the daemons; the trail sealed; the QR's key into the Keystore; privacy fixes

## October 2026
- 1 October , health from the watch reaches the box and stays; the day told after the check-in; news and rates kept by the box, which fetches for itself when the phone is away (seven venues, a price every minute); whole countries on the phone; thinking switched on; the feeds on Box Status
- 2 October , wisp 0.0.1, the first release, and 0.0.2 the same day, each with the app and the source beside the server set; Wikipedia and the heights on the box; the box's own price, a blend of every market; prices and news from Redis; HEIF stills told from clips; the README, CI and a Mac build; the enrolment link never written out
- 3 October , wisp 0.0.3; the weather moves to the box (one daily pull of the world's larger places, never a position); Go 1.27.1 from the mirror; the geocoder gone; the map's live dot
- 4 October , wisp 0.0.4; the box's voice and Wikipedia's place in an answer; CHECK-IN and MEMORY pages; a box from the release alone, the volume as a file; one memory per person and trips from outings; Frankfurter out; Wikipedia into Postgres
- 5 October , wisp 0.0.5 published and 0.0.6 opened; SETTINGS › SERVER whole, the shelf, prices first, check-in notes, a question asked aloud; the elevation tiles in packs; the Wikipedia import without its indexes
- 9 October , people through edits and names in transcripts; SOURCES and feeds from the app; weather on HOME; the ⓘ explainers and a UX pass; the widget; Wikipedia's stats, state recovery and tables owned by ghost_rw; INTEGRATIONS
- 10 October , wisp 0.0.6 cut and 0.0.7 opened; device certificates of two weeks, renewed daily; the weather forecast by the box itself (internal/grib2, internal/nwp, ICON-D2 and ICON-EU); a check-in for any day and the recorder; one signing key for every build; the health diagnostics
- 11 October , HEALTH three pages deep, the step count four ways
- 12 October , mail for the ghost, specified, revised twice and built (the relay at mx.localghost.ai, verdicts on the box, the box signs); the relay moves to the web repository; health read in the background and the watch's own step count; the Samsung Health download imported, then cut; the WEATHER page
- 13 October , the mail address leaves the box's zone; the box's name out of the record
- 14 October , the first relay live; two ctl fixes
- 16 October , the calendar and the contacts on the box; the check-in typed properly
- 17 October , the map's base in a layer; water in the tiles; trips by country
- 18 October , the relay off the deploy user
- 23 October , wisp 0.0.7 cut; a security audit of both trees and its findings closed; the first mail round trip; the map's heat, the water's order and the country names; the widget's prices
- 24 October , mail status in seconds; an OpenPGP of the box's own; the setup checked, and a doctor for it
- 30 October , the gallery refreshed; clips framed and timed; the screen stays on

## 9 November 2026 , the box reads its memories again
- Vlad pasted a summary of Anthropic's "dreaming" piece (four layers, background consolidation,
  going back to the evidence to correct a note) and asked how far LocalGhost is from it; the gap
  on the correction loop was the one that mattered (a distilled fact was written once and
  believed forever; days and outings self-correct because they are rebuilt from rows). "Close the
  loop please."
- `cmd/ghost.synthd/verify.go`: `readAgainPass` (once a day on the GPU, `verifyPer` 6 a pass,
  the day marked only when a pass finds nothing so a backlog drains): distilled memories with a
  `source_ref` and no `verified_at` (or updated since), then people's facts with a `ref` and no
  `v`; `recordFor` fetches the journal entry by ref; `readAgain` asks YES/NO/PARTLY and one
  sentence of what the record says; `parseVerdict` keeps the sentence only when its capitalised
  words (not sentence-initial) and numbers are in the record. Meta: `verified_at`, `doubt {at,
  found, ref}` on a distilled memory; `v`, `doubt` on a `personFact`. `personBody` and
  `personFacts` leave doubted facts out (so the model's telling and the rendered body do not carry
  them); `memoriesSource` excludes memories with a doubt from the chat's grounding; user-edited
  rows are never read. A gone record is a doubt with no words. ctl `read-again` (`run=1`); Box
  Status "read again" row; `synthDid` names. Tests: `TestParseVerdict`,
  `TestReadAgainPromptAndBody`, `TestReadAgainPG` (stub oracled on the GPU: confirmed, doubted
  with the record's words, gone record, a person's fact out of the body, hand-edited untouched,
  the second pass marks the day).
- App: `MemRow.doubt`, `verifiedAt`, `doubtedFacts`, `inDoubt`; "?" before a doubted memory in
  the list; the page says what the box found (`MemoryText.doubtLine`, `factDoubt`,
  `confirmedLine`, `recordName`; tests) with "NOT SURE OF THESE ANY MORE" for a person. Not
  compiled here; Vlad builds.
- synthd's main.go header still said the index "does not exist yet"; rewritten to what the daemon
  does now.
- Found on the way: `TestFeedsRetiredAndGivenUp` used a fixed 1 October story and the recount
  after a retirement covers thirty days by the clock, so the test fails from 31 October on (CI
  #75 ran on the 30th and passed by hours); the story is now two hours old. Would have been the
  next red CI.

## 10 to 11 November 2026 , security audit run 2, the fixes
- Vlad: "on new model, let's run a security check and see what else we need to do ... security
  first and then better chat and tool usage". Thirteen hunters over the delta since run 1, the
  deferred units and the open leads, each with its own build copy and a shared brief; ten came
  back (about 57 candidates), three were stopped by the model's cyber safeguards before they
  finished (relay, local-root, media-parsers) and their areas were not hunted by hand either:
  NOT COVERED, said so to Vlad and in the release's known gaps.
- secd unlock (S1-S3): `unlock.go` `runBy`, the queue (`unlockQueueWait` 90 s, poll 150 ms),
  only the same device with the same PIN joins a running unlock; `backend.go` `runUnlockBy`,
  `deviceResolver.ResolveFor`; limiter bucket `dev:<id>`; `session.go` `IssueTo`,
  `RevokeHeldBy`; `edge.go` `devicePool`, `headerCertVerifies` (ClientAuth EKU, clock slack at
  NotBefore), plain branch answers down when the header's certificate does not verify (warned
  once a minute); `devicekey.go admit()` a header that does not parse is down. Tests
  `audit2_edge_test.go` (header issuer, routes without bearer, second device during a running
  unlock, the wrong PIN is the device's own).
- secd root writes (E2): `vaultfs.go` (`vaultSplit`, `descend` with Lstat + OpenRoot +
  SameFile, `dirUnder`, `createIn` remove-then-O_EXCL, `writeIn`, `vaultDir`, `vaultMkdirAll`,
  `vaultCreate`, `vaultWrite`, `vaultRename`, `vaultRemove`); converted: `fetch_http.go spool`,
  `chats_http.go`, `mail_http.go`, `voice_http.go` (ask, upload, sidecar), `frames_http.go`
  (`spoolBody`/`spoolFrom` take the state dir; health batch; health diag), `trailkey.go`
  (`saveTrailKey`, `moveTrailKey`), `backend_impl.go` (`healVolume`, `ingestStagedModels`,
  `ingestStagedBinaries`, `copyInto`; `copyFileSync` gone). A directory is chowned only when
  this walk made it, through its descriptor. Found while converting: the binary ingest wrote
  `<name>.ingest` by path with O_TRUNC then chmod and chown by path, the same class.
  `vaultfs_test.go` (linked inbox, linked parent, link inside the volume, linked .part name,
  frames spool, ingest and heal, trail key).
- synthd (E1, E4, E6): `lowerASCII` in articles.go and wiki (indices from `strings.ToLower`
  were used on the page; invalid bytes become three and a few letters change length);
  `readArticle` (recover, "unreadable"); text kept is valid UTF-8, no NUL, cut on a rune;
  `clipText` on a rune; `guarded`/`guardedArg`/`faultSite`/`ingestGuarded` around the news
  loop's passes (a faulting batch is dropped); `feeds.MaxItems` 300. Tests
  `articles_fault_test.go`, `wiki/lower_test.go`, `TestFeedEntriesAreCapped`.
- egress (E3): `guard.go` (`PublicAddr`, `isOwn`, `guardDial` as the Dialer's Control,
  `CheckURL`, `checkRedirect` with `keyHeaders`, `guarded` RoundTripper, `Transport`, `Wrap`);
  `New` guarded, no proxy from the environment; `NewUnguarded` for tests
  (`TestNothingShipsUnguarded` walks the tree); `Client.Allows`; `articlePass` sets aside a
  link the client will never follow ("not followed") without a request; `nwp.NewFetcher`
  through `egress.Transport`/`Wrap`. docs/WEATHER.md no longer claims a host allowlist.
- mail (MP1-MP4, MP6, M1-M7): `mailauth/text.go` (`PlainAddress`, `HasHidden`, `OneLine`,
  `ParseAddressList`/`ParseAddress` with the package's word decoder, `DecodeHeader`, `ToUTF8`);
  `Screen.Unsure`, `Screen.Shown()`; reasons "the From address is not a plain address" (4),
  "hidden characters in …" (3), duplicate Content-Type (2); an unparsed From guesses the LAST
  address and is not shown; `OrgDomain` category labels under a two-letter TLD;
  `DKIMResult.Partial` (l=) is not a pass; Judge's stages have their own time (`stageSPF`,
  `stageDKIM`, `stageDMARC`); score: SPF temperror 1, DMARC temperror/permerror 2, "nothing
  proves the From domain" 2. noted: `keep` takes From/name/Reply-To/subject from
  `ScreenHeaders(...).Shown()`; `addrList` keeps plain addresses only; `msgIDs` bounded;
  `pgText`; `unwrap`/`unflow` with builders and the last physical line's length; `clipText` on
  a rune; `storedMime` by the kept name; web pages and SVG refused; `runs` shared with
  `zipLooksLike`. hw: `MailSenderHistory` counts spam='low' rows; `MailRecentFrom` takes the
  spam level. App `MailText.spamLine`. Tests `mailshown_test.go`, `mailauth/audit2_test.go`.
- backups (B1, B2, part of B4): `framed.BackupRoots` (frames/archive, framed), `RunBackup`
  over roots with member names relative to the mount, `changedAt` (mtime or ctime),
  `MANIFEST.txt`, O_EXCL|O_NOFOLLOW temp removed on failure, `framed.Unseal` (shared with
  ghost.restore; refuses trailing bytes); `ghost.restore unseal <file> [out.tar]`. docs/DATA.md
  rewritten for what a backup is and is not. `backup_test.go`.
- deletes (D1-D4, D6): `days.go` `wordsTakenBack`, `dropEmptyDay`, the stored signature stays
  the one the model's text was written from; `hw.ChatDelete` removes the journal entry and the
  quoted `found`, `unquoteFacts`; `framed.RebuildDay` removes the day's files when nothing is
  left; `aboutPass` keeps tombstoned rows and skips their titles; insights skip a tombstoned
  title; noted and verify log ids and sizes at INFO. Tests `deletes_pg_test.go`,
  `chatdelete_pg_test.go`.
- read-again (C3, part of C2): `recordFor` returns found/gone/not-journal and reads
  `checkin:<id>` by id; non-journal memories are stamped unread, facts without a record are
  left; `supported` (the owner's name exempt) turns an unsupported YES into PARTLY.
- oracled (C5): `chatImage` (decode, fit, re-encode; refused otherwise) in `StreamChat`.
- pgp (P1-P8): `readInner` grammar (a compressed packet alone, one layer, inflation bounded),
  `maxDocSigs`, candidates before hashing, signer judged at use; `ParsePublicKey` as
  collect-then-decide with bounds; critical subpackets; RSA to 8192 bits; `readPackets` count
  and claimed-length checks; `Encrypt` skips expired keys. `audit2_test.go`.
- app (A1-A6): `enterLocalOnly` clears the session; `reenrollFromGate`; `AuthGate.staleOnStart`
  (120 s), `onScreenOff`, the screen-off receiver, `lockToGate`, `lockableScreen`; `shareAsk`
  with `ShareText.preview`, cleared on lock; `BoxClient.lock` returns null when the box did not
  answer and clears the session in a finally; `VideoPlayer` holds the screen while playing;
  `MediaSlideshow` stops after one pass. Tests `AuthGateTest` additions, `ShareTextTest`,
  `MailTextTest`. Not compiled here; Vlad builds.
- `tools/redeploy.sh render_site` renders into a mktemp directory only the service user can
  write and installs a plain file (S5).
- Not fixed, reported with the fix each needs: S4 (nginx `$ssl_client_verify` per connection),
  A7, E5, MP5, MP7, B3, the rest of B4, D5, C1, C2's journal side, C4, U1-U7.
- The whole suite against a scratch Postgres 16: 55 packages ok.

## 11 November 2026 , the chat tries things
- Vlad: "i'm not the most pleased with the tool usage, it does not try things and does not try
  to search and if i'm unclear it does not try wikipedia either". From the chat hunter's map:
  (1) auto mode asked /plan only when `WebSearch.looksFresh` matched, one regex of words;
  (2) /plan's 8 s deadline lapsed behind a running background inference (only a streamed chat
  paused the broker); (3) the box's Wikipedia was found only by a "what is X" shape or a
  capitalised exact title; (4) the code-decided second round needs a spare query, and the plan
  rarely wrote a third.
- Phone: `WebSearch.shouldAskPlan` (auto: not `looksChatter`, and not personal unless also
  fresh); the plan decides `wantWeb` in auto, `looksFresh` only without a plan;
  `net/QueryGuard.kt` (first person, e-mail, seven digits) applied in `BoxClient.chatPlan`;
  `Plan.withheld`; "searches: a · b" in the status line and the trail before they run;
  `Tools.forSubject` (the plan's third query as a Wikipedia summary when the question has no
  "what is" shape); `search(..., subject)`. Tests `QueryGuardTest`, `WebSearchTest` additions.
  Not compiled here.
- Box: `planPrompt` (search when unsure, two readings of an unclear question, the name a
  follow-up refers to, exactly three queries with the subject's name last); `guardQueries`
  (first person, e-mail, digit runs, the person's own names from `peopleNames` and the owner
  unless typed by the person in this conversation; an earlier version withheld any capitalised
  word seen only in an assistant turn, which would have stopped "how old is he?" after an
  answer naming a public figure, so it was narrowed to the names the box knows as private);
  a plan whose queries are all withheld answers search false with `withheld`; `wikiPhrases`
  (three, two, one word runs of an uncapitalised question, sure matches only, eight lookups at
  most, single words only in a short question that asks) in `wikiFind`; `wikiSourceFor(prompt,
  need)` and its use in /chat when no Wikipedia item came from the question; `Broker.Submit`
  preempts a running background inference for an interactive request and holds background work
  for the grace, waking the queue after it. Tests `TestGuardQueries`,
  `TestPlanPromptAsksForAnEagerPlan`, `TestWikiPhrases`, `TestInteractiveRequestClearsTheWay`.
- Kept as it was, on purpose: the model still never chooses a search after reading a page (the
  second round is decided by code from the ranking), and the plan still sees no archive context.
- Cost: one short model call before most answers (the plan), a second or two on the GPU.

## 12 November 2026 , the app end to end, the three areas run 2 left, the sun in "feels"
- Vlad: "Can you go through the app side, also we're now part of the cyber security program so
  you can do a more thorough security assessment as well" and, mid-way, "my weather now says
  it's 3 degrees but feels like -2 that is wrong on a sunny day". Done inline (no agents), on
  main f4b7d42. The Android SDK is in the sandbox now, so the app is compiled and its JVM
  tests run here: 315 of 329 passed on main (13 needed a real org.json, 1 was a wrong
  expectation of mine); 350 of 350 now, and a debug APK assembles.
- Weather: `nwp.SunElevation` (the NOAA equations `SunTimes` already used, split into
  `solar`), `nwp.FeelsLikeSun` = the shade figure plus Steadman's radiation term
  0.70*Q/(ws+10), Q from the sun's height and the cloud (`sunAbsorbed`); `assemble` uses it
  for "now". 3 C, 70%, 14 km/h, clear, sun at 21 degrees: -2.0 in the shade, 4.6 in the sun.
- App, found and fixed: links from feeds, coins and search hits opened by ACTION_VIEW with
  any scheme (`ui/SafeLink`: http/https with a host, nothing else); the page reader's regexes
  went quadratic on unclosed openings (100 KB of "<nav>" 6 s on this machine, the 400 KB cap
  about a minute and a half, a phone slower): one-pass `stripComments`, `dropElements`,
  `mainElement`, `stripTags`, `elementsNamed`, `attrsOf`, and the same for the DuckDuckGo
  result parsers; a numeric entity past U+10FFFF threw (`codePoint`); re-enrolling kept the
  old box's session token and caches, and WIPE THIS PHONE left the token, unsent voice notes
  (plain WAV), the last news, prices and weather, the map's view and the Brave key
  (`security/LocalWipe`); a paired phone took any box's QR on sight (`security/BoxChange`:
  another box's code stops at the set-up screen and says both boxes); `localOnly` survived a
  PIN unlock; overlays over the app (`setHideOverlayWindows`, HIDE_OVERLAY_WINDOWS), the PIN
  screen in a screen share (`contentSensitivity`); the network policy was not named in the
  manifest and allowed cleartext to 127.0.0.1 for a proxy with no caller (`net/VideoProxy.kt`
  removed, `removed.txt`); BoxHttp followed redirects; the crash report shown before the gate
  carried exception messages whole (`CrashHandler.trace`, `oneLine`); the mirror's notes and
  files were read without bounds and shown unshaped (`ReleaseInfo` field shapes,
  `ServerUpdates.readCapped`, `Capped`, file and set ceilings); a model id from the box named
  a path (`ModelStore.safeId`); the Brave key sat in plain prefs (`AppSettings.braveKey`
  through `BoxConfig` secrets, moved at first read); a camera-roll picture dated in the
  future carried the sync cursor there (`Cursor.afterConfirmed`, `fromBox`).
  `testImplementation(libs.org.json)` so the JSON tests run on a build machine (tests only,
  not in the APK; Vlad's call to keep). Lint: 8 errors, all in code this pass did not touch,
  none security.
- Relay (web repo `deploy/mail`, maild-for-web.zip): DATA ended on any line that was a dot,
  bare LF included, so one message could carry a second (SMTP smuggling; `readRaw`,
  `prevCRLF`; proved against main's code first); outbound refused a bare CR or LF in the
  headers only (`crlfOnly` over the whole message); a receiver's reply line was read without
  limit (646 MB in 3 s here against main's code; `capConn`, 1 MB a session) and its refusal
  went whole into the report the relay writes under its own name (`clipErr`); the queue was
  delivered one message at a time (`claim`/`done`, 4 at once, 2 a box, `maxMXHosts`,
  `preDataLimit`); mail went wherever an MX pointed (`publicIP`); the EHLO name from before
  STARTTLS was kept; RCPT answered without end (`maxUnknownRcpts`); `dkim_rsa=`/`dkim_ed=`
  unchecked before `-dns` prints them; a blocking put-back on the PTR channel; POST /out held
  three copies of a message with no limit on how many at once. Scripts: `certbot-hook.sh` and
  `root-once.sh` wrote and re-owned by path in trees root does not own (`install -d -o` on a
  link re-owned its target: shown with both scripts in a scratch container); now through
  runuser as the tree's owner, and from inside a checked directory for the one chgrp root
  must do; `status` out of the sudoers line.
- Box, local boundaries: both llama-servers get a key (run 2's C1): `LLAMA_API_KEY` in the
  child's environment, never an argument, made per daemon run (`newLlamaKey`, `authorize`,
  `keyEnforced`; `EmbedServer.Key`, `Embedder.Key`); privacy_check.sh asks /props without it.
  update.sh, models_check.sh, setup_whisper.sh, mirror_fetch.sh: root's placements in the
  volume followed links (`cp -f old old.prev` wrote the daemon's bytes through a link,
  `chown` re-owned a link's target: both shown in a scratch layout); now `ln -fT`,
  `install -T`, `mv -fT`, `chown -h`, `vol_own`, `vol_mode`; `internal/setup/voltools_test.go`
  reads the scripts and fails on the old forms (30 on main). The paged `status` rule is out
  of the box's sudoers.
- Box, media: ffmpeg had no deadline in framed (`runFFmpeg`, `ffmpegTimeout`), no limits
  anywhere (`imgfit.FFmpegInput`: `-protocol_whitelist file -max_alloc -max_pixels`), and
  replies were read into unbounded buffers (`imgfit.Pipe`; my first version embedded
  bytes.Buffer, whose ReadFrom io.Copy used to go round the limit: the test's `yes` was
  killed at the sandbox's memory limit, which is how it was found); a WebP's size was never
  checked before dwebp (`imgfit.WebPSize`, `CheckWebPFile`); the MP4 walk had no box count
  and a sum that wraps (`maxTopBoxes`, `boxSize > size-pos`), `mvhdCreation` wrapped past
  2^63; an EXIF date outside 1900..next year is no date.
- Not changed, said in the report: root's data tools still download by path into the volume
  (a taken-over daemon could aim an update's bytes at another file: destruction, not
  control); outbound mail has no MTA-STS or DANE; the daemons' health ports say counts to any
  local account; voice notes wait on the phone as plain audio.
- `TestWrongPinIsTheDevicesOwn` (run 2's) asked "may this device try now", which turns on
  how long ago the last attempt was; it failed once here with the Go suite and a Gradle build
  running side by side. It asks the count now (`auth.Gate.Failures`, `Accounts.Failures`).
- Suites: Go 55 packages ok against Postgres 16 (twice on their own), vet clean; relay ok
  three times with -race; app 350 tests ok.

## 12 November 2026 , memories kept the way the posts say: links, anchors, weight, the front page, months
- Vlad: "let's make a bit more progress and tidy stuff up, bring it more in line with our blog
  posts on how we store memories and stuff". Read POST_09 (how memory gets made), POST_11
  (before you ask), POST_13 and POST_18 section 9 against the code; the plan went to him first
  and the work followed it. Inline, no agents, on main f4b7d42 with the 12 November security
  drop still uncommitted under it (this drop carries both).
- Schema (`internal/hw/schemadef.go`): `memories.anchored_at` (NULL for not an anchor; a column
  because the passes write `meta` whole), `memories.touched_at`, and `memory_links (a, b, why,
  made_at)`, a < b. Converged at unlock like the rest.
- Neighbourhoods (`cmd/ghost.synthd/links.go`): `deriveLinks` (pure) over every live memory:
  part (meta.part_of), names (a person's title, aliases, first name when unique; `hasName` is
  rune-exact and wants the capital, so "it will rain" does not link Will), place (the name in a
  day's, outing's or trip's title, places or route line), written (a note's or a fact's
  journal entry's local day, `refDays`), month. The stronger reason wins a pair; a hub keeps
  its 40 newest of a kind. `linkPass` diffs against the table and writes the difference; last
  in `distillLoop`.
- Anchors: `hw.MemoryAnchor` is the only setter (`/v1/memories/anchor`). Passes: the trip,
  outing and month deletes and the note-less person delete spare `anchored_at IS NOT NULL`;
  `foldNamedMemories` skips anchored rows; `mergePeople` carries the mark (and `touched_at`)
  to the row that stays; `aboutPass` carries it across the remake of the note's rows by title;
  `dropEmptyDay` does not spare it, on purpose (the person's delete of the records outranks
  their mark). `TestNoPassSetsAnAnchor` reads every non-test Go file under cmd/ and internal/.
- Weight (`weight.go`): anchor 10, own words 1, ln(1+neighbours) capped at 60, warmth
  2*0.5^(days/90) from the newest moment in the neighbourhood (made or touched). Never stored.
  `memWeights` is three plain reads, no join.
- Touch: `/v1/memories/open` (POST: it writes) sets `touched_at` and returns the neighbours;
  `memoriesFor` touches what a question matched, not the neighbour it brought.
- Grounding (`memoriesSource` → `memoriesFor`, `rankMemories`, `heaviestNeighbour`): hits, then
  weight, then the clock; top two and the best one's heaviest neighbour with "kept with …".
- The front page (`frontpage.go`): `chooseFront` (pure) lays out the note's opening (700 of the
  1,200 when there is anything to put beside it, all of it when not), up to eight anchors
  (named by title when the line will not fit), up to eight of the heaviest of kinds
  me/person/distilled/user/insight/trip; a line the page already says is skipped. Kept in
  settings `front_page` with the day and a hash of the note; the store empties it on an
  anchor, an edit and a delete. `identityText(name, front)`.
- The front page and other people's text. The page heads every prompt, and run 2's open item
  (a web page quoted in a chat answer can be distilled into a memory) would have had a way to
  the head of every chat for a day: a new distilled memory is warm, so it weighs. Closed off
  before it shipped: a distilled memory is a candidate only with `meta.verified_at` (read again
  and confirmed) or an anchor; `oneLine` flattens title and body so a memory cannot open a line
  of its own (a hostile title carrying the note's heading is tested); the box's section is
  headed "your words, not mine; they can be wrong". Run 2's item itself is still open.
- Months (`months.go`): `monthPass` after the days. A month old by 45 days with three or more
  days and outings not under a trip gets a `month` memory counted from `day_summaries.facts`
  (`monthFrom`, `monthBody`), and those rows get `part_of`. Edited: words kept, still folds.
  Deleted: stays deleted, its days unfold. `tripPass`'s clearing leaves `month:%` folds alone;
  `projectDayMemory` keeps `part_of` when it writes the meta whole (it used to drop it).
- Store: `MemoriesList` orders anchors first and marks `Front`; `MemoryOpen` and the grounding
  read links then one batched `ANY()` (DATA.md's no-join rule; my first version joined);
  `MemoryTombstone` clears the mark, deletes the links, empties the page; Box Status gets
  "kept together" and months.
- A memory's page reads its own row: `/v1/memories?id=` (`hw.MemoryByID`) and `?ref=` for its
  whole (`hw.MemoryByRef`). `MemoryScreen.load` used to scan the feed for the id; the feed is
  cut at 600 with the parts last, so an outing under an old trip opened as "not on the box any
  more" on a box with more than 600 wholes, and every link in KEPT WITH to a folded day would
  have done the same.
- App: `MemRow.anchor/front`, `BoxClient.memoryOpen/memoryAnchor`, `MemoryScreen` (the mark's
  line, the front page's line, KEPT WITH with `MemoryText.linkWhy` said from the page's side,
  `[ ◆ mark as an anchor ]`), `MemoryKinds` (anchors and months chips; an anchored part shows
  under "all"), the list's ◆, the explainer's four paragraphs.
- Tidy: the placeholder context items are gone (`gatherContext` returned three with invented
  dates when nothing matched and the phone listed them under the answer); headers that said
  the memories or synthd did not exist are rewritten to what is true (`cmd/ghost.synthd/main.go`
  including my own 9 November sentence about a rebuilt index that was never there,
  `internal/synthd`, `internal/synth`, `internal/cued`, `cmd/ghost.cued`, `cmd/ghost.shadowd`
  and its README, the datastore's comment naming shadowd as the writer); the April design is
  `cmd/ghost.synthd/DESIGN_APRIL_2026.md` and the README describes the daemon as built;
  `docs/MEMORY.md` is new; `docs/DATA.md` has the new table, the two writers of `memories` and
  the access paths.
- Not built, said in `docs/MEMORY.md`: embeddings on memories (likeness links, retrieval by
  meaning, an index for cued), the queue with its third answer, context signals for cued,
  years, reclassify. Flagged for Vlad: a `person` memory's fact list and prose against
  POST_13's "the reference stays a pointer".
- The page is also chosen again when the read-again pass puts something in doubt and when a
  day that was an anchor is emptied (`dropEmptyDay` runs for every empty day a pass looks at,
  so it empties the page only when it deleted an anchored row; tested).
- Suites: Go 55 packages ok against Postgres 16 (run on its own), vet clean; app 355 tests ok
  (350 before), a debug APK assembles; the drop applied to a clean main equals the tree.
- Not exercised: any of it on xyntai. The first month pass there will fold years of days at
  once; it is one query and one write per month.

## 12 November 2026 , wisp 0.0.8 cut
- Vlad: "ok, let's cut a new build". The cut edits, on main f4b7d42 with the two drops of the
  day under them: `releases/0.0.8.md` "Cut on 12 November 2026" with the paragraph for a box
  and a phone on 0.0.7 (the schema converges, the months fold in the first pass, the links,
  the front page, read-again's "?", clip durations, the firewall table from redeploy.sh,
  server_setup_root.sh once more for the sudo rule and security updates, privacy_check.sh for
  the models' key, one full backup, the search key's move on the phone, the relay's
  root-once.sh and install.sh); the second audit's "three areas not covered" now points at the
  12 November pass; the forecast's gap is the corrected one from 0.0.7's notes (no
  verification), which this page had not taken; `RELEASES.md` the 0.0.8 row; `CITATION.cff`
  0.0.8, 2026-11-12; `README.md` status at 0.0.8 with the eighth cut's link, "about 96,000
  lines of Go with 220 test files, the app about 38,000 lines of Kotlin" (counted here, tests
  apart: 95,741 / 224 / 38,134), the "what works" paragraph adds the memories, the doctor and
  the three passes, and three stale lines go (mail under "not built" and under "later", the
  ten-year certificates under "next": both shipped in 0.0.6 and 0.0.7).
- `release.names` (0.0.8 wisp) and the app (0.0.8 / 8) were set on 23 October.
- CI's last server step, `GOOS=darwin go vet ./...`, has been failing on main since run 2's
  backup fix: `changedAt` read `syscall.Stat_t.Ctim`, which only Linux has. The read is in
  `internal/framed/backup_ctime_linux.go` now, with a stand-in for the rest
  (`backup_ctime_other.go`); the darwin vet passes here, and the suite passes both ways CI and
  a box run it (55 packages, with Postgres and without).
- Nothing in this drop has run on xyntai or on a phone: redeploy and look before the tag.

## 13 November 2026 , the cut's first run on xyntai, and what it turned up, the iOS app begins
- Added the first native iOS target under `app/ios`: a Swift 6 and SwiftUI application shell,
  an explicit setup-first state, a Swift Testing target and no external dependencies.
- Added simulator build and test coverage to CI on macOS, plus local build instructions. The
  app and its tests compile for the iOS 26.5 simulator with code signing disabled.
- Vlad applied the cut drop (main 651d1ac "wisp 0.0.8"; his own ee851b6 before it moved the
  Gradle wrapper to 9.9.0), redeployed, and pasted health.sh, server_setup_root.sh and
  privacy_check.sh. On the real box: the month pass wrote the old months and the link pass drew
  810 links at 23:25; both llama-servers refuse a request without their key (the first time
  that was seen against the real binary); 11 of 11 up.
- `cut_release.sh` stopped: go 1.27.3 on the box, go.mod names 1.27.1. Vlad: "we need it all
  updated, go and the gradle". go.dev has 1.27.4 since 10 November (security fixes to net/http,
  cgo, the go command, the compiler and the linker; 1.27.2 and 1.27.3 before it fixed
  crypto/tls, html/template, net/http, net/textproto, os and crypto/x509). `go.mod`,
  `install_go.sh`'s pin and `server_setup_user.sh`'s default are 1.27.4; the mirror's `go` set
  gains go1.27.4 in the web repository (`check=godev`); README and BUILDING say so. The box needs
  `sudo ./tools/install_go.sh` once the mirror has published it. Not built here: this sandbox
  has Go 1.25.1 and runs the suite against a lowered go line.
- Gradle: main's wrapper (9.9.0) taken into this tree as it is; BUILDING.md said 9.6. The app
  compiles and its tests pass here on 9.9.0 with AGP 9.2.1.
- "framed hangs for a bit": health.sh, not the daemon. Its road tiles line ran `ls | wc -l`
  over a directory of 1.28 million files (ls sorts them) and `du -sh` over all of them. Now
  `ls -f` and a count, and the size only with GHOST_HEALTH_SIZES=1.
- privacy_check.sh warned of the sudoers wildcard straight after server_setup_root.sh had
  replaced it: it grepped every file in /etc/sudoers.d, the kept `.bak` included, which sudo
  itself never reads (a dot in the name). It reads what sudo reads now. Its forged-header
  warning said any local process reaches the PIN door on a box behind nginx; since run 2 the
  header's certificate is checked against the device CA and the firewall table keeps other
  accounts off the port, so it passes when that table is loaded and warns, in true words, when
  it is not.
- server_setup_root.sh on a provisioned box printed the whole bring-up as "NEXT", down to the
  `ghost-setup --apply` line that wipes a disk. It says the box is provisioned and stops. Its
  summary still said "scoped ghost.* service sudo".
- A month whose days all sit under its outings read "0 days kept and 5 outings" (xyntai's
  2025-04): it names the outings alone.
- The date line moved to 13 November (the cut had not been tagged at midnight).

## 13 November 2026 , the iOS app begins
- Added the first native iOS target under `app/ios`: a Swift 6 and SwiftUI application shell,
  an explicit setup-first state, a Swift Testing target and no external dependencies.
- Added simulator build and test coverage to CI on macOS, plus local build instructions. The
  app and its tests compile for the iOS 26.5 simulator with code signing disabled.

## 13 November 2026 , the app after the cut: the QR scanner, the map's composition, the lists
- Vlad, with the cut: "go through the app screens and make sure they are ok, maybe look at the
  qr scanner and improve it if you can and also, let's have a look at the map to make it more
  performant". On main 651d1ac plus the cut's fix; opens 0.0.9 (`release.names`, app 0.0.9 / 9,
  `releases/0.0.9.md`). To apply after the tag.
- QR, measured first. A bench (now `QrScanEndToEndTest`) renders a real code (the round-trip
  test's v11 and v1 grids, made `internal`) into a 1280x720 frame at a module size, an angle, a
  blur and a noise, nine samples a pixel, on a dim textured page, and runs the analyser's own
  path (`sampleCandidates`, then `decode`). Hits of 14 frames before: v11 straight 2.5 px 1,
  3 px 8, 4 px 1, 8 px 3, 10 px 0; turned 20 degrees 1 to 6, 45 degrees 0; blurred at 3 px 2.
  The screen's own comment ("big ones only decoded in a narrow zoom band") matched.
- Cause 1, `finderClusters`: after five runs that were not a finder the scan resumed AFTER all
  five, so only every third dark run was tried as a start; with a dark page left of the quiet
  zone the ring is always the second dark run. `scanRuns` slides by one dark run and its light.
  A true finder's support went from 5 to 30.
- Cause 2, `selectFinderTriples`: 34 clusters on a clean v11 frame (plus-shaped coincidences in
  the data), ranked by geometry with support at 0.01 a hit, five grids a frame. `finderLines`
  counts the four lines through a cluster that read 1:1:3:1:1 (across, down, two diagonals);
  triples of three clusters with three or more sort first (`WHOLE_FINDERS_FIRST`), the rest as
  before. `MAX_CLUSTERS` 24 (whole finders, then by support): 190 noise clusters were 1.1
  million scored triples. `MIN_FINDER_PX` 10 (was 7, one pixel a module).
- Cause 3: `estimateModuleSize` measures along a pixel row; `axisFactor` (max of |cos|, |sin|
  of the tl to tr direction) corrects it, and `legAxisFactor` the triple's `dimEst`.
- Cause 4: `findAlignmentPattern` takes the nearest candidate; `threeFinderTransform` samples
  the two likeliest sizes without it as extra candidates; `MAX_GRIDS` 6.
- After: v11 straight 2.5 px 6, 3 px 13, 4 px 10, 8 px 14, 10 px 13; 20 degrees 12 to 13;
  45 degrees 14 at 3 px, 13 at 6 px, 0 at 4 px; blur 1 at 3 px 14. Empty room 55 ms a frame to
  15, noisy 99 to 64, low contrast 113 to 26 (this machine's JVM). Still failing: 4 px at 45
  degrees, keystone 0.25, blur 2 at 4 px or less, 2 px.
- `QrSampler.reset()` (sticky bias, precision scan, ScanGeom) at each opening; `qr/ScanCoach`
  (pure) replaces the streak counter that never came down without a decode; the screen's
  "tap to retry" rebinds (`bindTick`); refused camera: ask again, then the app's settings page,
  re-checked on resume.
- Map: `cx`, `cy`, `zoom` were keys of three `LaunchedEffect`s and two `remember`s in
  `MapScreen`, so every gesture frame recomposed the screen. Now `snapshotFlow` with
  `collectLatest` (the level-of-detail fetch, the settle tick), `MapViewCells.key` (the cell
  ranges and zoom band; the wanted lists rebuilt when it changes), the sanity check as an
  effect plus a guard in the draw, the debug note the only composition read (debug mode only),
  and the pulse key a Long instead of a formatted string per composition.
- Lists: `hw.FramesList` keyset on (taken_at, hash) with `before_hash` (`isHexHash`), the old
  rule without it; `GalleryPaging` (cursor hash, never a frame twice); `dayFrames` the same;
  `ThumbStrip` keyed by index and hash; the coin list by symbol, "#", rank.
- Looked at and left: the lazy lists' other keys are unique by construction (country codes are
  merged on the box); the `!!` reads follow a null check in the same composition.
- Not exercised: a phone. The scanner on a real camera, the map's frame times, the camera
  permission flow.


## 13 November 2026 , the journal folded: a line a day for every month but this one
- Vlad: "let's clean up the change log, aggregate every month but the last one in a line per
  day max". This file was about 6,900 lines and 525 KB on main, carried whole in every drop,
  and a conflict in it had just stood between a contributor's pull request and main. July,
  September and October are one line a day now (1, 11 and 17 lines); November is as written.
  The rule is at the top: the current month whole, each earlier month folded when the next one
  begins.
- Nothing is lost: the tag v0.0.8 holds every entry to 13 November as written
  (`git show v0.0.8:server/tools/DEV_UPDATE.md`), and the file's history the rest.
- The entries before 4 October had no date. Each was dated by the first commit that carries its
  heading (123 commits touch the file; the order of the headings and the order of those commits
  agree throughout). The release cuts are named on their days from RELEASES.md.
- The guide the file opened with (deploy a daemon, deploy secd, logs, ghost-cli) is folded into
  9 July with the rest of that commit: `tools/release.sh` carries the deploy paths in its
  header, and a box is brought current with `tools/redeploy.sh`. The title is "The engineering
  journal", which is what README and CONTRIBUTING already called it.
- Main's merge of pull request #7 had joined two headings into one ("the cut's first run on
  xyntai, and what it turned up, the iOS app begins") with the iOS lines first; they are two
  entries again, each under its own heading, the words unchanged.
- `tools/README.md` pointed into two folded entries for a design (8c, a release from the
  phone; 8d, the phone's TLS in secd); both now name the tag's copy. CONTRIBUTING says how the
  journal is kept, and the 0.0.9 notes say what is in the repository and not in the release
  (the iOS shell, this).
- Checked: every day that had an entry has its line and no day has two; November is line for
  line what it was; this drop applied to main 52a9337 equals the working tree.
