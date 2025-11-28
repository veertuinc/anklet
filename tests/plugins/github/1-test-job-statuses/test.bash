#!/usr/bin/env bash
set -eo pipefail
TEST_DIR_NAME="$(basename "$(pwd)")"
echo "==========================================="
echo "START $TEST_DIR_NAME/test.bash"
echo "==========================================="
# Source the helper functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/helpers.bash"

echo "] Running $TEST_DIR_NAME test..."


sleep 60

exit 1

############
# t2-6c14r-1
WORKFLOW_NAME="t2-6c14r-1"
echo "]] Triggering workflow runs..."
if ! trigger_workflow_runs "veertuinc" "anklet" "${WORKFLOW_NAME}.yml" 1; then
    echo "ERROR: Failed to trigger workflow runs"
    exit 1
fi
if ! wait_for_workflow_runs_to_complete "veertuinc" "anklet" "${WORKFLOW_NAME}" "success"; then
    echo "ERROR: Failed to wait for workflow runs to complete"
    exit 1
fi
# Read log file paths into array (portable, works with Bash 3.x on macOS)
workflow_log_files=()
while IFS= read -r line; do
    workflow_log_files+=("$line")
done < <(get_workflow_run_logs "veertuinc" "anklet" "${WORKFLOW_NAME}")

assert_logs_contain "Ankas-Virtual-Machine.local" "${workflow_log_files[@]}" || exit 1
assert_logs_contain "butt" "${workflow_log_files[@]}" || exit 1

cleanup_log_files "${workflow_log_files[@]}"
check_anklet_process
############


echo "==========================================="
echo "END $TEST_DIR_NAME/test.bash"
echo "==========================================="
