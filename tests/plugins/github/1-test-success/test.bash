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

# Set up trap to cancel orphaned workflow runs on exit (runs before test report finalization)
cleanup() {
    echo ""
    echo "==========================================="
    echo "START $TEST_DIR_NAME/test.bash cleanup..."
    cancel_running_workflow_runs "veertuinc" "anklet" "t1-" "t2-" || echo "WARNING: Some workflow cancellations may have failed"
    echo "END $TEST_DIR_NAME/test.bash cleanup..."
    echo "==========================================="
}
trap 'cleanup; _finalize_test_report_on_exit' EXIT

assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER1"
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_RECEIVER1"

############
# t1-with-tag-1
begin_test "t1-with-tag-1"
if run_workflow_and_get_logs "veertuinc" "anklet" "t1-with-tag-1" "success"; then
    assert_logs_contain "queued job found" /tmp/anklet.log
    assert_logs_contain "checkForCompletedJobs -> queued job found in pluginQueue" /tmp/anklet.log
    assert_logs_contain "handling anka workflow run job" /tmp/anklet.log
    assert_logs_contain "job updated in database" /tmp/anklet.log
    assert_logs_contain "vm has enough resources now to run; starting runner" /tmp/anklet.log
    assert_logs_contain "anka -j clone 84266873-da90-4e0d-903b-ed0233471f9f anklet-vm" /tmp/anklet.log
    assert_logs_contain "job found registered runner and is now in progress" /tmp/anklet.log
    assert_logs_contain "checkForCompletedJobs -> job is in mainCompletedQueue" /tmp/anklet.log
    assert_logs_contain "ending checkForCompletedJobs" /tmp/anklet.log
    assert_logs_contain "job is still in progress" /tmp/anklet.log
    assert_logs_contain "cleanup | anka.VM | queuedJob" /tmp/anklet.log
    assert_logs_contain "successfully deleted vm" /tmp/anklet.log
    assert_logs_contain "cleanup | removing completed job from database" /tmp/anklet.log
    assert_logs_contain "cleanup | WorkflowJobPayload | queuedJob" /tmp/anklet.log
    assert_logs_contain "job removed from queue" /tmp/anklet.log
    assert_logs_contain "GITHUB_HANDLER1" /tmp/anklet.log
    assert_logs_contain "GITHUB_HANDLER2" /tmp/anklet.log
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
    assert_json_log_contains /tmp/anklet.log "msg=handling anka workflow run job,attributes.name=GITHUB_HANDLER1"
    assert_json_log_contains /tmp/anklet.log "msg=handling anka workflow run job,attributes.name=GITHUB_HANDLER2"
    assert_logs_not_contain "anka -j registry pull" /tmp/anklet.log
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
    assert_logs_contain "anka -j registry pull" /tmp/anklet.log
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
    # Add assertions here if needed
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
    # Add assertions here if needed
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
    # Add assertions here if needed
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

############
# t2-12c20r-1 (resource-constrained - check anklet.log instead of GitHub status)
begin_test "t2-12c20r-1"
# This workflow requires more resources than the host has available
# Verify Anklet properly handles this by checking the log
if run_workflow_and_check_anklet_log "veertuinc" "anklet" "t2-12c20r-1" "host does not have enough resources to run vm"; then
    record_pass
else
    record_fail "expected resource error not found in anklet.log"
fi
end_test
############

############
# t2-12c50r-1 (resource-constrained - check anklet.log instead of GitHub status)
begin_test "t2-12c50r-1"
# This workflow requires more resources than the host has available
# Verify Anklet properly handles this by checking the log
if run_workflow_and_check_anklet_log "veertuinc" "anklet" "t2-12c50r-1" "host does not have enough resources to run vm"; then
    record_pass
else
    record_fail "expected resource error not found in anklet.log"
fi
end_test
############

############
# t2-20c20r-1 (resource-constrained - check anklet.log instead of GitHub status)
begin_test "t2-20c20r-1"
# This workflow requires more resources than the host has available
# Verify Anklet properly handles this by checking the log
if run_workflow_and_check_anklet_log "veertuinc" "anklet" "t2-20c20r-1" "host does not have enough resources to run vm"; then
    record_pass
else
    record_fail "expected resource error not found in anklet.log"
fi
end_test
############

############
# t2-dual-without-tag
begin_test "t2-dual-without-tag"
if run_workflow_and_get_logs "veertuinc" "anklet" "t2-dual-without-tag" "success"; then
    # Add assertions here if needed
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
