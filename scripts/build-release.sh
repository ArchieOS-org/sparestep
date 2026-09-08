#!/bin/sh
set -eu
version=${1:-0.3.0}
mkdir -p dist
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w -X github.com/ArchieOS-org/sparestep/internal/app.Version=$version" -o "dist/sparestep-linux-$arch" .
done
cp install.sh dist/install.sh
cp LICENSE dist/LICENSE
(cd dist && sha256sum sparestep-linux-amd64 sparestep-linux-arm64 install.sh LICENSE > SHA256SUMS)
