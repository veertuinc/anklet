#!/usr/bin/env bash
set -exo pipefail
SCRIPT_NAME="$(basename "${BASH_SOURCE[0]}")"
echo "==========================================="
echo "START ${SCRIPT_NAME}"
echo "==========================================="
# Source the helper functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/helpers.bash"

if [[ ${CLEAN:-false} == true || "$1" == "--clean" ]]; then
    clean_anklet "receiver"
    exit 0
fi

# Called from trap - just wait, don't send another signal (Ctrl+C already sent it via TTY)
wait_for_exit() {
    echo "]] Waiting for receiver to exit..."
    if [[ -f /tmp/anklet-pids ]]; then
        for pid in $(cat /tmp/anklet-pids); do
            while ps -p $pid > /dev/null 2>&1; do
                echo "Waiting for process $pid to exit..."
                sleep 1
            done
            echo "Process $pid exited"
        done
        rm -f /tmp/anklet-pids
    fi
}
trap 'wait_for_exit' SIGTERM SIGINT

clean_anklet "receiver"
start_anklet_backgrounded_but_not_attached "receiver"
# check that it started properly
check_anklet_process
echo "==========================================="
echo "END ${SCRIPT_NAME}"
echo "==========================================="