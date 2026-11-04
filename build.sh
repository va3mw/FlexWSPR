#!/bin/sh
# Build FlexWSPR into dist/:
#   flexwspr-<os>-<arch>      headless program with its web page
#                             (Debian x86-64, Raspberry Pi 64/32-bit, Windows)
#   flexwspr-gui-windows-amd64.exe   desktop app for Windows
#   flexwspr-gui-linux-<arch>        desktop app for this Linux machine, when
#                                    GTK/WebKit dev packages are installed
# Usage: ./build.sh [version]
set -e
cd "$(dirname "$0")"
VERSION=${1:-0.5.0}
LDFLAGS="-s -w -X main.version=$VERSION"
mkdir -p dist
for target in linux/amd64 linux/arm64 linux/arm windows/amd64; do
	os=${target%/*}
	arch=${target#*/}
	out=dist/flexwspr-$os-$arch
	[ "$os" = windows ] && out=$out.exe
	echo "building $out"
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOARM=7 \
		go build -trimpath -ldflags "$LDFLAGS" -o "$out" .
done

echo "building dist/flexwspr-gui-windows-amd64.exe"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
	go build -trimpath -tags desktop,production -ldflags "$LDFLAGS -H windowsgui" \
	-o dist/flexwspr-gui-windows-amd64.exe ./cmd/flexwspr-gui

# The Linux desktop app links GTK and WebKit, so it is built only on Linux,
# for this machine's architecture.
if [ "$(go env GOHOSTOS)" = linux ]; then
	if pkg-config --exists webkit2gtk-4.1 gtk+-3.0 2>/dev/null; then
		out=dist/flexwspr-gui-linux-$(go env GOHOSTARCH)
		echo "building $out"
		CGO_ENABLED=1 go build -trimpath -tags desktop,production,webkit2_41 -ldflags "$LDFLAGS" -o "$out" ./cmd/flexwspr-gui
	else
		echo "skipping the Linux desktop app: sudo apt install build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev"
	fi
fi
cp flexwspr.example.yaml flexwspr.service dist/
