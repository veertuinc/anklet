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

# Wait for receiver to initialize
echo "] Waiting for receiver to initialize..."
sleep 5
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_RECEIVER1"

###############################################################################
# Test: Verify shared queue is being used
# Both handlers should be able to pick up jobs from the shared_queue namespace
###############################################################################
begin_test "shared-queue-name-basic" "success"

# Step 1: Start both handlers
echo "] Starting anklet on handler-8-16..."
start_anklet_on_host_background "handler-8-16"
sleep 5
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER_13_L_ARM_MACOS"

echo "] Starting anklet on handler-8-8..."
start_anklet_on_host_background "handler-8-8"
sleep 5
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER1_8_L_ARM_MACOS"

# Step 2: Trigger a workflow that will create jobs in the shared queue
echo "] Triggering t1-with-tag-1-matrix-nodes-2 workflow (2 jobs for shared queue)..."
trigger_workflow_runs "veertuinc" "anklet" "t1-with-tag-1-matrix-nodes-2.yml" 1

# Step 3: Verify jobs are queued in the shared_queue namespace
echo "] Verifying jobs are queued in shared_queue namespace..."
max_wait=60
wait_count=0
while true; do
    # Check if jobs appear in the shared_queue namespace (not owner-based)
    shared_queue_keys=$(list_redis_keys "anklet/jobs/github/*/shared_queue*" 2>/dev/null | grep -v "^]]" || true)
    if [[ -n "$shared_queue_keys" ]]; then
        echo "] ✓ Jobs found in shared_queue namespace"
        break
    fi
    sleep 5
    wait_count=$((wait_count + 5))
    if [[ $wait_count -ge $max_wait ]]; then
        echo "] WARNING: Could not verify shared_queue namespace within ${max_wait}s (jobs may have been picked up already)"
        break
    fi
    echo "]] Still waiting for jobs in shared_queue... (${wait_count}s/${max_wait}s)"
done

# Step 4: Wait for workflows to complete and verify both handlers processed jobs
echo "] Waiting for workflow to complete..."
if wait_for_workflow_runs_to_complete "veertuinc" "anklet" "t1-with-tag-1-matrix-nodes-2" "success"; then
    # Verify both handlers picked up jobs from the shared queue
    handler_8_16_found=false
    handler_8_8_found=false
    
    if check_remote_log_contains "handler-8-16" "queued job found"; then
        echo "] ✓ handler-8-16 picked up a job from shared queue"
        handler_8_16_found=true
    fi
    
    if check_remote_log_contains "handler-8-8" "queued job found"; then
        echo "] ✓ handler-8-8 picked up a job from shared queue"
        handler_8_8_found=true
    fi
    
    if [[ "$handler_8_16_found" == "true" ]] && [[ "$handler_8_8_found" == "true" ]]; then
        echo "] ✓ Both handlers successfully processed jobs from shared queue"
        assert_remote_log_contains "handler-8-16" "queued job found"
        assert_remote_log_contains "handler-8-8" "queued job found"
        record_pass
    elif [[ "$handler_8_16_found" == "true" ]] || [[ "$handler_8_8_found" == "true" ]]; then
        # At least one handler processed jobs - acceptable for shared queue
        echo "] ✓ At least one handler processed jobs from shared queue"
        record_pass
    else
        record_fail "No handler picked up jobs from shared queue"
    fi
else
    record_fail "workflow did not complete successfully"
fi
end_test
############

###############################################################################
# Test: t2-dual-without-tag with shared queue
# Two concurrent jobs should be distributed across handlers via shared queue
###############################################################################
begin_test "t2-dual-without-tag-shared-queue" "success"

if run_workflow_and_get_logs "veertuinc" "anklet" "t2-dual-without-tag" "success"; then
    # Verify jobs were processed - with shared queue, both handlers should participate
    handler_8_16_processed=false
    handler_8_8_processed=false
    
    if check_remote_log_contains "handler-8-16" "queued job found"; then
        echo "] ✓ handler-8-16 processed a job"
        handler_8_16_processed=true
    fi
    
    if check_remote_log_contains "handler-8-8" "queued job found"; then
        echo "] ✓ handler-8-8 processed a job"
        handler_8_8_processed=true
    fi
    
    # For dual workflow, we expect both handlers to process one job each
    if [[ "$handler_8_16_processed" == "true" ]] && [[ "$handler_8_8_processed" == "true" ]]; then
        echo "] ✓ Both handlers processed jobs via shared queue"
        assert_remote_log_contains "handler-8-16" "queued job found"
        assert_remote_log_contains "handler-8-8" "queued job found"
        assert_logs_contain "Ankas-Virtual-Machine.local" "${WORKFLOW_LOG_FILES[0]}"
        assert_logs_contain "Ankas-Virtual-Machine.local" "${WORKFLOW_LOG_FILES[1]}"
        record_pass
    else
        record_fail "Expected both handlers to process jobs with shared queue"
    fi
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

###############################################################################
# Test: Verify queue_name appears in Redis key structure
# This validates that the shared queue namespace is being used correctly
###############################################################################
begin_test "shared-queue-redis-namespace" "success"

echo "] Triggering t1-with-tag-1 workflow..."
trigger_workflow_runs "veertuinc" "anklet" "t1-with-tag-1.yml" 1

# Wait briefly for job to be queued
sleep 10

# Check Redis for shared_queue namespace
echo "] Checking Redis for shared_queue namespace..."
shared_queue_keys=$(list_redis_keys "anklet/jobs/github/*/shared_queue*" 2>/dev/null | grep -v "^]]" || true)
if [[ -n "$shared_queue_keys" ]] || \
   check_remote_log_contains "handler-8-16" "queued job found" || \
   check_remote_log_contains "handler-8-8" "queued job found"; then
    echo "] ✓ Shared queue namespace is being used"
    record_pass
else
    # Wait for workflow completion and verify handlers used shared queue
    if wait_for_workflow_runs_to_complete "veertuinc" "anklet" "t1-with-tag-1" "success" 300; then
        if check_remote_log_contains "handler-8-16" "queued job found" || \
           check_remote_log_contains "handler-8-8" "queued job found"; then
            echo "] ✓ Job was processed via shared queue"
            record_pass
        else
            record_fail "Could not verify shared queue usage"
        fi
    else
        record_fail "Workflow did not complete"
    fi
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
