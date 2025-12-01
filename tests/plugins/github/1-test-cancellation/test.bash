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
# t1-cancelled-failure-no-tag-in-registry
begin_test "t1-cancelled-failure-no-tag-in-registry"
run_workflow_and_get_logs "veertuinc" "anklet" "t1-cancelled-failure-no-tag-in-registry" "cancelled" && record_pass || record_fail "workflow did not complete as expected"
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER1"
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_RECEIVER1"
end_test
############

############
# t1-cancelled-failure-no-tag
begin_test "t1-cancelled-failure-no-tag"
run_workflow_and_get_logs "veertuinc" "anklet" "t1-cancelled-failure-no-tag" "cancelled" && record_pass || record_fail "workflow did not complete as expected"
end_test
############

############
# t1-cancelled-failure-no-template-in-registry
begin_test "t1-cancelled-failure-no-template-in-registry"
run_workflow_and_get_logs "veertuinc" "anklet" "t1-cancelled-failure-no-template-in-registry" "cancelled" && record_pass || record_fail "workflow did not complete as expected"
end_test
############

############
# t1-cancelled-failure-no-template-specified
begin_test "t1-cancelled-failure-no-template-specified"
run_workflow_and_get_logs "veertuinc" "anklet" "t1-cancelled-failure-no-template-specified" "cancelled" && record_pass || record_fail "workflow did not complete as expected"
end_test
############

############
# t1-cancelled-failure-no-template
begin_test "t1-cancelled-failure-no-template"
run_workflow_and_get_logs "veertuinc" "anklet" "t1-cancelled-failure-no-template" "cancelled" && record_pass || record_fail "workflow did not complete as expected"
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
