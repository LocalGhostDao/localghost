# LocalGhost (iOS)

The native iOS client for LocalGhost. This first target only establishes the application shell;
pairing, the pinned mTLS channel and product screens will land as separate changes.

## Requirements

- Xcode 26 or later
- iOS 17 or later

## Build and test

Open `LocalGhost.xcodeproj` in Xcode, or use a simulator from the command line:

```sh
xcodebuild \
  -project LocalGhost.xcodeproj \
  -scheme LocalGhost \
  -destination 'platform=iOS Simulator,name=iPhone 17 Pro' \
  test \
  CODE_SIGNING_ALLOWED=NO
```

The app intentionally has no external dependencies.

