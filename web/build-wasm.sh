#!/usr/bin/env bash
# Builds the GOOS=js GOARCH=wasm artifact used by the @anacrolix/torrent
# npm package. Output is written to web/dist/ alongside Go's
# wasm_exec.js runtime.
set -euo pipefail

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$here/.." && pwd)"
out_dir="$here/dist"
mkdir -p "$out_dir"

# Locate Go's wasm_exec.js shipped with the active toolchain.
goroot="$(go env GOROOT)"
wasm_exec=""
for candidate in \
    "$goroot/lib/wasm/wasm_exec.js" \
    "$goroot/misc/wasm/wasm_exec.js"; do
    if [[ -f "$candidate" ]]; then
        wasm_exec="$candidate"
        break
    fi
done
if [[ -z "$wasm_exec" ]]; then
    echo "could not find wasm_exec.js in GOROOT=$goroot" >&2
    exit 1
fi

echo "==> building torrent.wasm"
(
    cd "$repo_root"
    CGO_ENABLED=0 GOOS=js GOARCH=wasm go build \
        -ldflags="-s -w" \
        -trimpath \
        -tags "disable_libutp" \
        -o "$out_dir/torrent.wasm" \
        ./web/wasm
)

echo "==> copying wasm_exec.js"
install -m 0644 "$wasm_exec" "$out_dir/wasm_exec.js"

echo "==> built: $out_dir/torrent.wasm ($(du -h "$out_dir/torrent.wasm" | awk '{print $1}'))"
