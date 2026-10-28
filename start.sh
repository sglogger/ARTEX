#!/bin/sh
# ARTEX supervisor startup script (Linux / macOS / Docker ENTRYPOINT)
#
# Usage:
#   ./start.sh                       run in the foreground (Ctrl-C to stop)
#   nohup ./start.sh >artex.log 2>&1 &   keep running in the background
#   ./start.sh -addr :9000           extra arguments are passed through to artex unchanged
#
# It does one thing only: run artex, and after the process exits decide from the exit code whether to relaunch it.
#
#   0      stopped normally by the user  → leave the loop
#   75     restart requested by artex    → rerun immediately (the UI's "one-click update" or "rollback" was used)
#   other  crash                         → rerun after a backoff (1→2→4… up to 60 seconds)
#
# Downloading, SHA256 verification and swapping are deliberately not done here: that logic would need two implementations in sh and bat,
# and it is exactly the step that must not fail. Once a binary that cannot run is swapped in, this script will faithfully
# relaunch it forever and the user has to fix the machine by hand. So verification/swapping stays entirely in Go (the selfupdate package),
# done by artex itself at startup, and the script stays dumb.
set -u

cd "$(dirname "$0")" || exit 1

BIN=./artex
[ -x "$BIN" ] || { echo "[artex] executable not found: $BIN" >&2; exit 1; }

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# Forward stop signals to the artex process.
#
# This is required under Docker: docker stop only sends SIGTERM to PID 1 (this script)
# and not to child processes. Without forwarding, artex never receives the signal and cannot shut down gracefully; after 10 seconds it is
# hard-killed with SIGKILL and running tasks are cut off mid-way.
forward() {
	stopping=1
	if [ "$child" -ne 0 ]; then
		kill -TERM "$child" 2>/dev/null || true
	fi
}
trap forward INT TERM

delay=1
while :; do
	"$BIN" "$@" &
	child=$!

	# A signal interrupts wait and makes it return >128. The child is actually still shutting down gracefully,
	# so wait must be called again to get its real exit code.
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[artex] stopped"
		exit 0
	fi

	case "$code" in
		0)
			echo "[artex] exited normally"
			exit 0
			;;
		"$RESTART_CODE")
			# Update/rollback is ready: after the rerun artex completes the swap at startup (see selfupdate.Bootstrap).
			echo "[artex] restart requested (applying new version)…"
			delay=1
			;;
		*)
			echo "[artex] exited abnormally (code=$code), restarting in ${delay}s" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done
