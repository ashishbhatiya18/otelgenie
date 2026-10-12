#!/usr/bin/env bash
# Builds the otelgenie program, with the page inside it, for every platform,
# and packs each one for release.
#
#   scripts/build-cli.sh [VERSION]
#
# Written to dist/: the programs, one archive per platform holding only the
# program, and checksums.txt. Needs Go and Node.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
version="${1:-dev}"
version="${version#v}"
cd "$root"

# The page, built as plain files, goes inside the program.
(cd web && npm run build)
rm -rf cmd/otelgenie/ui
cp -R web/out cmd/otelgenie/ui

rm -rf dist
mkdir -p dist
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os="${target%/*}"
  arch="${target#*/}"
  name="otelgenie-$os-$arch"
  bin=otelgenie
  [ "$os" = windows ] && bin=otelgenie.exe
  stage="dist/stage/$name"
  mkdir -p "$stage"
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$stage/$bin" ./cmd/otelgenie
  # The archive holds the program and nothing else.
  if [ "$os" = windows ]; then
    (cd "$stage" && zip -q "../../$name.zip" "$bin")
    echo "dist/$name.zip"
  else
    tar -C "$stage" -czf "dist/$name.tar.gz" "$bin"
    echo "dist/$name.tar.gz"
  fi
  cp "$stage/$bin" "dist/$name${bin#otelgenie}"
done
rm -rf dist/stage
(cd dist && if command -v sha256sum >/dev/null 2>&1; then sha256sum ./*.tar.gz ./*.zip; else shasum -a 256 ./*.tar.gz ./*.zip; fi | sed 's| \./| |' >checksums.txt)
echo "dist/checksums.txt"
