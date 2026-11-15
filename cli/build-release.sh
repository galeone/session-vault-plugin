#!/usr/bin/env bash
# Cross-compiles the session-vault client for every supported platform into
# dist/, ready to attach to a GitHub release.
#
# Usage: ./build-release.sh <version>   (e.g. ./build-release.sh 0.2.0)
set -euo pipefail
cd "$(dirname "$0")"

version="${1:?usage: build-release.sh <version>}"
mkdir -p dist

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os="${target%/*}"
  arch="${target#*/}"
  ext=""
  [[ "$os" == windows ]] && ext=".exe"
  out="dist/session-vault_${os}_${arch}${ext}"
  echo "building $out"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$version" -o "$out" .
done

(cd dist && sha256sum session-vault_*) >dist/checksums.txt
echo "done; artifacts in dist/"
