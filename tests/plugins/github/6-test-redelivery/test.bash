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
    cancel_running_workflow_runs "veertuinc" "anklet" "t1-" || echo "WARNING: Some workflow cancellations may have failed"
    
    echo "] Stopping anklet on handler..."
    stop_anklet_on_host "handler-8-16" || true
    
    echo "] Stopping anklet on receiver (local)..."
    pkill -INT -f '^/tmp/anklet$' 2>/dev/null || true
    
    echo "END $TEST_DIR_NAME/test.bash cleanup..."
    echo "==========================================="
}
trap 'cleanup; _finalize_test_report_on_exit' EXIT

###############################################################################
# Test: Webhook redelivery after receiver downtime
#
# This tests the redelivery code path where the receiver polls the GitHub
# Hook Delivery API for failed webhook deliveries and redelivers them.
# The raw GitHub payload has repository.owner as a JSON object, which
# must be handled correctly during unmarshaling.
#
# Flow:
#   1. Start handler only (receiver is down)
#   2. Trigger workflow (webhook fails because receiver is down)
#   3. Start receiver with skip_redeliver: false
#   4. Receiver polls for failed deliveries, unmarshals raw payload, redelivers
#   5. Handler processes the job successfully
###############################################################################

begin_test "webhook-redelivery-after-receiver-downtime" "success"

# Step 1: Start ONLY the handler (no receiver)
echo "] Starting anklet on handler-8-16 (without receiver)..."
start_anklet_on_host_background "handler-8-16"
sleep 10
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER1"

# Step 2: Trigger a workflow while receiver is DOWN
# GitHub will try to deliver the webhook but the receiver endpoint is not listening,
# so the delivery will be recorded with a non-200 status code.
echo "] Triggering t1-with-tag-1 workflow while receiver is DOWN..."
trigger_workflow_runs "veertuinc" "anklet" "t1-with-tag-1.yml" 1

# Wait for GitHub to attempt and record the failed delivery
echo "] Waiting 30s for GitHub to record the failed webhook delivery..."
sleep 30

# Step 3: Start the receiver with redelivery enabled (skip_redeliver: false)
# The receiver will poll the GitHub Hook Delivery API, find the failed delivery,
# fetch the raw payload, unmarshal it (exercising the owner object fix), and
# request redelivery.
echo "] Starting anklet on receiver (local) with redelivery enabled..."
start_anklet_backgrounded_but_attached "receiver"

# Wait for receiver to initialize and complete redelivery processing
echo "] Waiting for receiver to initialize and process redeliveries..."
sleep 10
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_RECEIVER1"

# Step 4: Verify the receiver found and redelivered the failed hook
# The receiver should log about processing hooks for redelivery
echo "] Checking receiver logs for redelivery activity..."
max_wait=120
wait_count=0
while ! assert_json_log_contains /tmp/anklet.log "msg=processing hooks scheduled for redelivery" 2>/dev/null; do
    sleep 5
    wait_count=$((wait_count + 5))
    if [[ $wait_count -ge $max_wait ]]; then
        echo "] ERROR: Receiver did not process redeliveries within ${max_wait}s"
        record_fail "receiver did not log redelivery processing"
        end_test
        exit 1
    fi
    echo "]] Waiting for receiver to process redeliveries... (${wait_count}s/${max_wait}s)"
done
echo "] Receiver processed redeliveries"

# Verify no unmarshal errors (this is the core assertion for the owner object fix)
assert_json_log_not_contains /tmp/anklet.log "msg=error running plugin"

# Step 5: Wait for the workflow to complete via redelivery
echo "] Waiting for workflow to complete (via redelivered webhook)..."
if wait_for_workflow_runs_to_complete "veertuinc" "anklet" "t1-with-tag-1" "success" 600; then
    # Verify handler processed the job
    assert_remote_log_contains "handler-8-16" "queued job found"
    assert_remote_log_contains "handler-8-16" "handling anka workflow run job"
    assert_remote_log_contains "handler-8-16" "job completed"
    record_pass
else
    record_fail "workflow did not complete successfully via redelivery"
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
