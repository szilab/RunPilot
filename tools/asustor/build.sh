#!/bin/sh

set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
VERSION=${1:-${RUNPILOT_VERSION:-$(go run ./cmd/runpilot version | sed 's/^RunPilot //')}}
TMP=$(mktemp -d "${TMPDIR:-/tmp}/runpilot-asustor-build.XXXXXX")
trap 'rm -rf "$TMP"' EXIT HUP INT TERM

cd "$ROOT"
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
	-ldflags "-s -w -X github.com/szilab/RunPilot/internal/version.Version=$VERSION" \
	-o "$TMP/runpilot" ./cmd/runpilot
go run ./cmd/asustor-apkg -version "$VERSION" -binary "$TMP/runpilot" -source packaging/asustor -out dist
