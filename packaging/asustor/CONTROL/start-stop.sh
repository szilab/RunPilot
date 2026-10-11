#!/bin/sh

set -eu

APP_NAME=${APKG_PKG_NAME:-runpilot}
APP_DIR=${APKG_PKG_DIR:-${APKG_REPO_DIR:-/usr/local/AppCentral}/$APP_NAME}
BIN=$APP_DIR/bin/runpilot
META_DIR=/volume1/.@plugins/AppCentral/runpilot
DEFAULT_DATA_DIR=$META_DIR/data
DATA_ROOT_FILE=$META_DIR/data-root
DATA_DIR=$DEFAULT_DATA_DIR
PID_FILE=$META_DIR/runpilot.pid
LOG_FILE=$META_DIR/runpilot.log

ensure_meta_dir() {
	[ ! -L "$META_DIR" ] || { echo "Refusing symbolic link lifecycle directory: $META_DIR" >&2; return 1; }
	if [ ! -d "$META_DIR" ]; then
		mkdir -m 700 -p "$META_DIR"
		chown admin:administrators "$META_DIR"
		chmod 700 "$META_DIR"
	fi
}

load_data_root() {
	[ ! -L "$DATA_ROOT_FILE" ] || { echo "Refusing symbolic link data-root locator: $DATA_ROOT_FILE" >&2; return 1; }
	if [ -f "$DATA_ROOT_FILE" ]; then
		DATA_DIR=$(cat "$DATA_ROOT_FILE")
		case "$DATA_DIR" in /*) ;; *) echo "Data-root locator must contain an absolute path" >&2; return 1 ;; esac
	fi
}

initialize_data_root_locator() {
	if [ ! -e "$DATA_ROOT_FILE" ]; then
		umask 077
		printf '%s\n' "$DEFAULT_DATA_DIR" > "$DATA_ROOT_FILE"
		chown admin:administrators "$DATA_ROOT_FILE"
		chmod 600 "$DATA_ROOT_FILE"
	fi
}

owned_data_dir() {
	if [ -e "$DATA_DIR" ]; then
		[ ! -L "$DATA_DIR" ] && [ -d "$DATA_DIR" ] &&
			[ ! -L "$DATA_DIR/.runpilot-owned" ] && [ -f "$DATA_DIR/.runpilot-owned" ] || {
			echo "Refusing to use unmarked data directory: $DATA_DIR" >&2
			return 1
		}
		return 0
	else
		mkdir -m 700 "$DATA_DIR"
		: > "$DATA_DIR/.runpilot-owned"
		chown admin:administrators "$DATA_DIR/.runpilot-owned"
		chown admin:administrators "$DATA_DIR"
	fi
	chown admin:administrators "$DATA_DIR"
	chmod 700 "$DATA_DIR"
	chown admin:administrators "$DATA_DIR/.runpilot-owned"
	chmod 600 "$DATA_DIR/.runpilot-owned"
}

is_running() {
	[ -x "$BIN" ] || return 1
	start-stop-daemon -K -t -q -p "$PID_FILE" -x "$BIN" -u admin
}

start_app() {
	[ -x "$BIN" ] || { echo "RunPilot executable not found: $BIN" >&2; return 1; }
	ensure_meta_dir
	load_data_root
	if [ "$DATA_DIR" = "$DEFAULT_DATA_DIR" ]; then
		owned_data_dir
	else
		[ ! -L "$DATA_DIR" ] && [ -d "$DATA_DIR" ] && [ -r "$DATA_DIR" ] && [ -w "$DATA_DIR" ] || {
			echo "Selected data directory is unavailable or not writable by admin: $DATA_DIR" >&2
			return 1
		}
	fi
	initialize_data_root_locator
	if is_running; then
		echo "RunPilot is already running"
		return 0
	fi
	if [ -e "$PID_FILE" ]; then
		[ ! -L "$PID_FILE" ] || { echo "Refusing symbolic link PID file: $PID_FILE" >&2; return 1; }
		rm -f "$PID_FILE"
	fi
	[ ! -L "$LOG_FILE" ] || { echo "Refusing symbolic link log file: $LOG_FILE" >&2; return 1; }
	: >> "$LOG_FILE"
	chown admin:administrators "$LOG_FILE"
	chmod 600 "$LOG_FILE"
	(
		umask 077
		RUNPILOT_DATA_ROOT_FILE=$DATA_ROOT_FILE
		export RUNPILOT_DATA_ROOT_FILE
		start-stop-daemon -S -b -m -p "$PID_FILE" -x "$BIN" \
			-c admin:administrators -- run --bind-address 0.0.0.0 --port 9070 >> "$LOG_FILE" 2>&1
	)
}

stop_app() {
	if ! is_running; then
		[ ! -e "$PID_FILE" ] || rm -f "$PID_FILE"
		return 0
	fi
	start-stop-daemon -K -p "$PID_FILE" -x "$BIN" -u admin -s TERM -o
	i=0
	while is_running && [ "$i" -lt 15 ]; do
		sleep 1
		i=$((i + 1))
	done
	if is_running; then
		start-stop-daemon -K -p "$PID_FILE" -x "$BIN" -u admin -s KILL -o
	fi
	i=0
	while is_running && [ "$i" -lt 5 ]; do
		sleep 1
		i=$((i + 1))
	done
	if is_running; then
		echo "RunPilot did not stop" >&2
		return 1
	fi
	[ ! -e "$PID_FILE" ] || rm -f "$PID_FILE"
}

case "${1:-}" in
	start) start_app ;;
	stop) stop_app ;;
	restart) stop_app; start_app ;;
	status)
		if is_running; then echo "RunPilot is running"; else echo "RunPilot is stopped"; exit 1; fi
		;;
	*) echo "Usage: $0 {start|stop|restart|status}" >&2; exit 2 ;;
esac
