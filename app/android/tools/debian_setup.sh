#!/usr/bin/env bash
# One-time setup on Debian 13 to build the LocalGhost APK headlessly (no Android Studio).
# Installs JDK 17, the Android command-line tools, and the exact SDK packages this project uses.
set -euo pipefail

ANDROID_HOME="${ANDROID_HOME:-$HOME/android-sdk}"
CMDLINE_VER="latest"

echo "> JDK 17 (required by the Android Gradle Plugin)..."
if ! command -v javac >/dev/null || ! java -version 2>&1 | grep -q '"17'; then
    sudo apt-get update
    sudo apt-get install -y openjdk-17-jdk-headless unzip wget
fi
export JAVA_HOME="$(dirname "$(dirname "$(readlink -f "$(command -v javac)")")")"
echo "  JAVA_HOME=$JAVA_HOME"

echo "> Android command-line tools..."
mkdir -p "$ANDROID_HOME/cmdline-tools"
if [ ! -d "$ANDROID_HOME/cmdline-tools/$CMDLINE_VER" ]; then
    # Latest cmdline-tools zip. If this URL 404s, get the current one from
    # https://developer.android.com/studio#command-line-tools-only and replace it.
    TOOLS_ZIP="commandlinetools-linux-13114758_latest.zip"
    wget -q "https://dl.google.com/android/repository/$TOOLS_ZIP" -O /tmp/cmdtools.zip
    unzip -q /tmp/cmdtools.zip -d /tmp/cmdtools
    mv /tmp/cmdtools/cmdline-tools "$ANDROID_HOME/cmdline-tools/$CMDLINE_VER"
fi

export PATH="$ANDROID_HOME/cmdline-tools/$CMDLINE_VER/bin:$ANDROID_HOME/platform-tools:$PATH"

echo "> Accepting licenses + installing SDK packages this project needs..."
yes | sdkmanager --sdk_root="$ANDROID_HOME" --licenses >/dev/null
sdkmanager --sdk_root="$ANDROID_HOME" \
    "platform-tools" \
    "platforms;android-36" \
    "build-tools;36.0.0"

# Persist env for future shells.
PROFILE="$HOME/.localghost_android_env"
cat > "$PROFILE" <<ENV
export JAVA_HOME="$JAVA_HOME"
export ANDROID_HOME="$ANDROID_HOME"
export PATH="\$ANDROID_HOME/cmdline-tools/$CMDLINE_VER/bin:\$ANDROID_HOME/platform-tools:\$ANDROID_HOME/build-tools/36.0.0:\$PATH"
ENV
echo
echo "Done. Add this to your shell rc (or 'source' it before building):"
echo "    source $PROFILE"
echo
echo "The build also needs a local.properties pointing at the SDK. The release script writes it."
