#!/usr/bin/env bash
set -eo pipefail
TEST_DIR_NAME="$(basename "$(pwd)")"
echo "==========================================="
echo "START $TEST_DIR_NAME/test.bash"
echo "==========================================="
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/helpers.bash"

# Written on the handler under HOST_MOUNT_HOST_DIR before dispatch; guest sees this tree under
# /Volumes/My Shared Files/anklet_mount_test/ (see README "Host-to-guest folder mounts").
readonly HOST_MOUNT_HOST_DIR="/tmp/anklet_ci_mount_host"
readonly HOST_MOUNT_MARKER_FILENAME=".anklet_ci_mount_probe"
readonly HOST_MOUNT_MARKER_BODY="anklet-ci-mount-ok"

echo "] Running $TEST_DIR_NAME test..."

init_test_report "$TEST_DIR_NAME"
list_all_hosts

cleanup() {
    echo ""
    echo "==========================================="
    echo "START $TEST_DIR_NAME/test.bash cleanup..."

    echo "] Removing host mount marker from handler ${HOST_MOUNT_HOST_DIR}..."
    ssh_to_host "handler-8-16" "rm -rf '${HOST_MOUNT_HOST_DIR}'" 2>/dev/null || true

    echo "] Cancelling running workflow runs..."
    cancel_running_workflow_runs "veertuinc" "anklet" "t1-" "t2-" "t8-" || echo "WARNING: Some workflow cancellations may have failed"

    echo "] Stopping anklet on handler..."
    stop_anklet_on_host "handler-8-16" || true

    echo "] Stopping anklet on receiver (local)..."
    pkill -INT -f '^/tmp/anklet$' 2>/dev/null || true

    echo "END $TEST_DIR_NAME/test.bash cleanup..."
    echo "==========================================="
}
trap 'cleanup; _finalize_test_report_on_exit' EXIT

echo "] Starting anklet on receiver (local)..."
start_anklet_backgrounded_but_attached "receiver"

echo "] Starting anklet on handler-8-16..."
start_anklet_on_host_background "handler-8-16"

echo "] Waiting for anklet to register with Redis..."
sleep 10

assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER1"
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_RECEIVER1"

############
begin_test "t8-host-to-guest-folder-mount"
echo "] Creating marker file on handler ${HOST_MOUNT_HOST_DIR}/${HOST_MOUNT_MARKER_FILENAME} (must appear inside guest via mount)..."
# World-readable marker: guest Actions runner (anka) must read host-owned mount contents.
ssh_to_host "handler-8-16" "rm -rf '${HOST_MOUNT_HOST_DIR}' && mkdir -p '${HOST_MOUNT_HOST_DIR}' && printf '%s' '${HOST_MOUNT_MARKER_BODY}' > '${HOST_MOUNT_HOST_DIR}/${HOST_MOUNT_MARKER_FILENAME}' && chmod 755 '${HOST_MOUNT_HOST_DIR}' && chmod 644 '${HOST_MOUNT_HOST_DIR}/${HOST_MOUNT_MARKER_FILENAME}'"
echo "] Verifying handler config includes host_to_guest_folder_mounts..."
ssh_to_host "handler-8-16" "grep -q 'host_to_guest_folder_mounts' ~/.config/anklet/config.yml && grep -q 'anklet_ci_mount_host' ~/.config/anklet/config.yml"
if run_workflow_and_get_logs "veertuinc" "anklet" "t8-host-to-guest-folder-mount" "success"; then
    assert_remote_log_contains "handler-8-16" "handling anka workflow run job"
    assert_remote_log_contains "handler-8-16" "mounted host folder into guest vm"
    assert_remote_log_contains "handler-8-16" "anklet_mount_test"
    record_pass
else
    record_fail "workflow did not complete as expected"
fi
end_test
############

finalize_test_report "$TEST_DIR_NAME"

echo "==========================================="
echo "END $TEST_DIR_NAME/test.bash"
echo "==========================================="

if [[ $TEST_FAILED -gt 0 ]]; then
    exit 1
fi
