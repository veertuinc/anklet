#!/usr/bin/env bash
set -eo pipefail
TEST_DIR_NAME="$(basename "$(pwd)")"
echo "==========================================="
echo "START $TEST_DIR_NAME/test.bash"
echo "==========================================="
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/helpers.bash"

echo "] Running $TEST_DIR_NAME test..."

init_test_report "$TEST_DIR_NAME"
list_all_hosts

RECEIVER_SECRET="${RECEIVER_SECRET:-12345}"
RECEIVER_PORT="${GITHUB_RECEIVER_PORT:-54321}"
RECEIVER_URL="http://127.0.0.1:${RECEIVER_PORT}/jobs/v1/receiver"
QUEUE_OWNER="${GITHUB_OWNER:-veertuinc}"
QUEUED_KEY="anklet/jobs/github/queued/${QUEUE_OWNER}"
IN_PROGRESS_KEY="anklet/jobs/github/in_progress/${QUEUE_OWNER}"
COMPLETED_KEY="anklet/jobs/github/completed/${QUEUE_OWNER}"
PAYLOAD_DIR="/tmp/receiver-webhook-payloads"
mkdir -p "${PAYLOAD_DIR}"

cleanup() {
    echo ""
    echo "==========================================="
    echo "START $TEST_DIR_NAME/test.bash cleanup..."
    echo "] Stopping anklet on receiver (local)..."
    pkill -INT -f '^/tmp/anklet$' 2>/dev/null || true
    echo "END $TEST_DIR_NAME/test.bash cleanup..."
    echo "==========================================="
}
trap 'cleanup; _finalize_test_report_on_exit' EXIT

redis_llen() {
    redis-cli -h "${REDIS_HOST}" -p "${REDIS_PORT}" -n "${REDIS_DATABASE}" LLEN "$1" 2>/dev/null
}

redis_del() {
    redis-cli -h "${REDIS_HOST}" -p "${REDIS_PORT}" -n "${REDIS_DATABASE}" DEL "$@" >/dev/null
}

sign_payload_file() {
    local payload_file="$1"
    openssl dgst -sha256 -hmac "${RECEIVER_SECRET}" "${payload_file}" | awk '{print $2}'
}

print_receiver_log_since() {
    local before_bytes="$1"
    local log_file="${2:-/tmp/anklet.log}"
    echo "] receiver log:"
    if [[ ! -f "${log_file}" ]]; then
        echo "] (no ${log_file})"
        return
    fi
    if ! tail -c +"$((before_bytes + 1))" "${log_file}" 2>/dev/null | grep .; then
        echo "] (no new lines)"
    fi
}

post_receiver_payload() {
    local payload_file="$1"
    local event_type="${2:-workflow_job}"
    local signature="${3:-}"
    local delivery_id="${4:-manual-$(date +%s)-$RANDOM}"
    local before_bytes
    local code
    if [[ -z "${signature}" ]]; then
        signature="sha256=$(sign_payload_file "${payload_file}")"
    fi
    before_bytes=$(wc -c < /tmp/anklet.log 2>/dev/null || echo 0)
    before_bytes="${before_bytes// /}"
    code=$(curl -sS -o "${PAYLOAD_DIR}/last-body" -w "%{http_code}" \
        -X POST "${RECEIVER_URL}" \
        -H "Content-Type: application/json" \
        -H "X-GitHub-Event: ${event_type}" \
        -H "X-GitHub-Delivery: ${delivery_id}" \
        -H "X-Hub-Signature-256: ${signature}" \
        --data-binary "@${payload_file}")
    {
        echo "] POST ${payload_file} -> HTTP ${code}"
        echo "] body: $(cat "${PAYLOAD_DIR}/last-body" 2>/dev/null || true)"
        print_receiver_log_since "${before_bytes}"
    } >&2
    echo "${code}"
}

write_payload() {
    local path="$1"
    cat > "${path}"
}

echo "] Starting anklet on receiver (local)..."
start_anklet_backgrounded_but_attached "receiver"
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_RECEIVER1"

