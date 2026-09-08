#!/bin/sh
# Build first and publish atomically; a compiler error leaves the old binary intact.
set -eu
source_dir=${CPA_SOURCE_DIR:-/workspace}
build_dir=${CPA_BUILD_DIR:-/tmp/cpa-hot-reload}
mkdir -p "$build_dir"
cd "$source_dir"
commit=$(git -c safe.directory="$source_dir" rev-parse HEAD)
if ! git -c safe.directory="$source_dir" diff --quiet HEAD -- cmd internal sdk go.mod go.sum scripts/local_hot_build.sh ||
   [ -n "$(git -c safe.directory="$source_dir" ls-files --others --exclude-standard -- cmd internal sdk go.mod go.sum)" ]; then
    commit="$commit-dirty"
fi
candidate="$build_dir/CLIProxyAPI.next"
trap 'rm -f "$candidate"' EXIT HUP INT TERM
CGO_ENABLED=1 go build -mod=readonly -buildvcs=false \
    -ldflags="-X main.Version=CPA-fork-hot -X main.Commit=$commit -X main.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o "$candidate" ./cmd/server
mv -f "$candidate" "$build_dir/CLIProxyAPI"
printf '[hot-reload] built %s\n' "$commit"
