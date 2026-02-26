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

wait_for_in_progress_run_count() {
    local owner="$1"
    local repo="$2"
    local workflow_pattern="$3"
    local desired_count="$4"
    local timeout_seconds="${5:-240}"
    local poll_interval_seconds=5
    local start_time
    start_time="$(date +%s)"
    local api_url="https://api.github.com/repos/${owner}/${repo}/actions/runs?per_page=30"

    while true; do
        local current_time
        current_time="$(date +%s)"
        local elapsed_seconds=$((current_time - start_time))
        if [[ $elapsed_seconds -ge $timeout_seconds ]]; then
            echo "ERROR: timed out waiting for ${desired_count} in_progress runs for pattern '${workflow_pattern}'"
            return 1
        fi

        local tmp_response="/tmp/${TEST_DIR_NAME}-runs-$$.json"
        local http_code
        http_code=$(
            curl -s -w "%{http_code}" \
                -H "Authorization: Bearer ${ANKLET_TEST_GITHUB_PAT}" \
                -H "Accept: application/vnd.github.v3+json" \
                -o "$tmp_response" \
                "$api_url"
        )

        if [[ "$http_code" != "200" ]]; then
            echo "WARNING: failed to fetch workflow runs (HTTP $http_code), retrying..."
            rm -f "$tmp_response"
            sleep "$poll_interval_seconds"
            continue
        fi

        local in_progress_count
        in_progress_count="$(
            jq -r --arg pattern "$workflow_pattern" '
                [
                    .workflow_runs[]
                    | select(.path | test($pattern))
                    | select(.status == "in_progress")
                ] | length
            ' "$tmp_response" 2>/dev/null || echo "0"
        )"
        rm -f "$tmp_response"

        echo "]] in_progress runs for '${workflow_pattern}': ${in_progress_count}/${desired_count} (${elapsed_seconds}s/${timeout_seconds}s)"

        if [[ "$in_progress_count" -ge "$desired_count" ]]; then
            return 0
        fi

        sleep "$poll_interval_seconds"
    done
}

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

echo "] Waiting for anklet to register with Redis..."
sleep 10
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER1"
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_RECEIVER1"

###############################################################################
# Test: overcommit on a single 13-L-ARM host (8 cores)
###############################################################################
begin_test "t2-6c14r-1-overcommit-two-runs-single-host" "success"

# Trigger two identical jobs that each need 6 cores / 14 GB RAM.
# On an 8-core host, this requires CPU overcommit to run simultaneously.
echo "] Triggering t2-6c14r-1 workflow twice..."
trigger_workflow_runs "veertuinc" "anklet" "t2-6c14r-1.yml" 2

echo "] Waiting for both runs to be in_progress at the same time..."
if ! wait_for_in_progress_run_count "veertuinc" "anklet" "t2-6c14r-1" 2 240; then
    record_fail "did not observe two simultaneous in_progress t2-6c14r-1 runs"
    end_test
    exit 1
fi
echo "] ✓ observed two simultaneous in_progress runs"

assert_remote_log_contains "handler-8-16" "skipping VM CPU and memory resource checks"

echo "] Waiting for both runs to complete..."
if wait_for_workflow_runs_to_complete "veertuinc" "anklet" "t2-6c14r-1" "success" 1200; then
    local_in_progress_log_count="$(
        ssh_to_host "handler-8-16" "grep -c 'job found registered runner and is now in progress' /tmp/anklet.log 2>/dev/null || true" \
            | tr -d '[:space:]'
    )"
    if [[ -z "$local_in_progress_log_count" ]]; then
        local_in_progress_log_count=0
    fi
    if [[ "$local_in_progress_log_count" -lt 2 ]]; then
        record_fail "expected at least 2 in_progress log entries on handler, found ${local_in_progress_log_count}"
    else
        record_pass
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
