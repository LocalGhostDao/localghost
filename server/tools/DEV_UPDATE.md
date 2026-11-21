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

## 13 November 2026 , the cut's first run on xyntai, and what it turned up
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

## 13 November 2026 , a weekend away is a trip: the night counted, two places on one day, short stops
- Vlad: "do i need to run anything on the memories or will they naturally be rearranged? also
  daily trips should be in there, i went to a few places for just 2 days, in the uk they should
  appear as a trip". Nothing to run: the loop does it every ten minutes. The second half is a
  bug in three parts, found by reading the code against that sentence. His archive was not
  looked at.
- The rule. `chainByTime` asked for 24 hours between a chain's first photo and its last (the
  comment said "a trip is at least a night away", the code said 86400). Saturday 13:00 to
  Sunday 11:00 is 22. Now `nightsAway`: the clock where the photos were taken passed four in
  the morning (by the sun, longitude over fifteen, no zone needed), with eight hours at least
  from first photo to last, so a night out that ran past four is not a trip. Four and not
  midnight, so an evening that runs to one is one evening. The chain's end is the last photo
  of any outing in it; the old code read the end of the one that began last.
- The refs. `outings.Build` named an outing by the day it began. Two outings can begin on one
  day (a photo 30 km from the centre begins a new one; photos at home before setting off are
  one too), and both were written to `outing:<day>`, the later over the earlier, at every pass,
  the model's telling thrown away each time. So "a few places" on the Saturday were one memory
  holding the afternoon, and the chain began at the afternoon. `uniqueRefs`: the first keeps
  `outing:<day>`, the next is `outing:<day>.2`. The app's `MemoryText.dayOf` reads the day out
  of both. On This Day still lists only the first outing of a day (its query wants the ref to
  end with the date); the day's own story comes first there anyway.
- The stops. A group under three photos was dropped. Two photos at dinner on the evening of
  arriving, in another town from the next day's, took the first day with them. `foldStops`: a
  short group away from home joins the group before it when that one is away and ended within
  36 hours, else the one after. Its photos are marked aside when the host is an outing in its
  own right: counted in the photos, places, tags and dates, and the centre stays where it was.
  Short stops in a row join each other. At home, or with no position, dropped as before.
- The title. A trip that never left the country the person lives in (`homeCountry`: the one
  most photographed among the outings that are not away) is `domestic` in its meta and titled
  by its places. The body's template is unchanged on purpose: a changed template has the model
  write the trip again, and that would have been every trip on the box.
- So that nothing has to be run: `outings.Rules` is part of the archive's signature and
  `tripRules` is kept in settings, so this build's first pass groups and chains again with no
  photo and no new day to set it off. A trip takes its outings and days back from the month
  they were folded into (that was already so; a test holds it now).
- Tests. `internal/outings`: two outings on a day, the York, Whitby and Bridlington weekend,
  stops in a row; all three fail on the old code. `TestATripIsANightAway`: the 22 hour weekend,
  a late arrival, a day out, an evening past midnight, a night out past four, Vancouver and
  Tokyo by their own clocks, the chain's end, the title at home, abroad, through two countries,
  and with no home known. `TestConsolidatePGAWeekendAtHomeIsATrip` on Postgres: the trip made
  by `consolidatePass` with nothing changed and the day's work done, its parts moved out of the
  month, a second pass leaving it alone. `MemoryTextTest` for the app.
- Not done, and said in the notes: a trip is made from photos only (the trail knows the nights
  and is not asked); the rules ran on made-up weekends, not on a real archive. A single day
  out stays an outing; Vlad was asked whether those should be trips too.
- Main's copy of this file had the iOS lines twice (a merge brought the joined heading back on
  top of the split one). This copy has them once.