echo "] Waiting for receiver HTTP listen..."
wait_count=0
while ! grep -q '"msg":"receiver finished starting"' /tmp/anklet.log 2>/dev/null; do
    sleep 2
    wait_count=$((wait_count + 2))
    if [[ $wait_count -ge 60 ]]; then
        echo "] ERROR: receiver did not log HTTP listen"
        cat /tmp/anklet.log || true
        exit 1
    fi
done

echo "] Clearing test queue keys..."
redis_del "${QUEUED_KEY}" "${IN_PROGRESS_KEY}" "${COMPLETED_KEY}"

############
begin_test "unsigned payload is rejected"
write_payload "${PAYLOAD_DIR}/unsigned.json" <<'EOF'
{"action":"queued","workflow_job":{"id":9100000001,"labels":["anka-template:test"]}}
EOF
unsigned_code=$(post_receiver_payload "${PAYLOAD_DIR}/unsigned.json" "workflow_job" "sha256=deadbeef")
if [[ "${unsigned_code}" != "400" ]]; then
    record_fail "unsigned payload returned HTTP ${unsigned_code}, want 400"
elif ! assert_redis_key_not_exists "${QUEUED_KEY}"; then
    record_fail "unsigned payload wrote ${QUEUED_KEY}"
else
    record_pass
fi
end_test
############

############
begin_test "payload missing workflow_job.id is rejected"
write_payload "${PAYLOAD_DIR}/missing-id.json" <<'EOF'
{"action":"queued"}
EOF
missing_id_code=$(post_receiver_payload "${PAYLOAD_DIR}/missing-id.json")
if [[ "${missing_id_code}" != "400" ]]; then
    record_fail "missing id returned HTTP ${missing_id_code}, want 400"
elif ! assert_redis_key_not_exists "${QUEUED_KEY}"; then
    record_fail "missing id wrote ${QUEUED_KEY}"
else
    record_pass
fi
end_test
############

############
begin_test "optional fields enqueue simplified queued job"
write_payload "${PAYLOAD_DIR}/optional-queued.json" <<'EOF'
{"action":"queued","workflow_job":{"id":9100000002,"labels":["anka-template:test"]}}
EOF
optional_code=$(post_receiver_payload "${PAYLOAD_DIR}/optional-queued.json")
if [[ "${optional_code}" != "200" ]]; then
    record_fail "optional queued payload returned HTTP ${optional_code}, want 200"
elif ! assert_redis_list_json_contains "${QUEUED_KEY}" 0 "type" "WorkflowJobPayload"; then
    record_fail "queued job type mismatch"
elif ! assert_redis_list_json_contains "${QUEUED_KEY}" 0 "action" "queued"; then
    record_fail "queued job action mismatch"
elif ! assert_redis_list_json_contains "${QUEUED_KEY}" 0 "workflow_job.id" "9100000002"; then
    record_fail "queued job id mismatch"
else
    queued_json=$(get_redis_list_index "${QUEUED_KEY}" 0)
    owner=$(echo "${queued_json}" | jq -r '.repository.owner')
    run_id=$(echo "${queued_json}" | jq -r '.workflow_job.run_id')
    if [[ "${owner}" != "null" ]]; then
        record_fail "optional owner should be null, got ${owner}"
    elif [[ "${run_id}" != "null" ]]; then
        record_fail "optional run_id should be null, got ${run_id}"
    elif [[ "$(redis_llen "${QUEUED_KEY}")" != "1" ]]; then
        record_fail "queued list length is $(redis_llen "${QUEUED_KEY}"), want 1"
    else
        record_pass
    fi
fi
end_test
############

############
begin_test "duplicate queued job is not written again"
dup_code=$(post_receiver_payload "${PAYLOAD_DIR}/optional-queued.json")
if [[ "${dup_code}" != "200" ]]; then
    record_fail "duplicate queued payload returned HTTP ${dup_code}, want 200"
elif [[ "$(redis_llen "${QUEUED_KEY}")" != "1" ]]; then
    record_fail "duplicate queued job changed list length to $(redis_llen "${QUEUED_KEY}")"
else
    record_pass
