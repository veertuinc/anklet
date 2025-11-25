#!/usr/bin/env bash
set -eo pipefail
echo "==========================================="
echo "START start-handler.bash"
echo "==========================================="
# Source the helper functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/helpers.bash"

# Called explicitly via --clean flag - sends SIGINT
clean() {
    echo "]] Cleaning up handler..."
    clean_anklet "handler"
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

if [[ ${CLEAN_HANDLER:-false} == true || "$1" == "--clean" ]]; then
    clean
    exit 0
fi

trap 'wait_for_exit' SIGTERM SIGINT
trap 'clean' ERR

clean

echo "]] Starting handler..."
start_anklet "handler"
sleep 30
# check that it started properly
check_anklet_process

echo "] Running tests..."

echo "]] Triggering workflow runs..."
if ! trigger_workflow_runs "veertuinc" "anklet" "t2-6c14r-1.yml" 1; then
    echo "ERROR: Failed to trigger workflow runs"
    clean
    exit 1
fi
if ! wait_for_workflow_runs_to_complete "veertuinc" "anklet" "t2-6c14r-1"; then
    echo "ERROR: Failed to wait for workflow runs to complete"
    clean
    exit 1
fi
check_anklet_process

clean

# wait for the logs of the handler to show the expected output
echo "==========================================="
echo "END start-handler.bash"
echo "==========================================="
