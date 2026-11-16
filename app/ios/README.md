# LocalGhost (iOS)

The native iOS client for LocalGhost. The current target includes the application shell and the
offline enrolment flow: scan a v2/v3 box QR, review the address and fingerprint, then store the
device certificate and private key in the device-only Keychain.

The pinned mTLS channel and product screens will land as separate changes. The imported private key
is box-generated, so it cannot gain Secure Enclave non-exportability after enrolment; the Keychain
record is instead restricted to this device and to periods when the device is unlocked.

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
  test \
  CODE_SIGNING_ALLOWED=NO
```

The app intentionally has no external dependencies.