fi
end_test
############

############
begin_test "job without anka-template is not queued"
write_payload "${PAYLOAD_DIR}/no-label.json" <<'EOF'
{"action":"queued","workflow_job":{"id":9100000003,"labels":["ubuntu-latest"]}}
EOF
no_label_code=$(post_receiver_payload "${PAYLOAD_DIR}/no-label.json")
if [[ "${no_label_code}" != "200" ]]; then
    record_fail "no-label payload returned HTTP ${no_label_code}, want 200"
elif [[ "$(redis_llen "${QUEUED_KEY}")" != "1" ]]; then
    record_fail "no-label job changed queued length to $(redis_llen "${QUEUED_KEY}")"
else
    record_pass
fi
end_test
############

############
begin_test "in_progress job is stored"
write_payload "${PAYLOAD_DIR}/in-progress.json" <<'EOF'
{"action":"in_progress","workflow_job":{"id":9100000004,"labels":["anka-template:test"]}}
EOF
in_progress_code=$(post_receiver_payload "${PAYLOAD_DIR}/in-progress.json")
if [[ "${in_progress_code}" != "200" ]]; then
    record_fail "in_progress payload returned HTTP ${in_progress_code}, want 200"
elif ! assert_redis_list_json_contains "${IN_PROGRESS_KEY}" 0 "action" "in_progress"; then
    record_fail "in_progress action mismatch"
elif ! assert_redis_list_json_contains "${IN_PROGRESS_KEY}" 0 "workflow_job.id" "9100000004"; then
    record_fail "in_progress job id mismatch"
else
    record_pass
fi
end_test
############

############
begin_test "cancelled in_progress job is not stored"
before_in_progress=$(redis_llen "${IN_PROGRESS_KEY}")
write_payload "${PAYLOAD_DIR}/cancelled.json" <<'EOF'
{"action":"in_progress","workflow_job":{"id":9100000005,"labels":["anka-template:test"],"conclusion":"cancelled"}}
EOF
cancelled_code=$(post_receiver_payload "${PAYLOAD_DIR}/cancelled.json")
after_in_progress=$(redis_llen "${IN_PROGRESS_KEY}")
if [[ "${cancelled_code}" != "200" ]]; then
    record_fail "cancelled in_progress returned HTTP ${cancelled_code}, want 200"
elif [[ "${after_in_progress}" != "${before_in_progress}" ]]; then
    record_fail "cancelled in_progress changed list length from ${before_in_progress} to ${after_in_progress}"
else
    record_pass
fi
end_test
############

############
begin_test "completed job is stored when already queued"
write_payload "${PAYLOAD_DIR}/completed.json" <<'EOF'
{"action":"completed","workflow_job":{"id":9100000002,"labels":["anka-template:test"]}}
EOF
completed_code=$(post_receiver_payload "${PAYLOAD_DIR}/completed.json")
if [[ "${completed_code}" != "200" ]]; then
    record_fail "completed payload returned HTTP ${completed_code}, want 200"
elif ! assert_redis_list_json_contains "${COMPLETED_KEY}" 0 "action" "completed"; then
    record_fail "completed action mismatch"
elif ! assert_redis_list_json_contains "${COMPLETED_KEY}" 0 "workflow_job.id" "9100000002"; then
    record_fail "completed job id mismatch"
else
    record_pass
fi
end_test
############

############
begin_test "completed job is skipped when not in a queued list"
write_payload "${PAYLOAD_DIR}/completed-unknown.json" <<'EOF'
{"action":"completed","workflow_job":{"id":9100000009,"labels":["anka-template:test"]}}
EOF
unknown_code=$(post_receiver_payload "${PAYLOAD_DIR}/completed-unknown.json")
unknown_len=$(redis_llen "${COMPLETED_KEY}")
if [[ "${unknown_code}" != "200" ]]; then
    record_fail "unknown completed payload returned HTTP ${unknown_code}, want 200"
elif [[ "${unknown_len}" != "1" ]]; then
    record_fail "unknown completed job changed completed length to ${unknown_len}"
else
    record_pass
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
