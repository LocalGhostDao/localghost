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
- **Box status.** `GET /v1/status`: the daemons and the datastores.
- **Forget this box.** Deletes the device key, its certificate and the session from this iPhone.
  The box still lists the iPhone until it is retired there.
- **Reinstalling forgets too.** Keychain items outlive the app on iOS; the first launch after an
  install deletes whatever an earlier install left there (`InstallMark`).

Not built yet: the rest of the Android app's screens, notifications, uploads.

## Where things are

```
LocalGhost/
├── App/                  the entry point and the root view (setup, the box, credentials unreadable)
├── Core/
│   ├── Networking/       the channel to the box: the pin, the client identity, the calls
│   └── Security/         the device keys, the rekey proof, the session, the install mark
├── DesignSystem/         the terminal look shared by every screen
└── Features/
    ├── Box/              the unlock, the rekey, Box Status
    └── Enrolment/        the QR link, the scanner, the setup screen
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
