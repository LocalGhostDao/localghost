# Building the LocalGhost app (Windows and Linux)

Everything needed to build and install the APK, from a clean machine. The same Gradle project builds
on both. Windows is the day-to-day path (Android Studio). Linux is the release path, and works for
debug builds too (command line only).

## What the build needs

| What | Version | Why |
|---|---|---|
| JDK | 21 (17 or newer works) | runs Gradle 9.6 and the Android Gradle Plugin 9.2 |
| Android SDK Platform | API 37 and API 36 | compileSdk 37, targetSdk 36 (minSdk 35) |
| Android SDK Build-Tools | 36.0.0 | pinned in `app/build.gradle.kts` |
| Android SDK Platform-Tools | latest | `adb`, to install on the phone |
| NDK (Side by side) | 28.2.13676358 | pinned (`ndkVersion`); compiles the phone model's runtime |
| CMake | 3.22.1 | pinned; drives that native build |
| Network on the first build | | Gradle's dependencies (Google Maven, Maven Central), and the llama.cpp source tarball from the LocalGhost mirror |

The NDK and CMake are needed because the llama.cpp pin is set, so every build compiles the on-phone
model's runtime into the APK. The build fetches llama.cpp from `https://www.localghost.ai/mirror`,
the same source the box builds from. It finds the file through the mirror's manifest and refuses it
unless its SHA-256 matches the pin in `app/src/main/cpp/CMakeLists.txt`. Nothing comes from GitHub.

## Windows (Android Studio)

1. **Install Android Studio** (current stable) from developer.android.com/studio. It brings its own
   JDK 21 (the JBR) and the SDK Manager. Let the setup wizard install the default SDK.
2. **Install the SDK packages.** In Android Studio: Settings › Languages & Frameworks › Android SDK.
   - **SDK Platforms:** tick API 37 and API 36.
   - **SDK Tools:** tick "Show Package Details" and install:
     - Android SDK Build-Tools **36.0.0**;
     - NDK (Side by side) **28.2.13676358**;
     - CMake **3.22.1**;
     - Android SDK Platform-Tools.
3. **Get the code.** Unzip the drop (or clone) to a path without spaces, for example
   `C:\Users\<you>\localghost`. Open the `app\android` folder in Android Studio (File › Open).
   Android Studio writes `app\android\local.properties` with `sdk.dir` on first open.
4. **Optional: build without downloading llama.cpp.** Copy the tarball from the box
   (`/opt/localghost/llama.cpp.mirror-dl/llama.cpp-v0.5.0-7fe450e.tar.gz`) and add one line to
   `app\android\local.properties`, with forward slashes:
   ```
   llamaTarball=C:/Users/<you>/llama.cpp-v0.5.0-7fe450e.tar.gz
   ```
   Without it, the first build downloads the tarball from the mirror. Either way, the build checks
   the file against the pin.
5. **Connect the phone.** Turn on Developer options › USB debugging, plug it in, and accept the
   prompt on the phone.
6. **Build and install.** Press Run in Android Studio, or from a terminal in `app\android`:
   ```
   gradlew.bat installDebug
   ```
   For a terminal build, point `JAVA_HOME` at Android Studio's JDK first:
   `set JAVA_HOME=C:\Program Files\Android\Android Studio\jbr`.

The first build takes several minutes longer than the others: about 75 native files for llama.cpp.
Later builds reuse them.

**Keep the permissions between installs.** Android replaces an installed app only when the new
APK carries the same signing key; a different key means an uninstall first, and the app's data
goes with it: the enrolment, every permission granted, Health Connect's grants. A release cut
is signed with the release keystore and an editor build with Android Studio's debug key, so
installing one over the other asked for everything again. Put the keystore and its password in
`~/.config/localghost/release.env` (`tools/app_keystore.sh --use <file.jks> --store-pass` in the
server tree writes it, mode 600) and the build signs every variant with it; Gradle says so at
the start of a build. Without the password it keeps the debug key and says that instead.

## Linux (Debian 13 or Ubuntu 24.04, command line)

1. **Run the setup script once** from the repository root (it uses `sudo` for apt):
   ```
   app/android/tools/debian_setup.sh
   source ~/.localghost_android_env
   ```
   It installs:
   - JDK 21 (`openjdk-21-jdk-headless`), plus `unzip` and `wget`;
   - the Android command-line tools in `~/android-sdk`;
   - the SDK licences, accepted;
   - `platform-tools`, `platforms;android-37.0`, `platforms;android-36`, `build-tools;36.0.0`,
     `ndk;28.2.13676358` and `cmake;3.22.1`.

   It also writes `~/.localghost_android_env` (JAVA_HOME, ANDROID_HOME, PATH) and, if missing,
   `app/android/local.properties` with `sdk.dir`.

   Doing it by hand instead:
   ```
   sudo apt-get install -y openjdk-21-jdk-headless unzip wget
   # command-line tools from developer.android.com/studio#command-line-tools-only
   #   into ~/android-sdk/cmdline-tools/latest
   yes | sdkmanager --licenses
   sdkmanager "platform-tools" "platforms;android-37.0" "platforms;android-36" \
       "build-tools;36.0.0" "ndk;28.2.13676358" "cmake;3.22.1"
   echo "sdk.dir=$HOME/android-sdk" > app/android/local.properties
   ```
   API 37 installs as `platforms;android-37.0`. `compileSdk = release(37)` finds it under that name.
