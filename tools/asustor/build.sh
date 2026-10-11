#!/bin/sh

set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
APK_VERSION=${1:-${RUNPILOT_APKG_VERSION:-}}
CORE_VERSION=${2:-${RUNPILOT_CORE_VERSION:-$(go run ./cmd/runpilot version | sed 's/^RunPilot //')}}
if [ -z "$APK_VERSION" ]; then
	echo "Usage: $0 <app-central-package-version> [runpilot-core-version]" >&2
	exit 2
fi
TMP=$(mktemp -d "${TMPDIR:-/tmp}/runpilot-asustor-build.XXXXXX")
trap 'rm -rf "$TMP"' EXIT HUP INT TERM

cd "$ROOT"
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
	-ldflags "-s -w -X github.com/szilab/RunPilot/internal/version.Version=$CORE_VERSION -X github.com/szilab/RunPilot/internal/version.Deployment=asustor-appcentral" \
	-o "$TMP/runpilot" ./cmd/runpilot
go run ./cmd/asustor-apkg -version "$APK_VERSION" -binary "$TMP/runpilot" -source packaging/asustor -out dist
