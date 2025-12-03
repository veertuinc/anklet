#!/usr/bin/env bash
set -eo pipefail
TEST_DIR_NAME="$(basename "$(pwd)")"
echo "==========================================="
echo "START $TEST_DIR_NAME/test.bash"
echo "==========================================="
# Source the helper functions (includes test report tracking functions)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/helpers.bash"

echo "] Running $TEST_DIR_NAME test..."

# Initialize test report
init_test_report "$TEST_DIR_NAME"

# Show configured hosts
list_all_hosts

# Cleanup function - stops anklet on all hosts
cleanup() {
    echo ""
    echo "==========================================="
    echo "START $TEST_DIR_NAME/test.bash cleanup..."
    
    echo "] Cancelling running workflow runs..."
    cancel_running_workflow_runs "veertuinc" "anklet" "t1-" "t2-" || echo "WARNING: Some workflow cancellations may have failed"
    
    echo "] Stopping anklet on handler..."
    stop_anklet_on_host "handler-8-16" || true
    
    echo "] Stopping anklet on receiver (local)..."
    pkill -INT -f '^/tmp/anklet$' 2>/dev/null || true
    
    echo "END $TEST_DIR_NAME/test.bash cleanup..."
    echo "==========================================="
}
trap 'cleanup; _finalize_test_report_on_exit' EXIT

###############################################################################
# Start anklet on receiver (local - this is where the test runs)
###############################################################################
echo "] Starting anklet on receiver (local)..."
start_anklet_backgrounded_but_attached "receiver"

###############################################################################
# Start anklet on handler (remote)
###############################################################################
echo "] Starting anklet on handler-8-16..."
start_anklet_on_host_background "handler-8-16"

# Wait for anklet to fully initialize and register with Redis
echo "] Waiting for anklet to register with Redis..."
sleep 10

# Verify Redis keys are present
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER1"
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_RECEIVER1"

###############################################################################
# Test Cases - Cancellation scenarios
###############################################################################

############
# t1-cancelled-failure-no-tag-in-registry
begin_test "t1-cancelled-failure-no-tag-in-registry"
run_workflow_and_get_logs "veertuinc" "anklet" "t1-cancelled-failure-no-tag-in-registry" "cancelled" && record_pass || record_fail "workflow did not complete as expected"
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER1"
end_test
############

############
# t1-cancelled-failure-no-tag
begin_test "t1-cancelled-failure-no-tag"
run_workflow_and_get_logs "veertuinc" "anklet" "t1-cancelled-failure-no-tag" "cancelled" && record_pass || record_fail "workflow did not complete as expected"
end_test
############

############
# t1-cancelled-failure-no-template-in-registry
begin_test "t1-cancelled-failure-no-template-in-registry"
run_workflow_and_get_logs "veertuinc" "anklet" "t1-cancelled-failure-no-template-in-registry" "cancelled" && record_pass || record_fail "workflow did not complete as expected"
end_test
############

############
# t1-cancelled-failure-no-template-specified
begin_test "t1-cancelled-failure-no-template-specified"
run_workflow_and_get_logs "veertuinc" "anklet" "t1-cancelled-failure-no-template-specified" "cancelled" && record_pass || record_fail "workflow did not complete as expected"
end_test
############

############
# t1-cancelled-failure-no-template
begin_test "t1-cancelled-failure-no-template"
run_workflow_and_get_logs "veertuinc" "anklet" "t1-cancelled-failure-no-template" "cancelled" && record_pass || record_fail "workflow did not complete as expected"
end_test
############

# Finalize and print test report (cleanup runs via EXIT trap)
finalize_test_report "$TEST_DIR_NAME"

echo "==========================================="
echo "END $TEST_DIR_NAME/test.bash"
echo "==========================================="

# Exit with failure if any tests failed
if [[ $TEST_FAILED -gt 0 ]]; then
    exit 1
fi
