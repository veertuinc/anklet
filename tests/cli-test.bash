#!/usr/bin/env bash
set -eo pipefail
TESTS_DIR=$(cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd)
cd $TESTS_DIR/.. # make sure we're in the root

TEST_VM="15.1-arm64"
TEST_LOG="/tmp/test-log"

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
    rm -rf dist
    rm -f ~/.config/anklet/config.yml
    anka delete --yes "${TEST_VM}-1" &> /dev/null || true
    anka delete --yes "${TEST_VM}-2" &> /dev/null || true
}
trap cleanup EXIT

log_contains() {
    if grep "$1" $TEST_LOG &> /dev/null; then
        echo "  - SUCCESS: Log contains '$1'"
    else
        echo "  - ERROR: Log does not contain '$1'"
        exit 1
    fi
}

log_does_not_contain() {
    if ! grep "$1" $TEST_LOG &> /dev/null; then
        echo "  - SUCCESS: Log does not contain '$1'"
    else
        echo "  - ERROR: Log contains '$1'"
        exit 1
    fi
}

run_test() {
    TEST_YML=$1
    TEST_NAME=$(basename $TEST_YML | cut -d. -f1)
    TESTS=""
    while IFS= read -r line; do
        TESTS+="$line"$'\n'
    done
    echo "]] Running ${TEST_NAME}"
    ln -s ${TESTS_DIR}/$TEST_YML ~/.config/anklet/config.yml
    if [[ $2 =~ ^[0-9]+$ ]]; then
        SLEEP_SECONDS=$2
        $BINARY > $TEST_LOG 2>&1 &
        BINARY_PID=$!
        sleep $SLEEP_SECONDS
        kill -SIGQUIT $BINARY_PID
        wait $BINARY_PID
    else
        $BINARY > $TEST_LOG 2>&1 || true
    fi
    eval "${TESTS}"
    rm -f ~/.config/anklet/config.yml
}

echo "] Building binary..."
goreleaser --snapshot --clean

# find the binary
BINARY="$(ls -1 dist/anklet_*_$(uname | tr A-Z a-z)_$(arch))"
echo "] Using binary: $BINARY"

run_test cli-test-empty.yml <<TESTS
    log_contains "ERROR"
    log_contains "unable to load config.yml"
    log_does_not_contain "starting anklet"
TESTS

run_test cli-test-no-log-directory.yml <<TESTS
    log_contains "ERROR"
    log_contains "log directory does not exist"
    log_does_not_contain "starting anklet"
TESTS

# test no plugins
run_test cli-test-no-plugins.yml <<TESTS
    log_does_not_contain "ERROR"
    log_contains "starting anklet"
    log_contains "anklet (and all plugins) shut down"
    log_contains "config\":{\"Plugins\":null"
TESTS

# Test no name for plugin
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

# Test non-existent plugin
run_test cli-test-non-existent-plugin.yml <<TESTS
    log_contains "ERROR"
    log_contains "plugin not found"
    log_contains "\"pluginName\":\"RUNNER1\""
    log_contains "checking for jobs...."
    log_contains "\"pluginName\":\"RUNNER2\""
    log_contains "anklet (and all plugins) shut down"
TESTS

# test no db
run_test cli-test-no-db.yml <<TESTS
    log_contains "ERROR"
    log_contains "error getting database from context"
    log_does_not_contain "checking for jobs...."
    log_contains "\"pluginName\":\"RUNNER1\""
    log_contains "\"pluginName\":\"RUNNER2\""
    log_contains "anklet (and all plugins) shut down"
TESTS

# test capacity
anka clone "${TEST_VM}" "${TEST_VM}-1" &> /dev/null
anka start "${TEST_VM}-1" &> /dev/null
anka clone "${TEST_VM}" "${TEST_VM}-2" &> /dev/null
anka start "${TEST_VM}-2" &> /dev/null
run_test cli-test-capacity.yml <<TESTS
    log_contains "ERROR"
    log_contains "host does not have vm capacity"
    log_contains "anklet (and all plugins) shut down"
TESTS
anka delete --yes "${TEST_VM}-1"
anka delete --yes "${TEST_VM}-2"

# test start and stop
run_test cli-test-start-stop.yml 4 <<TESTS
    log_does_not_contain "ERROR"
    log_contains "starting anklet"
    log_contains "checking for jobs...."
    log_contains "anklet (and all plugins) shut down"
TESTS
