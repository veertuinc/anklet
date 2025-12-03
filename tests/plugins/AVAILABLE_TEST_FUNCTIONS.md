# Available Test Functions

These functions are available to `test.bash` files via `/tmp/redis-functions.bash` which is written by the test runner scripts.

---

## Remote Host Operation Functions

These functions allow a "tester" host to orchestrate and check other hosts in multi-host tests. They require `ALL_HOSTS` to be set (format: `"name:user@host|name2:user@host2"`) and `ANKLET_SSH_KEY_PATH` to point to a valid SSH key.

**⚠️ macOS Network Bug:** On macOS, when a process is started via SSH and the SSH session disconnects, the process loses its network interface context and cannot reach local network addresses (like Redis). See [go-macos-redis-client-network-bug-repro](https://github.com/NorseGaud/go-macos-redis-client-network-bug-repro). Always use `start_anklet_on_host_background` to keep SSH sessions alive.

| Function | Description |
| -------- | ----------- |
| `ssh_to_host <host_name> <command>` | Execute a command on a remote host by name. |
| `scp_to_host <host_name> <local_path> <remote_path>` | Copy a file to a remote host by name. |
| `start_anklet_on_host <host_name>` | Start anklet on a remote host (SSH stays connected, blocking call). Caller can background with `&`. |
| `start_anklet_on_host_background <host_name>` | **Recommended.** Start anklet on a remote host with SSH kept alive in background. Stores SSH PID in `/tmp/anklet-ssh-<host>.pid`. |
| `stop_anklet_on_host <host_name>` | Stop anklet on a remote host (graceful SIGINT, waits up to 30s, then SIGKILL). Also kills the background SSH session. |
| `get_anklet_log_from_host <host_name> [dest_file]` | Get anklet.log from a remote host and save locally. |
| `check_remote_log_contains <host_name> <pattern>` | Check if remote host's anklet.log contains pattern (returns true/false). |
| `assert_remote_log_contains <host_name> <pattern>` | Assert that remote host's anklet.log contains pattern (prints PASS/FAIL). |
| `list_all_hosts` | List all configured hosts from `ALL_HOSTS`. |

### Examples

```bash
# List all configured hosts
list_all_hosts

# Start anklet on handlers (background keeps SSH alive to avoid macOS network bug)
start_anklet_on_host_background "handler-8-16"
start_anklet_on_host_background "handler-8-8"

# Execute command on remote host
ssh_to_host "handler-8-16" "ls -la /tmp"

# Copy file to remote host
scp_to_host "handler-8-16" "/local/config.yml" "/tmp/config.yml"

# Check if a handler processed a job (returns true/false, no output)
if check_remote_log_contains "handler-8-16" "queued job found"; then
    echo "Handler processed a job"
fi

# Assert remote log contains pattern (prints PASS/FAIL)
assert_remote_log_contains "handler-8-16" "GITHUB_HANDLER_13_L_ARM_MACOS"

# Get anklet.log from remote host
get_anklet_log_from_host "handler-8-16" "/tmp/handler-8-16.log"

# Stop anklet when done
stop_anklet_on_host "handler-8-16"
stop_anklet_on_host "handler-8-8"
```

---

## Anklet Process Functions (Local)

These functions control anklet running on the **local** host (where the test script runs).

| Function | Description |
| -------- | ----------- |
| `check_anklet_process` | Checks that the anklet process is running. Exits with error if not running. |
| `clean_anklet [service_name]` | Cleans up anklet processes tracked in `/tmp/anklet-pids`. |
| `start_anklet_backgrounded_but_attached <service_name>` | Start anklet backgrounded but attached to terminal. |
| `start_anklet_backgrounded_but_not_attached <service_name>` | Start anklet fully backgrounded (detached). |

### Examples

```bash
# Start anklet locally (for single-host tests where tester IS the handler)
start_anklet_backgrounded_but_attached "anklet"

# Check anklet is running
check_anklet_process

# Clean up anklet processes
clean_anklet
clean_anklet "my-anklet"
```

---

## Test Report Functions

| Function | Description |
| -------- | ----------- |
| `init_test_report <test_dir_name>` | Initialize test report file for a test directory. |
| `begin_test <test_name>` | Begin a new test - call before running workflow. |
| `record_pass` | Record that the current test passed (only records once per test). |
| `record_fail <error_message>` | Record that the current test failed with an error message. |
| `end_test` | End test with cleanup - always call after assertions. |
| `finalize_test_report <test_dir_name>` | Finalize and print test report summary. |

### Example

```bash
TEST_DIR_NAME="1-test-basic"

init_test_report "${TEST_DIR_NAME}"

begin_test "${TEST_DIR_NAME}"
run_workflow_and_get_logs "veertuinc" "anklet" "${TEST_DIR_NAME}" "success"
if assert_logs_contain "Job completed" "${WORKFLOW_LOG_FILES[@]}"; then
    record_pass
else
    record_fail "Expected 'Job completed' in logs"
fi
end_test

finalize_test_report "${TEST_DIR_NAME}"
```

---

## GitHub Workflow Functions

| Function | Description |
| -------- | ----------- |
| `trigger_workflow_runs <owner> <repo> <workflow_id> [run_count]` | Trigger GitHub workflow runs via API. |
| `wait_for_workflow_runs_to_complete <owner> <repo> <pattern> [expected_conclusion] [timeout]` | Wait for workflow runs matching pattern to complete. |
| `cancel_running_workflow_runs <owner> <repo> [patterns...]` | Cancel all running workflow runs matching patterns. |
| `get_workflow_run_logs <owner> <repo> <pattern> [run_index]` | Get workflow run logs and save to files. Outputs file paths to stdout. |

**Note:** These functions require `ANKLET_TEST_GITHUB_PAT` environment variable to be set.

### Examples

```bash
# Trigger a single workflow run
trigger_workflow_runs "veertuinc" "anklet" "my-workflow.yml" 1

# Wait for workflows to complete with expected conclusion
wait_for_workflow_runs_to_complete "veertuinc" "anklet" "t1-basic" "success" 600

# Cancel all running test workflows
cancel_running_workflow_runs "veertuinc" "anklet" "t1-" "t2-"

# Get logs for most recent matching workflow run
log_files=()
while IFS= read -r line; do log_files+=("$line"); done < <(get_workflow_run_logs "veertuinc" "anklet" "t1-basic" 0)
```

---

## JSON Log Assertion Functions

These functions work with JSON log files (like `anklet.log`) and support checking specific JSON fields.

| Function | Description |
| -------- | ----------- |
| `assert_log_sequence <log_file> <conditions...>` | Assert JSON log entries appear in a specific order. |
| `assert_json_log_contains <log_file> <condition>` | Assert JSON log file contains an entry matching condition. |
| `assert_json_log_not_contains <log_file> <condition>` | Assert JSON log file does NOT contain an entry matching condition. |

### Condition Syntax

Conditions are comma-separated `key=value` pairs. Supports:
- Simple keys: `msg=starting plugin`
- Nested keys with dot notation: `attributes.name=GITHUB_HANDLER1`
- Boolean values: `attributes.finishedInitialRun=true`
- Numeric values: `attributes.runs=1`

### Examples

```bash
# Assert that plugins start in the correct order:
# 1. GITHUB_HANDLER1 starts with finishedInitialRun=false
# 2. GITHUB_HANDLER1 continues with finishedInitialRun=true
# 3. Only then does GITHUB_RECEIVER1 start
assert_log_sequence /tmp/anklet.log \
    "msg=starting github plugin,attributes.name=GITHUB_HANDLER1,attributes.finishedInitialRun=false" \
    "msg=starting github plugin,attributes.name=GITHUB_HANDLER1,attributes.finishedInitialRun=true" \
    "msg=starting plugin,attributes.name=GITHUB_RECEIVER1"

# Assert a specific log entry exists
assert_json_log_contains /tmp/anklet.log "msg=starting github plugin,attributes.finishedInitialRun=true"

# Assert no ERROR level logs
assert_json_log_not_contains /tmp/anklet.log "level=ERROR"

# Check plugin paused state
assert_json_log_contains /tmp/anklet.log "attributes.name=GITHUB_HANDLER1,attributes.paused=false"
```

---

## Log Assertion Functions

These functions work with plain text log files (like GitHub workflow logs).

| Function | Description |
| -------- | ----------- |
| `assert_logs_contain <pattern> <log_files...>` | Assert pattern exists in ANY of the log files. |
| `assert_all_logs_contain <pattern> <log_files...>` | Assert pattern exists in ALL log files. |
| `assert_logs_not_contain <pattern> <log_files...>` | Assert pattern does NOT exist in any log file. |
| `assert_logs_match_count <pattern> <count> <log_files...>` | Assert pattern appears exactly N times across all files. |
| `assert_log_file_contains <log_file> <pattern>` | Assert specific log file contains pattern. |
| `assert_log_file_not_contains <log_file> <pattern>` | Assert specific log file does NOT contain pattern. |
| `print_log_files <log_files...>` | Print log file contents for debugging. |
| `cleanup_log_files <log_files...>` | Clean up log files created by `get_workflow_run_logs`. |
| `move_log_files_to_test_dir <log_files...>` | Move log files to current test's log directory. |

### Examples

```bash

# Note: See run_workflow_and_get_logs for examples of how to get log files

# Assert pattern exists in any log file
assert_logs_contain "hostname: my-vm" "${WORKFLOW_LOG_FILES[@]}"

# Assert pattern exists in ALL log files
assert_all_logs_contain "Runner started" "${WORKFLOW_LOG_FILES[@]}"

# Assert pattern does NOT exist (error checking)
assert_logs_not_contain "FATAL ERROR" "${WORKFLOW_LOG_FILES[@]}"

# Assert exact match count
assert_logs_match_count "Job completed" 1 "${WORKFLOW_LOG_FILES[@]}"

# Assert specific file contains pattern
assert_log_file_contains "${WORKFLOW_LOG_FILES[0]}" "Starting workflow"

# Print logs for debugging
print_log_files "${WORKFLOW_LOG_FILES[@]}"

# Clean up when done
cleanup_log_files "${WORKFLOW_LOG_FILES[@]}"
```

---

## High-Level Test Helper Functions

| Function | Description |
| -------- | ----------- |
| `run_workflow_and_get_logs <owner> <repo> <workflow_name> <expected_conclusion> [run_count]` | Run workflow, wait for completion, get logs. Sets `WORKFLOW_LOG_FILES` array. |
| `run_workflow_and_check_anklet_log <owner> <repo> <workflow_name> <log_pattern>` | Run workflow and check local anklet.log for pattern (ignores GitHub status). |
| `run_workflow_with_timeout_and_check_remote_log <owner> <repo> <workflow_name> <host_name> <log_pattern> [timeout_seconds]` | Run workflow with timeout, check remote anklet.log for pattern, cancel on timeout. Default timeout: 120s. |

### Examples

```bash
# Run workflow and get logs for assertions
run_workflow_and_get_logs "veertuinc" "anklet" "t1-basic" "success"
assert_logs_contain "Job completed" "${WORKFLOW_LOG_FILES[@]}"
record_pass

# Run workflow expecting failure
run_workflow_and_get_logs "veertuinc" "anklet" "t1-should-fail" "failure"
assert_logs_contain "Expected error message" "${WORKFLOW_LOG_FILES[@]}"
record_pass

# Check local anklet.log for specific pattern (useful when workflow can't complete)
run_workflow_and_check_anklet_log "veertuinc" "anklet" "t2-resource-test" "not enough resources"
record_pass

# Check REMOTE anklet.log with timeout (for resource-constrained tests on remote handlers)
# Triggers workflow, polls remote log, cancels workflow on timeout or when pattern found
if run_workflow_with_timeout_and_check_remote_log "veertuinc" "anklet" "t2-12c20r-1" "handler-8-16" "not enough resources" 120; then
    record_pass
else
    record_fail "expected resource error not found"
fi
```

---

## Redis Functions

| Function | Description |
| -------- | ----------- |
| `list_redis_keys [pattern]` | List all keys matching pattern (default: "*"). |
| `flush_redis_database` | Flush all keys from Redis database. |
| `assert_redis_key_exists <key>` | Assert that a Redis key exists. |
| `assert_redis_key_not_exists <key>` | Assert that a Redis key does NOT exist. |
| `get_redis_hash_field <key> <field>` | Get a field from a Redis hash. |
| `get_redis_list_index <key> <index>` | Get an element from a Redis list by index. |
| `assert_redis_hash_json_contains <key> <field> <json_key> <expected_value>` | Assert hash field contains JSON with key/value. |
| `assert_redis_list_json_contains <key> <index> <json_key> <expected_value>` | Assert list element contains JSON with key/value. |
| `dump_redis_key <key> [type]` | Dump a Redis key's value for debugging. |

### Examples

```bash
# List all Redis keys
list_redis_keys

# List keys matching pattern
list_redis_keys "anklet/*"

# Assert key exists
assert_redis_key_exists "anklet/metrics/veertuinc/GITHUB_HANDLER1"

# Assert key does not exist
assert_redis_key_not_exists "anklet/old/key"

# Get hash field value
value=$(get_redis_hash_field "my:hash" "field_name")

# Assert JSON in hash field
assert_redis_hash_json_contains "anklet/job/123" "data" "status" "completed"

# Dump key for debugging
dump_redis_key "anklet/metrics/veertuinc/GITHUB_HANDLER1"
```

---

## Examples

[Complete Test Example](../github/1-test-job-statuses/test.bash)