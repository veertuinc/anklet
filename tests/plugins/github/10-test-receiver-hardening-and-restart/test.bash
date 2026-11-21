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

RECEIVER_SECRET="${RECEIVER_SECRET:-12345}"
RECEIVER_PORT="${GITHUB_RECEIVER_PORT:-54321}"
RECEIVER_BASE="http://127.0.0.1:${RECEIVER_PORT}"
RECEIVER_URL="${RECEIVER_BASE}/jobs/v1/receiver"
QUEUE_OWNER="${GITHUB_OWNER:-veertuinc}"
QUEUED_KEY="anklet/jobs/github/queued/${QUEUE_OWNER}"
SIBLING_QUEUED_KEY="anklet/jobs/github/queued/${QUEUE_OWNER}-sibling"
IN_PROGRESS_KEY="anklet/jobs/github/in_progress/${QUEUE_OWNER}"
COMPLETED_KEY="anklet/jobs/github/completed/${QUEUE_OWNER}"
PAYLOAD_DIR="/tmp/receiver-hardening-payloads"
mkdir -p "${PAYLOAD_DIR}"

cleanup() {
    echo ""
    echo "==========================================="
    echo "START $TEST_DIR_NAME/test.bash cleanup..."
    cancel_running_workflow_runs "veertuinc" "anklet" "t1-" || true
    pkill -INT -f '^/tmp/anklet$' 2>/dev/null || true
    stop_anklet_on_host "handler-8-16" || true
    redis-cli -h "${REDIS_HOST}" -p "${REDIS_PORT}" -n "${REDIS_DATABASE}" \
        DEL "${SIBLING_QUEUED_KEY}" "${QUEUED_KEY}" "${IN_PROGRESS_KEY}" "${COMPLETED_KEY}" >/dev/null 2>&1 || true
    rm -rf "${PAYLOAD_DIR}" || true
    echo "END $TEST_DIR_NAME/test.bash cleanup..."
    echo "==========================================="
}
trap 'cleanup; _finalize_test_report_on_exit' EXIT

wait_for_receiver_ready() {
    local waited=0
    while ! grep -q '"msg":"receiver finished starting"' /tmp/anklet.log 2>/dev/null; do
        sleep 2
        waited=$((waited + 2))
        if [[ ${waited} -ge 60 ]]; then
            echo "] ERROR: receiver did not start within 60s"
            cat /tmp/anklet.log || true
            exit 1
        fi
    done
}

stop_local_receiver() {
    pkill -INT -f '^/tmp/anklet$' 2>/dev/null || true
    local waited=0
    while pgrep -f '^/tmp/anklet$' >/dev/null 2>&1; do
        sleep 1
        waited=$((waited + 1))
        if [[ ${waited} -ge 20 ]]; then
            pkill -9 -f '^/tmp/anklet$' 2>/dev/null || true
            break
        fi
    done
}

sign_payload_file() {
    openssl dgst -sha256 -hmac "${RECEIVER_SECRET}" "$1" | awk '{print $2}'
}

echo "] Starting local receiver..."
start_anklet_backgrounded_but_attached "receiver"
wait_for_receiver_ready
assert_redis_key_exists "anklet/metrics/${QUEUE_OWNER}/GITHUB_RECEIVER1"

redis-cli -h "${REDIS_HOST}" -p "${REDIS_PORT}" -n "${REDIS_DATABASE}" \
    DEL "${SIBLING_QUEUED_KEY}" "${QUEUED_KEY}" "${IN_PROGRESS_KEY}" "${COMPLETED_KEY}" >/dev/null

############
begin_test "receiver rejects non-POST method and isolates ServeMux"
get_code="$(curl -sS -o /dev/null -w '%{http_code}' "${RECEIVER_URL}")"
metrics_code="$(curl -sS -o /dev/null -w '%{http_code}' "${RECEIVER_BASE}/metrics/v1?format=prometheus")"
if [[ "${get_code}" != "405" ]]; then
    record_fail "GET ${RECEIVER_URL} returned ${get_code}, want 405"
elif [[ "${metrics_code}" != "404" ]]; then
    record_fail "receiver port exposed /metrics/v1 with HTTP ${metrics_code}, want 404"
else
    record_pass
fi
end_test
############

