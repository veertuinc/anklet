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
#   1. Start receiver (establishes tunnel so GitHub can reach webhook endpoint)
#   2. Start handler
#   3. Stop receiver anklet process (tunnel stays up, but endpoint returns errors)
#   4. Trigger workflow (webhook delivery fails with non-200 status)
#   5. Restart receiver with skip_redeliver: false
#   6. Receiver polls for failed deliveries, unmarshals raw payload, redelivers
#   7. Handler processes the job successfully
###############################################################################

begin_test "webhook-redelivery-after-receiver-downtime" "success"

# Step 1: Start receiver to establish the tunnel/webhook endpoint
echo "] Starting anklet on receiver to establish tunnel..."
start_anklet_backgrounded_but_attached "receiver"
sleep 10
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_RECEIVER1"
echo "] Receiver is up and tunnel is established"

# Wait for the receiver to finish its full startup cycle (including any
# redelivery of old failed hooks from previous runs). If we stop it during
# the redelivery sleep, the HTTP handler stays alive with a cancelled context
# and webhooks get HTTP 200 but fail internally — GitHub marks them as
# delivered, so they won't appear as failed on the next receiver start.
echo "] Waiting for receiver to finish initial startup (including old redeliveries)..."
max_wait=180
wait_count=0
while ! grep -q '"msg":"receiver finished starting"' /tmp/anklet.log 2>/dev/null; do
    sleep 5
    wait_count=$((wait_count + 5))
    if grep -q '"msg":"error running plugin"' /tmp/anklet.log 2>/dev/null; then
        echo "] WARN: Receiver crashed during initial startup, continuing anyway..."
        break
    fi
    if [[ $wait_count -ge $max_wait ]]; then
        echo "] ERROR: Receiver did not finish initial startup within ${max_wait}s"
        record_fail "receiver did not complete initial startup"
        end_test
        exit 1
    fi
    echo "]] Waiting for receiver initial startup... (${wait_count}s/${max_wait}s)"
done
echo "] Receiver initial startup complete"

# Step 2: Start handler so it's ready to process jobs
echo "] Starting anklet on handler-8-16..."
start_anklet_on_host_background "handler-8-16"
sleep 10
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER1"

# Step 3: Stop just the anklet process on receiver (tunnel stays up)
# Now that the receiver is fully started and idle in the HTTP handler loop,
# SIGINT will shut it down cleanly. GitHub will route webhooks via the tunnel
# but anklet won't be listening, resulting in a failed delivery (non-200 status).
echo "] Stopping anklet on receiver (tunnel should remain up)..."
pkill -INT -f '^/tmp/anklet$' 2>/dev/null || true
sleep 10

# Step 4: Trigger a workflow while receiver anklet is DOWN
# GitHub will deliver the webhook via the tunnel, but get an error because
# anklet is not listening on the port. This creates a failed hook delivery.
echo "] Triggering t1-with-tag-1 workflow while receiver anklet is DOWN..."
trigger_workflow_runs "veertuinc" "anklet" "t1-with-tag-1.yml" 1

# Wait for GitHub to attempt delivery and record the failure
echo "] Waiting 30s for GitHub to record the failed webhook delivery..."
sleep 30

# Step 5: Restart receiver with redelivery enabled (skip_redeliver: false)
# The receiver will poll the GitHub Hook Delivery API, find the failed delivery,
# fetch the raw payload, unmarshal it (exercising the owner object fix), and
# request redelivery.
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

# Wait for the receiver to fully start. The redelivery cycle is:
#   1. Poll hook deliveries → find failed ones → unmarshal raw payloads → request redelivery
#   2. Sleep 1 minute (to let handlers process redelivered jobs)
#   3. Clean up in_progress queue
#   4. Log "receiver finished starting" → enter HTTP handler loop
#
# We wait for "receiver finished starting" so the full cycle (including the
# 1-minute sleep) is complete and the receiver is ready to handle webhooks.
# We also check early for the unmarshal error that occurs without the fix.
echo "] Waiting for receiver to complete full redelivery cycle..."
max_wait=180
wait_count=0
while ! grep -q '"msg":"receiver finished starting"' /tmp/anklet.log 2>/dev/null; do
    sleep 5
    wait_count=$((wait_count + 5))
    if grep -q '"msg":"error running plugin"' /tmp/anklet.log 2>/dev/null; then
        echo "] FAIL: Receiver crashed with error during redelivery processing"
        echo "] === /tmp/anklet.log AFTER crash ==="
        cat /tmp/anklet.log || true
        echo "] === END /tmp/anklet.log AFTER crash ==="
        record_fail "receiver crashed during redelivery: unmarshal error on raw webhook payload"
        end_test
        exit 1
    fi
    if [[ $wait_count -ge $max_wait ]]; then
        echo "] ERROR: Receiver did not finish starting within ${max_wait}s"
        echo "] === /tmp/anklet.log AFTER timeout ==="
        cat /tmp/anklet.log || true
        echo "] === END /tmp/anklet.log AFTER timeout ==="
        record_fail "receiver did not complete startup"
        end_test
        exit 1
    fi
    echo "]] Waiting for receiver to finish starting... (${wait_count}s/${max_wait}s)"
done
echo "] Receiver is fully started and listening for webhooks"
echo "] === /tmp/anklet.log AFTER successful startup ==="
cat /tmp/anklet.log || true
echo "] === END /tmp/anklet.log AFTER successful startup ==="

# Step 6: Verify the unmarshal fix — no "error unmarshalling" in logs.
assert_json_log_not_contains /tmp/anklet.log "msg=error running plugin,error=error unmarshalling hook request raw payload"

# Verify the receiver actually found and requested redelivery
assert_json_log_contains /tmp/anklet.log "msg=redelivering hook"

# Step 7: Wait for the redelivered webhook to be processed and the job queued.
# After "receiver finished starting", the HTTP handler is active and will
# process the redelivered webhook from GitHub.
echo "] Waiting for redelivered webhook to be processed..."
max_wait=120
wait_count=0
while ! grep -q '"msg":"job pushed to queued queue"' /tmp/anklet.log 2>/dev/null; do
    sleep 5
    wait_count=$((wait_count + 5))
    if [[ $wait_count -ge $max_wait ]]; then
        echo "] ERROR: Redelivered webhook was not processed within ${max_wait}s"
        record_fail "redelivered webhook was not processed (job not pushed to queue)"
        end_test
        exit 1
    fi
    echo "]] Waiting for job to be pushed to queue... (${wait_count}s/${max_wait}s)"
done
echo "] Job pushed to queue from redelivered webhook"

# Step 8: Wait for the workflow to complete.
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
