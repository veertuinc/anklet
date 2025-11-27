#!/usr/bin/env bash
set -eo pipefail
SCRIPT_NAME="$(basename "${BASH_SOURCE[0]}")"
echo "==========================================="
echo "START ${SCRIPT_NAME}"
echo "==========================================="
# Source the helper functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/helpers.bash"

# Called explicitly via --clean flag - sends SIGINT
clean_and_exit() {
    echo "]] Cleaning up handler..."
    clean_anklet "handler"
    exit 0
}

# Called from trap - just wait, don't send another signal (Ctrl+C already sent it via TTY)
wait_for_exit() {
    echo "]] Waiting for handler to exit..."
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

if [[ ${CLEAN_HANDLER:-false} == true || "$1" == "--clean" ]]; then
    clean_and_exit
fi

clean_anklet "handler"

echo "]] Starting handler..."
start_anklet "handler"
# sleep for 30 seconds to ensure the handler is started properly
sleep 30
# check that it started properly
check_anklet_process

# wait for the logs of the handler to show the expected output
echo "==========================================="
echo "END ${SCRIPT_NAME}"
echo "==========================================="
