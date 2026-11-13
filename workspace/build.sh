#!/bin/sh
set -eu
cd "$(dirname "$0")"
arch=${TARGETARCH:-$(go env GOARCH)}
mkdir -p .build
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -o ".build/bridge-$arch" ./bridge
