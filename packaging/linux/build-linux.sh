#!/bin/sh
# Builds the Linux release: dist/ShareMile-linux-amd64.tar.gz, plus a copy in
# builds/vX.Y.Z (never overwritten). Run on the oldest distribution you want
# to support (it sets the glibc floor); Ubuntu 24.04 gives glibc 2.34.
#   packaging/linux/build-linux.sh 1.8.0
# Needs: Go, gcc, pkg-config, libgtk-3-dev, libwebkit2gtk-4.1-dev
set -eu
V=${1:?version, e.g. 1.8.0}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
cd "$ROOT"
export GOFLAGS=-buildvcs=false CGO_ENABLED=1
ARCH=$(go env GOARCH)
STAGE=$(mktemp -d)
mkdir -p "$STAGE/ShareMile" dist
go build -trimpath -ldflags "-s -w -X main.appVersion=$V" -o "$STAGE/ShareMile/sharemile" .
cp packaging/linux/install.sh packaging/linux/uninstall.sh packaging/linux/README.txt LICENSE "$STAGE/ShareMile/"
cp assets/icon.png "$STAGE/ShareMile/sharemile.png"
chmod 644 "$STAGE/ShareMile/"*
chmod 755 "$STAGE/ShareMile" "$STAGE/ShareMile/sharemile" "$STAGE/ShareMile/install.sh" "$STAGE/ShareMile/uninstall.sh"
OUT="dist/ShareMile-linux-$ARCH.tar.gz"
tar -C "$STAGE" --owner=0 --group=0 -czf "$OUT" ShareMile
rm -rf "$STAGE"
if [ "${SKIP_BUILDS:-0}" = 1 ]; then :
elif [ -d "builds/v$V" ] && [ -f "builds/v$V/ShareMile-linux-$ARCH.tar.gz" ]; then
  echo "builds/v$V already has a Linux build; left untouched."
else
  mkdir -p "builds/v$V" && cp "$OUT" "builds/v$V/"
  echo "Saved build to builds/v$V"
fi
ls -l "$OUT"
