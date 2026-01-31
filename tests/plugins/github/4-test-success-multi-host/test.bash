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
# Test: handler-8-8 (8GB RAM) can't run 14GB job, handler-8-16 (16GB) can
###############################################################################
begin_test "t2-6c14r failover from 8GB to 16GB host" "success"

# Step 1: Start only handler-8-8 (8GB RAM host)
echo "] Starting anklet ONLY on handler-8-8 (8GB RAM)..."
start_anklet_on_host_background "handler-8-8"
sleep 5
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER1_8_L_ARM_MACOS"

# Step 2: Trigger workflow that needs 14GB RAM (more than 8GB host has)
echo "] Triggering t2-6c14r-1 workflow (requires 14GB RAM)..."
trigger_workflow_runs "veertuinc" "anklet" "t2-6c14r-1.yml" 1

# Step 3: Wait for handler-8-8 to report insufficient resources
echo "] Waiting for handler-8-8 to report 'not enough resources'..."
max_wait=120
wait_count=0
while ! check_remote_log_contains "handler-8-8" "host does not have enough resources to run vm"; do
    sleep 5
    wait_count=$((wait_count + 5))
    if [[ $wait_count -ge $max_wait ]]; then
        record_fail "handler-8-8 did not report 'host does not have enough resources' within ${max_wait}s"
        end_test
        exit 1
    fi
    echo "]] Still waiting... (${wait_count}s/${max_wait}s)"
done
echo "] ✓ handler-8-8 correctly reported insufficient resources"
assert_remote_log_contains "handler-8-8" "host does not have enough resources to run vm"

# Step 4: Now start handler-8-16 (16GB RAM host) - should be able to handle the job
echo "] Starting anklet on handler-8-16 (16GB RAM)..."
start_anklet_on_host_background "handler-8-16"
sleep 5
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER_13_L_ARM_MACOS"

