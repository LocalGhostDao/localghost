#!/usr/bin/env bash
# One-time setup on Debian 13 to build the LocalGhost APK headlessly (no Android Studio).
# Installs the EXACT JDK this project's releases are built with, the Android command-line tools, and
# the exact SDK packages this project uses. Matching the JDK matters: the JDK major version is
# build-determining, so a reproducer on a different JDK will not get matching bytes.
set -euo pipefail

ANDROID_HOME="${ANDROID_HOME:-$HOME/android-sdk}"
CMDLINE_VER="latest"

# The required JDK major version is the single source of truth in ghost/build-env.txt (jdk.version).
# Read it from there so this script, the manifest, and the release cannot drift to different JDKs.
# Falls back to 21 (the version LocalGhost releases are built with on Trixie) if build-env is absent.
# Anchor to the Android project root (this script lives in <project>/tools). The Android app is a
# subdirectory of the localghost monorepo, so we use the script's own location, not git's top-level,
# which would point at the monorepo root and make every project-relative path below wrong.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BUILD_ENV="$PROJECT_ROOT/ghost/build-env.txt"
JDK_MAJOR="21"
if [ -f "$BUILD_ENV" ]; then
    v="$(grep -E '^jdk\.version' "$BUILD_ENV" 2>/dev/null | awk '{print $2}' | cut -d. -f1)"
    [ -n "$v" ] && JDK_MAJOR="$v"
fi

# Debian 13 (Trixie) dropped openjdk-17 from main and ships JDK 21 (default) and 25. We install and
# REQUIRE exactly $JDK_MAJOR, not a range, so a verifier ends up on the same JDK the release used.
# (If you need a JDK not in Debian's repos, use the Adoptium Temurin repo for that major instead.)
echo "> JDK ${JDK_MAJOR} (the version this project's releases are built with)..."
need_install=1
if command -v javac >/dev/null && javac -version 2>&1 | grep -qE "javac ${JDK_MAJOR}\."; then
    need_install=0
fi
if [ "$need_install" = 1 ]; then
    sudo apt-get update
    sudo apt-get install -y "openjdk-${JDK_MAJOR}-jdk-headless" unzip wget
fi

# Resolve JAVA_HOME from javac so it points at the real JDK (not a /usr/bin symlink dir).
export JAVA_HOME="$(dirname "$(dirname "$(readlink -f "$(command -v javac)")")")"
echo "  JAVA_HOME=$JAVA_HOME"
javac -version
# Hard-fail if, after install, javac is still not the required major. Better to stop here than to
# build on the wrong JDK and ship/verify a non-matching APK.
if ! javac -version 2>&1 | grep -qE "javac ${JDK_MAJOR}\."; then
    echo "  ERROR: javac is not JDK ${JDK_MAJOR}. Releases are built with ${JDK_MAJOR}; a different"
    echo "         JDK will not reproduce the APK. Fix JAVA_HOME / your default java before building."
    exit 1
fi

echo "> Android command-line tools..."
mkdir -p "$ANDROID_HOME/cmdline-tools"
if [ ! -d "$ANDROID_HOME/cmdline-tools/$CMDLINE_VER" ]; then
    # Latest cmdline-tools zip. If this URL 404s, get the current one from
    # https://developer.android.com/studio#command-line-tools-only and replace it.
    # NOTE: the cmdline-tools VERSION is NOT build-determining — it only drives sdkmanager. A verifier
    # who gets a newer cmdline-tools still reproduces the APK as long as the SDK PACKAGES below match.
    TOOLS_ZIP="commandlinetools-linux-13114758_latest.zip"
    wget -q "https://dl.google.com/android/repository/$TOOLS_ZIP" -O /tmp/cmdtools.zip
    unzip -q /tmp/cmdtools.zip -d /tmp/cmdtools
    mv /tmp/cmdtools/cmdline-tools "$ANDROID_HOME/cmdline-tools/$CMDLINE_VER"
fi

