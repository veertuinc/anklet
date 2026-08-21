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

# Wait for a receiver JSON log msg. Dump the log and fail on crash or timeout.
wait_for_receiver_log() {
    local needle="$1"
    local max_wait="$2"
    local fail_msg="$3"
    local wait_count=0
    while ! grep -q "\"msg\":\"${needle}\"" /tmp/anklet.log 2>/dev/null; do
        sleep 5
        wait_count=$((wait_count + 5))
        if grep -q '"msg":"error running plugin"' /tmp/anklet.log 2>/dev/null; then
            echo "] FAIL: Receiver crashed while waiting for '${needle}'"
            echo "] === /tmp/anklet.log AFTER crash ==="
            cat /tmp/anklet.log || true
            echo "] === END /tmp/anklet.log AFTER crash ==="
            record_fail "receiver crashed: ${fail_msg}"
            return 1
        fi
        if [[ $wait_count -ge $max_wait ]]; then
            echo "] ERROR: did not see '${needle}' within ${max_wait}s"
            echo "] === /tmp/anklet.log AFTER timeout ==="
            cat /tmp/anklet.log || true
            echo "] === END /tmp/anklet.log AFTER timeout ==="
            record_fail "${fail_msg}"
            return 1
        fi
        echo "]] Waiting for '${needle}'... (${wait_count}s/${max_wait}s)"
    done
    return 0
}

###############################################################################
# Test: Webhook redelivery after receiver downtime
#
# The receiver lists failed hook deliveries on startup (background walk) and
# POSTs redelivery for orphaned Anklet jobs. HTTP and FinishedInitialRun are
# set before that walk. The raw GitHub payload has repository.owner as a JSON
# object and must unmarshal.
#
# Flow:
#   1. Start receiver (tunnel + HTTP). Wait until the startup walk ends.
#   2. Stop receiver anklet (tunnel stays up; GitHub gets a non-200).
#   3. Trigger workflow so GitHub records a failed delivery.
#   4. Restart receiver (skip_redeliver: false). Wait for the walk to POST.
#   5. Start handler after redelivery so GetWorkflowJobByID does not see
#      in_progress and skip the hook.
#   6. Handler processes the job.
###############################################################################

begin_test "webhook-redelivery-after-receiver-downtime" "success"

# Step 1: Start receiver to establish the tunnel/webhook endpoint
echo "] Starting anklet on receiver to establish tunnel..."
start_anklet_backgrounded_but_attached "receiver"
sleep 10
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_RECEIVER1"
echo "] Receiver is up and tunnel is established"

# HTTP logs "receiver finished starting" before the walk. Stop only after the
# walk ends so SIGINT does not wait on listing, and so GitHub cannot get HTTP
# 200 from a still-running listener.
echo "] Waiting for HTTP listen and the startup redelivery walk..."
if ! wait_for_receiver_log "receiver finished starting" 180 "receiver did not log HTTP listen"; then
    end_test
    exit 1
fi
if ! wait_for_receiver_log "finished processing hooks for redelivery" 180 "startup redelivery walk did not finish"; then
    end_test
    exit 1
fi
echo "] Receiver HTTP is up and the startup walk has finished"

# Step 2: Stop just the anklet process on receiver (tunnel stays up)
# The walk goroutine may still be in the 1-minute post-POST sleep. SIGINT
# cancels that sleep, then the HTTP server shuts down.
echo "] Stopping anklet on receiver (tunnel should remain up)..."
pkill -INT -f '^/tmp/anklet$' 2>/dev/null || true
sleep 10

# Step 3: Trigger a workflow while receiver anklet is DOWN
# GitHub will deliver the webhook via the tunnel, but get an error because
# anklet is not listening on the port. This creates a failed hook delivery.
echo "] Triggering t1-with-tag-1 workflow while receiver anklet is DOWN..."
trigger_workflow_runs "veertuinc" "anklet" "t1-with-tag-1.yml" 1

# Wait for GitHub to attempt delivery and record the failure
echo "] Waiting 30s for GitHub to record the failed webhook delivery..."
sleep 30

# Step 4: Restart receiver with redelivery enabled (skip_redeliver: false)
# The walk lists failed deliveries, unmarshals the raw payload, and POSTs.
#
# Truncate the anklet log before restarting so we don't match stale entries
# from the first receiver start (which also ran the redelivery code path).
echo "] === /tmp/anklet.log BEFORE restart ==="
cat /tmp/anklet.log || true
echo "] === END /tmp/anklet.log BEFORE restart ==="

echo "] Truncating anklet log before restart..."
> /tmp/anklet.log

echo "] Restarting anklet on receiver with redelivery enabled..."
start_anklet_backgrounded_but_attached "receiver"

# Do not treat "receiver finished starting" as walk-complete. That log is
# immediate. Wait for the POST itself.
echo "] Waiting for the redelivery walk to POST the failed hook..."
if ! wait_for_receiver_log "redelivering hook" 180 "receiver did not request hook redelivery"; then
    end_test
    exit 1
fi
echo "] Receiver requested hook redelivery"
echo "] === /tmp/anklet.log AFTER redelivery POST ==="
cat /tmp/anklet.log || true
echo "] === END /tmp/anklet.log AFTER redelivery POST ==="

# Step 5: Verify the unmarshal fix — no "error unmarshalling" in logs.
assert_json_log_not_contains /tmp/anklet.log "msg=error running plugin,error=error unmarshalling hook request raw payload"
assert_json_log_contains /tmp/anklet.log "msg=redelivering hook"

# Start the handler after the walk POSTs. If it is already running, a GitHub
# auto-retry can move the job to in_progress and the walk skips the hook.
echo "] Starting anklet on handler-8-16..."
start_anklet_on_host_background "handler-8-16"
sleep 10
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER1"

# Step 6: Wait for the redelivered webhook to be processed and the job queued.
# HTTP is already listening (before the walk), so ingest can happen as soon
# as GitHub POSTs the redelivered payload.
echo "] Waiting for redelivered webhook to be processed..."
if ! wait_for_receiver_log "job pushed to queued queue" 120 "redelivered webhook was not processed (job not pushed to queue)"; then
    end_test
    exit 1
fi
echo "] Job pushed to queue from redelivered webhook"

# Step 7: Wait for the workflow to complete.
echo "] Waiting for workflow to complete..."
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
