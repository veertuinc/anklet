#!/usr/bin/env bash
set -eo pipefail
TEST_DIR_NAME="$(basename "$(pwd)")"
echo "==========================================="
echo "START $TEST_DIR_NAME/test.bash"
echo "==========================================="
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/helpers.bash"

init_test_report "${TEST_DIR_NAME}"
list_all_hosts

QUEUE_OWNER="${GITHUB_OWNER:-veertuinc}"
PLUGIN_QUEUE_KEY="anklet/jobs/github/queued/${QUEUE_OWNER}/{GITHUB_HANDLER1}"
CLEANING_QUEUE_KEY="${PLUGIN_QUEUE_KEY}/cleaning"
ORPHAN_VM_NAME="anklet-vm-crash-recovery-test"

cleanup() {
    echo ""
    echo "==========================================="
    echo "START $TEST_DIR_NAME/test.bash cleanup..."
    cancel_running_workflow_runs "veertuinc" "anklet" "t1-" || true
    pkill -INT -f '^/tmp/anklet$' 2>/dev/null || true
    stop_anklet_on_host "receiver" || true
    anka delete --yes "${ORPHAN_VM_NAME}" >/dev/null 2>&1 || true
    redis-cli -h "${REDIS_HOST}" -p "${REDIS_PORT}" -n "${REDIS_DATABASE}" \
        DEL "${PLUGIN_QUEUE_KEY}" "${CLEANING_QUEUE_KEY}" >/dev/null 2>&1 || true
    echo "END $TEST_DIR_NAME/test.bash cleanup..."
    echo "==========================================="
}
trap 'cleanup; _finalize_test_report_on_exit' EXIT

############
begin_test "onStartRun crash recovery deletes orphaned VM stuck in /cleaning"
base_template="$(anka -j list | jq -r '.body[0].uuid // empty')"
if [[ -z "${base_template}" ]]; then
    record_fail "no local Anka template found to clone orphan VM"
else
    anka delete --yes "${ORPHAN_VM_NAME}" >/dev/null 2>&1 || true
    anka clone "${base_template}" "${ORPHAN_VM_NAME}"

    redis-cli -h "${REDIS_HOST}" -p "${REDIS_PORT}" -n "${REDIS_DATABASE}" \
        DEL "${PLUGIN_QUEUE_KEY}" "${CLEANING_QUEUE_KEY}" >/dev/null
    redis-cli -h "${REDIS_HOST}" -p "${REDIS_PORT}" -n "${REDIS_DATABASE}" \
        RPUSH "${PLUGIN_QUEUE_KEY}" '{"type":"WorkflowJobPayload","action":"preparing","workflow_job":{"id":9300000001,"status":"completed"}}' >/dev/null
    redis-cli -h "${REDIS_HOST}" -p "${REDIS_PORT}" -n "${REDIS_DATABASE}" \
        RPUSH "${CLEANING_QUEUE_KEY}" "{\"type\":\"anka.VM\",\"anka_vm\":{\"name\":\"${ORPHAN_VM_NAME}\"},\"workflow_job\":{\"id\":9300000001,\"status\":\"completed\"}}" >/dev/null

    start_anklet_backgrounded_but_attached "handler-8-16"

    recovered=false
    for _ in $(seq 1 45); do
        clean_len="$(redis-cli -h "${REDIS_HOST}" -p "${REDIS_PORT}" -n "${REDIS_DATABASE}" LLEN "${CLEANING_QUEUE_KEY}")"
        if [[ "${clean_len}" == "0" ]] && ! anka show "${ORPHAN_VM_NAME}" >/dev/null 2>&1; then
            recovered=true
            break
        fi
        sleep 2
    done

    if [[ "${recovered}" != "true" ]]; then
        record_fail "orphan VM ${ORPHAN_VM_NAME} or /cleaning queue was not cleaned up on startup"
    else
        record_pass
    fi
fi
end_test
############

############
begin_test "SIGQUIT drains active job cleanly and shuts down without orphaning VM"
start_anklet_on_host_background "receiver"
assert_redis_key_exists "anklet/metrics/${QUEUE_OWNER}/GITHUB_RECEIVER1"

trigger_workflow_runs "veertuinc" "anklet" "t1-with-tag-1.yml" 1

echo "] Waiting for registered runner to be in progress before sending SIGQUIT..."
runner_in_progress=false
for _ in $(seq 1 180); do
    if grep -q "job found registered runner and is now in progress" /tmp/anklet.log 2>/dev/null; then
        runner_in_progress=true
        break
    fi
    sleep 2
done

if [[ "${runner_in_progress}" != "true" ]]; then
    record_fail "runner was not in progress before SIGQUIT window"
else
    anklet_pid="$(pgrep -f '^/tmp/anklet$' | head -n 1 || true)"
    if [[ -z "${anklet_pid}" ]]; then
        record_fail "could not find running /tmp/anklet PID"
    else
        echo "] Sending SIGQUIT to /tmp/anklet (PID ${anklet_pid})..."
        kill -QUIT "${anklet_pid}"

        wait_for_workflow_runs_to_complete "veertuinc" "anklet" "t1-with-tag-1" "success" 600

        exited=false
        for _ in $(seq 1 60); do
            if ! kill -0 "${anklet_pid}" 2>/dev/null; then
                exited=true
                break
            fi
            sleep 2
        done

        if [[ "${exited}" != "true" ]]; then
            record_fail "anklet PID ${anklet_pid} did not exit after active job finished"
        elif ! grep -q "graceful shutdown, waiting for jobs to finish..." /tmp/anklet.log; then
            record_fail "missing SIGQUIT graceful shutdown message in log"
        elif ! grep -q "anklet (and all plugins) shut down" /tmp/anklet.log; then
            record_fail "missing final shutdown message in log"
        else
            record_pass
        fi
    fi
fi
end_test
############

finalize_test_report "${TEST_DIR_NAME}"

echo "==========================================="
echo "END $TEST_DIR_NAME/test.bash"
echo "==========================================="

if [[ $TEST_FAILED -gt 0 ]]; then
    exit 1
fi
