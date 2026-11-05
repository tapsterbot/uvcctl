#!/bin/sh
# Builds release binaries into dist/. Run on a Mac: the macOS build needs
# cgo (IOKit); Linux and Windows builds are pure Go and cross-compile.
set -eu
cd "$(dirname "$0")"
# Set VERSION=1.2.3 to stamp a release; otherwise main.go's version is used.
LDFLAGS="-s -w"
[ -n "${VERSION:-}" ] && LDFLAGS="$LDFLAGS -X main.version=$VERSION"
rm -rf dist && mkdir -p dist

if [ "$(uname)" = Darwin ]; then
  CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -ldflags "$LDFLAGS" -o dist/uvcctl-arm64 .
  CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/uvcctl-amd64 .
  mkdir -p dist/uvcctl-macos
  lipo -create -output dist/uvcctl-macos/uvcctl dist/uvcctl-arm64 dist/uvcctl-amd64
  rm dist/uvcctl-arm64 dist/uvcctl-amd64
fi

for t in linux/amd64 linux/arm64 linux/arm windows/amd64 windows/arm64; do
  os=${t%/*} arch=${t#*/} ext=
  [ "$os" = windows ] && ext=.exe
  dir="dist/uvcctl-$os-$arch"
  mkdir -p "$dir"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOARM=7 go build -ldflags "$LDFLAGS" -o "$dir/uvcctl$ext" .
done

for d in dist/*/; do
  name=$(basename "$d")
  cp README.md "$d"
  (cd dist && zip -qr "$name.zip" "$name")
done
ls -lh dist/*.zip
