# Building LocalGhost: dev on Windows, release on Debian

Two paths that don't interfere. Same repo, same Gradle.

## Dev loop, Windows + Android Studio (Quail)
Unchanged. Open the project, Run to your device over USB. Debug builds use the debug keystore;
no signing pipeline, no manifest. `GIT_TREE_CLEAN` will usually show DIRTY in the VERIFY screen
on dev builds, correct, since you're iterating on uncommitted changes. These are not releases.

Your existing `local.properties` (with `sdk.dir` and any dev `NAS_BASE_URL`/`DEVICE_TOKEN`)
stays on the Windows machine. It is gitignored and never shipped.

## Release, Debian 13 (same box as the website)
Linux is the better release target: more deterministic builds, and your `info@localghost.ai`
GPG key + `gpg --batch` workflow already live there.

### One-time setup
```
tools/debian_setup.sh          # JDK 17 + Android cmdline-tools + platform-36 + build-tools 36.0.0
source ~/.localghost_android_env
```
Requirements (installed by the script): JDK 17 (the Android Gradle Plugin requires it),
`platforms;android-36`, `build-tools;36.0.0`, matching this project's compileSdk 37 / targetSdk 36 / buildToolsVersion 36.0.0.
No Android Studio, no emulator needed.

You also need your release keystore on the box (the key the APK is signed with, its fingerprint
is what users verify). Point the release script at it:
```
export LG_KEYSTORE=/path/to/localghost-release.jks
export LG_KEY_ALIAS=localghost
```

### Build + sign + publish
```
git pull                       # get the commit you want to release
tools/release.sh
```
This: checks the tree is clean → signs the source manifest (info@localghost.ai) and commits it →
writes a release `local.properties` with EMPTY box values + the SDK path → `assembleRelease` →
zipalign + apksigner sign → prints the four values to publish.

Then:
```
git push
# create a GitHub release/tag, attach app-release.apk, and paste into the notes:
#   commit, apk sha-256, manifest root, signing cert sha-256
```

### Where the APK goes
Attach `app/build/outputs/apk/release/app-release.apk` to the GitHub Release for that tag. That
is the public, verifiable binary. Users follow VERIFY.md / the in-app VERIFY BUILD screen to
confirm it matches the source at that commit and is signed by your key.

## Why the release APK is reproducible
The release `local.properties` sets `NAS_BASE_URL=` and `DEVICE_TOKEN=` (empty). The app reads
the real box URL + device token from its own encrypted storage, written during setup, so the
APK contains no machine-specific data and anyone rebuilding from the commit gets the same bytes.
(`optimization { enable = false }` keeps R8 out of the build, which also helps determinism.)

## Reproducibility caveat (pin the toolchain)
For byte-identical rebuilds, the toolchain must be pinned and committed: the Gradle wrapper
(`gradle/wrapper/gradle-wrapper.properties`), AGP + Kotlin versions (`gradle/libs.versions.toml`),
and `build-tools;36.0.0`. A verifier installs the same JDK 17 + build-tools 36.0.0 and uses your
committed `./gradlew`. Confirm the wrapper and version catalog are committed to the repo.
