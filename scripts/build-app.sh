#!/bin/sh
# Build bqbot.app for macOS with the MyGo version pinned in go.mod.
#
# Usage: scripts/build-app.sh [-a arm64|amd64|universal] [-s identity] [--dmg] [--open]
#   -a, --arch      target architecture (default: host architecture)
#   -s, --sign      codesign identity (default: mygo.json, or "-" for ad hoc)
#   --dmg           also create the disk image
#   --open          reveal the built app in Finder
set -eu

cd "$(dirname "$0")/.."

case "$(uname -m)" in
  arm64) arch=arm64 ;;
  x86_64) arch=amd64 ;;
  *) arch=arm64 ;;
esac
sign=""
dmg=0
reveal=0

usage() {
  sed -n '4,8p' "$0" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    -a | --arch)
      [ $# -ge 2 ] || { usage >&2; exit 2; }
      arch=$2
      shift 2
      ;;
    -s | --sign)
      [ $# -ge 2 ] || { usage >&2; exit 2; }
      sign=$2
      shift 2
      ;;
    --dmg) dmg=1; shift ;;
    --open) reveal=1; shift ;;
    -h | --help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$arch" in
  arm64 | amd64 | universal) ;;
  *) echo "unsupported architecture: $arch" >&2; exit 2 ;;
esac

if [ "$(uname -s)" != "Darwin" ]; then
  echo "building a signed .app requires macOS" >&2
  exit 1
fi

platform="darwin/$arch"
app="build/darwin-$arch/bqbot.app"

set -- build -platform "$platform"
[ "$dmg" -eq 1 ] || set -- "$@" -skip-dmg
[ -z "$sign" ] || set -- "$@" -sign "$sign"

echo "==> mygo $* ($(go version | awk '{print $3}'))"
# The buildCommand (scripts/prepare-cua.py) reads RELEASE_TARGETS for the driver.
RELEASE_TARGETS="$platform" go run github.com/egoist/mygo/cmd/mygo "$@"

if [ ! -d "$app" ]; then
  echo "expected app bundle not found: $app" >&2
  exit 1
fi

echo "==> verifying signature"
codesign --verify --strict --deep "$app"
driver="$app/Contents/Resources/cua-driver/cua-driver"
if [ -f "$driver" ]; then
  codesign --verify --strict "$driver"
else
  echo "warning: bundled cua-driver missing: $driver" >&2
fi

echo "==> built $app"
[ "$reveal" -eq 0 ] || open -R "$app"
