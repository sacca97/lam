#!/usr/bin/env bash
# Build the macOS GUI (lam.app) for arm64 from a Linux x86_64 machine.
#
#   MACOS_SDK=/path/to/MacOSX.sdk scripts/cross-darwin-gui.sh [outdir]
#
# The GUI links AppKit, Metal and QuartzCore and compiles Objective-C, which
# the stub files scripts/cross-darwin.sh uses for the terminal build cannot
# stand in for. It needs Apple's SDK, which is not redistributable: take
# MacOSX.sdk from Xcode or the Command Line Tools on a Mac (for example
# /Library/Developer/CommandLineTools/SDKs/MacOSX.sdk) and copy it over.
#
# Produces <outdir>/lam-gui-darwin-arm64 and <outdir>/lam-app-darwin-arm64.tar.gz,
# which holds lam.app, signed ad hoc when rcodesign is installed. Unpack it into
# ~/Applications on the Mac; a downloaded copy needs a right-click, Open the
# first time (or xattr -dr com.apple.quarantine lam.app) as it has no developer ID.
set -euo pipefail

[ -n "${MACOS_SDK:-}" ] || { echo "set MACOS_SDK to a MacOSX.sdk directory (see the header of this script)" >&2; exit 1; }
cd "$(dirname "$0")/.."
OUT="$(realpath -m "${1:-dist-darwin}")"
mkdir -p "$OUT"

GO_TAGS=gui scripts/cross-darwin.sh "$OUT/lam-gui-darwin-arm64"

rm -rf "$OUT/Applications"
make --no-print-directory macapp APP_DIR="$OUT/Applications" GUI_BINARY="$OUT/lam-gui-darwin-arm64"
# Notifications need a signed app. An ad-hoc signature (no identity) is enough;
# rcodesign makes one from Linux: cargo install apple-codesign
if command -v rcodesign >/dev/null 2>&1; then
    rcodesign sign "$OUT/Applications/lam.app" >/dev/null
    echo "  signed lam.app (ad hoc)"
else
    echo "  warning: rcodesign not found, lam.app is not signed (notifications need a signature: run codesign --force --deep -s - lam.app on the Mac)" >&2
fi
tar -C "$OUT/Applications" -czf "$OUT/lam-app-darwin-arm64.tar.gz" lam.app
echo "  built $OUT/lam-app-darwin-arm64.tar.gz"
