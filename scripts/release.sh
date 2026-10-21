#!/usr/bin/env bash
# Build lam for Linux and macOS and prepare a tag that publishes them.
#
#   scripts/release.sh v0.2.0     (or: make release TAG=v0.2.0)
#   git push origin v0.2.0
#
# lam links libghostty through cgo, so it is built here: natively for this
# machine and, on Linux x86_64, for macOS arm64 with Zig (scripts/cross-darwin.sh).
# The executables (lam-linux-amd64, lam-darwin-arm64) are committed under release/
# in a commit that only the tag points at, so main's history stays free of
# binaries and release/ is export-ignored from the source tarball. Pushing the tag
# runs the release workflow, which attaches them to a GitHub release next to the
# source tarball GitHub makes itself; nothing is built in CI. `lam update` and
# install.sh download the executable that matches the system they detect.
set -euo pipefail

tag="${1:-}"
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "usage: $0 vMAJOR.MINOR.PATCH" >&2
    exit 2
fi
cd "$(dirname "$0")/.."

if [ -n "$(git status --porcelain)" ]; then
    echo "the working tree has uncommitted changes; commit first so the build matches $tag" >&2
    exit 1
fi
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
    echo "tag $tag already exists; delete it first (git tag -d $tag) to prepare it again" >&2
    exit 1
fi

out="$(mktemp -d)"
index="$(mktemp -u)"
trap 'rm -rf "$out" "$index" ./-.o' EXIT

make build VERSION="$tag"
files=("lam-$(go env GOOS)-$(go env GOARCH)")
cp lam "$out/${files[0]}"
if [ "$(go env GOOS)/$(go env GOARCH)" = linux/amd64 ]; then
    VERSION="$tag" scripts/cross-darwin.sh "$out/lam-darwin"
    mv "$out/lam-darwin" "$out/lam-darwin-arm64"
    files+=("lam-darwin-arm64")
else
    echo "not on Linux x86_64: only $(go env GOOS)/$(go env GOARCH) was built" >&2
fi

# A commit on top of HEAD that adds release/, made without touching the working
# tree or the current branch.
export GIT_INDEX_FILE="$index"
git read-tree HEAD
for f in "${files[@]}"; do
    blob="$(git hash-object -w "$out/$f")"
    git update-index --add --cacheinfo "100644,$blob,release/$f"
done
tree="$(git write-tree)"
commit="$(git commit-tree "$tree" -p HEAD -m "Release $tag")"
unset GIT_INDEX_FILE
git tag -a "$tag" "$commit" -m "lam $tag"

echo
echo "prepared $tag with: ${files[*]}"
echo "publish it with:  git push origin $tag"
