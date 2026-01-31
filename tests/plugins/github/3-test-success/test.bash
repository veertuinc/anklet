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
    assert_remote_log_contains "handler-8-16" "handling anka workflow run job"
    assert_remote_log_contains "handler-8-16" "vm has enough resources now to run; starting runner"
    assert_remote_log_contains "handler-8-16" "job found registered runner and is now in progress"
    assert_remote_log_contains "handler-8-16" "cleanup | WorkflowJobPayload | queuedJob"
    assert_remote_log_contains "handler-8-16" "cleanup | anka.VM | queuedJob"
    assert_remote_log_contains "handler-8-16" "job is still in progress"
    assert_remote_log_contains "handler-8-16" "job completed"
    assert_remote_log_contains "handler-8-16" "GITHUB_HANDLER1"
    
    # Verify metrics endpoint shows correct status after job completion
    # This validates the fix for the data race bug where metrics showed "paused" 
    # when the plugin was actually idle (internal paused state was false)
    echo "] Verifying handler metrics endpoint shows 'idle' status..."
    sleep 2 # Allow metrics to update after job completion
    HANDLER_METRICS=$(ssh_to_host "handler-8-16" "curl -s http://127.0.0.1:8080/metrics/v1?format=prometheus")
    if echo "$HANDLER_METRICS" | grep -q "plugin_status{name=GITHUB_HANDLER1.*} idle"; then
        echo "PASS: Handler GITHUB_HANDLER1 metrics status is 'idle'"
        record_pass
    else
        echo "FAIL: Handler GITHUB_HANDLER1 metrics status is NOT 'idle'"
        echo "Metrics output:"
        echo "$HANDLER_METRICS" | grep "plugin_status" || echo "(no plugin_status found)"
        record_fail "metrics endpoint did not show expected 'idle' status for GITHUB_HANDLER1"
    fi
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
# t2-dual-without-tag
begin_test "t2-dual-without-tag"
if run_workflow_and_get_logs "veertuinc" "anklet" "t2-dual-without-tag" "success"; then
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

############
# metrics-status-validation
# This test validates the fix for the metrics data race bug where the metrics endpoint
# could show "paused" status while the plugin was actually idle. The bug was caused by
# handlePrometheusMetrics reading metrics data without holding a read lock.
begin_test "metrics-status-validation"
echo "] Validating metrics endpoints show correct 'idle' status for all plugins..."
sleep 3 # Ensure metrics have time to update after previous tests

METRICS_TEST_PASSED=true

# Check handler metrics (remote)
echo "] Checking handler-8-16 metrics endpoint..."
HANDLER_METRICS=$(ssh_to_host "handler-8-16" "curl -s http://127.0.0.1:8080/metrics/v1?format=prometheus" 2>/dev/null || echo "CURL_FAILED")
if [[ "$HANDLER_METRICS" == "CURL_FAILED" ]]; then
    echo "FAIL: Could not reach handler metrics endpoint"
    METRICS_TEST_PASSED=false
else
    # Check GITHUB_HANDLER1 status
    if echo "$HANDLER_METRICS" | grep -q "plugin_status{name=GITHUB_HANDLER1.*} idle"; then
        echo "PASS: GITHUB_HANDLER1 metrics status is 'idle'"
    else
        echo "FAIL: GITHUB_HANDLER1 metrics status is NOT 'idle'"
        echo "  Status found: $(echo "$HANDLER_METRICS" | grep "plugin_status{name=GITHUB_HANDLER1" || echo "not found")"
        METRICS_TEST_PASSED=false
    fi
    # Check GITHUB_HANDLER2 status if it exists
    if echo "$HANDLER_METRICS" | grep -q "plugin_status{name=GITHUB_HANDLER2"; then
        if echo "$HANDLER_METRICS" | grep -q "plugin_status{name=GITHUB_HANDLER2.*} idle"; then
            echo "PASS: GITHUB_HANDLER2 metrics status is 'idle'"
        else
            echo "FAIL: GITHUB_HANDLER2 metrics status is NOT 'idle'"
            echo "  Status found: $(echo "$HANDLER_METRICS" | grep "plugin_status{name=GITHUB_HANDLER2" || echo "not found")"
            METRICS_TEST_PASSED=false
        fi
    fi
fi

# Check receiver metrics (local)
echo "] Checking local receiver metrics endpoint..."
RECEIVER_METRICS=$(curl -s http://127.0.0.1:8080/metrics/v1?format=prometheus 2>/dev/null || echo "CURL_FAILED")
if [[ "$RECEIVER_METRICS" == "CURL_FAILED" ]]; then
    echo "FAIL: Could not reach receiver metrics endpoint"
    METRICS_TEST_PASSED=false
else
    if echo "$RECEIVER_METRICS" | grep -q "plugin_status{name=GITHUB_RECEIVER1.*} idle"; then
        echo "PASS: GITHUB_RECEIVER1 metrics status is 'idle'"
    else
        echo "FAIL: GITHUB_RECEIVER1 metrics status is NOT 'idle'"
        echo "  Status found: $(echo "$RECEIVER_METRICS" | grep "plugin_status{name=GITHUB_RECEIVER1" || echo "not found")"
        METRICS_TEST_PASSED=false
    fi
fi

# Record test result
if [[ "$METRICS_TEST_PASSED" == "true" ]]; then
    record_pass
else
    record_fail "one or more plugins did not show expected 'idle' status in metrics"
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
