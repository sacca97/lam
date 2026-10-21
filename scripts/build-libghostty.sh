#!/usr/bin/env bash
# Build libghostty-vt (the terminal emulator core of Ghostty) into
# $LAM_DEPS/libghostty (stored in the existing dependency cache), using a pinned Zig
# toolchain and a pinned Ghostty commit. Re-running is cheap once built.
set -euo pipefail

ZIG_VERSION=0.17.0
ZIG_SHA256_x86_64_linux=1cbe9df9f27e6b78d14ccbca43b6703a404ef79ef1c463de901d7f088d4e2026
ZIG_SHA256_aarch64_macos=b607e9b9234790a008116ae5bdb71c6243b84b9fb42a53a9e70fde41c06c536a
GHOSTTY_COMMIT=83fb95624b0439ee660c2679b43b38c8b63769cd # Ghostty v1.4.0

DEPS="${LAM_DEPS:-$HOME/.cache/lam}"
PREFIX="$DEPS/libghostty"

if [ -f "$PREFIX/lib/libghostty-vt.a" ] && [ "$(cat "$DEPS/libghostty.commit" 2>/dev/null)" = "$GHOSTTY_COMMIT" ]; then
    exit 0
fi

case "$(uname -s)-$(uname -m)" in
    Linux-x86_64) ZIG_TARGET=x86_64-linux; ZIG_SHA256=$ZIG_SHA256_x86_64_linux; CHECKSUM_CMD=(sha256sum -c -); ZIG_EXTRA_ARGS= ;;
    Darwin-arm64|Darwin-aarch64) ZIG_TARGET=aarch64-macos; ZIG_SHA256=$ZIG_SHA256_aarch64_macos; CHECKSUM_CMD=(shasum -a 256 -c -); ZIG_EXTRA_ARGS=-Demit-xcframework=false ;;
    *) echo "  build-libghostty.sh: unsupported platform $(uname -sm); build libghostty-vt manually and set PKG_CONFIG_PATH." >&2; exit 1 ;;
esac

mkdir -p "$DEPS"
ZIG_DIR="$DEPS/zig-$ZIG_TARGET-$ZIG_VERSION"
if [ ! -x "$ZIG_DIR/zig" ]; then
    echo "  Downloading Zig $ZIG_VERSION..."
    tarball="$DEPS/zig-$ZIG_VERSION.tar.xz"
    curl -fsSL -o "$tarball" "https://ziglang.org/download/$ZIG_VERSION/zig-$ZIG_TARGET-$ZIG_VERSION.tar.xz"
    echo "$ZIG_SHA256  $tarball" | "${CHECKSUM_CMD[@]}" >/dev/null || { echo "  Zig checksum mismatch" >&2; rm -f "$tarball"; exit 1; }
    tar -xf "$tarball" -C "$DEPS"
    rm -f "$tarball"
fi

SRC="$DEPS/ghostty-src"
if [ ! -d "$SRC/.git" ]; then
    git init -q "$SRC"
    git -C "$SRC" remote add origin https://github.com/ghostty-org/ghostty.git
fi
if [ "$(git -C "$SRC" rev-parse HEAD 2>/dev/null || true)" != "$GHOSTTY_COMMIT" ]; then
    echo "  Fetching Ghostty $GHOSTTY_COMMIT..."
    git -C "$SRC" fetch -q --depth 1 origin "$GHOSTTY_COMMIT"
    git -C "$SRC" checkout -q FETCH_HEAD
fi

echo "  Building libghostty-vt (first run takes a few minutes)..."
(cd "$SRC" && "$ZIG_DIR/zig" build -Demit-lib-vt $ZIG_EXTRA_ARGS -Doptimize=ReleaseFast --prefix "$PREFIX")
echo "$GHOSTTY_COMMIT" > "$DEPS/libghostty.commit"
echo "  libghostty-vt installed to $PREFIX"
