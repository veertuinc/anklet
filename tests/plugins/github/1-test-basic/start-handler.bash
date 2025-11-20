#!/usr/bin/env bash
set -eo pipefail
echo "==========================================="
echo "START start-handler.bash"
echo "==========================================="
# Source the helper functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/helpers.bash"

if [[ ${CLEAN:-false} == true || "$1" == "--clean" ]]; then
    echo "]] Cleaning up handler..."
    clean_anklet "handler"
    exit 0
fi
echo "]] Cleaning up handler..."
clean_anklet "handler"

echo "]] Starting handler..."
start_anklet "handler"

echo "] Running tests..."

sleep 800 # wait for the handler to start and do a single no queue jobs found for the logs

echo "]] Triggering workflow runs..."
# # trigger the api call to trigger the workflow run
# ../trigger-workflow-runs.bash "veertuinc" "anklet" 1 "t2-6c14r-1.yml"

# # wait for the workflow to run to finish
# ../wait-workflow-completion.bash "veertuinc" "anklet" "t2-6c14r-1.yml" 3

# wait for the logs of the handler to show the expected output
echo "==========================================="
echo "END start-handler.bash"
echo "==========================================="