export PATH="$ANDROID_HOME/cmdline-tools/$CMDLINE_VER/bin:$ANDROID_HOME/platform-tools:$PATH"

# Persist env for future shells NOW, before the SDK package install. Writing it early means that if a
# later step fails, you are not stranded with no ANDROID_HOME — you can source this and finish by
# hand. (It used to be written last, so a mid-script failure left no profile at all.)
PROFILE="$HOME/.localghost_android_env"
cat > "$PROFILE" <<ENV
export JAVA_HOME="$JAVA_HOME"
export ANDROID_HOME="$ANDROID_HOME"
export PATH="\$ANDROID_HOME/cmdline-tools/$CMDLINE_VER/bin:\$ANDROID_HOME/platform-tools:\$ANDROID_HOME/build-tools/36.0.0:\$PATH"
ENV
echo "  wrote $PROFILE"

echo "> Accepting licenses..."
# sdkmanager --licenses reads 'y' for each prompt. Under 'set -o pipefail' a naive
# 'yes | sdkmanager --licenses' makes the WHOLE script die: when sdkmanager stops reading, 'yes' gets
# SIGPIPE and the pipeline reports failure, which set -e treats as fatal — silently, before the real
# install runs. (That is exactly what stranded this setup.) Feed the y's without a pipe and never let
# this step abort the script; a license decline shows up as the package install failing below, which
# we DO surface.
yes 2>/dev/null | sdkmanager --sdk_root="$ANDROID_HOME" --licenses >/dev/null 2>&1 || \
    echo "  (license acceptance returned non-zero; continuing — the install below will report if anything is unaccepted)"

echo "> Installing SDK packages this project needs..."
# These ARE build-determining and are pinned exactly (see ghost/build-env.txt: buildTools/compileSdk).
# Note: API 37 installs as "platforms;android-37.0" (not android-37). compileSdk = release(37)
# resolves against it. If a build ever fails "looking for android-37", that .0 naming is why.
# This one we DO want to fail loudly if it fails, so it is not silenced.
if ! sdkmanager --sdk_root="$ANDROID_HOME" \
    "platform-tools" \
    "platforms;android-37.0" \
    "platforms;android-36" \
    "build-tools;36.0.0"; then
    echo "  ERROR: SDK package install failed. The profile at $PROFILE is already written, so once"
    echo "         you resolve the cause you can re-run just the sdkmanager line above by hand."
    exit 1
fi

