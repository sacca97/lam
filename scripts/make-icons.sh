#!/usr/bin/env bash
# Regenerate every icon from the SVGs in assets/icon: the macOS .icns, the
# Linux PNGs and the Android launcher layers. The outputs are committed, so
# this is only needed after changing an SVG.
#
# Needs: rsvg-convert (librsvg), python3.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$ROOT/assets/icon"
PNG="$SRC/png"
RES="$ROOT/android/app/src/main/res"
mkdir -p "$PNG"

# Small sizes drop the session dots, which would be mush there.
for size in 16 32 48 64; do
  rsvg-convert -w "$size" -h "$size" "$SRC/lam-desktop-small.svg" -o "$PNG/lam-$size.png"
done
for size in 128 256 512 1024; do
  rsvg-convert -w "$size" -h "$size" "$SRC/lam-desktop.svg" -o "$PNG/lam-$size.png"
done

# macOS: an .icns is a header and PNG chunks.
python3 - "$PNG" "$SRC/lam.icns" <<'PY'
import struct, sys
png, out = sys.argv[1], sys.argv[2]
chunks = [("icp4", 16), ("icp5", 32), ("icp6", 64), ("ic07", 128), ("ic08", 256), ("ic09", 512), ("ic10", 1024)]
body = b""
for tag, size in chunks:
    data = open(f"{png}/lam-{size}.png", "rb").read()
    body += tag.encode() + struct.pack(">I", len(data) + 8) + data
open(out, "wb").write(b"icns" + struct.pack(">I", len(body) + 8) + body)
PY

# Android: adaptive icon layers, 108dp at each density.
declare -A DENSITY=([mdpi]=108 [hdpi]=162 [xhdpi]=216 [xxhdpi]=324 [xxxhdpi]=432)
for d in "${!DENSITY[@]}"; do
  mkdir -p "$RES/mipmap-$d"
  rsvg-convert -w "${DENSITY[$d]}" -h "${DENSITY[$d]}" "$SRC/lam-android-foreground.svg" -o "$RES/mipmap-$d/ic_launcher_foreground.png"
  rsvg-convert -w "${DENSITY[$d]}" -h "${DENSITY[$d]}" "$SRC/lam-android-monochrome.svg" -o "$RES/mipmap-$d/ic_launcher_monochrome.png"
done
mkdir -p "$RES/mipmap-anydpi-v26"
cat > "$RES/mipmap-anydpi-v26/ic_launcher.xml" <<'XML'
<?xml version="1.0" encoding="utf-8"?>
<adaptive-icon xmlns:android="http://schemas.android.com/apk/res/android">
    <background android:drawable="@color/ic_launcher_background" />
    <foreground android:drawable="@mipmap/ic_launcher_foreground" />
    <monochrome android:drawable="@mipmap/ic_launcher_monochrome" />
</adaptive-icon>
XML
cp "$RES/mipmap-anydpi-v26/ic_launcher.xml" "$RES/mipmap-anydpi-v26/ic_launcher_round.xml"
cat > "$RES/values/colors.xml" <<'XML'
<?xml version="1.0" encoding="utf-8"?>
<resources>
    <color name="ic_launcher_background">#111411</color>
</resources>
XML
echo "icons written"
