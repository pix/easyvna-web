#!/usr/bin/env bash
# Build release archives into dist/: Windows zips, a macOS tar.gz and
# SHA256SUMS. Used by the release workflow and for local releases.
#   scripts/dist.sh v0.1.0
set -euo pipefail
VERSION="${1:?usage: scripts/dist.sh vX.Y.Z}"
cd "$(dirname "$0")/.."
rm -rf dist
mkdir -p dist

targets=(windows/amd64 windows/arm64 darwin/arm64)
for t in "${targets[@]}"; do
  goos=${t%/*}
  goarch=${t#*/}
  name="easyvna-web-${VERSION}-${goos}-${goarch}"
  dir="dist/${name}"
  exe=easyvna-web
  [ "$goos" = windows ] && exe=easyvna-web.exe
  mkdir -p "$dir"
  CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" -o "${dir}/${exe}" .
  cp README.md LICENSE "$dir/"
  if [ "$goos" = windows ]; then
    (cd dist && zip -qr "${name}.zip" "$name")
  else
    tar -C dist -czf "dist/${name}.tar.gz" "$name"
  fi
  rm -rf "$dir"
done
(cd dist && sha256sum ./*.zip ./*.tar.gz | sed 's# \./# #' > SHA256SUMS)
ls -l dist
