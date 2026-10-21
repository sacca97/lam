#!/usr/bin/env bash
# Build lam for macOS arm64 from a Linux x86_64 machine, using only the Zig
# toolchain that scripts/build-libghostty.sh already downloads.
#
#   scripts/cross-darwin.sh [output]        (default: ./lam-darwin-arm64)
#
# Zig cross-compiles libghostty-vt and acts as the C compiler and linker for
# cgo. The one thing it lacks is Apple's SDK: Go links the CoreFoundation and
# Security frameworks and libresolv, and the linker wants a .tbd stub for each
# to check the symbols. Those stubs are generated here from the symbols the
# installed Go toolchain imports, so they track Go upgrades. They only satisfy
# the linker: at run time the Mac binds to its own frameworks.
set -euo pipefail

[ "$(uname -s)-$(uname -m)" = Linux-x86_64 ] || { echo "run this on Linux x86_64" >&2; exit 1; }
cd "$(dirname "$0")/.."
scripts/build-libghostty.sh

DEPS="${LAM_DEPS:-$HOME/.cache/lam}"
ZIG="$DEPS/zig-x86_64-linux-0.17.0/zig"
TARGET="aarch64-macos.12.0"
PREFIX="$DEPS/libghostty-darwin-arm64"
COMMIT="$(cat "$DEPS/libghostty.commit")"
STUBS="$DEPS/darwin-stubs"
OUT="${1:-lam-darwin-arm64}"
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"

if [ ! -f "$PREFIX/lib/libghostty-vt.a" ] || [ "$(cat "$PREFIX/commit" 2>/dev/null)" != "$COMMIT" ]; then
    echo "  Building libghostty-vt for macOS arm64..."
    (cd "$DEPS/ghostty-src" && "$ZIG" build -Demit-lib-vt -Demit-xcframework=false -Dtarget="$TARGET" -Doptimize=ReleaseFast --prefix "$PREFIX")
    echo "$COMMIT" > "$PREFIX/commit"
fi

rm -rf "$STUBS"
mkdir -p "$STUBS"
python3 - "$STUBS" "$(go env GOROOT)" <<'PY'
import re, subprocess, sys
out, goroot = sys.argv[1], sys.argv[2]
src = subprocess.run("grep -rh cgo_import_dynamic crypto/x509/internal/macos net internal",
                     shell=True, capture_output=True, text=True, cwd=goroot + "/src").stdout
libs = {}
for m in re.finditer(r'cgo_import_dynamic \S+ (\S+) "([^"]+)"', src):
    libs.setdefault(m.group(2), set()).add("_" + m.group(1))
import os
for lib, syms in libs.items():
    if "/System/Library/Frameworks/" in lib:
        name = lib.split("/")[-1]
        os.makedirs(f"{out}/{name}.framework", exist_ok=True)
        path = f"{out}/{name}.framework/{name}.tbd"
    elif lib.startswith("/usr/lib/libresolv"):
        path = f"{out}/libresolv.tbd"
    else:
        continue  # libSystem is provided by Zig
    with open(path, "w") as f:
        f.write("--- !tapi-tbd\ntbd-version:     4\ntargets:         [ arm64-macos ]\n"
                f"install-name:    '{lib}'\ncurrent-version: 1\nexports:\n"
                "  - targets:         [ arm64-macos ]\n"
                f"    symbols:         [ {', '.join(sorted(syms))} ]\n...\n")
PY

# Go passes "-framework CoreFoundation" once for every package that needs it
# and Zig's linker records each one, which dyld rejects as a duplicate dylib.
# The wrapper keeps the first of each framework.
cat > "$STUBS/cc" <<EOF
#!/bin/sh
n=\$#
seen=" "
while [ \$n -gt 0 ]; do
    a="\$1"; shift; n=\$((n - 1))
    if [ "\$a" = -framework ] && [ \$n -gt 0 ]; then
        name="\$1"; shift; n=\$((n - 1))
        case "\$seen" in *" \$name "*) continue ;; esac
        seen="\$seen\$name "
        set -- "\$@" -framework "\$name"
    else
        set -- "\$@" "\$a"
    fi
done
exec "$ZIG" cc -target $TARGET "\$@"
EOF
chmod +x "$STUBS/cc"

PKG_CONFIG_PATH="$PREFIX/share/pkgconfig" GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 CC="$STUBS/cc" \
    CGO_LDFLAGS="-F$STUBS -L$STUBS" \
    go build -trimpath -ldflags "-s -w -X github.com/sacca97/lam/internal/app.version=$VERSION" -o "$OUT" ./cmd/lam
echo "  built $OUT ($VERSION)"
