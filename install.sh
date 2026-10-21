#!/usr/bin/env bash
# Install LAM.
#
#   curl -fsSL https://raw.githubusercontent.com/sacca97/lam/main/install.sh | bash
#
# downloads the latest GitHub release for this OS and architecture, checks its
# SHA-256 and installs it to ~/.local/bin. Run from a checkout (./install.sh), it
# builds from source with `make install` instead.
#
#   LAM_VERSION=v0.2.0   install that release instead of the latest
#   PREFIX=/usr/local    install under PREFIX/bin (default ~/.local)
set -euo pipefail

prefix="${PREFIX:-${HOME}/.local}"
repo="sacca97/lam"
source_file="${BASH_SOURCE[0]:-}"
here=""
if [ -n "$source_file" ] && [ -f "$source_file" ]; then
    here="$(cd "$(dirname "$source_file")" && pwd)"
fi

if [ -n "$here" ] && [ -f "$here/Makefile" ] && [ -d "$here/cmd/lam" ]; then
    in_checkout=1
fi

if [ -n "${in_checkout:-}" ]; then
    cd "$here"
    missing=()
    for cmd in go cc pkg-config make git curl tar xz; do
        command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
    done
    if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
        missing+=("sha256sum or shasum")
    fi
    if [ ${#missing[@]} -gt 0 ]; then
        echo "  Missing dependencies: ${missing[*]}"
        echo "  Install them and re-run ./install.sh."
        exit 1
    fi
    make install PREFIX="$prefix" "$@"
    exit 0
fi

for cmd in curl; do
    command -v "$cmd" >/dev/null 2>&1 || { echo "  Missing dependency: $cmd" >&2; exit 1; }
done

case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) echo "  Unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) echo "  Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

tag="${LAM_VERSION:-}"
if [ -z "$tag" ]; then
    tag="$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)"
fi
if [ -z "$tag" ]; then
    echo "  Could not find a release of $repo. Build from source instead:" >&2
    echo "    git clone https://github.com/$repo.git && cd lam && make install" >&2
    exit 1
fi

base="${LAM_RELEASE_BASE:-https://github.com/$repo/releases/download}/$tag"
name="lam-${os}-${arch}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "  Downloading lam $tag for $os/$arch..."
curl -fsSL -o "$tmp/lam" "$base/$name" || { echo "  No $os/$arch build in release $tag." >&2; exit 1; }

# GitHub records a SHA-256 digest for each release asset; check it when it is there.
want="$(curl -fsSL "${LAM_API_BASE:-https://api.github.com/repos/$repo/releases}/tags/$tag" 2>/dev/null |
    awk -v n="$name" '/"name":/ {cur = ($0 ~ "\"" n "\"")} cur && /"digest":/ {gsub(/.*sha256:|".*/, ""); print; exit}')"
if [ -n "$want" ]; then
    if command -v sha256sum >/dev/null 2>&1; then
        got="$(sha256sum "$tmp/lam" | awk '{print $1}')"
    else
        got="$(shasum -a 256 "$tmp/lam" | awk '{print $1}')"
    fi
    if [ "$want" != "$got" ]; then
        echo "  Checksum mismatch for $name; not installing." >&2
        exit 1
    fi
fi

mkdir -p "$prefix/bin"
install -m 755 "$tmp/lam" "$prefix/bin/lam.new"
mv "$prefix/bin/lam.new" "$prefix/bin/lam"
echo "  Installed $prefix/bin/lam ($tag)"
case ":$PATH:" in *":$prefix/bin:"*) ;; *) echo "  Add $prefix/bin to your PATH to run lam." ;; esac
if [ -f "$HOME/.config/systemd/user/lam-daemon.service" ] || [ -f "$HOME/Library/LaunchAgents/com.lam.daemon.plist" ]; then
    "$prefix/bin/lam" install-service || true
fi
