#!/usr/bin/env bash
# Usage: ./tests/cli-tests/run.bash [test_name]
# If test_name is provided, run only that test. Otherwise, run all tests.
# Available test names: empty, no-log-directory, no-plugins, no-plugin-name,
#                      non-existent-plugin, no-db, capacity, start-stop
set -eo pipefail
TESTS_DIR=$(cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd)
ROOT_DIR=$(cd "${TESTS_DIR}/../.." && pwd)
cd "${ROOT_DIR}"

TEST_VM_ID="84266873-da90-4e0d-903b-ed0233471f9f"
TEST_VM_NAME="${TEST_VM_ID}"
TEST_FAILED=0
TEST_ASSERTION_FAILED=0
LAST_TEST_FAILURE=""
PASSED_TESTS=()
FAILED_TESTS=()
LAST_COMMAND=""
LAST_COMMAND_OUTPUT=""

# Trap ERR to show which command failed
on_error() {
    local exit_code=$?
    local line_no=$1
    echo ""
    echo "========================================="
    echo "ERROR: Command failed with exit code $exit_code"
    echo "  Line: $line_no"
    if [[ -n "$LAST_COMMAND" ]]; then
        echo "  Command: $LAST_COMMAND"
        LAST_TEST_FAILURE="command failed: $LAST_COMMAND (exit $exit_code)"
    fi
    if [[ -n "$LAST_COMMAND_OUTPUT" ]]; then
        echo "  Output: $LAST_COMMAND_OUTPUT"
    fi
    echo "========================================="
}
trap 'on_error $LINENO' ERR

# Run a command and capture output for error reporting
run_cmd() {
    LAST_COMMAND="$*"
    LAST_COMMAND_OUTPUT=$("$@" 2>&1) || {
        local exit_code=$?
        return $exit_code
    }
}

