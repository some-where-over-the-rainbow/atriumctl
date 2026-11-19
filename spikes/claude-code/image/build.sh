#!/usr/bin/env bash
# Build the harness image with the locally installed Claude Code version.
set -euo pipefail
cd "$(dirname "$0")"
ctx=$(mktemp -d); trap 'rm -rf "$ctx"' EXIT
bin=$(readlink -f "$(command -v claude)")
ver=$("$bin" --version | awk '{print $1}')
cp "$bin" "$ctx/claude"
CGO_ENABLED=0 go build -C .. -o "$ctx/atriumctl-shim" ./shim
cp Containerfile "$ctx/"
podman build --build-arg CLAUDE_VERSION="$ver" -t "atriumctl-harness:$ver" -t atriumctl-harness:spike "$ctx"
echo "built atriumctl-harness:$ver"