echo
echo "Done. Add this to your shell rc (or 'source' it before building):"
echo "    source $PROFILE"
echo
# --- llama.cpp (our only external native dependency) ---
# Pinned by IMMUTABLE COMMIT (not just the tag) and verified after fetch. The pin lives only in
# CMakeLists.txt (LLAMA_CPP_TAG + LLAMA_CPP_COMMIT). This step pre-clones at that commit so the
# native build runs offline and the exact source is part of the deploy, verifies it, resolves the
# full SHA for the tag if the pin still has the placeholder, and checks GitHub for a newer release.
CMAKE="$PROJECT_ROOT/app/src/main/cpp/CMakeLists.txt"
LLAMA_REPO="https://github.com/ggml-org/llama.cpp"
LLAMA_DIR="$PROJECT_ROOT/.cache/llama.cpp"
LLAMA_TAG="$(grep -oE 'LLAMA_CPP_TAG[^"]*"[^"]+"' "$CMAKE" 2>/dev/null | grep -oE '"[^"]+"$' | tr -d '"')"
LLAMA_COMMIT="$(grep -oE 'LLAMA_CPP_COMMIT[^"]*"[^"]+"' "$CMAKE" 2>/dev/null | grep -oE '"[^"]+"$' | tr -d '"')"

if [ -n "$LLAMA_TAG" ]; then
    echo "> llama.cpp pinned at tag $LLAMA_TAG, commit ${LLAMA_COMMIT:0:12} ..."

    # Resolve the full 40-char SHA the tag points to (so we can verify and, if needed, fill the pin).
    RESOLVED="$(git ls-remote "$LLAMA_REPO" "refs/tags/$LLAMA_TAG^{}" 2>/dev/null | awk '{print $1}' | head -1)"
    [ -z "$RESOLVED" ] && RESOLVED="$(git ls-remote "$LLAMA_REPO" "refs/tags/$LLAMA_TAG" 2>/dev/null | awk '{print $1}' | head -1)"

    if echo "$LLAMA_COMMIT" | grep -q "REPLACE_WITH_FULL"; then
        if [ -n "$RESOLVED" ]; then
            echo "  [action needed] LLAMA_CPP_COMMIT is a placeholder. Tag $LLAMA_TAG resolves to:"
            echo "      $RESOLVED"
            echo "  Set LLAMA_CPP_COMMIT = \"$RESOLVED\" in $CMAKE, then re-run, so the build verifies it."
        else
            echo "  [!] could not resolve $LLAMA_TAG from GitHub to fill the commit pin (offline?)."
        fi
    elif [ -n "$RESOLVED" ] && [ "$RESOLVED" != "$LLAMA_COMMIT" ]; then
        echo "  [!] WARNING: pinned commit does not match what tag $LLAMA_TAG resolves to now."
        echo "      pinned:   $LLAMA_COMMIT"
        echo "      tag now:  $RESOLVED"
        echo "      The tag may have been re-pointed. NOT changing the pin; investigate before bumping."
    fi

    # Fetch the exact pinned commit (skip if still placeholder).
    if ! echo "$LLAMA_COMMIT" | grep -q "REPLACE_WITH_FULL"; then
        mkdir -p "$LLAMA_DIR"
        if [ ! -d "$LLAMA_DIR/.git" ]; then
            ( cd "$LLAMA_DIR" && git init -q && git remote add origin "$LLAMA_REPO.git" )
        fi
        ( cd "$LLAMA_DIR" \
            && git fetch -q --depth 1 origin "$LLAMA_COMMIT" \
            && git checkout -q "$LLAMA_COMMIT" ) \
            || echo "  fetch of $LLAMA_COMMIT failed (check the pin / connectivity)"
        # Verify what we checked out.
        if [ -d "$LLAMA_DIR/.git" ]; then
            GOT="$(cd "$LLAMA_DIR" && git rev-parse HEAD 2>/dev/null)"
            if [ "$GOT" = "$LLAMA_COMMIT" ]; then
                echo "  verified: llama.cpp at $LLAMA_COMMIT"
            else
                echo "  [!] MISMATCH: got $GOT, expected $LLAMA_COMMIT. Do not build."
            fi
        fi
    fi

    # Inform about a newer release (does NOT bump).
    LATEST="$(curl -fsSL https://api.github.com/repos/ggml-org/llama.cpp/releases/latest 2>/dev/null \
        | grep -oE '"tag_name"[^,]*' | grep -oE 'b[0-9]+' | head -1)"
    STAMP="$PROJECT_ROOT/.cache/llama-version-check.txt"; mkdir -p "$PROJECT_ROOT/.cache"
    if [ -n "$LATEST" ]; then
        echo "pinned_tag=$LLAMA_TAG pinned_commit=$LLAMA_COMMIT latest_tag=$LATEST checked=$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$STAMP"
        if [ "$LATEST" != "$LLAMA_TAG" ]; then
            echo "  [i] newer llama.cpp available: $LATEST (pinned: $LLAMA_TAG). To adopt, update both"
            echo "      LLAMA_CPP_TAG and LLAMA_CPP_COMMIT in $CMAKE, rebuild, re-test the JNI."
        else
            echo "  up to date with upstream latest ($LATEST)."
        fi
    else
        echo "  (could not reach GitHub for a version check; offline is fine)"
    fi
else
    echo "> (could not read LLAMA_CPP_TAG from $CMAKE; skipping llama.cpp pre-fetch)"
fi

echo ""
echo "The build also needs a local.properties pointing at the SDK. The release script writes it."