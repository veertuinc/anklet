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

############
# t1-cancelled-failure-no-tag-in-registry
begin_test "t1-cancelled-failure-no-tag-in-registry" "cancelled"
run_workflow_and_get_logs "veertuinc" "anklet" "t1-cancelled-failure-no-tag-in-registry" "cancelled" && record_pass || record_fail "workflow did not complete as expected"
end_test
############

############
# t1-cancelled-failure-no-tag
begin_test "t1-cancelled-failure-no-tag" "cancelled"
run_workflow_and_get_logs "veertuinc" "anklet" "t1-cancelled-failure-no-tag" "cancelled" && record_pass || record_fail "workflow did not complete as expected"
end_test
############

############
# t1-cancelled-failure-no-template-in-registry
begin_test "t1-cancelled-failure-no-template-in-registry" "cancelled"
run_workflow_and_get_logs "veertuinc" "anklet" "t1-cancelled-failure-no-template-in-registry" "cancelled" && record_pass || record_fail "workflow did not complete as expected"
end_test
############

############
# t1-cancelled-failure-no-template-specified
begin_test "t1-cancelled-failure-no-template-specified" "cancelled"
run_workflow_and_get_logs "veertuinc" "anklet" "t1-cancelled-failure-no-template-specified" "cancelled" && record_pass || record_fail "workflow did not complete as expected"
end_test
############

############
# t1-cancelled-failure-no-template
begin_test "t1-cancelled-failure-no-template" "cancelled"
run_workflow_and_get_logs "veertuinc" "anklet" "t1-cancelled-failure-no-template" "cancelled" && record_pass || record_fail "workflow did not complete as expected"
end_test
############

############
# t1-failure-tag-1-in-vm
begin_test "t1-failure-tag-1-in-vm" "failure"
run_workflow_and_get_logs "veertuinc" "anklet" "t1-failure-tag-1-in-vm" "failure" && record_pass || record_fail "workflow did not complete as expected"
end_test
############

############
# t1-with-tag-1-matrix-nodes-2
begin_test "t1-with-tag-1-matrix-nodes-2" "success"
if run_workflow_and_get_logs "veertuinc" "anklet" "t1-with-tag-1-matrix-nodes-2" "success"; then
    # Add assertions here if needed
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

############
# t1-with-tag-1
begin_test "t1-with-tag-1" "success"
if run_workflow_and_get_logs "veertuinc" "anklet" "t1-with-tag-1" "success"; then
    # Add assertions here if needed
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

############
# t1-with-tag-2
begin_test "t1-with-tag-2" "success"
if run_workflow_and_get_logs "veertuinc" "anklet" "t1-with-tag-2" "success"; then
    # Add assertions here if needed
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

############
# t1-without-tag
begin_test "t1-without-tag" "success"
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
begin_test "t2-6c14r-1" "success"
if run_workflow_and_get_logs "veertuinc" "anklet" "t2-6c14r-1" "success"; then
    assert_logs_contain "Ankas-Virtual-Machine.local" "${WORKFLOW_LOG_FILES[@]}"
    assert_logs_contain "butt" "${WORKFLOW_LOG_FILES[@]}"
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

############
# t2-6c14r-2-5m-pause
begin_test "t2-6c14r-2-5m-pause" "success"
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
begin_test "t2-8c14r-1" "success"
if run_workflow_and_get_logs "veertuinc" "anklet" "t2-8c14r-1" "success"; then
    # Add assertions here if needed
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

############
# t2-12c20r-1
begin_test "t2-12c20r-1" "success"
if run_workflow_and_get_logs "veertuinc" "anklet" "t2-12c20r-1" "success"; then
    # Add assertions here if needed
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

############
# t2-12c50r-1
begin_test "t2-12c50r-1" "success"
if run_workflow_and_get_logs "veertuinc" "anklet" "t2-12c50r-1" "success"; then
    # Add assertions here if needed
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

############
# t2-20c20r-1
begin_test "t2-20c20r-1" "success"
if run_workflow_and_get_logs "veertuinc" "anklet" "t2-20c20r-1" "success"; then
    # Add assertions here if needed
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

############
# t2-dual-without-tag
begin_test "t2-dual-without-tag" "success"
if run_workflow_and_get_logs "veertuinc" "anklet" "t2-dual-without-tag" "success"; then
    # Add assertions here if needed
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

# Finalize and print test report
finalize_test_report "$TEST_DIR_NAME"

echo "==========================================="
echo "END $TEST_DIR_NAME/test.bash"
echo "==========================================="

# Exit with failure if any tests failed
if [[ $TEST_FAILED -gt 0 ]]; then
    exit 1
fi
