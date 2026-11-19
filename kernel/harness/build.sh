#!/usr/bin/env bash
# Build the harness image atriumctl-harness:<claude version> from the locally
# installed Claude Code and kernel/cmd/atriumctl-shim.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
ctx=$(mktemp -d); trap 'rm -rf "$ctx"' EXIT
bin=$(readlink -f "$(command -v claude)")
ver=$("$bin" --version | awk '{print $1}')
cp "$bin" "$ctx/claude"
CGO_ENABLED=0 go build -C "$here/.." -trimpath -o "$ctx/atriumctl-shim" ./cmd/atriumctl-shim
cp "$here/Containerfile" "$ctx/"
podman build --build-arg CLAUDE_VERSION="$ver" -t "atriumctl-harness:$ver" "$ctx"
echo "atriumctl-harness:$ver"