2. **Optional: a local llama.cpp tarball.** As on Windows, `llamaTarball=/path/to/llama.cpp-v0.5.0-7fe450e.tar.gz`
   in `local.properties`. On the LocalGhost box itself, the tarball that `setup_llama.sh` verified
   (`/opt/localghost/llama.cpp.mirror-dl/`) is used automatically. The box does not need any of
   this to run: it only matters if you build the app there.
3. **Connect the phone.** Turn on USB debugging, then run `adb devices` and accept the prompt on
   the phone. If it shows "no permissions", install `android-sdk-platform-tools-common` (the udev
   rules) and re-plug the phone.
4. **Build and install:**
   ```
   cd app/android
   ./gradlew installDebug        # or assembleDebug: app/build/outputs/apk/debug/app-debug.apk
   ```

## Checking it worked

In the app, open MODELS. The top line should say "runtime in this build (llama.cpp v0.5.0-…)". If it
says "this build carries no model runtime (its llama.cpp pin is not set)", that APK was built
without the native part.

## Releases (Linux)

A release is built on the Debian box that hosts the website, where the GPG key lives:
```
source ~/.localghost_android_env
export LG_KEYSTORE=/path/to/localghost-release.jks
export LG_KEY_ALIAS=localghost
export LG_LLAMA_TARBALL=/path/to/llama.cpp-v0.5.0-7fe450e.tar.gz   # optional, else the mirror
app/android/tools/release.sh
```
`release.sh`:
1. checks the tree is clean;
2. writes `ghost/build-env.txt` and signs the source manifest;
3. writes a release `local.properties` with empty box values;
4. builds;
5. zipaligns and signs with `apksigner`, and GPG-signs the APK;
6. prints the four values to publish (commit, APK SHA-256, manifest root, signing cert SHA-256).

VERIFY.md explains how anyone checks a release. For byte-identical rebuilds, use the same pinned
toolchain: the Gradle wrapper, `gradle/libs.versions.toml`, build-tools 36.0.0, NDK 28.2.13676358,
CMake 3.22.1 and the llama.cpp pin.

## The llama.cpp pin

`app/src/main/cpp/CMakeLists.txt` holds four values:
- `LLAMA_CPP_TARBALL`: the file on the mirror;
- `LLAMA_CPP_SHA256`: the pin;
- `LLAMA_CPP_TAG` and `LLAMA_CPP_COMMIT`: for people to read.

Gradle reads the pin: with it empty, the APK builds without the native part.

The tarball is looked for in this order:
1. `-PllamaTarball=<file>`;
2. `llamaTarball=` in `local.properties`;
3. on the box, `/opt/localghost/llama.cpp.mirror-dl/`;
4. the mirror.

To move the pin after the box updates its engine, run one of these on the box:
- `app/android/tools/pin_llama.sh --from-box`, which pins the tarball the box verified (it refuses
  a file whose hash has changed since);
- `app/android/tools/pin_llama.sh`, which reads the mirror's signed manifest (with gpg and the site
  key).

The JNI bridge (`app/src/main/cpp/llama_jni.cpp`) follows llama.cpp's C API. A new llama.cpp can
rename a field or function, so rebuild and test after moving the pin.

## When the build fails

- **"URL_HASH is set to SHA256=…;DOWNLOAD_EXTRACT_TIMESTAMP;TRUE".** The `CMakeLists.txt` is older
  than 30 Sep 2026. Take the current one, delete `app/.cxx`, and build again.
- **"no member named 'use_mmap' in 'llama_model_params'".** The `llama_jni.cpp` is older than
  30 Sep 2026. Take the current one.
- **"SHA256 hash of … does not match expected value".** The tarball is not the pinned one. Check
  `llamaTarball=`, or the mirror.
- **"the mirror's manifest has no llama.cpp-… with SHA-256 …".** The mirror no longer lists that
  tarball. Point `llamaTarball=` at the box's copy, or move the pin (above).
- **"could not read …/MANIFEST.txt".** The mirror cannot be reached from this machine. Use
  `llamaTarball=`.
- **"NDK not configured" / "No version of NDK matched the requested version 28.2.13676358".**
  Install exactly that NDK (SDK Manager, or `sdkmanager "ndk;28.2.13676358"`).
- **"CMake '3.22.1' was not found".** Install `cmake;3.22.1`.
- **A configure error after changing `CMakeLists.txt` or the pin.** Delete `app/.cxx` so CMake
  starts fresh.
- **An error naming android-37 (the platform not found).** Install `platforms;android-37.0`.

## The box's server (Linux only)

The Go server is not built on these machines. It builds on the box: `server/tools/setup.sh`
installs Go from the mirror at setup (the version `server/go.mod` names, 1.27.1 now; `redeploy.sh`
brings a box up to a newer one the same way), and `server/tools/redeploy.sh` builds and stages the
daemons (the next unlock puts them on the volume).