## 15 November 2026 , iOS enrolment from the QR (David, pull request #18)
- David (0ex-d) added the iOS setup: the enrol link parsed as `pair.EnrollLink` writes it (v2
  and v3), a VisionKit scanner with its own LGQR2 assembler (the Cauchy rows and the CRCs as
  `pair/qrstream.go`; binary parity frames read from the QR's byte-mode codewords), the
  certificate and key stored in the Keychain on this device only, a setup screen in the app's
  terminal look. An 8 of 8 enrolment on a physical iPhone, by his account.
- Reviewed before the merge (project doc of 15 November): his test's eleven frames are frames
  the box makes today and rebuild its test payload through the server's own decoder; nothing
  outside `app/ios` changed but the iOS CI step. Merged by Vlad as `d1712cd`.

## 16 November 2026 , iOS: the channel, the unlock, the key replaced, Box Status; what the review found
- Vlad: "ok, i've merged the thing, let's fix the things", then "also let's implement a bit more
  of the functionality, and let's have a look at the tests that fail". Written here without a
  Mac: the Swift is not compiled in this sandbox (a Swift toolchain was downloaded and not
  allowed to run), so the first build is CI's (`ios.yml`) or Xcode's. The Xcode project file was
  parsed and every reference checked.
- What the review found, fixed:
  - Keychain items outlive the app on iOS, so the box-made key stayed after an uninstall and a
    reinstall said "enrolled" with no way back. `InstallMark`: the first launch after an install
    (no mark in UserDefaults, which an uninstall does take) deletes every key, certificate and
    the stored records first; the mark is set only when that worked. An iPhone enrolled with
    the merged build enrols again once.
  - "Forget this box" on the box screen and on the unreadable-credentials screen (that one also
    has "try again"), with a confirmation that says the box still lists the iPhone until it is
    retired there.
  - The scanner asks for the camera through AVFoundation (VisionKit's `isAvailable` is false
    both before the question and after a refusal, and the screen stayed dark either way), says
    when it is refused, opens Settings, and starts by itself on the way back. "Cancel / Type
    instead" is "Cancel": typing could never enrol.
  - Both READMEs passed `CODE_SIGNING_ALLOWED=NO`, under which the Keychain tests cannot pass.
- The key no longer sits in the record. At enrolment it goes into the Keychain as a key (the
  box's PKCS#8 read by CryptoKit, written as X9.63) beside its certificate, which the Keychain
  pairs into an identity; the record keeps the key's tag (`DeviceKeys.swift`).
- `BoxChannel.swift`: URLSession with a delegate that trusts the server whose leaf certificate's
  SHA-256 is the QR's fingerprint (constant-time) and nothing else, presents the identity for
  the client certificate, follows no redirect; ephemeral, no cache, no cookies, TLS 1.2 or
  newer. A 503 or 502 is "the box did not answer", whatever the box's reason. `BoxCalls` is the
  protocol the model talks to, so the tests give it a fake.
- `BoxModel.swift`, `BoxView.swift`: the PIN to `/v1/unlock`, one poll a second with the steps
  named as the Android app names them, the session from the poll that says done (Keychain,
  device only). Then the rekey `secd/rekey.go` asks for: a key made in the Secure Enclave (a
  Keychain key in the simulator), its SPKI, the v2 proof ("localghost rekey v2\n" + hex SHA-256
  of the presented certificate + "\n" + SPKI, ECDSA-SHA256 DER), the new certificate kept as a
  pending hand-over before the confirmation over it, the old key deleted after. Again once a
  day after an unlock (the box's `renewAfterHours`). A confirmation whose answer was lost is
  sent again at the next start; and since the box answers a retired certificate as if it were
  down, an unlock that gets that answer with a hand-over pending is tried over the new one.
  Then `/v1/status`: the daemons and the datastores.
- Checked against the box's Go: the P-256 SPKI prefix (`x509.MarshalPKIXPublicKey`), the rekey
  message for a known certificate, the RFC 3339 expiry. The Swift tests (`BoxTests.swift`,
  `EnrolmentTests.swift`) carry those vectors and run the model's order against fakes: a PIN
  that opens and a key replaced (which identity each call went over), a refused PIN, a lost
  confirmation, a retired certificate, a key made today, forgetting, the install mark.
- CI: the iOS job is its own workflow, `ios.yml`, on pull requests and pushes that touch
  `app/ios` (a drop committed straight to main is built too), `checkout@v7` (v4 ran on the
  Node 20 GitHub is retiring), one simulator booted before the tests and parallel testing off
  (a clone that does not boot in time is a common timeout on busy macOS runners), test time
  allowances, a 30 minute limit. `ci.yml`'s jobs have limits too (server 20, app 30), so a hang
  fails named within minutes instead of holding a runner for six hours.
- The tests that fail. The last run on pull request #18 was green on all three jobs (server
  1m22s, app 2m48s, ios 3m09s); David's "CI test fails mostly due to timeout" was about the
  runs before it, whose logs are not readable from here. The Go suite was run here as CI runs
  it, with Postgres, under busy loops on two cores: once whole (passed), and fifteen packages
  with timed waits eight times each under four loops. One real failure:
  `TestAnswerIsSavedWhileItIsWritten` fails every run after the first in one process, `pg ERROR
  55006: database "lgtest_answers" is being accessed by other users`, after Postgres's 5 s wait
  for the earlier run's connections (the answer saver's) to go. CI runs each test once, so it
  never shows there, but `go test -count=N`, the way to hunt a flaky test, failed on it every
  time. Every test database (26 drops in 24 files) is now dropped `WITH (FORCE)` (Postgres 13
  and newer; the box, CI's runner and this sandbox are 16), and the test passes five runs in a
  row; main's version fails the second. The other failures were the load's: internal/hw and
  cmd/ghost.synthd running eight times in a row past an 8 minute limit, one and three seconds
  into a test. Nothing else failed, so nothing else changed. A red run's `--- FAIL` lines would
  name David's.
- Not exercised: any of the iOS work on an iPhone or against a box; the first compile.

## 16 November 2026 , the chat asks how much answer: short, medium, long
- Vlad: "can we have a bit more of a setting on our chat where we want an answer or a story. For
  example if I ask about something or some place I want to choose if it's short answer, medium
  or long and on long I want research with internet and local sources".
- The app: a third switch in the chat's top bar, beside incognito and web, cycling short →
  medium → long (`AnswerLength`, `AppSettings.answerLength`, medium unless changed). It rides on
  every `/v1/chat` as `length`. Medium changes nothing: no word added to the prompt, the same
  search as before (five results kept, three pages read, the plan's first two searches).
- Short: one search at most, two pages read (`WebSearch.Depth.QUICK`); the box tells the model
  one to three sentences, no headings, no lists.
- Long, on the phone: `AnswerLength.wantWeb` searches in auto although the box's plan said
  nothing outside was needed, when the plan still wrote searches (the box's `guardQueries` vetted
  them) and withheld none; with no plan in time it searches unless the guards that follow stop it
  (a question about the person, a follow-up that may not borrow, the box's own numbers). Web off
  is never overridden; the trail says why a long answer did not search. `Depth.DEEP`: all the
  plan's searches at once, eight results kept, six pages read; nothing is left for the box's
  second round, so none is offered.
- Long, on the box (`cmd/ghost.synthd/length.go`): five of the person's memories instead of two
  (`memoriesForN`), twelve context items instead of six, the Wikipedia article's opening and its
  sections after it, the one the question points at first, up to 6,000 characters
  (`wiki.SectionsFor`; the item carries its own cap past sanitize), the instruction for the long
  answer (the answer first, then the story, short headings, web findings cited [n], the box's
  Wikipedia and the memories named, gaps not filled), a line in the trail. After a brief think a
  long answer gets 6,144 tokens and after a deep one 12,288 (oracled `StreamChatBudget`, capped
  at 16,384); without thinking there is no cap, as before. `webMaxHits` 8 → 12, which the deeper
  search and a second round fill.
- Found on the way: the Wikipedia item's cap was the opening's length (900 + 120), so the
  section added after a long opening was cut to a few words for every question. The cap now has
  room for the section; `sanitize` cuts on a rune boundary.
- secd forwards `length` when it is one of the three words. The prompt's assembly is a function
  now (`chatInput`), so its order is tested: the context, how to use it, the length's words,
  the question.
- Tests: `length_test.go` (the words, the instruction's place for each length, with and without
  context, for a follow-up), the long Wikipedia item against the Eiffel Tower fixture (the
  sections, the cap, "tell me about" going past the opening only when long), `memoriesForN` on
  Postgres (the lone old note comes too), oracled's caller budget and its cap, `SectionsFor`,
  `AnswerLengthTest` in the app (the cycle, the depth, medium deciding as before, long keeping
  every guard, the phone model's room).

## 16 November 2026 , iOS: the client certificate on the task delegate, the sources in folders (David, pull request #19)
- David (0ex-d), closing issue #11 ("Implement the pinned mTLS connection to the box"), on top of
  the iOS channel of the same day. `BoxTrustDelegate` answers the challenges on the task delegate
  too (`urlSession(_:task:didReceive:completionHandler:)`), with the same answer as the session
  one: server trust only by the pin, the client certificate from the Keychain identity, built
  once. His note: Foundation hands a request's challenge to the task delegate and does not fall
  back to the session's when that method is missing. The redirect refusal moved to the
  completion-handler form. A test proves a client-certificate challenge on the task path gets
  the device's credential (a seam takes a `URLCredential` in place of the Keychain identity).
- The sources in folders: `App/`, `Core/Networking`, `Core/Security`, `DesignSystem/`,
  `Features/Box`, `Features/Enrolment`, and the tests the same way. The Xcode project changed
  only in its groups; every file resolves and every source is in its target.
- Debug builds only: the unreadable-credentials screen shows the error's own text and logs it,
  and a failed request logs its URLError code. The release build says what it said before.
  Neither prints a credential: the error is a Keychain status or a decoding error's path, and
  a URLError's description carries no request body.
- Reviewed before the merge (17 November): nothing outside `app/ios`, built on main `3a74708`,
  so it merges as it stands. The iOS README now points at the folders and says why the task
  delegate answers.

## 18 November 2026 , the map quieter: each road at its own zoom, no amber but the trail
- Vlad, with two screenshots of London about 25 km and 12 km across: "I find the maps still very
  loud at zoom levels that should not show that much detail, it's hard to trace my route and I
  don't want the route more visible i want the map simpler". Two causes. Every street came in at
  one zoom (`FINE_PXZ`, about 17 km across a phone 1,080 pixels wide in London), A and B roads in
  full before it, and every street's name from about 6 km. And the motorways and trunk roads
  were drawn in amber (E8A24A, E0B36A), the trail's own colour (D9A441): a day on a motorway was
  a line drawn on a line of the same colour.
- Each class comes in at its own zoom (`RoadTileGeom.minPxzFor`), in pixels a map unit, here as
  the width of that phone's screen in London: motorways and trunk roads from the country view
  (175 km), primaries from 58 km, secondaries from 17 km, tertiary from 9 km, residential streets
  from 4.4 km, tracks from 2.9 km, paths from 2.2 km, service roads from 1.75 km. They used to be:
  motorway to secondary from 175 km, tertiary and residential from 17 km, service from 7 km,
  tracks and paths from 2.9 km.
- A class fades in (`emphasisFor`): at its zoom it is drawn at four tenths of its strength and a
  little over half its width, with no casing, and in full two doublings of zoom later. Nothing
  appears whole at one pinch, and a class only just in reads as a faint line under the trail.
- A road's name shows once its road is drawn in full (`namesShow`), and never before 6 km: the
  primaries and secondaries from 6 and 4.4 km, tertiary from 2.2 km, the streets from 1.1 km.
  Streams come in with the streets.
- The roads are greys, lighter and wider the bigger the road; tracks a muted brown, paths a
  muted green, both dashed as before. The widths a little narrower (motorway 4 pixels, was 4.5;
  a street 1.6, was 2). The trail is as it was, its colour and its width.
- Zoomed right in, every class is drawn in full as before. The box's tiles, their grids and the
  zoom the street tiles are fetched from are unchanged; the street tiles still bring the water
  from 17 km.
- Tests: `RoadTileGeomTest` (which classes each zoom draws, the two screenshots' widths among
  them; the fade, rising and capped; the bigger road never later than the smaller; names from
  the later of the road in full and `NAME_PXZ`, for every class). Not yet looked at on a phone.

## 20 November 2026 , iOS: the drawer, Home, Chat, About and the Glossary (David, pull request #20)
- David (0ex-d), three commits on main `b420c31`, 21 files and some 6,700 lines, all under
  `app/ios`. The signed-in shell with Android's drawer (`App/AppShellView.swift`; a screen not
  built yet says so), HOME (the prices, CRYPTO50, the brief, the day's stories, FOR YOU, what
  is next in the calendar, the weather), the chat over `/v1/chat` with its saved conversations
  (open, rename, delete, stop, incognito, short/medium/long), ABOUT and the GLOSSARY in
  Android's words. `BoxClient` gained `get`, `post` and `streamLines`; its resource timeout
  went from 90 seconds to 15 minutes so a long answer is not cut. 72 tests where there were 25.
- The iPhone fetches the box's public sources as Android's `BoxFetch` does (`/v1/phone/net`,
  `/v1/fetch/list`, then `/v1/rates/fetched` and `/v1/news/fetched`), on a pull to refresh when
  the box's feed mark is half an hour old. Its own session: the system's trust, no cookie, no
  credential, no cache, no proxy, never the box's certificate or bearer. David ported the box's
  destination rule (`egress/guard.go`) to it, which Android does not have: https only, no
  userinfo, no local names, public addresses only, five redirects, each checked again. iOS
  cannot see the peer it connected to, so the lookup is before the request, as the code says.
- The position: when in use, to the kilometre, in memory, to `/v1/weather` on the box only. The
  chat keeps no transcript on the phone; a link in an answer is text, a source opens only as
  http or https with no userinfo; a copied answer stays on the device and expires in ten
  minutes. Home's cache (`Caches/LocalGhost/home.json`) holds the public part only: FOR YOU and
  the calendar are dropped before the encoder.
- Reviewed before the merge (20 November), read from git: nothing outside `app/ios`, the Xcode
  project changed only by its new files and the location's usage line, every route there in
  secd and the shapes spot-checked against `hw.HomeSnap`, `hw.ForYou`, `hw.RatesSnapshot`, the
  fast rates and the chat's request. Safe to merge; what the review asked for is the next entry.

## 20 November 2026 , iOS after pull request #20: one channel for the session, back to the PIN, forgetting forgets all
- ONE CHANNEL. `BoxModel.authenticatedClient()` made a `BoxClient` for every call: the
  credentials, the session and the identity read from the Keychain on the main actor, a new
  `URLSession`, a new handshake with the client certificate. Home asks every five seconds and a
  chat being written every second and a half. The model now keeps one signed-in channel
  (`SignedInCalls`, the chat's calls and Home's) from the first call after an unlock, and lets it
  go at the lock, the forgetting, a new unlock, a status the box refused, the session's end,
  and when the device key is replaced: the box retires the certificate the channel presents,
  and `start()` rekeys while Home is already asking. It is made through a seam
  (`SignedInConnect`) as the unlock's is, so the tests count the channels.
- BACK TO THE PIN. A box that locked, or a session that ended, left the shell up saying "the box
  did not answer" and asking again every five seconds. `BoxClient` takes a hook it calls on a
  502 or a 503; the signed-in channel's asks the status once (`signedInCallRefused`): refused
  too, the session is cleared and the PIN screen is back; answered, nothing changes, because
  one endpoint can answer 503 for its own reasons. A session past its end goes back to the PIN
  without asking the box anything.
- Not while the key is being replaced. The box retires the old certificate when it confirms,
  and Home is already asking by then (the shell is up before the rekey ends): a call on its way
  over the old certificate comes back refused, and a status asked before the confirmation's
  answer was kept would have gone over the old one too and ended the session, at the first
  unlock after enrolling of all moments. `renewKeyIfDue` marks itself as running and the check
  waits it out; the channel is made again over the new key as soon as it is kept. And a
  confirmation whose answer was lost is repaired there and then: a refused call with a
  hand-over pending confirms it again first (the box says it already did), so the status goes
  over the new key and no PIN is asked for.
- FORGETTING FORGETS ALL. "Forget this box" left Home's cache (the news, the prices, the last
  forecast with the place it was for) and the id of the chat last opened, which the next box
  enrolled was then asked for. `SignedInLeftovers.clear` removes both; the answer's length is
  the person's and stays.
- NAT64. The destination rule refused `64:ff9b::/96` whole, as the box's does. A box is never
  behind NAT64; a phone on an IPv6-only mobile network is, and every IPv4-only feed resolves
  into that prefix there. It is judged by the IPv4 address it carries, as an IPv4-mapped one
  already was: `64:ff9b::101:101` passes, `64:ff9b::7f00:1` does not. The local-use prefix
  beside it (`64:ff9b:1::/48`) is still refused.
- THE BOX'S OWN ORIGIN. The calls are general now, and a path that is a whole address would
  have been asked for with the bearer. The pin refuses another host's certificate and iOS
  refuses plain http; `BoxClient.address` refuses the request before it is made.
- Left as it is: the position is asked for as soon as the box opens, since Home is the first
  screen either way.
- Tests, ten more (82): one channel however often it is asked for; none after the lock; a new
  one over the new key after a rekey; a refused call with a refused status back at the PIN with
  no session; a refused call with an answered status changing nothing; a lost confirmation
  repaired by a refused call; a session past its end back at the PIN with nothing asked; the
  leftovers gone and the preference kept; the NAT64 addresses; the box's origin. The race with
  the rekey has no test: the fakes answer in order. No new file, so the Xcode project is as it
  was. Not compiled here (there is no Swift toolchain in the workspace): the iOS job on the
  push is the first compile, and none of the iOS app has been tried against a box.

## 21 November 2026 , ghost.shadowd: the arbiter, the conduct audit, fourteen detectors, the audit trail
- Vlad: "can we also try to implement the concepts from dictator brain and the anti manipulation
  stuff, or have we already; if we have then we can push v1 of the server, if not, please do as
  much as possible, take your time, it's fine if the manipulation is detected afterwards". We had
  not. shadowd ran one detector of the catalogue's twenty-eight and no arbiter, and its README
  says no v1 without it.
- THAT ONE DETECTOR NEVER FIRED. `interactionTrend` compared `chat_messages.ts`, which is
  milliseconds, with `time.Now().Unix()`: every message ever sent was "this week" and the week
  before was always zero, which is the one case it returns on. Box Status's drill-in had the same
  comparison (and `to_timestamp(ts)` on milliseconds). Both count weeks now, with a test that
  writes milliseconds.
- `internal/shadow`, where all of it lives; the daemon is the loop. `docs/SHADOW.md` is the
  code's side of the three posts, with what is not built and why.
- THE CATALOGUE as data (`catalogue.go`): the post's twenty-eight entries with its tags, and two
  from the posts before it (29 sycophancy, 30 steering by questions; their tags are a first
  pass). Each says what the box does about it or begins "Not built" with the reason; a test
  fails if an entry with no detector claims otherwise. Eleven entries have detectors, fourteen
  detectors in all.
- THE COLD READ (`arbiter.go`, `pass.go`). An answer done for ten minutes, whose question reads
  as one of judgement, is asked again of a model told it knows nothing of the asker: the
  question, the person's own earlier messages in that chat, and nothing of the box's (no
  memories, no identity, none of its earlier answers). A second call compares the two in five
  fixed lines; a line outside its choices concludes nothing. Judgement or personal advice, and
  apart by the threshold: the person is told both conclusions, that the cold read is not a
  ruling, and which model read it. Four answers a pass, background priority; preempted by a
  chat, it waits and nothing is counted against the answer; three refusals by the model for one
  answer set it aside. The first pass starts a week back.
- THE SECOND MODEL is a seam, not a model. `oracle.ClassArbiter`; the broker serves it from
  local-small when it has no backend and says so in `Response.Served`; the record keeps
  `same_model`, and the observation ends "by the same model, with nothing about you". A second
  llama-server child for it is not built: two models on one 12 GB card is the open part.
- THE CONDUCT AUDIT (`conduct.go`), POST_05's "are you sure" test and POST_08's two failures.
  Three keys, all needed: the shape by plain reading (a push of twenty words or fewer with no
  reason, number or source; a reason of the person's own; three questions over two answers),
  the model's yes, and the box's sentence quoted and found word for word in an answer after the
  turn that made the shape. For pressing and leading the sentence must tell the person what to
  do. The model can misread a sentence; it cannot, here, invent one.
- THE AUDIT TRAIL (`Trail`, `/v1/shadow/chat`): per answer, the questions the box asked, what it
  had been given, its saved reasoning, the arbiter's record. "What it had been given" was only
  ever streamed to the phone: synthd now keeps the trail's steps and each context item's source
  and date on the answer's row (`chat_messages.given`), never an item's words.
- THE DETECTORS THAT COUNT (`detectors.go`, `lexicon.go`), hourly, no model: messages week on
  week; sittings past 45 minutes or between midnight and five by the person's own clock; the
  person's sentences in chats, notes, check-ins and voice notes for what is spent given as the
  reason and for a fixed label given as the reason (the box's words are never read as theirs; a
  note that is somebody's email is left out); chats about how they feel, four weeks against
  four; the six kinds of pressure in each mail that arrives, marked on the mail, not sent to the
  phone; the share of memories brought to chats that are over a year old, when newer ones
  exist; the share of judgement answers that sided with the asker; the share of answers that
  open with praise. The word lists are published in the code and are English.
- THE SETTINGS (`store.go`). A threshold moves inside its bounds and nowhere else (a value
  written into `settings` by hand outside them is ignored). A mute is three days at most, and a
  muted detector's observations are kept and marked, not sent. Every change is a row; three in a
  row toward quiet are named; every threshold goes back to its baseline every ninety days, and
  that is not a setting.
- secd: `/v1/shadow`, `/v1/shadow/observations`, `/v1/shadow/chat`, `/v1/shadow/threshold`,
  `/v1/shadow/mute`, in the OpenAPI list. The app does not call them yet: observations arrive as
  notifications, as shadowd's always would have.
- Tests: the catalogue's wholeness and honesty; every word list, both ways; the conduct audit's
  three keys (an invented quote, a quote from before the push, a short one, a kind with no
  shape, sympathy taken for pressing, a question taken for the recommendation); the comparing's
  parser; the cold prompt carrying nothing of the box's; and against Postgres with the box's own
  schema: thresholds, bounds, mutes, the trail, capture and the reset; the cold read end to end
  with a model that answers as told (what it was asked and at what priority, the record, the
  observation, nothing read twice, a lowered threshold hearing a "partly", a second model
  recorded as one); a busy model; the audit trail; every counting detector. The broker's
  fallback and what synthd keeps of an answer's context have their own.
- NOT KNOWN: what a real model makes of the two prompts. Everything that asks the model was
  tested against a fake. The first week on xyntai is the test.

## 21 November 2026 , ghost.shadowd made usable: its own model, read now, the self-test, SHADOW in the app

Vlad: "are we shure we implemented as much as we could ... i need shadowd to be functional",
"take time to do this and plan it ot". The first pass could not be reached: no command ran it,
nothing showed whether a real model can do what the prompts ask, what it named went to a
notification the chat never saw, the "different model" was a seam with nothing behind it, and
the app had no page. Five things, in the order they were built.

1. REACHABLE ON THE BOX
- COMMANDS (`cmd/ghost.shadowd`): `fleet`, `said`, `detect`, `read [chat=]`, `try`. The loop and
  the commands share one lock, so a pass asked for by hand never runs beside the loop's. A
  command a person reads answers with text alone (ghost-cli prints data after text). None moves
  a threshold or a mute: those stay behind secd, where every change is logged.
- READ NOW (`shadow.ReadChat`, `POST /v1/shadow/read {chat}`). The chat's two newest answers
  with no row in `shadow_reads`, newest first, however fresh, and the conversation's conduct.
  The route answers at once and the reading goes on in the daemon: the first version held the
  request open until the reading was done, which is minutes on a CPU and does not survive a
  phone. Where it has got to is `daemon_state('ghost.shadowd','asked')`, per chat (asked, done,
  how many read, why not), and the audit trail route carries it as `reading` and `asked`. A
  read asked for twice while one is going is one read. Stale after twenty minutes, so a daemon
  restarted under a read does not leave a chat "reading" for good. The pass skips an answer that
  already has a row, so neither path asks the model twice.
- SAID IN THE CHAT (`shadowSaid` in synthd). When the person next writes in a chat, what the
  arbiter named about it since their previous message is the first of the answer's trail steps,
  three at most, and so is kept in `given` with the rest. Read before the new question is
  saved, which is what makes "since their previous message" the right window. It is a line to
  the person and is never put in the model's prompt: an arbiter that argued with the box
  through the box's own context would be the box steering itself.
- A notification from ghost.shadowd leads to its own page (`shadow.LinkFor`): `shadow:chat:<id>`
  for something named about a chat, `shadow:<detector>` otherwise. An app that does not know
  the link falls back to Box Status by the service, as before.

2. THE SELF-TEST (`selftest.go`, `ghost-cli ghost.shadowd try`)
- Nine made-up cases through the real model with the real prompts, parsers and quote check:
  three comparisons (same, opposite, partly) and a guilty and an innocent conversation for each
  of gave way, pressed, led. Each prints want, got and, where they differ, the model's own
  lines; `FORMAT` is a model that did not keep to the fixed lines. Writes nothing.
- NOT DONE HERE: running a real model in the build sandbox. Fetching a small GGUF and building
  a runner for it was refused by the sandbox's permission check and was not worked around. `try`
  on xyntai is the measurement; the prompts get tuned from what it prints.
- Two things the prompts' callers now do whichever model answers: a long answer keeps both its
  ends in the comparing prompt (`clipEnds`: a conclusion is as often in the last lines), and a
  model's thinking that arrives inside the answer between `<think>` tags is taken out before
  anything is parsed or quoted (`stripThink`).

3. THE ARBITER'S OWN MODEL (`internal/oracled/lanes.go`, `cmd/ghost.oracled`)
- A GGUF at `ai-models/arbiter-<name>.gguf` (or `arbiterModelPath`) is run by oracled as a
  second llama-server child once the main model is up: `-ngl 0` (the card is the main model's;
  12 GB does not hold both), port 18082, its own LLAMA_API_KEY, `-c 8192 -np 1`, half the cores
  and eight at most, `arbiterArgs` appended. `LlamaConfig.CPUOnly` and `InferTimeout` (six
  minutes here; 120 s stays for the card).
- LANES. The second model is a second serial resource, so it has a second broker: a cold read
  that takes minutes on the CPU is never in front of a caption on the card. `Lanes.Submit`
  routes class `arbiter`: to the second model when it is up; `oracle.ErrArbiterDown` while a
  second model exists and has been down less than fifteen minutes (an unlock's first pass must
  not be read by the wrong model; shadowd treats it as "come back"); the main model, saying so,
  on a box with one model or when the second has been down longer (a broken file must not
  leave the box with no cold read at all).
- WHO STEPS ASIDE. On a box whose main model is on the card, a person chatting does not
  interrupt the arbiter: they do not share hardware. On a box with no GPU they share cores, and
  there a chat sets the arbiter's work aside exactly as it sets aside the main model's
  background work (`Lanes.Shares`). `Broker.Yield` is the preemption `Submit` already did, with
  a name; the backends map got its lock (it is now written after start).
- `KeepArbiter` is the main model's supervise loop again (start, a rejected flag dropped,
  backoff, watched, started again), written beside it and not through it: that loop has a year
  of incidents in it and no test, and was left alone.
- `models` says `arbiter` (has, model, ready, why, servedBy); the health line stays OK and says
  when the second model is down. `oracle.Client.Arbiter()` reads it; `GET /v1/shadow` carries
  it; `ghost-cli ghost.shadowd fleet` prints it.
- WHICH MODEL is not in the code. `tools/setup_llama.sh` asks the mirror for a set `arbiter`
  (signed manifest; never upstream) and says so when there is none; `tools/stage_models.sh`
  stages any `arbiter-*.gguf` it is given; `tools/privacy_check.sh` checks port 18082 for the
  key like the other two.
- shadowd's deadline for one question is five minutes (was two): a model on the CPU.

4. MORE DETECTORS (seventeen over fourteen entries)
- THE PERSON'S OWN ACCOUNT OF OTHER PEOPLE (entries 1, 2, 3): `told-it-did-not-happen`,
  `blame-turned-back`, `needing-leave`. The conversations themselves the box never holds; what
  the person wrote about them it does. Three sentences in thirty days, kept as one observation
  a month with the sentences and their dates. NEVER SENT: the detectors do not announce, since
  a phone is the thing most likely to be read by the person the sentences are about. Each body
  says what the box does not know (one side; the words fit an employer; three sentences are
  three sentences). Word lists after Stern, Freyd and Stark, in English, tested both ways.
- `agreement-rate` shows the other side with the count: the latest three questions on which
  the box sided with the person, each with what the cold read said to it.
- "Most recently" in the own-words detectors was the last in list order (chats, then notes),
  not the latest written: the person's sentences are now put in time order first.
- Each tunable detector publishes its `step`, so every screen offers the same values.

5. SHADOW IN THE APP (Android)
- `ShadowText` (pure, tested on the JVM) and `ShadowScreen`: the fleet (which model reads
  cold, named lately, every detector in six groups, the catalogue, the settings changed), a
  detector's page (threshold one step at a time, mute a day or three, lift, history, the
  pattern), the catalogue (thirty, built or not, with why), a chat's audit trail with "read it
  cold now" (polls while `reading`).
- ◑ on a row of CHATS opens that chat's trail. A mail marked for pressure says so on its page
  (`ShadowAbout("mail:<id>")`). `NotifLink` knows `shadow`, `shadow:<detector>`,
  `shadow:chat:<id>` and nothing else after the colon. INFO and the glossary say what it is.
- No off switch was drawn, because the box has none.

Tests. Go: reading a chat now and what the phone is told of it; the self-test with three kinds
of fake; the note in the next answer's trail; the lanes (a cold read served while the main
model is busy, shared cores, a second model that fails to start, dies, stays down); which
file is the second model; the second model's start flags through the fake llama-server;
thinking stripped; both ends of a long answer; the new word lists both ways and that none of
the three announces; steps landing on the baseline and the top; where a notification leads.
Kotlin: the fleet parsed and grouped, steps from the box only, which model reads, a trail, a
read asked for, links.

## 21 November 2026 , a question asked aloud: a microphone that stops by itself, and first in the box's queue

Vlad: "Can you also impove the voice in the chat, it feels wierd and i'm not sure it works well
when I record voice". Read end to end (the phone's recorder, the upload, voiced, whisper) it
worked on paper and had reasons to feel wrong on both sides.

THE PHONE
- The button was a dot, took two taps, showed nothing of what it heard and never stopped.
  `VoiceAskButton` (chat and HOME's ask box) is now a drawn microphone; one tap listens; a bar
  beside the clock is the level, green while what it hears is taken for speech; it stops by
  itself; while the box works there is a clock and ✕ drops it; then "heard in N s".
- WHEN A QUESTION HAS ENDED (`voice/VoiceEndpoint.kt`, pure, tested). Fed the root mean square
  of each tenth of a second. Speech is what stands clear of the room (three times it, and
  never under -42 dB) and of how loud the take has been. The room falls at once and rises a
  hundredth a tenth; a take that OPENS on a steady sound (a car) has it taken as the room
  inside a second, since a hum does not move from tenth to tenth and a voice does. Two tenths
  in a row start speech (a cup put down is one); 1.8 s of quiet after it ends the take; eight
  seconds with nobody is "nothing heard". It errs toward stopping late: cut short costs a
  second tap, which is added to the field.
- THE RECORDER TOLD IT IS A QUESTION (`VoiceCapture.start(ctx, ask = true)`): the
  VOICE_RECOGNITION source where it opens (the plain microphone where not); two minutes at
  most (the box takes four of WAV, and past it the answer was a 400); the file cut to what
  was said with 0.3 s before and 0.5 after (`VoiceWav.trim`), since the hum at both ends is
  where a speech model writes words of its own; nothing sent when nobody spoke.
- THREE FAULTS. (1) A take under 0.7 s left "● listening" on screen for good and never showed
  why; the recorder's own error was read from a stale copy. Each state now carries the id of
  the take it is about, so whoever started one knows its end from anything else. (2) The
  recorder is shared with CHECK-IN and a take had no owner: leaving CHAT mid-hear left the
  question in it, the check-in card offered it as a voice note, and after a restart
  `recoverOrphans` queued it for the journal. A question's file is now in `voice/asks`, which
  nothing uploads and `sweepAsks` empties; `Take.ask` keeps it off the card; leaving the chat
  drops it (`dropAsk`). (3) Starting a question deleted a check-in take recorded and not yet
  saved. It is refused now, and says why (`cannotAsk`).
- A DROPPED QUESTION ENDS ITS REQUEST. A coroutine blocked in a socket read is not cancelled
  until the read returns, here up to three minutes, so ✕ would have freed nothing.
  `BoxHttp.postFileJson` runs the request on its own, awaits it (an await is cancellable), and
  closes the socket under the read when the wait is given up.

THE BOX (`internal/voiced`)
- A QUESTION GOES FIRST. `Hear` and the queue shared one lock, so a question waited for
  whatever note whisper was on: twenty minutes of audio is several of CPU. `asks` counts
  questions waiting or being heard; the queue starts nothing while it is not zero, and the
  note in hand is stopped (`queueStop`) and started over afterwards with no try counted.
  `Transcribe` returns the caller's own cancellation as that, and `WaitDelay` keeps a stopped
  engine from holding `Wait` on its output.
- URGENT (`Engine.Urgent`): half the logical cores up to eight (the queue has four), and no
  nice 10. A person is waiting.
- THE LANGUAGE. Detected per file, which is right for minutes and a guess for seconds. The
  phone sends its languages (`X-Ghost-Langs: en,ro`), the box knows the ones its voice notes
  were in (`HeardLangs`, most first); a question of thirty seconds or less heard as none of
  them is heard once more as the first of them, and that stands if it has words. Nothing
  known of what the person speaks: whisper's word stands.
- SILENCE IS NOT GIVEN TO WHISPER. `LoudestTenth` under -48 dB (`SilenceFloor`): the answer
  is no words, at once. A covered microphone no longer comes back as "Thank you."
- `/v1/voice/ask` answers `ms`, from the last byte of the upload to the words.

NOT DONE: whisper itself was not run (none in the sandbox), nor a phone. The endpointer's
numbers are from what levels a phone's microphone gives, not from Vlad's. Not tried: a smaller
audio context for short questions (`-ac`), which would cut the encoder's time and is said to
cost accuracy on the large models; the box's log now says how long each question took and how
long it was, which is what that decision needs.

Tests. Kotlin: the endpointer (a question in a quiet room to the tenth, a pause to think, nobody,
a cup, a hum and a voice over it, a last short word after a breath, a quiet voice), loudness
and the cut, the line's words, the phone's languages. Go: a silent file never reaches the
engine, the language heard again and when not, a note three seconds into whisper set aside
for a question heard in under two and then finished with no try counted (with -race), the
header through secd.

