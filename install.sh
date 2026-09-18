#!/bin/sh
set -eu

repo="${RUNPILOT_REPO:-szilab/RunPilot}"
channel="${RUNPILOT_CHANNEL:-main}"
release_tag="${RUNPILOT_RELEASE_TAG:-}"
install_dir="${RUNPILOT_INSTALL_DIR:-$HOME/.local/bin}"
data_dir="${RUNPILOT_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/runpilot}"
start_service="${RUNPILOT_START_SERVICE:-1}"
skip_service="${RUNPILOT_SKIP_SERVICE:-0}"
port="${RUNPILOT_PORT:-}"
base_path="${RUNPILOT_BASE_PATH:-}"
assume_yes="${RUNPILOT_ASSUME_YES:-0}"
allow_service_restart="${RUNPILOT_ALLOW_SERVICE_RESTART:-0}"
service_unit="runpilot.service"
systemd_user_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"

case "$(uname -s)" in
	Linux) ;;
	*)
		echo "RunPilot installer currently supports Linux only." >&2
		exit 1
		;;
esac

case "$(uname -m)" in
	x86_64|amd64) arch="amd64" ;;
	*)
		echo "RunPilot installer currently supports Linux AMD64 only." >&2
		exit 1
		;;
esac

if [ -z "$release_tag" ]; then
	case "$channel" in
		main|stable) release_tag="main-latest" ;;
		develop|dev) release_tag="develop-latest" ;;
		*)
			echo "RUNPILOT_CHANNEL must be main or develop." >&2
			exit 1
			;;
	esac
fi

asset="runpilot-linux-$arch"
version_asset="$asset.version"
base_url="https://github.com/$repo/releases/download/$release_tag"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

has_tty=0
if (: < /dev/tty) 2>/dev/null && (: > /dev/tty) 2>/dev/null; then
	has_tty=1
fi

service_active=0
if [ "$skip_service" != "1" ] && command -v systemctl >/dev/null 2>&1 && systemctl --user is-active --quiet "$service_unit"; then
	service_active=1
fi

is_update=0
if [ -e "$install_dir/runpilot" ] || [ -e "$systemd_user_dir/$service_unit" ]; then
	is_update=1
fi

if [ "$is_update" = "1" ]; then
	operation="update"
else
	operation="install"
fi

echo "RunPilot Linux installer"
echo
echo "This will $operation RunPilot using these settings:"
echo "  Release:        $repo@$release_tag"
echo "  Binary asset:   $asset"
echo "  Install path:   $install_dir/runpilot"
echo "  Data directory: $data_dir"
if [ "$skip_service" = "1" ]; then
	echo "  User service:   skipped"
elif [ "$start_service" = "1" ]; then
	echo "  User service:   install/update and start"
else
	echo "  User service:   install/update without starting"
fi
if [ "$service_active" = "1" ]; then
	echo "  Active service: will be stopped before replacing the binary"
fi
if [ -n "$port" ]; then
	echo "  Port override:  $port"
fi
if [ -n "$base_path" ]; then
	echo "  Base path:      $base_path"
fi
echo

if [ "$assume_yes" = "1" ]; then
	echo "Proceeding without confirmation because RUNPILOT_ASSUME_YES=1."
else
	if [ "$has_tty" != "1" ]; then
		echo "Cannot ask for confirmation because no terminal is attached." >&2
		echo "Run this installer from an interactive shell, or set RUNPILOT_ASSUME_YES=1 for non-interactive use." >&2
		exit 1
	fi

	printf "Proceed with RunPilot %s? [y/N] " "$operation" > /dev/tty
	IFS= read -r answer < /dev/tty
	case "$answer" in
		y|Y|yes|YES) ;;
		*)
			echo "RunPilot $operation cancelled."
			exit 0
			;;
	esac
fi

if [ "$service_active" = "1" ] && [ "$has_tty" != "1" ] && [ "$allow_service_restart" != "1" ]; then
	echo "Refusing to stop the active RunPilot user service from a non-interactive run." >&2
	echo "Run interactively, set RUNPILOT_SKIP_SERVICE=1, or set RUNPILOT_ALLOW_SERVICE_RESTART=1." >&2
	exit 1
fi

download() {
	url="$1"
	output="$2"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$url" -o "$output"
	elif command -v wget >/dev/null 2>&1; then
		wget -q "$url" -O "$output"
	else
		echo "curl or wget is required to download RunPilot." >&2
		exit 1
	fi
}

download_optional() {
	url="$1"
	output="$2"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$url" -o "$output"
	elif command -v wget >/dev/null 2>&1; then
		wget -q "$url" -O "$output"
	else
		return 1
	fi
}

installed_version() {
	if [ -x "$install_dir/runpilot" ]; then
		"$install_dir/runpilot" version 2>/dev/null | sed -n 's/^RunPilot //p' | head -n 1
	fi
}

binary_tmp="$tmp_dir/$asset"
sum_tmp="$tmp_dir/$asset.sha256"
version_tmp="$tmp_dir/$version_asset"

latest_version=""
if download_optional "$base_url/$version_asset" "$version_tmp"; then
	latest_version="$(sed -n '1{s/[[:space:]]*$//;p;}' "$version_tmp")"
	current_version="$(installed_version)"
	if [ -n "$latest_version" ] && [ "$latest_version" = "$current_version" ]; then
		echo "RunPilot $current_version is already installed."
		exit 0
	fi
elif [ "$is_update" = "1" ]; then
	echo "Release version metadata is unavailable; checking the downloaded binary instead."
fi

echo "Downloading RunPilot $release_tag from $repo..."
download "$base_url/$asset" "$binary_tmp"
download "$base_url/$asset.sha256" "$sum_tmp"

if command -v sha256sum >/dev/null 2>&1; then
	expected_sum="$(awk '{print $1}' "$sum_tmp")"
	echo "$expected_sum  $binary_tmp" | sha256sum -c - >/dev/null
	echo "Checksum verified."
else
	echo "sha256sum is not available; skipping checksum verification." >&2
fi

if [ -z "$latest_version" ]; then
	latest_version="$("$binary_tmp" version 2>/dev/null | sed -n 's/^RunPilot //p' | head -n 1 || true)"
	current_version="$(installed_version)"
	if [ -n "$latest_version" ] && [ "$latest_version" = "$current_version" ]; then
		echo "RunPilot $current_version is already installed."
		exit 0
	fi
fi

mkdir -p "$install_dir" "$data_dir"

if [ "$service_active" = "1" ]; then
	echo "Stopping existing RunPilot user service for update..."
	systemctl --user stop "$service_unit"
fi

install -m 0755 "$binary_tmp" "$install_dir/runpilot"

echo "Installed $install_dir/runpilot"

if [ "$skip_service" = "1" ]; then
	echo "Skipping service installation because RUNPILOT_SKIP_SERVICE=1."
	exit 0
fi

set -- service install --data-dir "$data_dir"
if [ -n "$port" ]; then
	set -- "$@" --port "$port"
fi
if [ -n "$base_path" ]; then
	set -- "$@" --base-path "$base_path"
fi

"$install_dir/runpilot" "$@"

if [ "$start_service" = "1" ]; then
	"$install_dir/runpilot" service start
	echo "RunPilot service started."
else
	echo "RunPilot service installed. Start it with: $install_dir/runpilot service start"
fi

echo "Open http://127.0.0.1:9070 after the service starts."