# LocalGhost Android — clean project drop

This folder is the complete `app/src/main/` tree, all files version-aligned. Drop it in
wholesale to end version skew.

## How to install

1. Back up your current `app\src\main\` (rename to `main_old`).
2. Copy this `app/src/main/` over `localghost\app\android\app\src\main\`.
3. Keep your existing `build.gradle.kts`, `local.properties`, `settings.gradle.kts`,
   and `gradle/` — those are unchanged.
4. Delete any leftover old files NOT in this tree (notably old `ui/HomeScreen.kt` —
   it's replaced by MainShell + SyncScreen).
5. Build → Clean Project, then Run.

## Dependencies
Zero beyond what's already in your build.gradle: Compose, androidx.core (NotificationCompat),
androidx.work (WorkManager), kotlinx-coroutines. No ExifInterface, no Firebase, no third-party.

## What changed this round
- **Ghost notification icon**: res/drawable/ic_ghost_notif.xml replaces the (i). Monochrome
  silhouette with cut-out eyes; the OS tints it per-daemon colour.
- **Status bar overlap fixed**: GhostScaffold now provides real system-bar insets; MainShell
  pushes content below the status bar and the nav above the gesture bar.
- **Cooler menu**: custom terminal nav — per-tab glyph + label, green underline on the
  selected tab, no Material pill.
- **Sync activity bar**: thin bar above the nav showing `▶ syncing 412/1847 · IMG_2024.jpg`,
  live during sync.
- **Background sync every 15 min, Wi-Fi only**: SyncWorker with NetworkType.UNMETERED — never
  runs on 4G/5G. Notifications still poll on any network (tiny payloads).
- **Auto-sync-on-open kept, Wi-Fi only**: gated by an isUnmetered() check; manual SYNC button
  still works on any network.

## Notes
- Launcher icon: adaptive vector ghost (mipmap-anydpi-v26). If your project already had PNG
  mipmaps they can stay; this just adds the v26 adaptive version.
- The on-screen PIN keypad means the code never touches Gboard.
- Everything box-side is stubbed in net/BoxClient.kt. That is the ONLY file that becomes real
  when ghost.secd / ghost.framed / ghost.synthd land.
- Samsung: for background SyncWorker/PollWorker to fire reliably, set the app's battery
  setting to Unrestricted.

## Update — navigation drawer (Claude-app style)
- **Top bar** with a hamburger (≡) on the left + the current screen title.
- **Left slide-in drawer** (ModalNavigationDrawer): `LOCALGHOST` wordmark, then the
  **connection status at the TOP** (● connected to xyntai), destinations as icon+label
  rows (Chat, Memories, Notifications, Sync), a divider, then Settings, About, Lock.
  Nothing anchored at the bottom.
- **No bottom navigation** — the drawer is the single nav system.
- **Settings** is a full screen reached from the drawer. Holds:
  - "allow sync on mobile data" toggle — OFF by default (Wi-Fi only). ON = sync over 4G/5G.
    Flipping it reschedules SyncWorker with the right network constraint.
  - "mute daemon notifications" toggle.
- The Sync screen no longer has the mute/test buttons (moved to Settings); it keeps the
  grant rows, the SYNC button, and the photo/video/per-video progress bars.

## New files this round
- settings/AppSettings.kt
- ui/SettingsScreen.kt
- ui/AboutScreen.kt
(MainShell.kt, SyncScreen.kt, SyncWorker.kt, MainActivity.kt changed.)

## Note on box connection
`boxConnected` is hardcoded true for now (stub). It becomes real when ghost.secd reports
session/mTLS state.

## Update — full vision pass (companion that knows your life + always-on harness)
- **Chat (home)**: grounded empty state ("I have your synced life in context" + example
  prompts), placeholder "ask about your life…", streaming shows "◇ drawing on your
  memories…", replies show "◇ grounded in: …".
- **Memories**: now a life-model timeline — a header counting memories/photos/videos/voice
  notes ("updated just now — the fleet keeps this current"), then cards per extracted moment
  with daemon attribution and recency.
- **Harness** (new drawer destination ◉): the always-on fleet shown alive — each daemon with
  a state dot (WORKING/LISTENING/IDLE/ERROR), what it does, detail, and last-run.
- **Notifications**: reframed as "what your agents surfaced", per-daemon cards.
- **Sync**: reframed as AMBIENT INGEST — leads with "your box pulls in new media on its own…",
  access/grants secondary, button renamed SYNC NOW.

New files: ui/MemoriesScreen.kt, ui/HarnessScreen.kt, ui/NotificationsScreen.kt
(replaces ui/TabScreens.kt, now removed). BoxClient gained LifeContext / MemoryEntry /
DaemonStatus stubs — all become real with the box.

## Update — copy rewrite (plain technical, not marketing)
Reworded every surface to be concrete and reassuring through honesty, naming the box generically ("the box", "LocalGhost")
and where data lives, rather than vendor-speak:
- Chat empty state: "Local model on xyntai. Runs on your box… The prompt and the index never
  leave xyntai." Placeholder "query your index…", thinking "◇ retrieving from index…",
  replies "◇ retrieved: …".
- Memories header: "EXTRACTED ON XYNTAI", "indexed on your box · stays on xyntai".
- Harness: "DAEMONS ON XYNTAI — Processes running on your box… this app polls them, it does
  not run them."
- Notifications: "QUEUED BY DAEMONS — the phone polls every 15 minutes. Nothing is pushed
  through a third party."
- Sync: "New photos and videos are copied to xyntai every 15 minutes over Wi-Fi… Originals
  stay on your phone; copies live on your box. Nothing is uploaded anywhere else."
- About: thin-client description, no model/index on the phone.

## Update — no machine-name branding
All copy now refers to "the box" / "your box" / "LocalGhost" generically. No specific
hostname appears in the UI. The drawer shows "connected to the box".

## Update — Settings data-control + brand alignment
Per https://www.localghost.ai/brand-guidelines:
- **Voice**: declarative, no apology/pleading. "Daemons", never "agents". Destructive copy
  in the no-excuses register ("There is no recovery — that is the design.").
- **Logo rule**: removed glow from the LOCALGHOST wordmark (lock, drawer, about). Glow stays
  only on non-logo green text, which the guidelines permit.
- **Warning red (#FF8A8A)** reserved for destructive paths.

New Settings (YOUR DATA / DESTRUCTIVE sections):
- **EXPORT TO JSON** — pulls the index/memories from the box (stub), writes to cache, shares
  via FileProvider chooser. Shows "exported · N bytes".
- **CHANGE CODE** — re-keys the box; old key destroyed, data with it. Dialog requires current
  + new + confirm, button reads [ RE-KEY & WIPE ]. On success: local teardown → lock screen.
- **WIPE EVERYTHING** — crypto-erase on the box. Typed-confirm dialog (type WIPE). On success:
  teardown → lock.

New files: ui/SettingsScreen.kt (rewritten), ui/ConfirmDialog.kt, ui/ChangeCodeDialog.kt,
res/xml/file_paths.xml. AndroidManifest gains a FileProvider. BoxClient gains exportJson /
wipeEverything / changePin stubs (real with ghost.secd).

Note: changing the code or wiping both return to the lock screen and clear local state. The
actual crypto-erase happens on the box when ghost.secd implements these.

## Update — Glossary in Settings/drawer
New drawer destination GLOSSARY (≣). Explains how everything works and defines every term,
with a PLAIN / TECHNICAL toggle (two readings of each entry). Sections: HOW IT WORKS (the
phone/box split and why), SYNC (sync, ingest, index), DAEMONS (framed/voiced/cued/shadowd/
synthd/watchd/secd), SECURITY (mTLS, code/PIN, persona/decoy, crypto-erase, change-code=wipe).
Content is in ui/GlossaryScreen.kt as a data list — extend the GLOSSARY val as the system grows.

## Update — CODES (PIN management), box-owned settings, multi-device
- **CODES** drawer destination (⚿): manages the codes for the CURRENTLY MOUNTED persona only.
  The box cannot show another persona's codes while this one is open — persona-scoped by
  crypto, not by UI. Each code = a PIN + a behaviour:
    - MOUNT REAL  → opens the persona it belongs to ("real" is relative to where you are)
    - MOUNT DECOY → opens a fallback persona
    - WIPE        → crypto-erase on entry while presenting a decoy
  ADD CODE dialog: enter code + label + pick behaviour. REMOVE per code. Counts and lists are
  persona-scoped; no view aggregates across personas.
- **DEVICES** section on the CODES screen: each enrolled device with its own per-device sync
  state (photos/videos/last sync). Per-(persona,device,stream) cursor; content dedup so the
  same item from two devices is one memory.
- **Settings are box-owned**: on unlock the phone reads BoxSettings from the box and updates
  its local caches (AppSettings/NotifyState are now caches, not sources). Toggling writes back
  to the box via setSettings. Every enrolled device converges on the same state.

New: ui/PinManagementScreen.kt. BoxClient gains PinEntry/PinBehaviour/DeviceInfo/BoxSettings +
personaPins/addPin/removePin/devices/settings/setSettings stubs. Glossary extended with
Per-device cursor, Dedup, Settings-live-on-the-box, Code behaviours, Codes-are-persona-scoped.

### secd implications captured for the box build
- Per-device enrollment (own cert/identity) — already in the enrollment design.
- Cursor keyed (persona, device, stream); advances per device independently.
- Content-addressed dedup at ingest (hash; link if present, else extract).
- Settings + code lists are persona-scoped encrypted state; only the mounted persona's are
  decryptable. "Real" is relative; no absolute real-flag stored.

## secd requirement — decoy persona construction (BOX-ONLY, never on the phone)
A decoy persona must be a fully inhabited, plausible life, not an empty shell. Construction
logic lives ENTIRELY in ghost.secd and must never appear in the client (the app is MIT /
public source — any decoy-building logic in the APK is reverse-engineerable and would reveal
the tells). The phone already mounts personas blind and renders them identically; it must
stay that way. It cannot know which persona is a decoy — that is what makes it impossible to
coerce the answer out of the phone.

Decoy properties (box-side):
- Own settings, own code list, own persona state — looks like a configured real account.
- Borrows non-sensitive photos from the main persona; some items legitimately land in
  multiple personas (shared, costs nothing, adds realism).
- The most recent few days are identical to the real persona (a stale decoy is a tell;
  real devices have recent activity).
- Financial picture deliberately deflated: assets ~1k, significant debt. The thing hidden
  is wealth — the decoy reads as convincingly broke.
- Side-persona history is back-filled from the main when spare compute allows.

Adversary assumption: suspicious, not credulous. Plausibility must survive inspection —
metadata/EXIF timestamps consistent with the decoy's narrative, financial story coherent,
no cross-persona leakage. "Real" is always relative to the mounted persona; no absolute flag.

Client invariants (already satisfied, must not regress):
- Phone holds no global persona map; CODES labels are per-persona data from the box.
- Identical rendering for every persona; no code path distinguishes real vs decoy.
- No decoy-construction logic, parameters, or tells anywhere in the app.

## Update — full feel pass
- **Back navigation**: BackHandler — open drawer closes on back; on any non-Chat destination,
  back returns to Chat (home); on Chat, default (exit). No more "back exits from anywhere".
- **Loading states**: box-fed screens (Memories, Harness, Notifications, Codes/devices) now use
  Loadable<T> (Loading/Loaded/Failed). They show "reading from the box…" with a spinner instead
  of flashing empty, and a distinct empty line only when genuinely empty. Real benefit on a slow
  box.
- **Chat stop button**: while streaming, SEND becomes STOP; cancels the chat coroutine.
- **Haptics**: a tick on each PIN keypad press; a confirm buzz on successful unlock. (Adds
  VIBRATE permission.)
- **Active destination**: drawer rows highlight the current screen (already wired via selected).

New files: ui/Loadable.kt, ui/StatusBlocks.kt. Most UI screens + MainShell + MainActivity touched.

## Later — offline local model (noted, not built)
When the box is unreachable, a small on-device model can answer generic questions with limited
context; the box's full-context RAG remains primary. UX should then show a "no box — limited
context" mode in chat. Captured for a future pass.

## Verification pass — two real bugs caught & fixed
A structural re-check of the scripted edits found two compile-breakers, now fixed:
1. MainShell call was missing the `onStopChat` argument (added in the feel pass but not passed
   from MainActivity) → would fail "no value passed for parameter onStopChat". Fixed.
2. removePin's re-read assigned a plain List to the now-Loadable `pins` state → type error.
   Wrapped in Loadable.Loaded. Fixed.
Verified: brace/paren balance across all files, MainShell↔MainActivity arg/param match, every
screen call matches its signature, when(dest) exhaustive (9/9), all BoxClient functions and
net types exist and are imported, all Loadable state assignments wrapped.

## Update — permission re-prompting + chat attachments (with dedup)
**Permissions (the "keep reminding / what if I say no" flow):**
- PermState { GRANTED, DENIED, BLOCKED }. BLOCKED = permanently denied; the in-app prompt is
  dead, so we deep-link to system settings (ACTION_APPLICATION_DETAILS_SETTINGS) instead.
- Standing PermissionBanner across the app (warning-red): DENIED -> "GRANT" re-prompts;
  BLOCKED -> "OPEN SETTINGS". Disappears when granted (full or partial Photo Picker selection).
- everAskedMedia flag distinguishes never-asked (prompt works) from blocked (settings only).
- onResume bumps permTick so PermState re-evaluates when returning from the settings page.
- If you deny media access: the banner stands, nothing syncs, tapping it re-prompts (or opens
  settings if blocked). The Sync screen GRANT path also still works.

**Chat attachments (image / voice):**
- Attach buttons in the chat input; pending attachments show as chips with [ x ] to remove.
- On attach: item is (a) queued to send with the next message as immediate context AND
  (b) ingested to the box index NOW via BoxClient.ingestAttachment, SAME raw-bytes path as sync.
- sendChat passes attachments to BoxClient.chat (context); message carries them for display.

**No double work:** chat-attach and camera-sync of the same file produce the same content hash
on the box, so ghost.secd links them into one memory, extracted once. Phone sends raw originals
on both paths so hashes match. Same content dedup already specced for multi-device.

New: ui/PermissionBanner.kt. Changed: chat/Message.kt (+Attachment), net/BoxClient.kt (chat
takes attachments; +ingestAttachment), ui/ChatScreen.kt, ui/MainShell.kt, MainActivity.kt
(pickers, ingest, PermState), settings/AppSettings.kt (+everAskedMedia).

## Update — "Add to chat" sheet + box-side connectors
Modelled on the Claude app's add-to-chat sheet, reframed for local-first.

**AddToChatSheet (the + button in chat opens it):**
- Source grid: Camera (TakePicture → FileProvider cache), Photos (image picker), Files
  (GetContent */*), Voice (audio picker). All ingest raw bytes via the deduped path.
- FOR THIS TURN capabilities:
  - "reach beyond the box" — OFF by default, warning-coloured. The ONLY capability that
    leaves the box; copy states the crossing ("this turn may fetch from the open web" vs
    "off — answers stay on the box"). Passed to BoxClient.chat as ChatCapabilities.
  - Daemons — ghost.synthd always answers; framed/voiced/shadowd are opt-in tools per turn.
- Route to Connectors.

**Connectors (new drawer destination ⊹, also reached from the sheet):**
- External sources the BOX pulls into the index (Gmail, Drive, Calendar — stubs). Copy is
  explicit: "the box holds the credentials and does the syncing — this phone only starts the
  connection and never sees the tokens." CONNECT / DISCONNECT per source.
- secd implication: OAuth + token storage live on the box; the phone triggers enrollment and
  polls status only. Never holds third-party tokens.

Chat input simplified: the two ▣/◍ buttons became a single + that opens the sheet.

New files: ui/AddToChatSheet.kt, ui/ConnectorsScreen.kt. BoxClient gains ChatCapabilities,
Connector, connectors/connect/disconnect/availableChatDaemons; chat() takes caps. MainShell +
MainActivity wired (camera + file pickers, caps state, connector connect/disconnect).

Design stance: "the only cloud is you" holds because every boundary crossing is explicit and
off by default. Reach is a declared act; connectors are box-owned. Nothing leaves silently.

## Update — ephemeral cache made explicit (lose the phone, lose nothing)
Confirmed the persistence model and made it provable rather than incidental:
- **Durable on the phone (correct):** DeviceIdentity/AppLock keypair (StrongBox — the enrollment
  credential, not life data), everAskedMedia (local UX flag), crash logs (diagnostics).
- **Cache, not source:** AppSettings.allowMobileSync + NotifyState.muted persist only so the UI
  isn't blank before the box answers; they're overwritten by BoxSettings on unlock and written
  back to the box on change. Box is authoritative.
- **Ephemeral (in-memory only):** memories, daemons, pending, pins, devices, connectors,
  availableDaemons, chat — all mutableStateOf, start Loadable.Loading, fill from BoxClient on
  unlock, never touch disk.

New: tearDownCache() returns ALL box-fed state to Loadable.Loading and clears chat. Called on
lock (onStop + onLock button) and after wipe/re-key. So a backgrounded/locked phone holds no
life data in RAM; the daemons push a full sync on next unlock. (Also fixed latent type errors:
wipe/change-code previously assigned emptyList() to now-Loadable state.)

All server calls remain mocked through BoxClient exactly like image upload — stubs return
believable data; raw-bytes paths (ingest/ingestAttachment) read the stream to completion. Only
BoxClient changes when ghost.secd lands.

## Update — on-phone model fallback (llama.cpp JNI, auto + manual)
When the box is unreachable, chat degrades to a small on-device model. Box RAG stays primary.

**Native (real, NOT verifiable here — needs NDK + llama.cpp + a GGUF; see GRADLE_ADDITIONS.md):**
- app/src/main/cpp/CMakeLists.txt — builds llama.cpp + liblocalghost_llm.so
- app/src/main/cpp/llama_jni.cpp — JNI: nativeLoad / nativeFree / nativeGenerate (streaming,
  cancellable via a Kotlin callback returning Boolean)

**Kotlin (swappable seam, like BoxClient):**
- local/NativeLlama.kt — raw JNI binding; ensureLibrary() is safe if the .so is absent
- local/LocalModel.kt — State { ABSENT, NOT_LOADED, LOADING, READY, FAILED }; model at
  filesDir/models/local.gguf (sideloaded/downloaded, never bundled); generate() streams via
  callbackFlow (trySend per token, cancellable)

**Routing + UX:**
- BoxClient.reachable() (stub; flip boxReachableStub to simulate box-down)
- sendChat: useLocal = forceLocalMode || !reachable() -> generateLocal else generateFromBox
- chat shows "◇ no box — on-phone model, limited context" when local-mode is active
- AddToChatSheet has a "use on-phone model" toggle (manual override; disabled if no model
  installed, with "no local model installed" sub)
- No box + no local model -> honest message, not a hang
- tearDownCache resets localModeActive on lock
- Glossary gains an "On-phone model (offline)" term (plain + technical)

Design: the on-phone model is the lifeboat, not the engine. No life-index access; generic
answers only; box reclaims primacy the moment it's reachable.

## Update — model download / load / storage (Edge-Gallery-style)
On-phone models are now downloadable and managed in-app, saved to the app's private storage.

- local/ModelCatalog.kt — list of downloadable GGUF models (Gemma 4 E2B, Qwen2.5 1.5B; fill
  `url` with a real GGUF; the Gallery's LiteRT/AICore files are a different runtime).
- local/ModelStore.kt — models at filesDir/models/<id>.gguf; installed list, active-id pref,
  delete. Never bundled, never leaves the device.
- local/ModelDownloader.kt — resumable HttpURLConnection download (HTTP Range), streams to a
  .part file, optional SHA-256 verify, renames on completion, emits DownloadProgress. No deps.
- LocalModel now loads ModelStore.activeFile (nullable) instead of a fixed path.
- ui/ModelsScreen.kt — MODELS drawer destination (▢): per-model DOWNLOAD with progress bar,
  CANCEL, USE THIS (activate), DELETE, ACTIVE marker. Brand-styled.
- Chat sheet's "use on-phone model" toggle shows "> get an on-phone model" → MODELS when none
  installed.
- MainActivity: downloadProgress state map, download/cancel/activate/delete handlers, refresh
  on unlock.

Windows build: confirmed — Android Studio + NDK + CMake builds the JNI + llama.cpp and bundles
liblocalghost_llm.so into the APK. The MODEL is downloaded at runtime (can't bundle 2.6 GB),
saved in filesDir/models. See GRADLE_ADDITIONS.md §6–7 for the Gemma 4 GGUF and download notes.

## Update — models served BY THE BOX (not the open internet)
The box is the model registry. The phone asks the box what it can run, downloads from the box
over the existing channel, and tracks local copies.
- BoxClient gains PhoneModel, availableModels(ctx), downloadModel(id, offset)->InputStream
  (resumable byte stream from the box). ModelCatalog (hardcoded HF URLs) REMOVED.
- ModelDownloader now streams from BoxClient.downloadModel instead of HttpURLConnection; still
  resumable (offset = existing .part length), still SHA-256-verifiable, no open-internet access.
- ModelsScreen lists the box's offered models; DOWNLOAD pulls from the box, DELETE removes the
  local copy only (box keeps it, re-pullable). Empty state when the box offers none.
- MainActivity: offeredModels loaded from BoxClient.availableModels on unlock; download handler
  resolves from that list.
- Copy + glossary updated: "models your box offers for this phone to run… delete removes the
  local copy only — the box keeps it."

secd implication: the box runs a model registry — stores many GGUFs, advertises the
phone-runnable subset, serves bytes with Range/resume. The phone never touches Hugging Face.

## Update — background model downloader (foreground service, survives lock/close)
The model pull is now a long-running WorkManager job, not an Activity coroutine. A 2.6 GB
download survives the app backgrounding or the screen locking (onStop → lock), shows a progress
notification, resumes on its own, and honours the Wi-Fi-only default.

- local/ModelDownloadWorker.kt — CoroutineWorker + setForeground (dataSync). Streams from the
  box via BoxClient.downloadModel(id, offset), resumable from the existing .part length, SHA-256
  verify, renames on completion, sets active if first. setProgress() publishes done/total;
  Result.retry() on transient failure (box unreachable / constraint lost) so WorkManager resumes.
  Wi-Fi-only by default (UNMETERED), or any network if mobile-sync is on.
- MainActivity: downloadModel() enqueues unique work (KEEP) + observeDownload() watches WorkInfo
  progress LiveData; reattachIfDownloading() re-attaches on unlock so a download started earlier
  still shows progress. cancelDownload() cancels the unique work. Coroutine ModelDownloader and
  the downloadJobs map REMOVED.
- Manifest: FOREGROUND_SERVICE + FOREGROUND_SERVICE_DATA_SYNC perms; SystemForegroundService
  declared with foregroundServiceType=dataSync (Android 14+).
- Channel "Model downloads" (IMPORTANCE_LOW) for the ongoing progress notification.

Everything still flows through secd via BoxClient — the model bytes ride the same authenticated
channel as chat/sync. The worker just makes the long transfer durable.

## Full review pass — findings + fixes
Went through the whole tree before the box build. Result: structurally clean, one real piece
of dead wiring removed.
- FIXED: SyncScreen had a dead `onUnmute` param (leftover from when mute lived on Sync; it
  moved to Settings). Removed from SyncScreen + the MainShell call. Mute still works via
  Settings (onToggleMute).
- VERIFIED clean: all 48 files brace/paren balanced; every file has a package; no bad
  RectangleShape imports; all 11 Dest values have a when-branch AND a drawer entry; all screens
  routed (Crash/Lock/Pin correctly shown by the Screen state machine, not Dest); no uncalled
  BoxClient stubs; MainShell↔MainActivity param parity complete; all SyncUiState fields read by
  SyncScreen exist; permissions used in code all declared (+ USE_BIOMETRIC/ACCESS_NETWORK_STATE
  for non-Manifest.permission paths); FileProvider authority consistent; all R.drawable/R.xml
  resources present; both notification channels created before use; auth flow Gate→Pin→Shell
  wired; boxConnected/boxReachableStub hardcoded true as expected until secd.
- No TODO/FIXME left (the two "placeholder" hits are a progress comment and a TextField
  placeholder, both legit).

## Update — PIN keypad redesign
- Bottom row is now symmetric: CLEAR (left) · 0 (centre) · DEL (right), all the same square
  cell as the digits, small 11sp labels. (Was: an oversized full-height DEL + a spacer.)
- Eye toggle (ic_eye / ic_eye_off vector drawables) next to the mask row reveals the typed
  code; tint goes terminal-green when revealed.
- OK button is bigger: full-width, 56dp tall.
- Fully responsive: keypad is a 3-column grid where key size = (rowWidth - 2*gap)/3 via
  BoxWithConstraints, so keys stay square and adapt to any phone; row width capped at 340dp so
  it doesn't sprawl on tablets/foldables. Fixed 72dp sizing removed.
New drawables: res/drawable/ic_eye.xml, ic_eye_off.xml.

## Fix — compile errors from a stray duplicate param block
A previous scripted edit had appended the 8 Settings/Codes params (onExport, exportState,
onChangeCode, onWipe, pins, devices, onAddPin, onRemovePin) to DrawerPanel's signature by
mistake. DrawerPanel never used them and its call didn't pass them → 8 "no value passed" errors
+ 8 "never used" warnings. Removed them from DrawerPanel (they belong only on MainShell, which
already has + uses them). Also dropped a redundant fully-qualified ModelRowState qualifier in
MainShell and a redundant FQ RectangleShape in GlossaryScreen. ("Typo: LOCALGHOST" is just the
IDE spellchecker flagging the brand wordmark — intentional, add to dictionary to silence.)
