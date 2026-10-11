#!/bin/sh

set -eu

APP_NAME=${APKG_PKG_NAME:-runpilot}
APP_DIR=${APKG_PKG_DIR:-${APKG_REPO_DIR:-/usr/local/AppCentral}/$APP_NAME}
BIN=$APP_DIR/bin/runpilot
DATA_DIR=/volume1/.@plugins/AppCentral/runpilot/data
PID_FILE=$DATA_DIR/runpilot.pid
LOG_FILE=$DATA_DIR/runpilot.log

owned_data_dir() {
	if [ -e "$DATA_DIR" ]; then
		[ ! -L "$DATA_DIR" ] && [ -d "$DATA_DIR" ] &&
			[ ! -L "$DATA_DIR/.runpilot-owned" ] && [ -f "$DATA_DIR/.runpilot-owned" ] || {
			echo "Refusing to use unmarked data directory: $DATA_DIR" >&2
			return 1
		}
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
	owned_data_dir
	if is_running; then
		echo "RunPilot is already running"
		return 0
	fi
	if [ -e "$PID_FILE" ]; then
		rm -f "$PID_FILE"
	fi
	: >> "$LOG_FILE"
	chown admin:administrators "$LOG_FILE"
	chmod 600 "$LOG_FILE"
	(
		umask 077
		start-stop-daemon -S -b -m -p "$PID_FILE" -x "$BIN" \
			-c admin:administrators -- run --data-dir "$DATA_DIR" \
			--bind-address 0.0.0.0 --port 9070 >> "$LOG_FILE" 2>&1
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
