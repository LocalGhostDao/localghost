# LocalGhost Android, clean project drop

This folder is the complete `app/src/main/` tree, all files version-aligned. Drop it in
wholesale to end version skew.

## How to install

1. Back up your current `app\src\main\` (rename to `main_old`).
2. Copy this `app/src/main/` over `localghost\app\android\app\src\main\`.
3. Keep your existing `build.gradle.kts`, `local.properties`, `settings.gradle.kts`,
   and `gradle/`, those are unchanged.
4. Delete any leftover old files NOT in this tree (notably old `ui/HomeScreen.kt` , 
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
- **Cooler menu**: custom terminal nav, per-tab glyph + label, green underline on the
  selected tab, no Material pill.
- **Sync activity bar**: thin bar above the nav showing `▶ syncing 412/1847 · IMG_2024.jpg`,
  live during sync.
- **Background sync every 15 min, Wi-Fi only**: SyncWorker with NetworkType.UNMETERED, never
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

## Update, navigation drawer (Claude-app style)
- **Top bar** with a hamburger (≡) on the left + the current screen title.
- **Left slide-in drawer** (ModalNavigationDrawer): `LOCALGHOST` wordmark, then the
  **connection status at the TOP** (● connected to xyntai), destinations as icon+label
  rows (Chat, Memories, Notifications, Sync), a divider, then Settings, About, Lock.
  Nothing anchored at the bottom.
- **No bottom navigation**, the drawer is the single nav system.
- **Settings** is a full screen reached from the drawer. Holds:
  - "allow sync on mobile data" toggle, OFF by default (Wi-Fi only). ON = sync over 4G/5G.
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

## Update, full vision pass (companion that knows your life + always-on harness)
- **Chat (home)**: grounded empty state ("I have your synced life in context" + example
  prompts), placeholder "ask about your life…", streaming shows "◇ drawing on your
  memories…", replies show "◇ grounded in: …".
- **Memories**: now a life-model timeline, a header counting memories/photos/videos/voice
  notes ("updated just now, the fleet keeps this current"), then cards per extracted moment
  with daemon attribution and recency.
- **Harness** (new drawer destination ◉): the always-on fleet shown alive, each daemon with
  a state dot (WORKING/LISTENING/IDLE/ERROR), what it does, detail, and last-run.
- **Notifications**: reframed as "what your agents surfaced", per-daemon cards.
- **Sync**: reframed as AMBIENT INGEST, leads with "your box pulls in new media on its own…",
  access/grants secondary, button renamed SYNC NOW.

New files: ui/MemoriesScreen.kt, ui/HarnessScreen.kt, ui/NotificationsScreen.kt
(replaces ui/TabScreens.kt, now removed). BoxClient gained LifeContext / MemoryEntry /
DaemonStatus stubs, all become real with the box.

## Update, copy rewrite (plain technical, not marketing)
Reworded every surface to be concrete and reassuring through honesty, naming the box generically ("the box", "LocalGhost")
and where data lives, rather than vendor-speak:
- Chat empty state: "Local model on xyntai. Runs on your box… The prompt and the index never
  leave xyntai." Placeholder "query your index…", thinking "◇ retrieving from index…",
  replies "◇ retrieved: …".
- Memories header: "EXTRACTED ON XYNTAI", "indexed on your box · stays on xyntai".
- Harness: "DAEMONS ON XYNTAI, Processes running on your box… this app polls them, it does
  not run them."
- Notifications: "QUEUED BY DAEMONS, the phone polls every 15 minutes. Nothing is pushed
  through a third party."
- Sync: "New photos and videos are copied to xyntai every 15 minutes over Wi-Fi… Originals
  stay on your phone; copies live on your box. Nothing is uploaded anywhere else."
- About: thin-client description, no model/index on the phone.

## Update, no machine-name branding
All copy now refers to "the box" / "your box" / "LocalGhost" generically. No specific
hostname appears in the UI. The drawer shows "connected to the box".

## Update, Settings data-control + brand alignment
Per https://www.localghost.ai/brand-guidelines:
- **Voice**: declarative, no apology/pleading. "Daemons", never "agents". Destructive copy
  in the no-excuses register ("There is no recovery, that is the design.").
- **Logo rule**: removed glow from the LOCALGHOST wordmark (lock, drawer, about). Glow stays
  only on non-logo green text, which the guidelines permit.
- **Warning red (#FF8A8A)** reserved for destructive paths.

New Settings (YOUR DATA / DESTRUCTIVE sections):
- **EXPORT TO JSON**, pulls the index/memories from the box (stub), writes to cache, shares
  via FileProvider chooser. Shows "exported · N bytes".
- **CHANGE CODE**, re-keys the box; old key destroyed, data with it. Dialog requires current
  + new + confirm, button reads [ RE-KEY & WIPE ]. On success: local teardown → lock screen.
- **WIPE EVERYTHING**, crypto-erase on the box. Typed-confirm dialog (type WIPE). On success:
  teardown → lock.

New files: ui/SettingsScreen.kt (rewritten), ui/ConfirmDialog.kt, ui/ChangeCodeDialog.kt,
res/xml/file_paths.xml. AndroidManifest gains a FileProvider. BoxClient gains exportJson /
wipeEverything / changePin stubs (real with ghost.secd).

Note: changing the code or wiping both return to the lock screen and clear local state. The
actual crypto-erase happens on the box when ghost.secd implements these.

## Update, Glossary in Settings/drawer
New drawer destination GLOSSARY (≣). Explains how everything works and defines every term,
with a PLAIN / TECHNICAL toggle (two readings of each entry). Sections: HOW IT WORKS (the
phone/box split and why), SYNC (sync, ingest, index), DAEMONS (framed/voiced/cued/shadowd/
synthd/watchd/secd), SECURITY (mTLS, code/PIN, persona/decoy, crypto-erase, change-code=wipe).
Content is in ui/GlossaryScreen.kt as a data list, extend the GLOSSARY val as the system grows.

## Update, CODES (PIN management), box-owned settings, multi-device
- **CODES** drawer destination (⚿): manages the codes for the CURRENTLY MOUNTED persona only.
  The box cannot show another persona's codes while this one is open, persona-scoped by
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
- Per-device enrollment (own cert/identity), already in the enrollment design.
- Cursor keyed (persona, device, stream); advances per device independently.
- Content-addressed dedup at ingest (hash; link if present, else extract).
- Settings + code lists are persona-scoped encrypted state; only the mounted persona's are
  decryptable. "Real" is relative; no absolute real-flag stored.

## secd requirement, decoy persona construction (BOX-ONLY, never on the phone)
A decoy persona must be a fully inhabited, plausible life, not an empty shell. Construction
logic lives ENTIRELY in ghost.secd and must never appear in the client (the app is MIT /
public source, any decoy-building logic in the APK is reverse-engineerable and would reveal
the tells). The phone already mounts personas blind and renders them identically; it must
stay that way. It cannot know which persona is a decoy, that is what makes it impossible to
coerce the answer out of the phone.

Decoy properties (box-side):
- Own settings, own code list, own persona state, looks like a configured real account.
- Borrows non-sensitive photos from the main persona; some items legitimately land in
  multiple personas (shared, costs nothing, adds realism).
- The most recent few days are identical to the real persona (a stale decoy is a tell;
  real devices have recent activity).
- Financial picture deliberately deflated: assets ~1k, significant debt. The thing hidden
  is wealth, the decoy reads as convincingly broke.
- Side-persona history is back-filled from the main when spare compute allows.

Adversary assumption: suspicious, not credulous. Plausibility must survive inspection , 
metadata/EXIF timestamps consistent with the decoy's narrative, financial story coherent,
no cross-persona leakage. "Real" is always relative to the mounted persona; no absolute flag.

Client invariants (already satisfied, must not regress):
- Phone holds no global persona map; CODES labels are per-persona data from the box.
- Identical rendering for every persona; no code path distinguishes real vs decoy.
- No decoy-construction logic, parameters, or tells anywhere in the app.

## Update, full feel pass
- **Back navigation**: BackHandler, open drawer closes on back; on any non-Chat destination,
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

## Later, offline local model (noted, not built)
When the box is unreachable, a small on-device model can answer generic questions with limited
context; the box's full-context RAG remains primary. UX should then show a "no box, limited
context" mode in chat. Captured for a future pass.

## Verification pass, two real bugs caught & fixed
A structural re-check of the scripted edits found two compile-breakers, now fixed:
1. MainShell call was missing the `onStopChat` argument (added in the feel pass but not passed
   from MainActivity) → would fail "no value passed for parameter onStopChat". Fixed.
2. removePin's re-read assigned a plain List to the now-Loadable `pins` state → type error.
   Wrapped in Loadable.Loaded. Fixed.
Verified: brace/paren balance across all files, MainShell↔MainActivity arg/param match, every
screen call matches its signature, when(dest) exhaustive (9/9), all BoxClient functions and
net types exist and are imported, all Loadable state assignments wrapped.

## Update, permission re-prompting + chat attachments (with dedup)
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

## Update, "Add to chat" sheet + box-side connectors
Modelled on the Claude app's add-to-chat sheet, reframed for local-first.

**AddToChatSheet (the + button in chat opens it):**
- Source grid: Camera (TakePicture → FileProvider cache), Photos (image picker), Files
  (GetContent */*), Voice (audio picker). All ingest raw bytes via the deduped path.
- FOR THIS TURN capabilities:
  - "reach beyond the box", OFF by default, warning-coloured. The ONLY capability that
    leaves the box; copy states the crossing ("this turn may fetch from the open web" vs
    "off, answers stay on the box"). Passed to BoxClient.chat as ChatCapabilities.
  - Daemons, ghost.synthd always answers; framed/voiced/shadowd are opt-in tools per turn.
- Route to Connectors.

**Connectors (new drawer destination ⊹, also reached from the sheet):**
- External sources the BOX pulls into the index (Gmail, Drive, Calendar, stubs). Copy is
  explicit: "the box holds the credentials and does the syncing, this phone only starts the
  connection and never sees the tokens." CONNECT / DISCONNECT per source.
- secd implication: OAuth + token storage live on the box; the phone triggers enrollment and
  polls status only. Never holds third-party tokens.

Chat input simplified: the two ▣/◍ buttons became a single + that opens the sheet.

New files: ui/AddToChatSheet.kt, ui/ConnectorsScreen.kt. BoxClient gains ChatCapabilities,
Connector, connectors/connect/disconnect/availableChatDaemons; chat() takes caps. MainShell +
MainActivity wired (camera + file pickers, caps state, connector connect/disconnect).

Design stance: "the only cloud is you" holds because every boundary crossing is explicit and
off by default. Reach is a declared act; connectors are box-owned. Nothing leaves silently.

## Update, ephemeral cache made explicit (lose the phone, lose nothing)
Confirmed the persistence model and made it provable rather than incidental:
- **Durable on the phone (correct):** DeviceIdentity/AppLock keypair (StrongBox, the enrollment
  credential, not life data), everAskedMedia (local UX flag), crash logs (diagnostics).
- **Cache, not source:** AppSettings.allowMobileSync + NotifyState.muted persist only so the UI
  isn't blank before the box answers; they're overwritten by BoxSettings on unlock and written
  back to the box on change. Box is authoritative.
- **Ephemeral (in-memory only):** memories, daemons, pending, pins, devices, connectors,
  availableDaemons, chat, all mutableStateOf, start Loadable.Loading, fill from BoxClient on
  unlock, never touch disk.

New: tearDownCache() returns ALL box-fed state to Loadable.Loading and clears chat. Called on
lock (onStop + onLock button) and after wipe/re-key. So a backgrounded/locked phone holds no
life data in RAM; the daemons push a full sync on next unlock. (Also fixed latent type errors:
wipe/change-code previously assigned emptyList() to now-Loadable state.)

All server calls remain mocked through BoxClient exactly like image upload, stubs return
believable data; raw-bytes paths (ingest/ingestAttachment) read the stream to completion. Only
BoxClient changes when ghost.secd lands.

## Update, on-phone model fallback (llama.cpp JNI, auto + manual)
When the box is unreachable, chat degrades to a small on-device model. Box RAG stays primary.

**Native (real, NOT verifiable here, needs NDK + llama.cpp + a GGUF; see GRADLE_ADDITIONS.md):**
- app/src/main/cpp/CMakeLists.txt, builds llama.cpp + liblocalghost_llm.so
- app/src/main/cpp/llama_jni.cpp, JNI: nativeLoad / nativeFree / nativeGenerate (streaming,
  cancellable via a Kotlin callback returning Boolean)

**Kotlin (swappable seam, like BoxClient):**
- local/NativeLlama.kt, raw JNI binding; ensureLibrary() is safe if the .so is absent
- local/LocalModel.kt, State { ABSENT, NOT_LOADED, LOADING, READY, FAILED }; model at
  filesDir/models/local.gguf (sideloaded/downloaded, never bundled); generate() streams via
  callbackFlow (trySend per token, cancellable)

**Routing + UX:**
- BoxClient.reachable() (stub; flip boxReachableStub to simulate box-down)
- sendChat: useLocal = forceLocalMode || !reachable() -> generateLocal else generateFromBox
- chat shows "◇ no box, on-phone model, limited context" when local-mode is active
- AddToChatSheet has a "use on-phone model" toggle (manual override; disabled if no model
  installed, with "no local model installed" sub)
- No box + no local model -> honest message, not a hang
- tearDownCache resets localModeActive on lock
- Glossary gains an "On-phone model (offline)" term (plain + technical)

Design: the on-phone model is the lifeboat, not the engine. No life-index access; generic
answers only; box reclaims primacy the moment it's reachable.

## Update, model download / load / storage (Edge-Gallery-style)
On-phone models are now downloadable and managed in-app, saved to the app's private storage.

- local/ModelCatalog.kt, list of downloadable GGUF models (Gemma 4 E2B, Qwen2.5 1.5B; fill
  `url` with a real GGUF; the Gallery's LiteRT/AICore files are a different runtime).
- local/ModelStore.kt, models at filesDir/models/<id>.gguf; installed list, active-id pref,
  delete. Never bundled, never leaves the device.
- local/ModelDownloader.kt, resumable HttpURLConnection download (HTTP Range), streams to a
  .part file, optional SHA-256 verify, renames on completion, emits DownloadProgress. No deps.
- LocalModel now loads ModelStore.activeFile (nullable) instead of a fixed path.
- ui/ModelsScreen.kt, MODELS drawer destination (▢): per-model DOWNLOAD with progress bar,
  CANCEL, USE THIS (activate), DELETE, ACTIVE marker. Brand-styled.
- Chat sheet's "use on-phone model" toggle shows "> get an on-phone model" → MODELS when none
  installed.
- MainActivity: downloadProgress state map, download/cancel/activate/delete handlers, refresh
  on unlock.

Windows build: confirmed, Android Studio + NDK + CMake builds the JNI + llama.cpp and bundles
liblocalghost_llm.so into the APK. The MODEL is downloaded at runtime (can't bundle 2.6 GB),
saved in filesDir/models. See GRADLE_ADDITIONS.md §6–7 for the Gemma 4 GGUF and download notes.

## Update, models served BY THE BOX (not the open internet)
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
  local copy only, the box keeps it."

secd implication: the box runs a model registry, stores many GGUFs, advertises the
phone-runnable subset, serves bytes with Range/resume. The phone never touches Hugging Face.

## Update, background model downloader (foreground service, survives lock/close)
The model pull is now a long-running WorkManager job, not an Activity coroutine. A 2.6 GB
download survives the app backgrounding or the screen locking (onStop → lock), shows a progress
notification, resumes on its own, and honours the Wi-Fi-only default.

- local/ModelDownloadWorker.kt, CoroutineWorker + setForeground (dataSync). Streams from the
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

Everything still flows through secd via BoxClient, the model bytes ride the same authenticated
channel as chat/sync. The worker just makes the long transfer durable.

## Full review pass, findings + fixes
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

## Update, PIN keypad redesign
- Bottom row is now symmetric: CLEAR (left) · 0 (centre) · DEL (right), all the same square
  cell as the digits, small 11sp labels. (Was: an oversized full-height DEL + a spacer.)
- Eye toggle (ic_eye / ic_eye_off vector drawables) next to the mask row reveals the typed
  code; tint goes terminal-green when revealed.
- OK button is bigger: full-width, 56dp tall.
- Fully responsive: keypad is a 3-column grid where key size = (rowWidth - 2*gap)/3 via
  BoxWithConstraints, so keys stay square and adapt to any phone; row width capped at 340dp so
  it doesn't sprawl on tablets/foldables. Fixed 72dp sizing removed.
New drawables: res/drawable/ic_eye.xml, ic_eye_off.xml.

## Fix, compile errors from a stray duplicate param block
A previous scripted edit had appended the 8 Settings/Codes params (onExport, exportState,
onChangeCode, onWipe, pins, devices, onAddPin, onRemovePin) to DrawerPanel's signature by
mistake. DrawerPanel never used them and its call didn't pass them → 8 "no value passed" errors
+ 8 "never used" warnings. Removed them from DrawerPanel (they belong only on MainShell, which
already has + uses them). Also dropped a redundant fully-qualified ModelRowState qualifier in
MainShell and a redundant FQ RectangleShape in GlossaryScreen. ("Typo: LOCALGHOST" is just the
IDE spellchecker flagging the brand wordmark, intentional, add to dictionary to silence.)

## Update, real-device fixes (from S26 screenshots)
It compiles and runs. Fixed the on-device issues:
1. RE-LOGIN ON PICKER (critical): pressing + / picking an image backgrounded the app, onStop
   fired tearDownCache()+lock, so returning landed on the lock screen. Added an expectingResult
   guard set before any in-app picker/camera launch (launchForResult helper) and cleared in
   onResume; onStop skips the lock when expectingResult is true.
2. SYSTEM INSETS: content drew under the status/nav bars. The content Box now applies
   pad.calculateBottomPadding() so list screens and the chat input clear the nav bar; top inset
   already on the Column. Chat input no longer clipped.
3. LOCALGHOST wordmark wrapped to two lines on the lock screen (displayLarge too wide). Now an
   auto-fitting single line: BoxWithConstraints computes font size (28–56sp) from width,
   maxLines=1, softWrap=false.
4. PIN keypad too big: key size capped at 84dp, keypad width cap 300dp (was 340), OK 52dp.
5. CHAT COMPOSER redesign: one rounded 24dp pill (BasicTextField, no Material box) with a
   circular + on the left and a circular send/stop on the right, filled terminal-green when
   actionable (↑), stop (■) while streaming, grey when empty. Empty state top-aligned instead of
   floating mid-screen.

## Update, model pill in the composer (the model switcher)
A pill above the chat input shows the active brain and switches it on tap (replacing the buried
"use on-phone model" toggle as the primary control).
- States: "▪ the box" (green) = box engine, synthd, full life-index; "◈ phone · <name>" (amber)
  = on-phone model, generic answers. Label auto-reflects box-unreachable fallback.
- Tap → dropdown: "the box · synthd (full context)" + each installed on-phone model
  ("phone · <name>"); if none installed, "> get an on-phone model" routes to MODELS.
- Picking the box clears force-local; picking a phone model activates it + sets force-local.
- Wiring: ChatScreen gains brainLabel/brainIsBox/phoneModels/onPickBox/onPickPhoneModel + a
  local nav to MODELS. MainActivity derives brainIsBox = !forceLocalMode && boxReachable,
  brainLabel from the active model name; boxReachable refreshed on unlock.

## Update, permanent escape-hatch pins + layout polish
PIN logic:
- WIPE and MOUNT_DECOY codes are now permanent: the Codes screen hides REMOVE for them and
  shows "permanent, change only". Only MOUNT_REAL codes are removable. A persona can never be
  stripped of its panic options under coercion.
- Copy + glossary updated: "Decoy and wipe codes can be changed but never deleted. A wipe code
  entered from a decoy erases only that decoy."
- secd MUST ENFORCE (box-side, not just UI): reject removePin for any WIPE/MOUNT_DECOY code;
  a WIPE code is scoped to the mounted persona, entered from a decoy it crypto-erases only that
  decoy's volume, never the real persona (no cross-persona key access).

Layout:
- enableEdgeToEdge now uses explicit transparent SystemBarStyle.dark for both bars, so the
  Android 3-button/gesture nav bar no longer renders a washed-out scrim over the black app;
  light icons over the dark UI.
- More breathing room: TopBar vertical padding 14→18dp, horizontal 16→20dp, +4dp above the
  bar beyond the status-bar inset; chat empty-state top 32→40dp.

## Correction, WIPE is GLOBAL (supersedes earlier per-decoy note)
WIPE is a single global panic action: entered from ANY persona (real or decoy) it erases
EVERYTHING, every persona at once. No per-decoy wipe. Rationale: under duress there's no time
to open and wipe each persona individually; one burn code burns it all.
- secd implementation: WIPE destroys the master key-encrypting key (the root KEK that every
  persona key is wrapped under), so all volumes become noise simultaneously WITHOUT the session
  needing to hold the individual persona keys. This preserves read-time cross-persona isolation
  (a mounted session still can't read another persona) while allowing one-shot total destruction.
- Still permanent: WIPE/DECOY codes can be changed, never deleted.
- UI/copy updated everywhere: Codes labels + add-pin dialog ("WIPE, erases EVERYTHING, all
  personas"), Codes header ("the wipe code is global, erases every persona at once"), Settings
  WIPE EVERYTHING + its confirm dialog ("global … every persona becomes noise at once"),
  glossary Code-behaviours term (both registers).

## Update, chat polish (multi-select, pill-in-composer, readable responses, full-access banner)
1. MULTI-SELECT: image/file/voice pickers now use GetMultipleContents, pick several at once;
   each is attached + ingested (deduped) as before.
2. PILL INSIDE COMPOSER: the model pill moved from a separate row above the input into the
   rounded composer container, as a top row above the + / field / send row.
3. RESPONSE READABILITY: model response body is now soft grey (GhostText #E0E0E0) instead of
   bright terminal-green, easy on the eyes on the dark ground. Injected memories collapse
   behind a green "+ N memories" toggle; expanding shows the list in white. (Was an always-on
   green "◇ retrieved: …" line.)
4. HOME BANNER: PermissionBanner reworded to grant FULL read access, "Allow full read access
   so the daemons can index your photos, videos and files", action "ALLOW ALL"; blocked variant
   points to settings. Shows across the app including the chat home.

## Fix, settings cutoff + rotation relock
1. SETTINGS (and ABOUT) cut off at the bottom under the nav bar: both were plain non-scrolling
   Columns. Added verticalScroll(rememberScrollState()) + bottom padding so content scrolls and
   clears the nav bar. (Other screens already use LazyColumn/scroll.)
2. ROTATION RELOCK: a portrait<->landscape change recreated the Activity → onStop fired →
   tearDownCache()+lock, forcing re-auth. Declared android:configChanges=
   "orientation|screenSize|screenLayout|keyboardHidden|smallestScreenSize|density" on
   MainActivity so the Activity handles rotation itself (Compose recomposes for the new size);
   no recreation, no relock. Genuine backgrounding/低-memory kills still relock, which is correct.

## Update, chat history + Harness→BOX STATUS rename
RENAME: "Harness" is now "BOX STATUS" in the UI (drawer label + screen header). Enum value
Dest.HARNESS kept internal to avoid a wide rename.

CHAT HISTORY (conversations live on the box / synthd; phone lists + loads, holds active in memory):
- BoxClient: new Conversation(id,title,updatedLabel,messageCount) type +
  conversations()/loadConversation(id)/createConversation()/deleteConversation(id); chat() now
  takes a convId so the box threads the message into the right conversation.
- MainActivity: conversations + activeConvId state, loaded on unlock; selectConversation (loads
  messages), newConversation (clears + creates), deleteConversation; tearDownCache clears them;
  chat() passes activeConvId.
- MainShell/Drawer: "RECENT CHATS" section in the drawer (title, updatedLabel·msgs, active in
  green, ✕ to delete, "＋ new"); drawer column made scrollable. TopBar shows a ＋ new-chat action
  on the Chat screen. Tapping a chat loads it and routes to Chat.

## Update, chats search/scroll + settings fixes
1. WI-FI TOGGLE HANG: setMobileSync ran SyncWorker.schedule() synchronously on the main thread,
   and allowMobileSync was read fresh each recompose (non-reactive) so the toggle stalled. Now
   allowMobileSyncState is mutableStateOf (instant UI update) and the WorkManager reschedule +
   box write run on Dispatchers.IO. No hang.
2. CONFUSING NOTIFICATION TOGGLE: "mute daemons / active" was ambiguous. Relabelled to
   "daemon notifications" with switch ON = active (intuitive), sublabel "active, daemons can
   notify you" / "muted, daemons stay silent". checked=!muted, onChange inverts. Default ON
   (isMuted defaults false). Sync sublabel also clarified ("off, Wi-Fi only (recommended)").
   Defaults confirmed correct already: Wi-Fi-only on (allowMobileSync default false),
   notifications on (isMuted default false).
3. FULL CHATS SCREEN: new Dest.CHATS + ChatsScreen.kt, a search box (live filter by title) over
   a scrolling LazyColumn of ALL conversations; active chat highlighted green; ✕ to delete; ＋ new.
   Drawer RECENT CHATS now caps at 5 with a "see all chats →" link to the CHATS screen.

## Fix, chat() arg order (compile error) + minor cleanups
- COMPILE ERROR: after adding convId to BoxClient.chat(history, prompt, convId, attachments,
  caps), the call still passed (…, atts, activeConvId, …) so List<Attachment> went into the
  convId:String? slot and vice-versa. Reordered to (messages, text, activeConvId, atts, chatCaps).
- permTick: mutableStateOf(0) -> mutableIntStateOf(0) (avoids Int autoboxing; IDE hint).
- BoxSettings: imported and the two inline fully-qualified uses shortened (redundant-qualifier).
- The "Typo" warnings (msgs, atts, mems, dedups, fileprovider) are IDE spellchecker noise on
  intentional identifiers, not errors; add to dictionary to silence.

## Update, pin scroll, landscape drawer, model picker
1. PIN SCREEN SCROLL: the keypad column was Arrangement.Center with no scroll, so on short/
   landscape screens 7/8/9/OK fell below the fold and were unreachable. Now verticalScroll
   (rememberScrollState), top-aligned with padding, fully reachable in any orientation.
2. LANDSCAPE AUTO-OPENED DRAWER: ModalNavigationDrawer could re-settle open on a rotation
   (config change retained via configChanges). Added LaunchedEffect(orientation) { drawerState
   .close() } to force it shut on rotation; drawer width capped at 360dp so it doesn't dominate
   landscape.
3. MODEL PILL: label was wrapping to two lines (long model names). Now single line with
   maxLines=1 + ellipsis, max 240dp. Dropdown redesigned with ON THE BOX / ON THIS PHONE
   section labels, leading glyphs, and sublabels.
4. FULL BOX MODEL LIST: the pill now shows the FULL set the box (ghost.secd/ghost.synthd)
   advertises, not just installed ones. phoneModels is now Triple(id, name, installed); each
   row shows "downloaded" or "tap to download" (the latter routes to MODELS to fetch it).
   availableModels stub expanded to a realistic list (Gemma 4 E2B, Qwen2.5 1.5B/3B, Phi-4 mini,
   Llama 3.2 3B).

## Update, portrait lock + build verifiability
PORTRAIT: app locked to portrait (android:screenOrientation="portrait" on MainActivity).
Sidesteps landscape layout issues entirely. configChanges kept (harmless now). The PIN-scroll
and drawer-close-on-rotation fixes remain but simply never trigger.

VERIFIABILITY (prove the APK was built by us from an unmodified GitHub commit):
- NEW ui/VerifyScreen.kt + Dest.VERIFY ("VERIFY BUILD" in the drawer). Shows: source commit
  (full + short), working-tree-clean flag (DIRTY shown in warning red), build time UTC, version,
  and the signing cert SHA-256 (read live via PackageManager). Button opens the source at that
  exact commit on GitHub. Includes a 4-step "how to verify" and links to VERIFY.md.
- NEW GRADLE_VERIFY_ADDITIONS.md: drop-in Gradle (Kotlin DSL) to inject BuildConfig fields from
  git at build time, GIT_COMMIT, GIT_COMMIT_SHORT, GIT_TREE_CLEAN, BUILD_TIME_UTC, GITHUB_REPO
  (+ requires buildFeatures.buildConfig = true). Also notes reproducible-build hygiene
  (dependenciesInfo off) and the build flow (commit -> assembleRelease -> sha256sum the APK).
- NEW VERIFY.md: human guide, 3 independent checks (what the app claims, that source matches
  what you read, that the binary reproduces from source), plus signing-fingerprint comparison.

IMPORTANT BUILD ORDER: VerifyScreen references BuildConfig.GIT_COMMIT etc., so the app will NOT
compile until the GRADLE_VERIFY_ADDITIONS.md fields are added to app/build.gradle.kts. That is
step one of the build. Set GITHUB_REPO to the real public repo path.

Build flow on the build machine:
  git add -A && git commit -m "build: ..."   # clean tree so GIT_TREE_CLEAN=true
  ./gradlew assembleRelease                   # stamps commit/build into BuildConfig
  sha256sum app/build/outputs/apk/release/app-release.apk   # publish in release notes

## Update, full verifiability runbook + source manifest signing
The VERIFY BUILD screen is now a complete, followable runbook (the verifier needs nothing else):
- Shows: built (UTC), version, source commit (full), working-tree-clean flag, SOURCE MANIFEST
  ROOT, and the live signing cert SHA-256. Every value is tap-to-copy.
- Three link buttons: source at the commit, the signed MANIFEST.sha256, and the release.
- "VERIFY ON YOUR PC": 5 numbered steps, each with a tap-to-copy command block , 
  (1) clone+checkout+clean-check, (2) gpg --verify the manifest + tools/verify_manifest.sh,
  (3) assembleRelease + sha256sum the APK, (4) adb pull the phone APK + sha256sum,
  (5) apksigner verify --print-certs. Horizontal-scroll so long commands aren't clipped.

NEW source-manifest signing layer (deepest provenance):
- tools/gen_manifest.sh, writes MANIFEST.sha256 (sha256 of every tracked source file, stable
  LC_ALL=C sort) + MANIFEST.root (sha256 of the manifest). You then GPG detached-sign it
  (MANIFEST.sha256.asc).
- tools/verify_manifest.sh, verifier side: checks the GPG signature, prints the root hash,
  re-hashes every file against the manifest.
- BuildConfig gains MANIFEST_ROOT (read from MANIFEST.root at build; GRADLE_VERIFY_ADDITIONS.md
  updated). The app displays it so the verifier can match it to verify_manifest.sh output.
- VERIFY.md rewritten: the 3-link provenance chain (signed source → reproducible binary →
  signed APK), full PC steps, and the releaser steps (gen_manifest → gpg sign → commit →
  assembleRelease → publish hashes).

Releaser publishes per release: commit SHA, APK SHA-256, signing cert SHA-256, manifest root.

## Update, verify wiring merged into real build.gradle.kts + website-convention signing
- app/build.gradle.kts: merged the provenance wiring into the user's actual Gradle (version
  catalog style). Adds git reads (commit/short/clean/buildTime) + MANIFEST.root read, injects
  BuildConfig GIT_COMMIT/GIT_COMMIT_SHORT/GIT_TREE_CLEAN/BUILD_TIME_UTC/MANIFEST_ROOT/GITHUB_REPO,
  and dependenciesInfo includeInApk/Bundle=false for reproducibility. Original NAS_BASE_URL /
  DEVICE_TOKEN buildConfigFields kept.
- PUBLIC build reproducibility: release leaves NAS_BASE_URL/DEVICE_TOKEN EMPTY in local.properties.
  The app reads box URL + device token from its OWN ENCRYPTED STORAGE (written at setup); empty
  at runtime = unconfigured = show setup, else use. So the public APK has no machine-specific
  data baked in and is byte-reproducible. (The encrypted BoxConfig store + setup-vs-use routing
  is part of the ghost.secd enrollment work, noted, not yet built; BoxClient still stubbed.)
- tools/sign_source.sh + tools/verify_source.sh: match the WEBSITE deploy-manifest convention , 
  header (# LocalGhost App Source Manifest / # Build / # Signed), sha256sum sorted by path,
  gpg --batch --yes --armor --local-user info@localghost.ai --detach-sign. Output under ghost/
  (source-manifest.txt + .asc) plus MANIFEST.root. Verify screen + VERIFY.md updated to these
  paths and the info@localghost.ai key.
- GRADLE_VERIFY_ADDITIONS.md slimmed to a pointer (wiring now lives in build.gradle.kts).

ghost.secd note: enrollment writes NAS_BASE_URL + DEVICE_TOKEN (+ other user settings) into the
app's encrypted storage. The app's unconfigured/configured state is the setup-vs-use signal.

## Update, Linux (Debian) release build path
Confirmed the app builds headlessly on Debian (no Android Studio). Dev stays on Windows/Android
Studio; release happens on the Debian box (same one as the website).
- Requirements (per current AGP/Gradle): JDK 17, Android cmdline-tools, platforms;android-36,
  build-tools;36.0.0, matching compileSdk 36 / buildToolsVersion 36.0.0.
- tools/debian_setup.sh, one-time: installs JDK 17 + cmdline-tools + the SDK packages, writes
  ~/.localghost_android_env (JAVA_HOME/ANDROID_HOME/PATH). (cmdline-tools zip URL may need
  refreshing from developer.android.com over time.)
- tools/release.sh, clean-tree check → sign_source.sh (+ commit the manifest so the release
  commit contains it) → write release local.properties (sdk.dir + EMPTY NAS_BASE_URL/DEVICE_TOKEN)
  → gradlew clean assembleRelease → zipalign + apksigner sign with $LG_KEYSTORE/$LG_KEY_ALIAS →
  prints commit, APK sha-256, manifest root, signing cert to publish.
- BUILD_LINUX.md, the dev-vs-release split, setup, build, publish-to-GitHub flow, and the
  toolchain-pinning caveat for reproducibility.
- Ordering verified: sign_source.sh writes MANIFEST.root → release.sh commits it → assembleRelease
  reads it at configure time into BuildConfig.MANIFEST_ROOT. Self-consistent (manifest excludes
  itself + MANIFEST.root).

Flow: dev local builds from Windows; when ready, build the signed release on Debian and attach
the APK + the four hashes to a GitHub release. Users verify via VERIFY BUILD / VERIFY.md.

## Update, GPG detached signature over the APK (one identity)
The APK now carries TWO signatures: the mandatory Android keystore signature (v2/v3, apksigner , 
what lets Android install it and proves the binary is ours) AND a detached GPG signature by
info@localghost.ai (the same key as the website + source manifest), tying the exact binary to one
identity. GPG cannot replace the keystore signature, Android only accepts apksigner/keystore , 
so this is additive.
- release.sh: after apksigner, runs `gpg --batch --yes --armor --local-user info@localghost.ai
  --detach-sign app-release.apk` → app-release.apk.asc. Publishes the .asc alongside the APK.
- VerifyScreen: step 6, `gpg --verify app-release.apk.asc app-release.apk`.
- VERIFY.md: provenance link 3 now covers keystore + GPG-over-APK; verify step 6 added; releaser
  steps gpg-sign the APK and attach the .asc to the release.

Signature summary: source manifest = GPG (info@localghost.ai); APK = keystore (apksigner,
Android-required) + GPG (info@localghost.ai, identity tie). Three checks, one identity.

## Fix, build.gradle.kts provenance block (config-cache compatible)
The git() helper used the legacy Project.exec {} + ByteArrayOutputStream, which is unresolved in
modern Gradle (configuration cache), and referenced java.time.* fully-qualified inline (also
unresolved in the Kotlin DSL script). Fixed:
- git() now uses providers.exec { commandLine("git", *args); isIgnoreExitValue = true }
  .standardOutput.asText.get().trim(), the configuration-cache-safe API.
- Added top-of-file imports: java.time.Instant, java.time.ZoneOffset,
  java.time.format.DateTimeFormatter; buildTimeUtc uses the bare class names.

## Update — fixes + writing-style pass
- ChatScreen: removed the duplicate @Composable above MessageBubble (the line-170 dupe you hit).
- WorkManager: bumped to 2.11.2. It's the one dependency using a raw string instead of the
  version catalog (libs.xxx) — that's why it looks different. WORKMANAGER_CATALOG.md shows how to
  move it into gradle/libs.versions.toml so it matches the others (optional, no behaviour change).
- PGP key: added the public key link (localghost.ai/.well-known/pgp-key.asc) to the VERIFY screen
  and VERIFY.md, plus a "gpg --import" step before verifying signatures.
- WRITING STYLE pass against the guidelines: removed ALL em dashes everywhere (52 in UI strings +
  comments, plus all in the .md docs) — replaced with commas, periods, or parentheticals per
  context. Removed display-string colons (Sync "$label:" and chat "attached:"). Rewrote the one
  "a daemon serves" line (copula/銀 borderline) to plain prose. Checked: no rhetorical flips
  ("not X, it's Y"), no banned verbs (serves as/features/boasts/etc.), no triads, British spelling
  already consistent. The box-serves-bytes "serves" stays (real verb of motion, allowed).

## Update — libs.toml review + auth tests
- libs.versions.toml: nothing to remove, all current. WorkManager is the only raw-string dep;
  LIBS_REVIEW.md gives the catalog lines to move it in. Noted the missing kotlin-android plugin
  (fine if the build resolves, leave unless it complains).
- AUTH TESTS: extracted the lock state machine into security/AuthGate.kt (pure, no device/biometric)
  encoding the real rules: locked on launch, locked + cache-torn-down on background, biometric
  only advances to PIN (never straight to shell), PIN opens the shell, in-app pickers
  (expectResult) suppress the lock, crash survives backgrounding, picker guard consumed on resume.
  MainActivity.onStop now DELEGATES its lock/tear-down decision to authGate (and launchForResult/
  onResume use it), so the tests guard the real code path, not a copy.
- src/test/java/.../security/AuthGateTest.kt: 12 JUnit tests (./gradlew test, JVM, no device)
  covering launch, background re-auth, both-factors-required, the picker exception, explicit lock,
  crash precedence, and half-authenticated (biometric-but-no-PIN) backgrounding.

## Update — AuthGate shrunk to what production actually drives (honest tests)
The previous AuthGate modelled a full GATE/PIN/SHELL/CRASH state machine, but production only ever
called expectResult/onStop/onResume — the biometric->pin->shell transitions still wrote `screen =`
directly and bypassed the gate. So half the tests guarded a parallel machine, not the real path.

Fixed by shrinking AuthGate to exactly the onStop lock decision it drives:
- AuthGate now has only: expectingResult (the picker guard), expectResult(), onStop(crashShowing),
  onResume(). No state enum, no biometric/pin/lock methods.
- onStop(crashShowing) takes the crash flag as a PARAMETER (MainActivity passes
  `screen is Screen.Crash`), so there's no second piece of state to keep in sync.
- Behaviour proven identical to the original onStop via truth table over (expectingResult, crash):
    E=T,C=T -> no lock (both old+new)
    E=T,C=F -> no lock
    E=F,C=T -> no lock (crash survives)
    E=F,C=F -> lock + tearDown
- AuthGateTest: 7 tests, each guards the real decision — normal background locks, picker
  suppresses the lock, picker guard is one-shot cleared on onResume, crash survives backgrounding,
  guard-wins precedence, fresh gate has no guard.
- Verified: no stray refs to removed methods, MainShell call<->def param parity intact, all
  main+test files brace/paren balanced.

## Update — model integrity test (and stop there)
Surveyed the codebase for critical, unit-testable logic. Most is Compose UI (breaks loudly when
run), BoxClient stubs (no real logic yet), or thin Android-API wrappers (Context/Cursor/keystore,
need a device). The one piece worth pinning beyond AuthGate: the SHA-256 model integrity check.
- Extracted local/ModelVerifier.kt (pure): sha256Hex(InputStream) + matches(InputStream, expected)
  with case-insensitive hex compare, trims expected, null/blank expected returns false.
- ModelDownloadWorker now calls ModelVerifier.matches. Behaviour preserved: the Worker keeps its
  `sha != null` guard, so a model with no published hash still skips verification and is accepted
  exactly as before; matches() is only reached with a non-null hash.
- ModelVerifierTest: 8 tests using real SHA-256 vectors (empty + "abc", both confirmed against
  sha256sum) covering correct/wrong/tampered/case-insensitive/whitespace/null-expected.

Deliberately NOT testing: ModelStore paths (trivial, breaks loudly, needs Context), CameraReader
(dedup is box-side), AppLock/DeviceIdentity (StrongBox, needs hardware), Compose UI. The
persona/WIPE/permanent-code invariants are critical but live in ghost.secd (the box), so those
tests belong in the Go codebase, not the app.

Test suite now: AuthGate (7, lock-on-background decision) + ModelVerifier (8, download integrity).
Both guard real code paths. ./gradlew test, JVM, no device.

## Fix — compileSdk 37 + WorkManager moved to catalog
- Build failed checkDebugAarMetadata: core-ktx 1.19.0, core 1.19.0, lifecycle-runtime-compose
  2.11.0 all require compiling against API 37+. Bumped compileSdk from release(36){minorApiLevel=1}
  to release(37). Left targetSdk = 36 and minSdk = 35 UNCHANGED on purpose, so we compile against
  the newer APIs without opting into Android 17 (API 37) runtime behaviour changes (mandatory
  adaptive layouts, the orientation-override escape hatch removal) until we test for them. The AAR
  error itself notes compileSdk can move independently of targetSdk/minSdk.
- API 37 installs as the SDK package "platforms;android-37.0" (not android-37). debian_setup.sh
  updated to install android-37.0; noted the .0 naming as the cause if a build ever fails "looking
  for android-37". build-tools 36.0.0 stays (fine to compile against a newer platform).
- WorkManager finally moved into the version catalog (this was documented before but not applied):
  build.gradle.kts now uses implementation(libs.androidx.work.runtime.ktx); gradle/libs.versions.toml
  gains workRuntime = "2.11.2" + the androidx-work-runtime-ktx library line. A full updated
  libs.versions.toml is in the repo at gradle/libs.versions.toml. These two changes are a pair,
  apply both or the build won't resolve the dependency.