# Step 5: Wait for workflow to complete (handler-8-16 should pick it up)
echo "] Waiting for workflow to complete (handler-8-16 should process it)..."
if wait_for_workflow_runs_to_complete "veertuinc" "anklet" "t2-6c14r-1" "success"; then
    # Verify handler-8-16 processed the job
    if check_remote_log_contains "handler-8-16" "queued job found"; then
        echo "] ✓ handler-8-16 processed the job successfully"
        assert_remote_log_contains "handler-8-16" "queued job found"
        
        # Step 6: Verify metrics show correct 'idle' status after job completion
        # This validates the fix for the data race bug where metrics showed "paused"
        # when the plugin was actually idle
        echo "] Verifying metrics endpoints show 'idle' status after failover..."
        sleep 3
        
        # Check handler-8-16 metrics
        HANDLER_16_METRICS=$(ssh_to_host "handler-8-16" "curl -s http://127.0.0.1:8080/metrics/v1?format=prometheus" 2>/dev/null || echo "CURL_FAILED")
        if [[ "$HANDLER_16_METRICS" != "CURL_FAILED" ]] && echo "$HANDLER_16_METRICS" | grep -q "plugin_status{name=GITHUB_HANDLER_13_L_ARM_MACOS.*} idle"; then
            echo "] ✓ handler-8-16 (GITHUB_HANDLER_13_L_ARM_MACOS) metrics status is 'idle'"
        else
            echo "] WARN: handler-8-16 metrics check - status: $(echo "$HANDLER_16_METRICS" | grep "plugin_status{name=GITHUB_HANDLER_13_L_ARM_MACOS" || echo "not found")"
        fi
        
        # Check handler-8-8 metrics
        HANDLER_8_METRICS=$(ssh_to_host "handler-8-8" "curl -s http://127.0.0.1:8080/metrics/v1?format=prometheus" 2>/dev/null || echo "CURL_FAILED")
        if [[ "$HANDLER_8_METRICS" != "CURL_FAILED" ]] && echo "$HANDLER_8_METRICS" | grep -q "plugin_status{name=GITHUB_HANDLER1_8_L_ARM_MACOS.*} idle"; then
            echo "] ✓ handler-8-8 (GITHUB_HANDLER1_8_L_ARM_MACOS) metrics status is 'idle'"
        else
            echo "] WARN: handler-8-8 metrics check - status: $(echo "$HANDLER_8_METRICS" | grep "plugin_status{name=GITHUB_HANDLER1_8_L_ARM_MACOS" || echo "not found")"
        fi
        
        record_pass
    else
        record_fail "handler-8-16 did not process the job"
    fi
else
    record_fail "workflow did not complete successfully"
fi
end_test
############

###########################
# Test: t2-dual-without-tag
###########################

begin_test "t2-dual-without-tag"
if run_workflow_and_get_logs "veertuinc" "anklet" "t2-dual-without-tag" "success"; then
    assert_remote_log_contains "handler-8-16" "queued job found"
    assert_remote_log_contains "handler-8-8" "queued job found"
    assert_logs_contain "Ankas-Virtual-Machine.local" "${WORKFLOW_LOG_FILES[0]}"
    assert_logs_contain "Ankas-Virtual-Machine.local" "${WORKFLOW_LOG_FILES[1]}"
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

####################################
# Test: t1-with-tag-1-matrix-nodes-2
####################################
begin_test "t1-with-tag-1-matrix-nodes-2"
if run_workflow_and_get_logs "veertuinc" "anklet" "t1-with-tag-1-matrix-nodes-2" "success"; then
    assert_remote_log_contains "handler-8-16" "queued job found"
    assert_remote_log_contains "handler-8-8" "queued job found"
    assert_logs_contain "Ankas-Virtual-Machine.local" "${WORKFLOW_LOG_FILES[0]}"
    assert_logs_contain "Ankas-Virtual-Machine.local" "${WORKFLOW_LOG_FILES[1]}"
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

###################
# Handling of different sized Templates
# Tests that when a host doesn't have enough resources for a second VM,
# it pauses the job and another host with available resources picks it up.
begin_test "different-sized-templates-paused-job-handoff" "success"

# Step 1: Stop handler-8-16 (we'll start it later), keep only handler-8-8 running
echo "] Stopping handler-8-16 for this test..."
stop_anklet_on_host "handler-8-16" || true
sleep 5

# Verify handler-8-8 is still running (it should be from previous tests)
echo "] Verifying handler-8-8 is running..."
if ! ssh_to_host "handler-8-8" "pgrep -f '^/tmp/anklet\$' > /dev/null" 2>/dev/null; then
    echo "] handler-8-8 not running, starting it..."
    start_anklet_on_host_background "handler-8-8"
    sleep 5
fi
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER1_8_L_ARM_MACOS"

# Step 2: Trigger t2-3c6r-1-2m-pause twice (uses 3c6r template, sleeps 2m)
# This should consume resources on handler-8-8, causing the second job to pause
echo "] Triggering t2-3c6r-1-2m-pause workflow twice..."
trigger_workflow_runs "veertuinc" "anklet" "t2-3c6r-1-2m-pause.yml" 2

# Wait for handler-8-8 to pick up a job (poll instead of fixed sleep)
echo "] Waiting for handler-8-8 to pick up a job..."
wait_elapsed=0
wait_timeout=180  # 3 minutes
wait_interval=5
while ! check_remote_log_contains "handler-8-8" "queued job found"; do
    sleep $wait_interval
    wait_elapsed=$((wait_elapsed + wait_interval))
    if [[ $wait_elapsed -ge $wait_timeout ]]; then
        echo "] ERROR: handler-8-8 did not pick up job within ${wait_timeout}s"
        record_fail "handler-8-8 did not pick up job within timeout"
        end_test
        exit 1
    fi
    echo "]] Still waiting for handler-8-8 to pick up job... (${wait_elapsed}s/${wait_timeout}s)"
done
echo "] ✓ handler-8-8 picked up a job"

# need some time for the job to be picked up by handler-8-8 and the next job to be paused
sleep 10

# Step 3: Start handler-8-16 (13-L-ARM config)
echo "] Starting handler-8-16 (should pick up paused job)..."
start_anklet_on_host_background "handler-8-16"
sleep 5
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER_13_L_ARM_MACOS"

# Step 4: Wait for handler-8-16 to pick up the paused job (poll with 5 min timeout)
echo "] Waiting for handler-8-16 to pick up paused job..."
wait_elapsed=0
wait_timeout=60  # 5 minutes
wait_interval=10
while ! check_remote_log_contains "handler-8-16" "paused job found to run"; do
    sleep $wait_interval
    wait_elapsed=$((wait_elapsed + wait_interval))
    if [[ $wait_elapsed -ge $wait_timeout ]]; then
        echo "] Timeout: handler-8-16 did not pick up paused job within ${wait_timeout}s"
        break  # Continue to assertions to report detailed failures
    fi
    echo "]] Still waiting for paused job handoff... (${wait_elapsed}s/${wait_timeout}s)"
done
if [[ $wait_elapsed -lt $wait_timeout ]]; then
    echo "] ✓ handler-8-16 picked up paused job after ${wait_elapsed}s"
fi

# Step 5: Check handler-8-8 logs for paused job behavior
echo "] Checking handler-8-8 logs for paused job behavior..."
test_passed=true

if ! check_remote_log_contains "handler-8-8" "pushed job to paused queue"; then
    echo "] FAIL: handler-8-8 did not push job to paused queue"
    test_passed=false
else
    echo "] ✓ handler-8-8 pushed job to paused queue"
    assert_remote_log_contains "handler-8-8" "pushed job to paused queue"
fi

if ! check_remote_log_contains "handler-8-8" "cannot run vm yet, waiting for enough resources to be available"; then
    echo "] FAIL: handler-8-8 did not report waiting for resources"
    test_passed=false
else
    echo "] ✓ handler-8-8 reported waiting for resources"
    assert_remote_log_contains "handler-8-8" "cannot run vm yet, waiting for enough resources to be available"
fi

# Step 6: Check handler-8-16 logs for picking up paused job
echo "] Checking handler-8-16 logs for picking up paused job..."
if ! check_remote_log_contains "handler-8-16" "paused job found to run"; then
    echo "] FAIL: handler-8-16 did not pick up paused job"
    test_passed=false
else
    echo "] ✓ handler-8-16 picked up paused job"
    assert_remote_log_contains "handler-8-16" "paused job found to run"
fi

# Wait for workflows to complete
echo "] Waiting for workflows to complete..."
wait_for_workflow_runs_to_complete "veertuinc" "anklet" "t2-3c6r-1-2m-pause" "success" 300 || true

# Step 7: Verify metrics show correct 'idle' status after paused job handoff
# This is critical - after a job transitions through paused state, the metrics
# must correctly show 'idle' when the job is complete
echo "] Verifying metrics endpoints show 'idle' status after paused job handoff..."
sleep 5

# Check handler-8-16 metrics (picked up the paused job)
HANDLER_16_METRICS=$(ssh_to_host "handler-8-16" "curl -s http://127.0.0.1:8080/metrics/v1?format=prometheus" 2>/dev/null || echo "CURL_FAILED")
if [[ "$HANDLER_16_METRICS" != "CURL_FAILED" ]]; then
    if echo "$HANDLER_16_METRICS" | grep -q "plugin_status{name=GITHUB_HANDLER_13_L_ARM_MACOS.*} idle"; then
        echo "] ✓ handler-8-16 (GITHUB_HANDLER_13_L_ARM_MACOS) metrics status is 'idle' after paused job handoff"
    else
        echo "] FAIL: handler-8-16 metrics status is NOT 'idle' after paused job handoff"
        echo "  Status found: $(echo "$HANDLER_16_METRICS" | grep "plugin_status{name=GITHUB_HANDLER_13_L_ARM_MACOS" || echo "not found")"
        test_passed=false
    fi
else
    echo "] WARN: Could not reach handler-8-16 metrics endpoint"
fi

# Check handler-8-8 metrics (was waiting for resources, then job was handed off)
HANDLER_8_METRICS=$(ssh_to_host "handler-8-8" "curl -s http://127.0.0.1:8080/metrics/v1?format=prometheus" 2>/dev/null || echo "CURL_FAILED")
if [[ "$HANDLER_8_METRICS" != "CURL_FAILED" ]]; then
    if echo "$HANDLER_8_METRICS" | grep -q "plugin_status{name=GITHUB_HANDLER1_8_L_ARM_MACOS.*} idle"; then
        echo "] ✓ handler-8-8 (GITHUB_HANDLER1_8_L_ARM_MACOS) metrics status is 'idle' after paused job handoff"
    else
        echo "] FAIL: handler-8-8 metrics status is NOT 'idle' after paused job handoff"
        echo "  Status found: $(echo "$HANDLER_8_METRICS" | grep "plugin_status{name=GITHUB_HANDLER1_8_L_ARM_MACOS" || echo "not found")"
        test_passed=false
    fi
else
    echo "] WARN: Could not reach handler-8-8 metrics endpoint"
fi

if [[ "$test_passed" == "true" ]]; then
    record_pass
else
    record_fail "paused job handoff did not work as expected"
fi
end_test
###################

############
# metrics-status-validation-multi-host
# Final comprehensive test validating that all plugins across all hosts show correct
# 'idle' status. This validates the fix for the data race bug where metrics showed
# "paused" when the plugin was actually idle (internal paused state was false).
begin_test "metrics-status-validation-multi-host"
echo "] Final validation: checking metrics endpoints on all hosts..."

METRICS_TEST_PASSED=true

# Ensure handlers are still running before checking metrics
echo "] Verifying handlers are still running..."
if ! ssh_to_host "handler-8-16" "pgrep -f '^/tmp/anklet\$' > /dev/null" 2>/dev/null; then
    echo "] handler-8-16 not running, restarting..."
    start_anklet_on_host_background "handler-8-16"
    sleep 10
fi
if ! ssh_to_host "handler-8-8" "pgrep -f '^/tmp/anklet\$' > /dev/null" 2>/dev/null; then
    echo "] handler-8-8 not running, restarting..."
    start_anklet_on_host_background "handler-8-8"
    sleep 10
fi

# Give metrics time to stabilize
sleep 5

# Check handler-8-16 metrics
echo "] Checking handler-8-16 metrics endpoint..."
HANDLER_16_METRICS=$(ssh_to_host "handler-8-16" "curl -s http://127.0.0.1:8080/metrics/v1?format=prometheus" 2>&1)
# Check if we got valid metrics data (contains plugin_status)
if echo "$HANDLER_16_METRICS" | grep -q "plugin_status"; then
    if echo "$HANDLER_16_METRICS" | grep -q "plugin_status{name=GITHUB_HANDLER_13_L_ARM_MACOS.*} idle"; then
        echo "PASS: GITHUB_HANDLER_13_L_ARM_MACOS metrics status is 'idle'"
    else
        echo "FAIL: GITHUB_HANDLER_13_L_ARM_MACOS metrics status is NOT 'idle'"
        echo "  All plugin_status lines from handler-8-16:"
        echo "$HANDLER_16_METRICS" | grep "plugin_status"
        METRICS_TEST_PASSED=false
    fi
else
    echo "FAIL: Could not get valid metrics from handler-8-16"
    echo "  Raw output:"
    echo "$HANDLER_16_METRICS"
    METRICS_TEST_PASSED=false
fi

# Check handler-8-8 metrics
echo "] Checking handler-8-8 metrics endpoint..."
HANDLER_8_METRICS=$(ssh_to_host "handler-8-8" "curl -s http://127.0.0.1:8080/metrics/v1?format=prometheus" 2>&1)
# Check if we got valid metrics data (contains plugin_status)
if echo "$HANDLER_8_METRICS" | grep -q "plugin_status"; then
    if echo "$HANDLER_8_METRICS" | grep -q "plugin_status{name=GITHUB_HANDLER1_8_L_ARM_MACOS.*} idle"; then
        echo "PASS: GITHUB_HANDLER1_8_L_ARM_MACOS metrics status is 'idle'"
    else
        echo "FAIL: GITHUB_HANDLER1_8_L_ARM_MACOS metrics status is NOT 'idle'"
        echo "  All plugin_status lines from handler-8-8:"
        echo "$HANDLER_8_METRICS" | grep "plugin_status"
        METRICS_TEST_PASSED=false
    fi
else
    echo "FAIL: Could not get valid metrics from handler-8-8"
    echo "  Raw output:"
    echo "$HANDLER_8_METRICS"
    METRICS_TEST_PASSED=false
fi

# Check receiver metrics (local)
echo "] Checking local receiver metrics endpoint..."
RECEIVER_METRICS=$(curl -s http://127.0.0.1:8080/metrics/v1?format=prometheus 2>&1)
# Check if we got valid metrics data (contains plugin_status)
if echo "$RECEIVER_METRICS" | grep -q "plugin_status"; then
    if echo "$RECEIVER_METRICS" | grep -q "plugin_status{name=GITHUB_RECEIVER1.*} idle"; then
        echo "PASS: GITHUB_RECEIVER1 metrics status is 'idle'"
    else
        echo "FAIL: GITHUB_RECEIVER1 metrics status is NOT 'idle'"
        echo "  All plugin_status lines from receiver:"
        echo "$RECEIVER_METRICS" | grep "plugin_status"
        METRICS_TEST_PASSED=false
    fi
else
    echo "FAIL: Could not get valid metrics from receiver"
    echo "  Raw output:"
    echo "$RECEIVER_METRICS"
    METRICS_TEST_PASSED=false
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