# Check if a specific test was requested
SINGLE_TEST=""
if [[ $# -gt 0 ]]; then
    SINGLE_TEST="$1"
fi

# Validate test name if provided
validate_test_name() {
    local test_name=$1
    local test_file="${TESTS_DIR}/cli-test-${test_name}.yml"

    if [[ -f "$test_file" ]]; then
        return 0
    else
        echo "ERROR: Unknown test '$test_name'"
        echo "Available tests:"
        ls -1 ${TESTS_DIR}/cli-test-*.yml | sed 's|.*/cli-test-\(.*\)\.yml|  - \1|' | sort
        return 1
    fi
}

# Validate single test if provided
if [[ -n "$SINGLE_TEST" ]]; then
    if ! validate_test_name "$SINGLE_TEST"; then
        exit 1
    fi
    echo "] Running only test: $SINGLE_TEST"
fi

if [[ -e ~/.config/anklet/config.yml ]]; then
    mv ~/.config/anklet/config.yml ~/.config/anklet/config.yml.bak
fi

if ! anka version &> /dev/null; then
    echo "ERROR: Anka CLI not found"
    exit 1
fi

SECRETS_DIR="${SECRETS_DIR:-/tmp/secrets-core}"
ANKLET_PRIVATE_KEY="${ANKLET_PRIVATE_KEY_PATH:-${SECRETS_DIR}/anklet-private-key.pem}"
if [[ ! -f "${ANKLET_PRIVATE_KEY}" ]]; then
    ANKLET_PRIVATE_KEY="${HOME}/anklet-private-key.pem"
fi
if [[ ! -f "${ANKLET_PRIVATE_KEY}" ]]; then
    echo "ERROR: GitHub app private key not found at ${SECRETS_DIR}/anklet-private-key.pem or ${HOME}/anklet-private-key.pem"
    exit 1
fi
ln -sf "${ANKLET_PRIVATE_KEY}" /tmp/private-key.pem

detect_registry_url() {
    if [[ -n "${ANKA_REGISTRY_URL:-}" ]]; then
        echo "${ANKA_REGISTRY_URL}"
        return 0
    fi
    if [[ -n "${ANKLET_TEST_REGISTRY_URL:-}" ]]; then
        echo "${ANKLET_TEST_REGISTRY_URL}"
        return 0
    fi

    local config_file="${TESTS_DIR}/cli-test-start-stop.yml"
    if [[ -f "${config_file}" ]]; then
        local url_line
        url_line=$(grep -E "^[[:space:]]*registry_url:" "${config_file}" | head -n 1 || true)
        if [[ -n "${url_line}" ]]; then
            echo "${url_line#*:}" | xargs
            return 0
        fi
    fi

    return 1
}

resolve_template_name_from_registry() {
    local registry_url="$1"
    local vm_id="$2"
    local vm_json
    local vm_name=""

    if ! command -v curl >/dev/null 2>&1; then
        return 1
    fi

    vm_json="$(curl -fsSL "${registry_url}/registry/vm?id=${vm_id}" 2>&1)"
    if [[ -z "${vm_json}" ]]; then
        return 1
    fi

    if command -v jq >/dev/null 2>&1; then
        vm_name="$(echo "${vm_json}" | jq -r '.name // empty' 2>/dev/null)"
    elif command -v python3 >/dev/null 2>&1; then
        vm_name="$(echo "${vm_json}" | python3 -c 'import json,sys
try:
    print(json.load(sys.stdin).get("name", ""))
except (json.JSONDecodeError, ValueError):
    pass' 2>/dev/null)"
    fi

    if [[ -z "${vm_name}" ]]; then
        vm_name="$(echo "${vm_json}" | sed -n 's/.*"name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
    fi

    echo "${vm_name}"
}

ensure_template_present() {
    if anka show "${TEST_VM_NAME}" &> /dev/null; then
        return 0
    fi

    local registry_url
    registry_url="$(detect_registry_url || true)"
    if [[ -z "${registry_url}" ]]; then
        echo "ERROR: VM ${TEST_VM_ID} not found and no registry URL provided" >&2
        echo "Set ANKA_REGISTRY_URL or ANKLET_TEST_REGISTRY_URL to enable auto-pull" >&2
        exit 1
    fi

    local resolved_name
    resolved_name="$(resolve_template_name_from_registry "${registry_url}" "${TEST_VM_ID}")"
    if [[ -z "${resolved_name}" ]]; then
        echo "ERROR: Failed to resolve template name for ${TEST_VM_ID} from ${registry_url}" >&2
        echo "Check registry reachability and VM ID. Try: curl -fsSL \"${registry_url}/registry/vm?id=${TEST_VM_ID}\"" >&2
        exit 1
    fi

    TEST_VM_NAME="${resolved_name}"
    echo "] Pulling template ${TEST_VM_NAME} from registry..."
    anka registry -a "${registry_url}" pull "${TEST_VM_NAME}"

    if ! anka show "${TEST_VM_NAME}" &> /dev/null; then
        echo "ERROR: Template ${TEST_VM_NAME} not found after pull" >&2
        exit 1
    fi
}

ensure_template_present

cleanup() {
    pwd
    echo "] Cleaning up..."
    rm -rf dist || true
    rm -f /tmp/private-key.pem || true
    rm -f ~/.config/anklet/config.yml || true
    mv ~/.config/anklet/config.yml.bak ~/.config/anklet/config.yml &> /dev/null || true
    anka delete --yes "${TEST_VM_NAME}-1" &> /dev/null || true
    anka delete --yes "${TEST_VM_NAME}-2" &> /dev/null || true
    echo "] DONE"
}
trap cleanup EXIT

print_log_on_failure() {
    if [[ "${LOG_DUMPED:-0}" -eq 1 ]]; then
        return
    fi
    LOG_DUMPED=1
    local LOG_FILE="/tmp/${TEST_NAME}.log"
    echo "          log: $LOG_FILE"
    echo "          --- log contents ---"
    if [[ -f "$LOG_FILE" ]]; then
        cat "$LOG_FILE" | sed 's/^/          /'
    else
        echo "          (log file not found)"
    fi
    echo "          --- end log ---"
}

log_contains() {
    local LOG_FILE="/tmp/${TEST_NAME}.log"
    if grep "$1" "$LOG_FILE" &> /dev/null; then
        echo "    PASS: log contains '$1'"
    else
        echo "    FAIL: log does not contain '$1'"
        print_log_on_failure
        TEST_ASSERTION_FAILED=1
        LAST_TEST_FAILURE="log does not contain '$1'"
        TEST_FAILED=1
        return 1
    fi
}

log_does_not_contain() {
    local LOG_FILE="/tmp/${TEST_NAME}.log"
    if ! grep "$1" "$LOG_FILE" &> /dev/null; then
        echo "    PASS: log does not contain '$1'"
    else
        echo "    FAIL: log contains '$1'"
        print_log_on_failure
        TEST_ASSERTION_FAILED=1
        LAST_TEST_FAILURE="log contains '$1'"
        TEST_FAILED=1
        return 1
    fi
}

log_contains_at_least() {
    local min_count=$1
    local pattern=$2
    local LOG_FILE="/tmp/${TEST_NAME}.log"
    local count
    count=$(grep -c "$pattern" "$LOG_FILE" 2>/dev/null || true)
    count=${count:-0}

    if [[ "${count}" -ge "${min_count}" ]]; then
        echo "    PASS: log contains '${pattern}' at least ${min_count} time(s) (found ${count})"
    else
        echo "    FAIL: log contains '${pattern}' only ${count} time(s), expected at least ${min_count}"
        print_log_on_failure
        TEST_ASSERTION_FAILED=1
        LAST_TEST_FAILURE="log contains '${pattern}' only ${count} time(s), expected at least ${min_count}"
        TEST_FAILED=1
        return 1
    fi
}

run_test() {
    TEST_YML=$1
    STARTUP_DELAY=${2:-2}
    TEST_NAME=$(basename $TEST_YML | cut -d. -f1)
    TEST_LOG_FILE="/tmp/${TEST_NAME}.log"
    export LOG_LEVEL=${LOG_LEVEL:-debug}
    TESTS=""
    while IFS= read -r line; do
        TESTS+="$line"$'\n'
    done
    echo "]] Running ${TEST_NAME} (log: ${TEST_LOG_FILE})"
    ln -s ${TESTS_DIR}/$TEST_YML ~/.config/anklet/config.yml
    $BINARY > $TEST_LOG_FILE 2>&1 &
    BINARY_PID=$!
    echo "]]] Waiting ${STARTUP_DELAY}s for initialization..."
    sleep $STARTUP_DELAY
    # If process is still running after delay, send SIGINT to trigger graceful shutdown
    if ps -p $BINARY_PID > /dev/null 2>&1; then
        kill -SIGINT $BINARY_PID 2>/dev/null || true
    fi
    # Wait for process to exit before checking logs
    wait $BINARY_PID 2>/dev/null || true
    TEST_ASSERTION_FAILED=0
    LOG_DUMPED=0
    set +e
    eval "${TESTS}"
    set -e
    rm -f ~/.config/anklet/config.yml

    if [[ "${TEST_ASSERTION_FAILED}" -eq 1 ]]; then
        return 1
    fi
    return 0
}

record_test_result() {
    local test_name=$1
    local status=$2

    echo ""
    if [[ "${status}" == "pass" ]]; then
        PASSED_TESTS+=("${test_name}")
        echo "========================================="
        echo "PASS: ${test_name}"
        echo "========================================="
    else
        FAILED_TESTS+=("${test_name}")
        echo "========================================="
        echo "FAIL: ${test_name}"
        if [[ -n "${LAST_TEST_FAILURE}" ]]; then
            echo "  ${LAST_TEST_FAILURE}"
        fi
        echo "========================================="
    fi
}

run_test_case_with_result() {
    local test_name=$1
    local test_rc=0

    echo ""
    echo "-----------------------------------------"
    echo "TEST: ${test_name}"
    echo "-----------------------------------------"

    LAST_TEST_FAILURE=""
    set +e
    run_test_case "${test_name}"
    test_rc=$?
    set -e

    if [[ "${test_rc}" -eq 0 ]]; then
        record_test_result "${test_name}" pass
    else
        if [[ -z "${LAST_TEST_FAILURE}" ]]; then
            LAST_TEST_FAILURE="test case exited with status ${test_rc}"
        fi
        record_test_result "${test_name}" fail
    fi

    return 0
}

print_test_summary() {
    local total=$(( ${#PASSED_TESTS[@]} + ${#FAILED_TESTS[@]} ))

    echo ""
    echo "========================================="
    echo "CLI TEST SUMMARY (${total} total)"
    echo "========================================="

    if [[ ${#PASSED_TESTS[@]} -gt 0 ]]; then
        echo "Passed (${#PASSED_TESTS[@]}):"
        local test_name
        for test_name in "${PASSED_TESTS[@]}"; do
            echo "  PASS  ${test_name}"
        done
    fi

    if [[ ${#FAILED_TESTS[@]} -gt 0 ]]; then
        if [[ ${#PASSED_TESTS[@]} -gt 0 ]]; then
            echo ""
        fi
        echo "Failed (${#FAILED_TESTS[@]}):"
        for test_name in "${FAILED_TESTS[@]}"; do
            echo "  FAIL  ${test_name}"
        done
    fi

    echo "========================================="
    if [[ ${#FAILED_TESTS[@]} -gt 0 ]]; then
        echo "RESULT: FAILED (${#FAILED_TESTS[@]} of ${total})"
        return 1
    fi
    echo "RESULT: PASSED (${total}/${total})"
    return 0
}

run_test_case() {
    local test_name=$1

    case $test_name in
        "empty")
            run_test cli-test-empty.yml <<TESTS
    log_contains "ERROR"
    log_contains "unable to load config.yml"
    log_does_not_contain "starting anklet"
TESTS
            ;;
        "no-log-directory")
            run_test cli-test-no-log-directory.yml <<TESTS
    log_contains "ERROR"
    log_contains "log directory does not exist"
    log_does_not_contain "starting anklet"
TESTS
            ;;
        "no-plugins")
            run_test cli-test-no-plugins.yml <<TESTS
    log_does_not_contain "ERROR"
    log_contains "starting anklet"
    log_contains "anklet (and all plugins) shut down"
    log_contains "config\":{\"Plugins\":null"
TESTS
            ;;
        "no-plugin-name")
            run_test cli-test-no-plugin-name.yml <<TESTS
    log_contains "ERROR"
    log_contains "name is required for plugins"
    log_contains "RUNNER2"
    log_contains "plugin1"
    log_contains "plugin2"
    log_contains "mycompanyone"
    log_contains "mycompanytwo"
    log_contains "SleepInterval\":10"
    log_contains "SleepInterval\":5"
TESTS
            ;;
        "non-existent-plugin")
            run_test cli-test-non-existent-plugin.yml 30 <<TESTS
    log_contains "ERROR"
    log_contains "plugin not supported"
    log_contains "\"name\":\"RUNNER1\""
    log_contains "\"Name\":\"RUNNER2\""
    log_contains "anklet (and all plugins) shut down"
TESTS
            ;;
        "no-db")
            run_test cli-test-no-db.yml <<TESTS
    log_contains "ERROR"
    log_contains "error getting database from context"
    log_contains "\"name\":\"RUNNER1\""
    log_does_not_contain "\"name\":\"RUNNER2\""
    log_contains "anklet (and all plugins) shut down"
TESTS
            ;;
        "capacity")
            run_cmd anka clone "${TEST_VM_NAME}" "${TEST_VM_NAME}-1"
            run_cmd anka start "${TEST_VM_NAME}-1"
            run_cmd anka clone "${TEST_VM_NAME}" "${TEST_VM_NAME}-2"
            run_cmd anka start "${TEST_VM_NAME}-2"
            run_test cli-test-capacity.yml 30 <<TESTS
    log_does_not_contain "ERROR"
    log_contains_at_least 2 "host does not have vm capacity"
    log_contains_at_least 2 "starting github plugin"
    log_contains "anklet (and all plugins) shut down"
TESTS
            run_cmd anka delete --yes "${TEST_VM_NAME}-1"
            run_cmd anka delete --yes "${TEST_VM_NAME}-2"
            ;;
        "start-stop")
            # Second param = seconds to wait for initialization before sending SIGINT
            run_test cli-test-start-stop.yml 25 <<TESTS
    log_does_not_contain "ERROR"
    log_contains "starting anklet"
    log_contains "starting github plugin"
    log_contains "anklet (and all plugins) shut down"
TESTS
            ;;
        *)
            echo "ERROR: Unknown test '$test_name'"
            echo "Available tests: empty, no-log-directory, no-plugins, no-plugin-name, non-existent-plugin, no-db, capacity, start-stop"
            exit 1
            ;;
    esac
}

run_single_test() {
    local test_name=$1
    run_test_case_with_result "$test_name"
}

BINARY="/tmp/anklet"
if [[ -f "main.go" ]] && [[ -f "VERSION" ]]; then
    echo "] Building binary..."
    VERSION="$(cat VERSION)"
    go build -ldflags "-X main.version=${VERSION}" -o "${BINARY}" .
    chmod +x "${BINARY}"
elif [[ -x "${BINARY}" ]]; then
    echo "] Using pre-built binary: ${BINARY}"
else
    echo "ERROR: No anklet binary at ${BINARY} and cannot build from source (missing main.go or VERSION)" >&2
    exit 1
fi
echo "] Using binary: $BINARY"

# Run tests based on the SINGLE_TEST variable
if [[ -n "$SINGLE_TEST" ]]; then
    run_single_test "$SINGLE_TEST"
else
    echo "] Running all tests..."

    run_test_case_with_result "empty"
    run_test_case_with_result "no-log-directory"
    run_test_case_with_result "no-plugins"
    run_test_case_with_result "no-plugin-name"
    run_test_case_with_result "non-existent-plugin"
    run_test_case_with_result "no-db"
    run_test_case_with_result "capacity"
    run_test_case_with_result "start-stop"
fi

print_test_summary
exit $?
