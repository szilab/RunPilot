#!/bin/sh
# RunPilot container entrypoint.
#
# Started as root (the default), it prepares the container and then runs
# RunPilot as the unprivileged "runpilot" user:
#   - PUID/PGID (default 1000) set that user's IDs to match host file owners;
#   - the data directory is handed to that user when it is owned by someone else;
#   - a mounted Docker socket's group is granted so the Docker plugin works;
#   - a warning is logged when the data directory is not mounted at the same
#     path on the host, which Docker Compose relative bind mounts require;
#   - RUNPILOT_EXTRA_PACKAGES (for example "ffmpeg git") is installed with apk.
# PUID=0 keeps RunPilot running as root. Started with "docker run --user",
# the entrypoint changes nothing and runs RunPilot directly.
set -eu

data_dir="${RUNPILOT_DATA_DIR:-/var/lib/runpilot}"

log() { echo "runpilot-entrypoint: $*" >&2; }

numeric() {
    case "$1" in
        '' | *[!0-9]*) return 1 ;;
        *) return 0 ;;
    esac
}

docker_socket() {
    case "${DOCKER_HOST:-}" in
        '') echo /var/run/docker.sock ;;
        unix://*) echo "${DOCKER_HOST#unix://}" ;;
        *) echo "" ;;
    esac
}

# grant_socket_group adds the runpilot user to the group owning the Docker
# socket, creating a group for that GID when the image has none.
grant_socket_group() {
    socket="$1"
    gid="$(stat -c %g "$socket")"
    group="$(getent group "$gid" | cut -d: -f1 || true)"
    if [ -z "$group" ]; then
        group=docker-host
        while getent group "$group" >/dev/null; do group="${group}x"; done
        addgroup -g "$gid" "$group"
    fi
    if ! id -nG runpilot | tr ' ' '\n' | grep -qx "$group"; then
        addgroup runpilot "$group"
    fi
    log "Docker socket access granted through group $group ($gid)"
}

# check_data_mount warns when Docker reports the data directory mounted from a
# different host path, or not mounted at all. Best effort: it needs the socket.
check_data_mount() {
    command -v docker >/dev/null 2>&1 || return 0
    self="$(hostname)"
    format="{{range .Mounts}}{{if eq .Destination \"$data_dir\"}}{{.Type}} {{.Source}}{{end}}{{end}}"
    mount="$(timeout 5 docker inspect --format "$format" "$self" 2>/dev/null)" || return 0
    if [ -z "$mount" ]; then
        log "WARNING: $data_dir is not a mounted volume; RunPilot data is lost when the container is removed"
        return 0
    fi
    type="${mount%% *}"
    source="${mount#* }"
    if [ "$type" != bind ] || [ "$source" != "$data_dir" ]; then
        log "WARNING: $data_dir is mounted from $type $source; Docker Compose projects with relative bind mounts need the same path on the host (-v $data_dir:$data_dir)"
    fi
}

# install_extra_packages installs RUNPILOT_EXTRA_PACKAGES (space- or
# comma-separated Alpine package names, optionally with version constraints).
# Packages live in the container layer: a restarted container keeps them, a
# recreated one installs them again. A failed download only logs a warning so
# RunPilot still starts.
install_extra_packages() {
    requested="$(printf '%s' "${RUNPILOT_EXTRA_PACKAGES:-}" | tr ',' ' ')"
    set -f
    # shellcheck disable=SC2086 # word splitting is intended; globbing is off
    set -- $requested
    set +f
    [ "$#" -gt 0 ] || return 0
    for package in "$@"; do
        case "$package" in
            -* | *[!A-Za-z0-9._+:=\<\>~@-]*)
                log "RUNPILOT_EXTRA_PACKAGES: invalid package name \"$package\""
                exit 2
                ;;
        esac
    done
    if apk info -e "$@" >/dev/null 2>&1; then
        return 0
    fi
    log "installing extra packages: $*"
    if apk add --no-cache "$@" >&2; then
        return 0
    fi
    # apk also fails when only a package script failed; check what is usable.
    if apk info -e "$@" >/dev/null 2>&1; then
        log "WARNING: extra packages installed ($*), but some package scripts failed (see the apk output above)"
    else
        log "WARNING: could not install extra packages ($*); starting without them"
    fi
}

if [ "$(id -u)" != 0 ]; then
    if [ -n "${RUNPILOT_EXTRA_PACKAGES:-}" ]; then
        log "WARNING: RUNPILOT_EXTRA_PACKAGES needs the container to start as root; skipped"
    fi
    exec runpilot "$@"
fi

install_extra_packages

puid="${PUID:-1000}"
pgid="${PGID:-1000}"
if ! numeric "$puid" || ! numeric "$pgid"; then
    log "PUID and PGID must be numeric"
    exit 2
fi

mkdir -p "$data_dir"
socket="$(docker_socket)"
if [ -n "$socket" ] && [ -S "$socket" ]; then
    check_data_mount
fi

if [ "$puid" = 0 ]; then
    log "PUID=0: running RunPilot as root"
    exec runpilot "$@"
fi

if [ "$(id -g runpilot)" != "$pgid" ]; then
    groupmod -o -g "$pgid" runpilot
fi
if [ "$(id -u runpilot)" != "$puid" ]; then
    usermod -o -u "$puid" runpilot
fi
if [ "$(stat -c %u "$data_dir")" != "$puid" ]; then
    log "assigning $data_dir to $puid:$pgid"
    chown -R "$puid:$pgid" "$data_dir"
fi
chown "$puid:$pgid" /home/runpilot

if [ -n "$socket" ] && [ -S "$socket" ]; then
    grant_socket_group "$socket"
fi

export HOME=/home/runpilot USER=runpilot LOGNAME=runpilot
exec su-exec runpilot runpilot "$@"