############
begin_test "receiver rejects oversized payload (>10 MiB) with HTTP 413"
dd if=/dev/zero bs=1048576 count=11 2>/dev/null | tr '\000' 'a' > "${PAYLOAD_DIR}/oversized.json"
oversized_code="$(curl -sS -o /dev/null -w '%{http_code}' \
    -X POST "${RECEIVER_URL}" \
    -H "Content-Type: application/json" \
    -H "X-GitHub-Event: workflow_job" \
    --data-binary "@${PAYLOAD_DIR}/oversized.json")"
rm -f "${PAYLOAD_DIR}/oversized.json"
if [[ "${oversized_code}" != "413" ]]; then
    record_fail "oversized payload returned HTTP ${oversized_code}, want 413"
else
    record_pass
fi
end_test
############

############
begin_test "completed webhook does not match sibling org prefix queue"
redis-cli -h "${REDIS_HOST}" -p "${REDIS_PORT}" -n "${REDIS_DATABASE}" \
    RPUSH "${SIBLING_QUEUED_KEY}" '{"type":"WorkflowJobPayload","workflow_job":{"id":9200000099,"labels":["anka-template:test"]}}' >/dev/null
cat > "${PAYLOAD_DIR}/sibling-completed.json" <<'EOF'
{"action":"completed","workflow_job":{"id":9200000099,"labels":["anka-template:test"]}}
EOF
sig="sha256=$(sign_payload_file "${PAYLOAD_DIR}/sibling-completed.json")"
comp_code="$(curl -sS -o /dev/null -w '%{http_code}' \
    -X POST "${RECEIVER_URL}" \
    -H "Content-Type: application/json" \
    -H "X-GitHub-Event: workflow_job" \
    -H "X-Hub-Signature-256: ${sig}" \
    --data-binary "@${PAYLOAD_DIR}/sibling-completed.json")"
comp_len="$(redis-cli -h "${REDIS_HOST}" -p "${REDIS_PORT}" -n "${REDIS_DATABASE}" LLEN "${COMPLETED_KEY}")"
redis-cli -h "${REDIS_HOST}" -p "${REDIS_PORT}" -n "${REDIS_DATABASE}" DEL "${SIBLING_QUEUED_KEY}" >/dev/null
if [[ "${comp_code}" != "200" ]]; then
    record_fail "sibling completed payload returned HTTP ${comp_code}, want 200"
elif [[ "${comp_len}" != "0" ]]; then
    record_fail "sibling org prefix matched! completed queue length=${comp_len}, want 0"
else
    record_pass
fi
end_test
############

############
begin_test "receiver restart preserves in_progress queue and allows active workflow to complete"
sentinel_payload='{"type":"WorkflowJobPayload","action":"in_progress","workflow_job":{"id":9200000088,"labels":["anka-template:test"]}}'
redis-cli -h "${REDIS_HOST}" -p "${REDIS_PORT}" -n "${REDIS_DATABASE}" RPUSH "${IN_PROGRESS_KEY}" "${sentinel_payload}" >/dev/null

stop_local_receiver
: > /tmp/anklet.log
start_anklet_backgrounded_but_attached "receiver"
wait_for_receiver_ready

preserved_len="$(redis-cli -h "${REDIS_HOST}" -p "${REDIS_PORT}" -n "${REDIS_DATABASE}" LLEN "${IN_PROGRESS_KEY}")"
if [[ "${preserved_len}" != "1" ]]; then
    record_fail "receiver restart wiped in_progress queue (len=${preserved_len}, want 1)"
else
    # Clean sentinel, then run a real job and restart receiver mid-job.
    redis-cli -h "${REDIS_HOST}" -p "${REDIS_PORT}" -n "${REDIS_DATABASE}" DEL "${IN_PROGRESS_KEY}" >/dev/null
    start_anklet_on_host_background "handler-8-16"
    assert_redis_key_exists "anklet/metrics/${QUEUE_OWNER}/GITHUB_HANDLER1"

    trigger_workflow_runs "veertuinc" "anklet" "t1-with-tag-1.yml" 1
    assert_remote_log_contains "handler-8-16" "job found registered runner and is now in progress" 300

    echo "] Restarting receiver while job is in_progress..."
    stop_local_receiver
    : > /tmp/anklet.log
    start_anklet_backgrounded_but_attached "receiver"
    wait_for_receiver_ready

    wait_for_workflow_runs_to_complete "veertuinc" "anklet" "t1-with-tag-1" "success" 600
    record_pass
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
