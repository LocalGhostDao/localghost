# LocalGhost (iOS)

The native iOS client for LocalGhost. The Android app (`app/android`) is the reference: where the
two differ, Android's behaviour and the box's code (`server/internal/secd`) are the contract.

## What it does

- **Enrolment.** Scan the box's QR (v2/v3 links, the rotating LGQR2 frames included), check the
  address and the fingerprint, enrol. The QR's private key goes into the Keychain as a key, beside
  its certificate, so the two make an identity TLS can present; the enrolment record keeps only
  the key's tag. All of it is on this device only and readable only while the iPhone is unlocked.
- **The channel** (`Core/Networking/BoxChannel.swift`). Mutual TLS: the box is trusted when the
  SHA-256 of the certificate it presents equals the fingerprint from the QR, and nothing else is
  trusted; the phone presents its identity. The challenges are answered on the task delegate as
  well as the session one, the same answer either way: Foundation hands a request's challenge to
  the task delegate and does not fall back when that method is missing (David's finding, pull
  request #19). No redirect is followed, nothing is cached. The box answers every refusal
  (locked, no session, a retired certificate) with the same 503 page, and the app says one thing
  for all of them: the box did not answer.
- **The unlock** (`Features/Box/BoxModel.swift`, `BoxView.swift`). The PIN to `POST /v1/unlock`,
  then a poll a second (`GET /v1/unlock/poll?run=`) showing each step, and the session token from
  the poll that says done, kept in the Keychain until it expires (two days).
- **The key replaced** (`Core/Security/DeviceKeys.swift`). Anyone who photographed the QR holds a
  copy of its key, so after the first unlock the phone makes a key of its own in the Secure Enclave (a
  Keychain key in the simulator, which has none), signs the proof `secd/rekey.go` asks for, and
  the box signs a certificate for that key. The phone confirms over the new certificate, the box
  retires the QR's, and its key is deleted here. The same once a day after an unlock, so a phone
  in use never reaches the end of its two-week certificate. A confirmation whose answer was lost
  is confirmed again at the next start; if the box had already retired the old certificate, the
  unlock is tried over the new one.
- **The shell** (`App/AppShellView.swift`). Signed in, Android's drawer: the same destinations in
  the same groups. A screen that is not built yet says so.
- **One channel for the session** (`BoxModel.authenticatedClient`). Home and the chat share one
  pinned client from the first call after an unlock. It is let go at the lock, at forgetting,
  at a new unlock and when the device key is replaced. When the box answers a call as if it
  were down (502, 503) the status is asked once: refused too, the session is cleared and the PIN
  screen is back (not while the key is being replaced, when a refusal over the old certificate
  is expected). A session past its end goes back to the PIN without asking. A call can only go
  to the box's own origin (`BoxClient.address`).
- **Home** (`Features/Home`). `GET /v1/home`, `/v1/rates`, `/v1/news` each minute and
  `/v1/rates/fast` every five seconds while it is on screen: the prices, CRYPTO50, the brief,
  the day's stories, FOR YOU, what is next in the calendar. The weather is for where the iPhone
  is: the position is asked for when in use, to the kilometre, kept in memory, and sent to
  `/v1/weather` on the box and nowhere else. The last public answers are kept in Caches for the
  next launch (`HomeCache`); FOR YOU and the calendar are never written.
- **The phone fetches for the box** (`HomeFeedRefresher`). On a pull to refresh, when the box's
  feeds are half an hour old, the iPhone fetches the public sources the box lists
  (`/v1/fetch/list`) and hands the bytes over (`/v1/rates/fetched`, `/v1/news/fetched`), as the
  Android app does. Its own session: the system's trust, no cookie, no credential, no proxy,
  never the box's certificate or bearer. Only https, only public addresses, five redirects at
  most, each checked again (`HomePublicDestinationPolicy`, the box's own rule; an address in
  the NAT64 prefix is judged by the IPv4 address it carries).
- **Chat** (`Features/Chat`). `POST /v1/chat`, streamed; the saved conversations (open, rename,
  delete), stop, incognito, short/medium/long. Nothing of a conversation is kept on the phone.
  A link in an answer is text; a source opens only as http or https. A copied answer stays on
  this device and expires in ten minutes. No web search, attachments or voice yet.
- **Box status.** `GET /v1/status`: the daemons and the datastores.
- **Settings** (`Features/Settings`). This device's enrolment address, box fingerprint, session
  deadline and device-key state (certificate expiry on iOS 18+); weather location (coordinates
  rounded to two decimal places before retention or sending), public feed
  fetching, chat answer length and thinking, reduced motion. These phone preferences take effect
  in the existing feature paths and persist across launches. Local app lock drops this phone's
  session. Confirmed `POST /v1/lock` also shuts down the box: `locked: false`, even with HTTP 200,
  preserves the session and reports failure. Forgetting requires confirmation and awaits cache
  invalidation before returning to setup. No session token or private key is displayed.
- **Permissions** (`Features/Permissions`). Camera and foreground location states, including
  denied, restricted and approximate access, with per-permission actions and app-settings recovery.
  Merely opening this screen requests nothing. State is re-read when the app becomes active.
  Local Network links to system settings and reports "Managed by iOS":
  [Apple exposes no general query for that grant](https://developer.apple.com/documentation/technotes/tn3179-understanding-local-network-privacy).
  Photos, microphone, Health, contacts, calendar and notification access are not requested until
  their features exist. No background trail or sync is claimed.
- **About and the Glossary.** The Android app's words.
- **Forget this box.** Deletes the device key, its certificate and the session from this iPhone,
  and what the signed-in screens kept outside the Keychain: Home's cache and the chat last
  opened (`SignedInLeftovers`). The box still lists the iPhone until it is retired there. The
  order is what can be put back, first: the session, then the leftovers, and the keys and the
  enrolment last. A step that fails leaves a box still enrolled and locked, and the screen
  says so. An answer from the box that arrives afterwards changes nothing: a status read or a
  refusal from the old session is dropped, Home's retired cache writes nothing, and a key
  renewal that was on its way keeps none of what it is answered (`BoxModel.stillEnrolled`).
- **Reinstalling forgets too.** Keychain items outlive the app on iOS; the first launch after an
  install deletes whatever an earlier install left there (`InstallMark`).

Not built yet: the gallery, the map, memories, mail, health, check-in, phrases, sync, the box's
other screens, notifications, uploads, the chat's web search. None of it has been
tried against a box.

## Where things are

```
LocalGhost/
├── App/                  the entry point, the root view (setup, the box, credentials unreadable),
│                         the signed-in shell and its drawer
├── Core/
│   ├── Networking/       the channel to the box (the pin, the client identity, the calls), the
│   │                     chat's and Home's calls, the public feed fetch and its destination rule
│   └── Security/         the device keys, the rekey proof, the session, the install mark
├── DesignSystem/         the terminal look shared by every screen
└── Features/
    ├── About/            what the app is and what leaves the box
    ├── Box/              the unlock, the rekey, the signed-in channel, Box Status
    ├── Chat/             the chat and the saved conversations
    ├── Enrolment/        the QR link, the scanner, the setup screen
    ├── Glossary/         every word, plain and technical
    ├── Home/             Home, its cache, the position for the weather
    ├── Permissions/      OS access state, rationale and system-settings recovery
    └── Settings/         phone preferences, enrolment state and confirmed session controls
LocalGhostTests/          the same folders, a test file beside each feature
```

## Requirements

- Xcode 26 or later
- iOS 17 or later

## Build and test

Open `LocalGhost.xcodeproj` in Xcode, connect your iOS device or use a simulator from the command line:

```sh
xcodebuild \
  -project LocalGhost.xcodeproj \
  -scheme LocalGhost \
  -destination 'platform=iOS Simulator,name=iPhone 17 Pro' \
  test
```

Leave code signing on: the simulator build is signed to run locally (no team needed), and the
Keychain refuses an app with no signature at all, so the tests that store credentials fail with
`CODE_SIGNING_ALLOWED=NO`. CI runs the same command (`.github/workflows/ios.yml`) whenever
`app/ios` changes.

The app intentionally has no external dependencies.
