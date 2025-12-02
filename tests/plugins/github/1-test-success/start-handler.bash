#!/usr/bin/env bash
set -eo pipefail
SCRIPT_NAME="$(basename "${BASH_SOURCE[0]}")"
echo "==========================================="
echo "START ${SCRIPT_NAME}"
echo "==========================================="
# Source the helper functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/helpers.bash"

# Flag for signal handling
TERMINATE_REQUESTED=false

# Called explicitly via --clean flag - sends SIGINT
clean_and_exit() {
    echo "]] Cleaning up handler..."
    clean_anklet "handler"
    exit 0
}

# Signal handler - set flag for main loop to handle
handle_signal() {
    echo ""
    echo "[DEBUG] start-handler.bash] Signal received! Setting TERMINATE_REQUESTED=true"
    echo "[DEBUG] start-handler.bash] Current PID: $$"
    echo "[DEBUG] start-handler.bash] ANKLET_PID: ${ANKLET_PID}"
    TERMINATE_REQUESTED=true
}

# Cleanup function - called on exit
cleanup_handler() {
    echo "[DEBUG] start-handler.bash] cleanup_handler() called"
    echo "]] Cleaning up handler..."
    if [[ -n "${ANKLET_PID}" ]] && ps -p "${ANKLET_PID}" > /dev/null 2>&1; then
        echo "]] Sending SIGINT to anklet (PID: ${ANKLET_PID})..."
        kill -INT "${ANKLET_PID}" 2>/dev/null || true
        # Wait for graceful exit (up to 5 seconds)
        WAIT_COUNT=0
        while ps -p "${ANKLET_PID}" > /dev/null 2>&1 && [[ ${WAIT_COUNT} -lt 5 ]]; do
            sleep 1
            WAIT_COUNT=$((WAIT_COUNT + 1))
            echo "]] Waiting for anklet to exit... (${WAIT_COUNT}s)"
        done
        if ps -p "${ANKLET_PID}" > /dev/null 2>&1; then
            echo "]] Anklet didn't exit gracefully after 5s, sending SIGKILL..."
            kill -9 "${ANKLET_PID}" 2>/dev/null || true
        else
            echo "]] Anklet exited gracefully"
        fi
    fi
    rm -f /tmp/anklet-pids /tmp/anklet-handler-ready
}

trap handle_signal SIGTERM SIGINT
trap cleanup_handler EXIT

if [[ ${CLEAN_HANDLER:-false} == true || "$1" == "--clean" ]]; then
    clean_and_exit
fi

clean_anklet "handler"

echo "]] Starting handler (foreground, attached)..."
export LOG_LEVEL=${LOG_LEVEL:-debug}
echo "LOG_LEVEL: $LOG_LEVEL"

# Run anklet in the foreground and stay attached to it
# This keeps the SSH session alive which works around a macOS kernel bug where
# orphaned Go processes lose network connectivity to local IPs
# See: https://github.com/NorseGaud/go-macos-redis-client-network-bug-repro
/tmp/anklet &> /tmp/anklet.log &
ANKLET_PID=$!
echo "${ANKLET_PID}" > /tmp/anklet-pids
echo "]] Anklet started with PID: ${ANKLET_PID}"

# Wait 30 seconds to ensure anklet initializes properly
echo "]] Waiting 30 seconds for anklet to initialize..."
sleep 30

# Check that it's still running after initialization period
if ! ps -p "${ANKLET_PID}" > /dev/null 2>&1; then
    echo "ERROR: Anklet process died during startup"
    echo "]] Last 100 lines of anklet.log:"
    tail -100 /tmp/anklet.log
    exit 1
fi
echo "]] Anklet is running after 30 second initialization period"

# Create a ready file to signal that handler started successfully
touch /tmp/anklet-handler-ready
echo "]] Created ready file: /tmp/anklet-handler-ready"

# Stay attached - poll for anklet process to exit
# This keeps the script (and SSH session) alive
# Check TERMINATE_REQUESTED flag which is set by signal handler
echo "]] Waiting for anklet process (PID: ${ANKLET_PID})..."
echo "[DEBUG] start-handler.bash] My PID: $$, ANKLET_PID: ${ANKLET_PID}"
POLL_COUNT=0
while [[ "${TERMINATE_REQUESTED}" != "true" ]] && ps -p "${ANKLET_PID}" > /dev/null 2>&1; do
    sleep 1
    POLL_COUNT=$((POLL_COUNT + 1))
    if [[ $((POLL_COUNT % 30)) -eq 0 ]]; then
        echo "[DEBUG] start-handler.bash] Still polling... TERMINATE_REQUESTED=${TERMINATE_REQUESTED}, POLL_COUNT=${POLL_COUNT}"
    fi
done

echo "[DEBUG] start-handler.bash] Exited polling loop. TERMINATE_REQUESTED=${TERMINATE_REQUESTED}"

if [[ "${TERMINATE_REQUESTED}" == "true" ]]; then
    echo "]] Exiting due to signal..."
fi

# Remove ready file on exit
rm -f /tmp/anklet-handler-ready

echo "]] Anklet exited"
echo "==========================================="
echo "END ${SCRIPT_NAME}"
echo "==========================================="

# ----- COMMENTED OUT: Original code using nohup/disown -----
# This was a workaround attempt, but due to a macOS kernel bug, orphaned Go processes
# lose network connectivity to local IPs (like Redis at 10.8.100.100).
# The workaround is to keep the SSH session alive by staying attached to the process.
# See: https://github.com/NorseGaud/go-macos-redis-client-network-bug-repro
#
# # Called from trap - just wait, don't send another signal (Ctrl+C already sent it via TTY)
# wait_for_exit() {
#     echo "]] Waiting for handler to exit..."
#     if [[ -f /tmp/anklet-pids ]]; then
#         for pid in $(cat /tmp/anklet-pids); do
#             while ps -p $pid > /dev/null 2>&1; do
#                 echo "Waiting for process $pid to exit..."
#                 sleep 1
#             done
#             echo "Process $pid exited"
#         done
#         rm -f /tmp/anklet-pids
#     fi
# }
# 
# trap 'wait_for_exit' SIGTERM SIGINT
# 
# echo "]] Starting handler..."
# start_anklet "handler"
# # sleep for 30 seconds to ensure the handler is started properly
# sleep 30
# # check that it started properly
# check_anklet_process
# ----- END COMMENTED OUT -----
