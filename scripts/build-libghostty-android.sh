#!/usr/bin/env bash
# Build libghostty-vt for Android arm64 into $LAM_DEPS/libghostty-android-arm64
# using the pinned Zig (which ships its own bionic headers; no NDK needed here).
# Also prints the env needed to cgo-link it with the NDK clang.
#
#   scripts/build-libghostty-android.sh
set -euo pipefail
cd "$(dirname "$0")/.."
scripts/build-libghostty.sh   # fetches Zig and the pinned Ghostty checkout

DEPS="${LAM_DEPS:-$HOME/.cache/lam}"
ZIG="$DEPS/zig-x86_64-linux-0.17.0/zig"
PREFIX="$DEPS/libghostty-android-arm64"
COMMIT="$(cat "$DEPS/libghostty.commit")"
TARGET="${ANDROID_ZIG_TARGET:-aarch64-linux-android}"

if [ ! -f "$PREFIX/lib/libghostty-vt.a" ] || [ "$(cat "$PREFIX/commit" 2>/dev/null)" != "$COMMIT" ]; then
    echo "  Building libghostty-vt for $TARGET..."
    (cd "$DEPS/ghostty-src" && "$ZIG" build -Demit-lib-vt -Demit-xcframework=false -Dtarget="$TARGET" -Doptimize=ReleaseFast --prefix "$PREFIX")
    echo "$COMMIT" > "$PREFIX/commit"
fi
echo "  libghostty-vt: $PREFIX/lib/libghostty-vt.a"
