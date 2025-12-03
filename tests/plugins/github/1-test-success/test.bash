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
# Test Cases
###############################################################################

############
# t1-with-tag-1
begin_test "t1-with-tag-1"
if run_workflow_and_get_logs "veertuinc" "anklet" "t1-with-tag-1" "success"; then
    # Check handler's anklet.log for expected entries
    assert_remote_log_contains "handler-8-16" "queued job found"
    assert_remote_log_contains "handler-8-16" "checkForCompletedJobs -> queued job found in pluginQueue"
    assert_remote_log_contains "handler-8-16" "handling anka workflow run job"
    assert_remote_log_contains "handler-8-16" "job updated in database"
    assert_remote_log_contains "handler-8-16" "vm has enough resources now to run; starting runner"
    assert_remote_log_contains "handler-8-16" "job found registered runner and is now in progress"
    assert_remote_log_contains "handler-8-16" "successfully deleted vm"
    assert_remote_log_contains "handler-8-16" "job removed from queue"
    assert_remote_log_contains "handler-8-16" "GITHUB_HANDLER1"
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

############
# t1-with-tag-1-matrix-nodes-2
begin_test "t1-with-tag-1-matrix-nodes-2"
if run_workflow_and_get_logs "veertuinc" "anklet" "t1-with-tag-1-matrix-nodes-2" "success"; then
    assert_remote_log_contains "handler-8-16" "handling anka workflow run job"
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

############
# t1-with-tag-2
begin_test "t1-with-tag-2"
if run_workflow_and_get_logs "veertuinc" "anklet" "t1-with-tag-2" "success"; then
    assert_remote_log_contains "handler-8-16" "anka -j registry pull"
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

############
# t1-without-tag
begin_test "t1-without-tag"
if run_workflow_and_get_logs "veertuinc" "anklet" "t1-without-tag" "success"; then
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

############
# t2-6c14r-1
begin_test "t2-6c14r-1"
if run_workflow_and_get_logs "veertuinc" "anklet" "t2-6c14r-1" "success"; then
    assert_logs_contain "Ankas-Virtual-Machine.local" "${WORKFLOW_LOG_FILES[@]}"
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

############
# t2-6c14r-2-5m-pause
begin_test "t2-6c14r-2-5m-pause"
if run_workflow_and_get_logs "veertuinc" "anklet" "t2-6c14r-2-5m-pause" "success"; then
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

############
# t2-8c14r-1
begin_test "t2-8c14r-1"
if run_workflow_and_get_logs "veertuinc" "anklet" "t2-8c14r-1" "success"; then
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

############
# t2-12c20r-1 (resource-constrained)
begin_test "t2-12c20r-1"
# This workflow requires more resources than the host has available
# Trigger workflow and check handler's log for resource error
if run_workflow_and_get_logs "veertuinc" "anklet" "t2-12c20r-1" "failure" 120; then
    assert_remote_log_contains "handler-8-16" "host does not have enough resources to run vm"
    record_pass
else
    record_fail "expected resource error not found"
fi
end_test
############

############
# t2-12c50r-1 (resource-constrained)
begin_test "t2-12c50r-1"
if run_workflow_and_get_logs "veertuinc" "anklet" "t2-12c50r-1" "failure" 120; then
    assert_remote_log_contains "handler-8-16" "host does not have enough resources to run vm"
    record_pass
else
    record_fail "expected resource error not found"
fi
end_test
############

############
# t2-20c20r-1 (resource-constrained)
begin_test "t2-20c20r-1"
if run_workflow_and_get_logs "veertuinc" "anklet" "t2-20c20r-1" "failure" 120; then
    assert_remote_log_contains "handler-8-16" "host does not have enough resources to run vm"
    record_pass
else
    record_fail "expected resource error not found"
fi
end_test
############

############
# t2-dual-without-tag
begin_test "t2-dual-without-tag"
if run_workflow_and_get_logs "veertuinc" "anklet" "t2-dual-without-tag" "success"; then
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
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
