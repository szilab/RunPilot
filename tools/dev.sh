#!/usr/bin/env bash
# Run a development RunPilot instance from this checkout with its data in
# ./data. Every plugin under plugins/ is installed from source before start.
#
#   tools/dev.sh            install plugins and run on 0.0.0.0:9070
#   tools/dev.sh --wasm     also rebuild plugin WASM backends (TinyGo)
#   PORT=9071 tools/dev.sh  use another port
#
# On a fresh ./data the administrator setup is open for five minutes after
# start. Plugins are enabled on first start only; later changes stay yours.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DATA_DIR="$ROOT/data"
PORT="${PORT:-9070}"
BIND="${BIND:-0.0.0.0}"
TINYGO_IMAGE="tinygo/tinygo:0.38.0"

REBUILD_WASM=0
for argument in "$@"; do
    case "$argument" in
        --wasm) REBUILD_WASM=1 ;;
        -h|--help) sed -n '2,11p' "$0"; exit 0 ;;
        *) echo "Unknown option: $argument" >&2; exit 2 ;;
    esac
done

cd "$ROOT"
mkdir -p "$DATA_DIR"

if [[ "$REBUILD_WASM" == 1 ]]; then
    if command -v tinygo >/dev/null 2>&1; then
        tinygo_build() { (cd "$1" && tinygo build -target=wasm-unknown -tags=runpilot_wasm -scheduler=none -gc=conservative -stack-size=64kb -no-debug -o plugin.wasm .); }
    elif command -v docker >/dev/null 2>&1; then
        tinygo_build() {
            docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOFLAGS=-mod=mod \
                -v "$ROOT":/src -v "$(go env GOMODCACHE)":/go/pkg/mod -e GOMODCACHE=/go/pkg/mod \
                -w "/src/${1#"$ROOT"/}" "$TINYGO_IMAGE" \
                tinygo build -target=wasm-unknown -tags=runpilot_wasm -scheduler=none -gc=conservative -stack-size=64kb -no-debug -o plugin.wasm .
        }
    else
        echo "TinyGo or Docker is required for --wasm" >&2
        exit 1
    fi
    for backend in "$ROOT"/plugins/*/backend; do
        [[ -f "$backend/generate.go" ]] || continue
        echo "Building $(basename "$(dirname "$backend")") WASM backend"
        tinygo_build "$backend"
    done
fi

FIRST_START=0
[[ -f "$DATA_DIR/runpilot.yaml" ]] || FIRST_START=1

PLUGIN_IDS=()
for manifest in "$ROOT"/plugins/*/plugin.yaml; do
    source_dir="$(dirname "$manifest")"
    "$ROOT/tools/test-plugin-local.sh" "$source_dir" "$DATA_DIR" >/dev/null
    manifest_json="$(go run ./cmd/plugin-build -manifest "$source_dir")"
    PLUGIN_IDS+=("$(jq -er '.id' <<< "$manifest_json")")
    echo "Installed $(jq -er '.id + " " + .version' <<< "$manifest_json")"
done

if [[ "$FIRST_START" == 1 ]]; then
    {
        echo "plugins:"
        for id in "${PLUGIN_IDS[@]}"; do
            echo "  ${id}:"
            echo "    enabled: true"
        done
    } > "$DATA_DIR/runpilot.yaml"
    echo "Enabled plugins for the first start: ${PLUGIN_IDS[*]}"
fi

# Keep the data-root locator inside the development directory so this
# instance never changes another installation's locator.
export RUNPILOT_DATA_ROOT_FILE="$DATA_DIR/.data-root"
echo "Starting RunPilot on http://$BIND:$PORT with data in $DATA_DIR"
exec go run ./cmd/runpilot run --data-dir "$DATA_DIR" --bind-address "$BIND" --port "$PORT"
