#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

usage() {
    echo "Usage: $0 <plugin-source-dir> [data-dir]" >&2
    echo "Example: $0 plugins/tasks ./data" >&2
}

if [[ $# -lt 1 || $# -gt 2 ]]; then
    usage
    exit 2
fi

for command in go jq unzip; do
    if ! command -v "$command" >/dev/null 2>&1; then
        echo "Required command not found: $command" >&2
        exit 1
    fi
done

PLUGIN_SOURCE="$1"
if [[ "$PLUGIN_SOURCE" != /* ]]; then
    PLUGIN_SOURCE="$ROOT/$PLUGIN_SOURCE"
fi
if [[ ! -f "$PLUGIN_SOURCE/plugin.yaml" ]]; then
    echo "Plugin manifest not found: $PLUGIN_SOURCE/plugin.yaml" >&2
    exit 1
fi
PLUGIN_SOURCE="$(cd "$PLUGIN_SOURCE" && pwd)"

DATA_DIR="${2:-$ROOT/data}"
if [[ "$DATA_DIR" != /* ]]; then
    DATA_DIR="$PWD/$DATA_DIR"
fi
mkdir -p "$DATA_DIR"
DATA_DIR="$(cd "$DATA_DIR" && pwd)"

MANIFEST="$(cd "$ROOT" && go run ./cmd/plugin-build -manifest "$PLUGIN_SOURCE")"
PLUGIN_ID="$(jq -er '.id' <<< "$MANIFEST")"
VERSION="$(jq -er '.version' <<< "$MANIFEST")"

TEMP_DIR="$(mktemp -d)"
STAGE=""
BACKUP=""
DEST=""
cleanup() {
    if [[ -n "$STAGE" && -d "$STAGE" ]]; then
        rm -rf -- "$STAGE"
    fi
    if [[ -n "$BACKUP" && -d "$BACKUP" ]]; then
        if [[ -n "$DEST" && ! -e "$DEST" ]]; then
            mv -- "$BACKUP" "$DEST" || true
        else
            rm -rf -- "$BACKUP"
        fi
    fi
    rm -rf -- "$TEMP_DIR"
}
trap cleanup EXIT

ARCHIVE="$TEMP_DIR/$PLUGIN_ID-$VERSION.rpplugin"
(cd "$ROOT" && go run ./cmd/plugin-build -out "$ARCHIVE" "$PLUGIN_SOURCE")

PLUGIN_ROOT="$DATA_DIR/plugins/$PLUGIN_ID"
RELEASE_ROOT="$PLUGIN_ROOT/releases"
DEST="$RELEASE_ROOT/$VERSION"
mkdir -p "$RELEASE_ROOT"
STAGE="$(mktemp -d "$RELEASE_ROOT/.install.XXXXXX")"
unzip -q "$ARCHIVE" -d "$STAGE"
if [[ -e "$DEST" || -L "$DEST" ]]; then
    BACKUP="$RELEASE_ROOT/.backup.$PLUGIN_ID.$VERSION.$$"
    mv -- "$DEST" "$BACKUP"
fi
if ! mv -- "$STAGE" "$DEST"; then
    if [[ -n "$BACKUP" && -e "$BACKUP" ]]; then
        mv -- "$BACKUP" "$DEST"
        BACKUP=""
    fi
    echo "Could not install package into $DEST" >&2
    exit 1
fi
STAGE=""
if [[ -n "$BACKUP" ]]; then
    rm -rf -- "$BACKUP"
    BACKUP=""
fi

echo "Installed/updated $PLUGIN_ID $VERSION in $DEST"
echo "Stop any running RunPilot process, then restart it to load the updated package."
echo "Start RunPilot with: go run ./cmd/runpilot run --data-dir $DATA_DIR"
echo "If it is not already enabled, enable $PLUGIN_ID in Settings > Plugins before restarting."
