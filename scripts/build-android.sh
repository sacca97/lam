#!/usr/bin/env bash
# Build the Android client: the Go mobile package as an .aar (gomobile bind),
# then the debug APK with Gradle.
#
# Needs: Go, the Android SDK + NDK, a JDK, gomobile (installed on demand into
# GOBIN), and the static libghostty for android/arm64 (scripts/build-libghostty-android.sh
# builds it into ~/.cache/lam/libghostty-android-arm64).
#
# Output: android/app/libs/lammobile.aar and android/app/build/outputs/apk/<variant>/app-<variant>.apk
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEPS="${LAM_DEPS:-$HOME/.cache/lam}"
export ANDROID_HOME="${ANDROID_HOME:-$HOME/Android/Sdk}"
export ANDROID_NDK_HOME="${ANDROID_NDK_HOME:-$ANDROID_HOME/ndk/28.2.13676358}"
export ANDROID_API="${ANDROID_API:-26}"
export JAVA_HOME="${JAVA_HOME:-/opt/android-studio/jbr}"
export PATH="$JAVA_HOME/bin:$(go env GOPATH)/bin:$PATH"
export PKG_CONFIG_PATH="$DEPS/libghostty-android-arm64/share/pkgconfig"

if [ ! -f "$PKG_CONFIG_PATH/libghostty-vt-static.pc" ] && [ -z "$(ls "$PKG_CONFIG_PATH" 2>/dev/null)" ]; then
  echo "libghostty for android/arm64 not found in $DEPS; run scripts/build-libghostty-android.sh" >&2
  exit 1
fi

if ! command -v gomobile >/dev/null; then
  go install golang.org/x/mobile/cmd/gomobile@latest
  gomobile init
fi

cd "$ROOT"
mkdir -p android/app/libs
gomobile bind -target=android/arm64 -androidapi "$ANDROID_API" -ldflags="-s -w" \
  -o android/app/libs/lammobile.aar ./mobile

# LAM_VERSION (a tag like v0.2.0) sets the app's version; VARIANT=release builds
# the shrunk, signed APK (see scripts/release.sh for the key).
variant="${VARIANT:-debug}"
props=()
if [ -n "${LAM_VERSION:-}" ]; then
  v="${LAM_VERSION#v}"
  IFS=. read -r major minor patch <<<"$v"
  props=(-PlamVersion="$v" -PlamVersionCode="$((major * 10000 + minor * 100 + patch))")
fi
cd android
./gradlew --no-daemon ${props[@]+"${props[@]}"} "assemble${variant^}"
ls -l app/libs/lammobile.aar app/build/outputs/apk/$variant/app-$variant.apk
