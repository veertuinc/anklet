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
    
    echo "] Stopping anklet on all handlers..."
    stop_anklet_on_host "handler-8-16" || true
    stop_anklet_on_host "handler-8-8" || true
    
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
# Start anklet on both handlers (remote)
###############################################################################
echo "] Starting anklet on handler-8-16..."
start_anklet_on_host_background "handler-8-16"

echo "] Starting anklet on handler-8-8..."
start_anklet_on_host_background "handler-8-8"

# Wait for anklet to fully initialize and register with Redis
echo "] Waiting for anklet to register with Redis..."
sleep 10

# Verify Redis keys are present for receiver and both handlers
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_RECEIVER1"
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER_13_L_ARM_MACOS"
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER_8_L_ARM_MACOS"

###############################################################################
# Test Cases
###############################################################################

############
# t2-6c14r-1 - test that both handlers can process jobs
begin_test "t2-6c14r-1"
if run_workflow_and_get_logs "veertuinc" "anklet" "t2-6c14r-1" "success"; then
    # Check that at least one handler processed the job
    handler_8_16_processed=false
    handler_8_8_processed=false
    
    if check_remote_log_contains "handler-8-16" "queued job found"; then
        handler_8_16_processed=true
        echo "]] handler-8-16 processed a job"
        assert_remote_log_contains "handler-8-16" "GITHUB_HANDLER_13_L_ARM_MACOS"
    fi
    
    if check_remote_log_contains "handler-8-8" "queued job found"; then
        handler_8_8_processed=true
        echo "]] handler-8-8 processed a job"
        assert_remote_log_contains "handler-8-8" "GITHUB_HANDLER_8_L_ARM_MACOS"
    fi
    
    if [[ "$handler_8_16_processed" == "true" ]] || [[ "$handler_8_8_processed" == "true" ]]; then
        record_pass
    else
        record_fail "No handler processed the job"
    fi
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

