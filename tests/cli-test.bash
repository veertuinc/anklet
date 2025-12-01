#!/usr/bin/env bash
# Usage: ./cli-test.bash [test_name]
# If test_name is provided, run only that test. Otherwise, run all tests.
# Available test names: empty, no-log-directory, no-plugins, no-plugin-name,
#                      non-existent-plugin, no-db, capacity, start-stop
set -eo pipefail
TESTS_DIR=$(cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd)
cd $TESTS_DIR/.. # make sure we're in the root

TEST_VM="d792c6f6-198c-470f-9526-9c998efe7ab4"
TEST_FAILED=0
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
if ! anka show $TEST_VM &> /dev/null; then
    echo "ERROR: VM $TEST_VM not found"
    exit 1
fi

cleanup() {
    pwd
    echo "] Cleaning up..."
    rm -rf dist || true
    rm -f ~/.config/anklet/config.yml || true
    mv ~/.config/anklet/config.yml.bak ~/.config/anklet/config.yml &> /dev/null || true
    anka delete --yes "${TEST_VM}-1" &> /dev/null || true
    anka delete --yes "${TEST_VM}-2" &> /dev/null || true
    echo "] DONE"
}
trap cleanup EXIT

log_contains() {
    local LOG_FILE="/tmp/${TEST_NAME}.log"
    if grep "$1" "$LOG_FILE" &> /dev/null; then
        echo "  - SUCCESS: Log contains '$1'"
    else
        echo "  - ERROR: Log does not contain '$1'"
        echo "Open the log file to see the full output: $LOG_FILE"
        TEST_FAILED=1
        exit 1
    fi
}

log_does_not_contain() {
    local LOG_FILE="/tmp/${TEST_NAME}.log"
    if ! grep "$1" "$LOG_FILE" &> /dev/null; then
        echo "  - SUCCESS: Log does not contain '$1'"
    else
        echo "  - ERROR: Log contains '$1'"
        echo "Open the log file to see the full output: $LOG_FILE"
        TEST_FAILED=1
        exit 1
    fi
}

run_test() {
    TEST_YML=$1
    TIMEOUT=$2
    TEST_NAME=$(basename $TEST_YML | cut -d. -f1)
    TEST_LOG_FILE="/tmp/${TEST_NAME}.log"
    export LOG_LEVEL=${LOG_LEVEL:-debug}
    TESTS=""
    while IFS= read -r line; do
        TESTS+="$line"$'\n'
    done
    echo "]] Running ${TEST_NAME} (log: ${TEST_LOG_FILE})"
    ln -s ${TESTS_DIR}/$TEST_YML ~/.config/anklet/config.yml
    if [[ $TIMEOUT =~ ^[0-9]+$ ]]; then
        $BINARY > $TEST_LOG_FILE 2>&1 &
        BINARY_PID=$!
        sleep $TIMEOUT
        kill -SIGQUIT $BINARY_PID
        wait $BINARY_PID
    else
        $BINARY > $TEST_LOG_FILE 2>&1 || true
    fi
    eval "${TESTS}"
    rm -f ~/.config/anklet/config.yml
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
            run_test cli-test-non-existent-plugin.yml <<TESTS
    log_contains "ERROR"
    log_contains "plugin not supported"
    log_contains "\"name\":\"RUNNER1\""
    log_contains "\"name\":\"RUNNER2\""
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
            run_cmd anka clone "${TEST_VM}" "${TEST_VM}-1"
            run_cmd anka start "${TEST_VM}-1"
            run_cmd anka clone "${TEST_VM}" "${TEST_VM}-2"
            run_cmd anka start "${TEST_VM}-2"
            run_test cli-test-capacity.yml <<TESTS
    log_contains "ERROR"
    log_contains "host does not have vm capacity"
    log_contains "anklet (and all plugins) shut down"
TESTS
            run_cmd anka delete --yes "${TEST_VM}-1"
            run_cmd anka delete --yes "${TEST_VM}-2"
            ;;
        "start-stop")
            run_test cli-test-start-stop.yml 10 <<TESTS
    log_does_not_contain "ERROR"
    log_contains "starting anklet"
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
    run_test_case "$test_name"
}

# Build the binary only if we have valid tests to run
build_binary() {
    echo "] Building binary..."
    goreleaser --snapshot --clean

    # find the binary
    BINARY="$(ls -1 dist/anklet_*_$(uname | tr A-Z a-z)_$(arch))"
    echo "] Using binary: $BINARY"
}

# Run tests based on the SINGLE_TEST variable
if [[ -n "$SINGLE_TEST" ]]; then
    build_binary
    run_single_test "$SINGLE_TEST"
else
    echo "] Running all tests..."
    build_binary

    run_test_case "empty"
    run_test_case "no-log-directory"
    run_test_case "no-plugins"
    run_test_case "no-plugin-name"
    run_test_case "non-existent-plugin"
    run_test_case "no-db"
    run_test_case "capacity"
    run_test_case "start-stop"
fi
