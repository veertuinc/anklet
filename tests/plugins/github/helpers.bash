#!/usr/bin/env bash
set -aeo pipefail
PATH="/usr/local/bin:$PATH" # unable to find anka in path otherwise

# Helper functions for starting and managing anklet processes
# This will exist right next to the other bash scripts on the host machine that runs them. Be aware of paths you use.

# =============================================================================
# Test Report Tracking Functions
# =============================================================================
# These functions track test results and generate reports
# Initialize these in your test.bash before using assertions

TEST_REPORT_FILE="${TEST_REPORT_FILE:-/tmp/test-report.txt}"
TEST_RESULTS=()
TEST_PASSED=0
TEST_FAILED=0
CURRENT_TEST_NAME=""
CURRENT_TEST_EXPECTED=""
CURRENT_TEST_RECORDED=false

# Initialize test report file
# Usage: init_test_report <test_dir_name>
init_test_report() {
    local test_dir_name="$1"
    # Use fixed filename for consistency - host name is added when copying back
    TEST_REPORT_FILE="/tmp/test-report.txt"
    TEST_REPORT_DIR_NAME="$test_dir_name"
    TEST_REPORT_FINALIZED=false
    echo "TEST_REPORT_START" > "$TEST_REPORT_FILE"
    echo "test_dir=$test_dir_name" >> "$TEST_REPORT_FILE"
    echo "start_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "$TEST_REPORT_FILE"
    echo "---" >> "$TEST_REPORT_FILE"
    
    # Set trap to finalize report on exit (ensures report exists even on early failure)
    trap '_finalize_test_report_on_exit' EXIT
}

# Internal function called by trap - ensures report is always finalized
_finalize_test_report_on_exit() {
    if [[ "$TEST_REPORT_FINALIZED" != "true" ]] && [[ -n "$TEST_REPORT_FILE" ]]; then
        echo "---" >> "$TEST_REPORT_FILE"
        echo "end_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "$TEST_REPORT_FILE"
        echo "total=$((TEST_PASSED + TEST_FAILED))" >> "$TEST_REPORT_FILE"
        echo "passed=$TEST_PASSED" >> "$TEST_REPORT_FILE"
        echo "failed=$TEST_FAILED" >> "$TEST_REPORT_FILE"
        echo "TEST_REPORT_END" >> "$TEST_REPORT_FILE"
    fi
}

# Begin a test - call before running workflow
# Usage: begin_test <test_name>
begin_test() {
    CURRENT_TEST_NAME="$1"
    CURRENT_TEST_EXPECTED=""
    CURRENT_TEST_RECORDED=false
    CURRENT_TEST_LOGS_SAVED=false
    
    # Wait for anklet to finish writing from previous test
    sleep 2
    
    # Safely truncate anklet.log while preserving complete JSON lines
    # The previous test's logs were already saved by record_pass/record_fail
    # This approach keeps anklet's file descriptor valid
    _safe_truncate_anklet_log
    
    echo ""
    echo "############"
    echo "# ${CURRENT_TEST_NAME}"
}

# Record test passed (only records once per test)
# Usage: record_pass
record_pass() {
    if [[ "$CURRENT_TEST_RECORDED" == "true" ]]; then
        return 0
    fi
    CURRENT_TEST_RECORDED=true
    TEST_PASSED=$((TEST_PASSED + 1))
    echo "PASS|${CURRENT_TEST_NAME}|${CURRENT_TEST_EXPECTED}|" >> "$TEST_REPORT_FILE"
    echo "✓ PASS: ${CURRENT_TEST_NAME}"
    TEST_RESULTS+=("PASS|${CURRENT_TEST_NAME}|${CURRENT_TEST_EXPECTED}|")
    echo "############"
    
    # Save logs immediately
    _save_test_logs
    
    return 0
}

# Record test failed (only records once per test)
# Usage: record_fail <error_message>
record_fail() {
    if [[ "$CURRENT_TEST_RECORDED" == "true" ]]; then
        return 0  # Already recorded, don't fail
    fi
    CURRENT_TEST_RECORDED=true
    local error_msg="${1:-test failed}"
    TEST_FAILED=$((TEST_FAILED + 1))
    echo "FAIL|${CURRENT_TEST_NAME}|${CURRENT_TEST_EXPECTED}|${error_msg}" >> "$TEST_REPORT_FILE"
    echo "✗ FAIL: ${CURRENT_TEST_NAME} - ${error_msg}"
    TEST_RESULTS+=("FAIL|${CURRENT_TEST_NAME}|${CURRENT_TEST_EXPECTED}|${error_msg}")
    echo "############"
    
    # Save logs immediately
    _save_test_logs
    
    # Return 0 so script continues to end_test and other tests
    # The failure is already recorded in TEST_FAILED counter
    return 0
}

# Internal function to save test logs
# Safely truncate anklet.log while preserving JSON integrity
# This copies complete lines and truncates in place (keeps file descriptor valid)
_safe_truncate_anklet_log() {
    local log_file="/tmp/anklet.log"
    
    if [[ ! -f "$log_file" ]]; then
        return 0
    fi
    
    # Get current file size
    local original_size
    original_size=$(wc -c < "$log_file" 2>/dev/null || echo 0)
    
    if [[ "$original_size" -eq 0 ]]; then
        return 0
    fi
    
    # Copy to temp file
    local temp_file="/tmp/anklet.log.truncate.tmp"
    cp "$log_file" "$temp_file" 2>/dev/null || return 0
    
    # Find the position of the last newline (last complete line)
    # This ensures we don't cut a JSON line in half
    local last_newline_pos
    if command -v gawk >/dev/null 2>&1; then
        # Use gawk if available for byte-accurate position
        last_newline_pos=$(gawk 'BEGIN{RS="\n"; pos=0} {pos+=length($0)+1} END{print pos}' "$temp_file" 2>/dev/null)
    else
        # Fallback: count bytes of complete lines only
        # This reads only lines that end with newline
        last_newline_pos=$(awk '{sum+=length($0)+1} END{print sum}' "$temp_file" 2>/dev/null || echo 0)
    fi
    
    # If we couldn't determine position, just truncate everything
    if [[ -z "$last_newline_pos" ]] || [[ "$last_newline_pos" -eq 0 ]]; then
        : > "$log_file" 2>/dev/null || true
        rm -f "$temp_file" 2>/dev/null
        return 0
    fi
    
    # Truncate the original file in place
    # This keeps anklet's file descriptor valid
    : > "$log_file" 2>/dev/null || true
    
    rm -f "$temp_file" 2>/dev/null
    return 0
}

_save_test_logs() {
    # Temporarily disable exit on error - we don't want log saving to abort the test
    set +e
    
    echo "[DEBUG] _save_test_logs: Saving logs for test: ${CURRENT_TEST_NAME}"
    
    # Create test-specific log directory
    CURRENT_TEST_LOG_DIR="/tmp/test-logs/${CURRENT_TEST_NAME}"
    if ! mkdir -p "${CURRENT_TEST_LOG_DIR}"; then
        echo "[DEBUG] _save_test_logs: ERROR - Failed to create directory: ${CURRENT_TEST_LOG_DIR}"
        set -e
        return
    fi
    echo "[DEBUG] _save_test_logs: Created directory: ${CURRENT_TEST_LOG_DIR}"
    
    # List what's in /tmp/test-logs now
    # echo "[DEBUG] _save_test_logs: Contents of /tmp/test-logs:"
    # ls -la /tmp/test-logs/ 2>/dev/null || echo "[DEBUG] Could not list /tmp/test-logs"
    
    # Wait briefly for anklet to finish writing current entries
    sleep 1
    
    # Copy anklet.log - only complete lines (to avoid partial JSON)
    if [[ -f /tmp/anklet.log ]] && [[ -s /tmp/anklet.log ]]; then
        # Use grep to only copy complete lines (those ending with newline)
        # This prevents saving partial JSON that would cause parse errors
        grep -a '' /tmp/anklet.log > "${CURRENT_TEST_LOG_DIR}/anklet.log" 2>/dev/null || true
        
        # Validate JSON lines - remove any that don't parse
        if command -v jq >/dev/null 2>&1; then
            local temp_valid="${CURRENT_TEST_LOG_DIR}/anklet.log.valid"
            while IFS= read -r line; do
                if [[ -n "$line" ]] && echo "$line" | jq -e . >/dev/null 2>&1; then
                    echo "$line"
                fi
            done < "${CURRENT_TEST_LOG_DIR}/anklet.log" > "$temp_valid" 2>/dev/null
            mv "$temp_valid" "${CURRENT_TEST_LOG_DIR}/anklet.log" 2>/dev/null || true
        fi
        
        echo "[DEBUG] _save_test_logs: Copied anklet.log (valid JSON lines only)"
    else
        echo "[DEBUG] _save_test_logs: No anklet.log to copy (empty or missing)"
    fi
    
    # Move workflow logs to test folder
    echo "[DEBUG] _save_test_logs: Moving ${#WORKFLOW_LOG_FILES[@]} workflow log files"
    move_log_files_to_test_dir "${WORKFLOW_LOG_FILES[@]}"
    
    # Mark that logs have been saved for this test
    CURRENT_TEST_LOGS_SAVED=true
    echo "[DEBUG] _save_test_logs: Done saving logs for ${CURRENT_TEST_NAME}"
    echo "[DEBUG] _save_test_logs: Test logs at ${CURRENT_TEST_LOG_DIR} - will be synced to parent host"
    
    # Re-enable exit on error
    set -e
}

# End test with cleanup (always call after assertions)
# Usage: end_test
end_test() {
    check_anklet_process
    
    # Save logs if not already saved by record_pass/record_fail
    if [[ "${CURRENT_TEST_LOGS_SAVED}" != "true" ]]; then
        _save_test_logs
    fi
    
    # Reset for next test
    CURRENT_TEST_LOGS_SAVED=false
}

# Finalize and print test report
# Usage: finalize_test_report <test_dir_name>
finalize_test_report() {
    local test_dir_name="$1"
    TEST_REPORT_FINALIZED=true
    echo "---" >> "$TEST_REPORT_FILE"
    echo "end_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "$TEST_REPORT_FILE"
    echo "total=$((TEST_PASSED + TEST_FAILED))" >> "$TEST_REPORT_FILE"
    echo "passed=$TEST_PASSED" >> "$TEST_REPORT_FILE"
    echo "failed=$TEST_FAILED" >> "$TEST_REPORT_FILE"
    echo "TEST_REPORT_END" >> "$TEST_REPORT_FILE"

    echo ""
    echo "==========================================="
    echo "TEST SUMMARY: $test_dir_name"
    echo "==========================================="
    echo "Total:  $((TEST_PASSED + TEST_FAILED))"
    echo "Passed: $TEST_PASSED"
    echo "Failed: $TEST_FAILED"
    echo ""
    echo "Results:"
    for result in "${TEST_RESULTS[@]}"; do
        IFS='|' read -r status workflow expected error <<< "$result"
        if [[ "$status" == "PASS" ]]; then
            echo "  ✓ ${workflow} (${expected})"
        else
            echo "  ✗ ${workflow} (${expected}) - ${error}"
        fi
    done
    echo "==========================================="
}

# =============================================================================

# Trigger GitHub workflow runs via API
# Usage: trigger_workflow_runs <owner> <repo> <workflow_id> [run_count]
# workflow_id: The workflow filename (e.g., "my-workflow.yml")
# Requires: ANKLET_TEST_TRIGGER_GITHUB_PAT environment variable to be set
trigger_workflow_runs() {
    local owner="$1"
    local repo="$2"
    local workflow_id="$3"
    local run_count="${4:-1}"

    if [[ -z "$owner" ]]; then
        echo "ERROR: owner is required (arg 1)"
        return 1
    fi
    if [[ -z "$repo" ]]; then
        echo "ERROR: repo is required (arg 2)"
        return 1
    fi
    if [[ -z "$workflow_id" ]]; then
        echo "ERROR: workflow_id is required (arg 3)"
        return 1
    fi
    if [[ -z "${ANKLET_TEST_TRIGGER_GITHUB_PAT}" ]]; then
        echo "ERROR: ANKLET_TEST_TRIGGER_GITHUB_PAT environment variable is required"
        return 1
    fi

    echo "Triggering workflow runs: owner=$owner repo=$repo workflow_id=$workflow_id count=$run_count"

    for ((i=1; i<=run_count; i++)); do
        echo "[run $i/$run_count] Triggering workflow $workflow_id"
        local response
        response=$(curl -s -w "\n%{http_code}" \
            -X POST \
            -H "Authorization: Bearer ${ANKLET_TEST_TRIGGER_GITHUB_PAT}" \
            -H "Accept: application/vnd.github.v3+json" \
            "https://api.github.com/repos/${owner}/${repo}/actions/workflows/${workflow_id}/dispatches" \
            -d '{"ref":"main"}')
        
        local http_code
        http_code=$(echo "$response" | tail -n1)
        local body
        body=$(echo "$response" | sed '$d')
        
        if [[ "$http_code" != "204" ]]; then
            echo "ERROR: Failed to trigger $workflow_id (HTTP $http_code): $body"
            return 1
        fi
        sleep 1
    done
    
    echo "Workflow trigger complete"
}

# Wait for workflow runs to complete
# Usage: wait_for_workflow_runs_to_complete <owner> <repo> <workflow_pattern> [timeout_seconds] [expected_conclusion]
# expected_conclusion: success, failure, cancelled, neutral, skipped, stale, timed_out, action_required
#                      If not specified, any completion is accepted (but failure still returns error)
# Requires: ANKLET_TEST_TRIGGER_GITHUB_PAT environment variable to be set
wait_for_workflow_runs_to_complete() {
    sleep 5

    local owner="$1"
    local repo="$2"
    local workflow_pattern="$3"
    local expected_conclusion="${4:-}"  # Optional: success, failure, cancelled, etc.
    local timeout_seconds="${5:-1200}"  # Default 20 minutes

    if [[ -z "$owner" ]]; then
        echo "ERROR: owner is required (arg 1)"
        return 1
    fi
    if [[ -z "$repo" ]]; then
        echo "ERROR: repo is required (arg 2)"
        return 1
    fi
    if [[ -z "$workflow_pattern" ]]; then
        echo "ERROR: workflow_pattern is required (arg 3)"
        return 1
    fi
    if [[ -z "${ANKLET_TEST_TRIGGER_GITHUB_PAT}" ]]; then
        echo "ERROR: ANKLET_TEST_TRIGGER_GITHUB_PAT environment variable is required"
        return 1
    fi
    
    # Validate expected_conclusion if provided
    if [[ -n "$expected_conclusion" ]]; then
        case "$expected_conclusion" in
            success|failure|cancelled|neutral|skipped|stale|timed_out|action_required)
                ;;
            *)
                echo "ERROR: Invalid expected_conclusion '$expected_conclusion'. Valid values: success, failure, cancelled, neutral, skipped, stale, timed_out, action_required"
                return 1
                ;;
        esac
    fi

    local conclusion_msg=""
    [[ -n "$expected_conclusion" ]] && conclusion_msg=" expecting conclusion=$expected_conclusion"
    echo "Waiting for workflow runs to complete: owner=$owner repo=$repo pattern=$workflow_pattern timeout=${timeout_seconds}s${conclusion_msg}"

    local start_time
    start_time=$(date +%s)
    local poll_interval=10
    local api_url="https://api.github.com/repos/${owner}/${repo}/actions/runs?per_page=5"
    
    echo "[DEBUG] API URL: $api_url"

    while true; do
        local current_time
        current_time=$(date +%s)
        local elapsed=$((current_time - start_time))

        if [[ $elapsed -ge $timeout_seconds ]]; then
            echo "ERROR: Timeout waiting for workflow runs to complete after ${timeout_seconds}s"
            return 1
        fi

        # Get recent workflow runs - use temp file to avoid "argument list too long"
        local tmp_response="/tmp/workflow_runs_$$.json"
        local http_code
        
        http_code=$(curl -s -w "%{http_code}" \
            -H "Authorization: Bearer ${ANKLET_TEST_TRIGGER_GITHUB_PAT}" \
            -H "Accept: application/vnd.github.v3+json" \
            -o "$tmp_response" \
            "$api_url")

        echo "[DEBUG] HTTP response code: $http_code"

        if [[ "$http_code" != "200" ]]; then
            echo "WARNING: Failed to get workflow runs (HTTP $http_code)"
            cat "$tmp_response" 2>/dev/null || true
            rm -f "$tmp_response"
            sleep "$poll_interval"
            continue
        fi

        # Debug: Show total runs and workflow paths
        local total_runs
        total_runs=$(jq '.workflow_runs | length' "$tmp_response" 2>/dev/null || echo "0")
        echo "[DEBUG] Total workflow runs returned: $total_runs"
        
        local all_paths
        all_paths=$(jq -r '.workflow_runs[0:10] | .[].path // "null"' "$tmp_response" 2>/dev/null || true)
        echo "[DEBUG] First 10 workflow paths:"
        while read -r path; do
            echo "[DEBUG]   - $path"
        done <<< "$all_paths"
        
        # Debug: Show runs matching the pattern
        local matching_runs
        matching_runs=$(jq -r --arg pattern "$workflow_pattern" '
            [.workflow_runs[] | select(.path | test($pattern))] | length
        ' "$tmp_response" 2>/dev/null || echo "0")
        echo "[DEBUG] Runs matching pattern '$workflow_pattern': $matching_runs"

        # Find workflow runs matching our pattern that are in progress or queued
        local pending_runs
        pending_runs=$(jq -r --arg pattern "$workflow_pattern" '
            .workflow_runs[]
            | select(.path | test($pattern))
            | select(.status == "in_progress" or .status == "queued" or .status == "pending")
            | "\(.id) \(.status) \(.name)"
        ' "$tmp_response" 2>/dev/null || true)

        local pending_count=0
        if [[ -n "$pending_runs" ]]; then
            pending_count=$(wc -l <<< "$pending_runs" | tr -d ' ')
        fi
        echo "[DEBUG] Pending runs found: $pending_count"

        if [[ -z "$pending_runs" ]]; then
            echo "All matching workflow runs have completed"
            
            # Show final status of recent matching runs
            local completed_runs
            completed_runs=$(jq -r --arg pattern "$workflow_pattern" '
                [.workflow_runs[]
                | select(.path | test($pattern))]
                | .[0:5]
                | .[]
                | "\(.conclusion // "unknown"): \(.name) (#\(.run_number))"
            ' "$tmp_response" 2>/dev/null || true)
            
            if [[ -n "$completed_runs" ]]; then
                echo "Recent completed runs:"
                echo "$completed_runs"
            fi
            
            # Get the most recent matching run's conclusion
            local actual_conclusion
            actual_conclusion=$(jq -r --arg pattern "$workflow_pattern" '
                [.workflow_runs[]
                | select(.path | test($pattern))]
                | .[0].conclusion // "unknown"
            ' "$tmp_response" 2>/dev/null || echo "unknown")
            
            echo "[DEBUG] Most recent run conclusion: $actual_conclusion"
            
            rm -f "$tmp_response"
            
            echo "Waiting 10 seconds to ensure workflow completion is registered..."
            sleep 10
            
            # If expected_conclusion is specified, check it matches
            if [[ -n "$expected_conclusion" ]]; then
                if [[ "$actual_conclusion" == "$expected_conclusion" ]]; then
                    echo "Workflow completed with expected conclusion: $expected_conclusion"
                    return 0
                else
                    echo "ERROR: Expected conclusion '$expected_conclusion' but got '$actual_conclusion'"
                    return 1
                fi
            fi
            
            # Default behavior: fail if conclusion is failure
            if [[ "$actual_conclusion" == "failure" ]]; then
                echo "WARNING: Workflow run failed"
                return 1
            fi
            
            return 0
        fi

        echo "[${elapsed}s] Waiting for runs to complete:"
        while read -r line; do
            echo "  - $line"
        done <<< "$pending_runs"

        rm -f "$tmp_response"
        sleep "$poll_interval"
    done
}

# check if the anklet process is running
check_anklet_process() {
    echo "Checking for anklet process..."
    sleep 3
    if pgrep -f "^/tmp/anklet" > /dev/null; then
        echo "Anklet process is still running"
    else
        echo "Anklet process is not running"
        echo "===== /tmp/anklet.log contents ====="
        tail -15 /tmp/anklet.log
        echo "===================================="
        exit 1
    fi
}

# Function to check for duplicate JSON keys in the log file on a remote server
# Usage: check_duplicate_keys <host>
# host is required (e.g., user@hostname)
check_duplicate_keys() {
    local host="$1"
    local log_file="/tmp/anklet.log"

    if [[ -z "$host" ]]; then
        echo "ERROR: host parameter is required for check_duplicate_keys"
        return 1
    fi

    if ! ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=60 -i "${BUZYFORM_VM_SSH_KEY_PATH}" "$host" "test -f $log_file"; then
        echo "ERROR: log file $log_file does not exist"
        return 0
    fi

    echo "Checking for duplicate JSON keys in log file on $host..."

    # shellcheck disable=SC2087
    ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=60 -i "${BUZYFORM_VM_SSH_KEY_PATH}" "$host" "LOG_FILE='$log_file' python3" << 'EOF'
import sys
import re
import os

def detect_duplicate_keys_in_log(log_file):
    """Check for duplicate keys within each JSON object in log file"""
    lines_with_duplicates = []

    try:
        with open(log_file, 'r') as f:
            for line_num, line in enumerate(f, 1):
                original_line = line.strip()
                if not original_line:
                    continue

                duplicates = detect_duplicates_in_line(original_line)
                if duplicates:
                    lines_with_duplicates.append((line_num, original_line, duplicates))

    except Exception as e:
        print('Error reading log file: {}'.format(e), file=sys.stderr)
        return False

    if lines_with_duplicates:
        print('\n❌ Duplicate JSON keys found within individual objects:', file=sys.stderr)
        print('=' * 80, file=sys.stderr)

        for line_num, original_line, dups in lines_with_duplicates:
            print('📍 Line {} - Duplicate keys: {}'.format(line_num, ', '.join(sorted(dups))), file=sys.stderr)
            print('   Object: {}'.format(original_line), file=sys.stderr)
            print('', file=sys.stderr)

        print('📊 Summary: {} lines contain duplicate keys'.format(len(lines_with_duplicates)), file=sys.stderr)
        return False
    else:
        print('✅ No duplicate JSON keys found within individual objects.')
        return True

def detect_duplicates_in_line(json_str):
    """Detect duplicate keys by parsing JSON structure and checking each object individually"""
    try:
        import json

        def check_object_for_duplicates(obj, path="root"):
            """Check a single object for duplicate keys"""
            if not isinstance(obj, dict):
                return []

            duplicates = []
            seen_keys = set()

            for key in obj.keys():
                if key in seen_keys:
                    duplicates.append(key)
                else:
                    seen_keys.add(key)

            return duplicates

        # Parse the JSON and check each object individually
        parsed = json.loads(json_str)
        all_duplicates = []

        # Check root level
        root_dups = check_object_for_duplicates(parsed, "root")
        all_duplicates.extend(root_dups)

        # Recursively check nested objects
        def check_nested(obj, path="root"):
            dups = []
            if isinstance(obj, dict):
                for key, value in obj.items():
                    current_path = "{}.{}".format(path, key)
                    if isinstance(value, dict):
                        nested_dups = check_object_for_duplicates(value, current_path)
                        dups.extend(nested_dups)
                        deeper_dups = check_nested(value, current_path)
                        dups.extend(deeper_dups)
                    elif isinstance(value, list):
                        for i, item in enumerate(value):
                            if isinstance(item, dict):
                                array_path = "{}[{}]".format(current_path, i)
                                array_dups = check_object_for_duplicates(item, array_path)
                                dups.extend(array_dups)
                                array_nested_dups = check_nested(item, array_path)
                                dups.extend(array_nested_dups)
            return dups

        nested_duplicates = check_nested(parsed)
        all_duplicates.extend(nested_duplicates)

        return list(set(all_duplicates))

    except (json.JSONDecodeError, ValueError):
        return []

if __name__ == '__main__':
    log_file = os.environ.get('LOG_FILE')
    if not log_file:
        print("ERROR: LOG_FILE environment variable not set", file=sys.stderr)
        sys.exit(1)

    success = detect_duplicate_keys_in_log(log_file)
    sys.exit(0 if success else 1)
EOF
    return $?
}

# Clean up any running anklet processes tracked in /tmp/anklet-pids
# Be careful with this - it will potentially send two SIGINT signals to the process
clean_anklet() {
    local service_name="${1:-anklet}"
    echo "] Cleaning up $service_name..."
    
    # Kill anklet processes first
    if [[ -f /tmp/anklet-pids ]]; then
        for pid in $(cat /tmp/anklet-pids); do
            if ps -p $pid > /dev/null 2>&1; then
                echo "Process info for PID $pid:"
                ps -p $pid -o pid,ppid,comm,args
                echo "Sending SIGTERM to PID $pid..."
                kill -SIGTERM $pid || true
                while ps -p $pid > /dev/null 2>&1; do
                    echo "Waiting for process $pid to exit..."
                    sleep 1
                done
            else
                echo "Process with PID $pid is not running"
            fi
        done
        rm -f /tmp/anklet-pids
    fi
    
    # Note: check_duplicate_keys should be called from the orchestration machine
    # after cleanup completes, using: check_duplicate_keys "user@host"
}

# Get workflow run logs from GitHub and save to files
# Usage: get_workflow_run_logs <owner> <repo> <workflow_pattern> [run_index]
# workflow_pattern: Regex pattern to match workflow path (e.g., "1-test-basic")
# run_index: Which matching run to get logs for (0 = most recent, default)
# Outputs: File paths (one per line) to stdout for each job's logs
# Capture into array (Bash 3.x compatible):
#   log_files=()
#   while IFS= read -r line; do log_files+=("$line"); done < <(get_workflow_run_logs ...)
# Requires: ANKLET_TEST_TRIGGER_GITHUB_PAT environment variable to be set
# Note: Call cleanup_log_files "${log_files[@]}" when done
get_workflow_run_logs() {
    local owner="$1"
    local repo="$2"
    local workflow_pattern="$3"
    local run_index="${4:-0}"

    if [[ -z "$owner" ]]; then
        echo "ERROR: owner is required (arg 1)" >&2
        return 1
    fi
    if [[ -z "$repo" ]]; then
        echo "ERROR: repo is required (arg 2)" >&2
        return 1
    fi
    if [[ -z "$workflow_pattern" ]]; then
        echo "ERROR: workflow_pattern is required (arg 3)" >&2
        return 1
    fi
    if [[ -z "${ANKLET_TEST_TRIGGER_GITHUB_PAT}" ]]; then
        echo "ERROR: ANKLET_TEST_TRIGGER_GITHUB_PAT environment variable is required" >&2
        return 1
    fi

    echo "Getting workflow run logs: owner=$owner repo=$repo pattern=$workflow_pattern run_index=$run_index" >&2

    # Get recent workflow runs
    local tmp_runs="/tmp/workflow_runs_logs_$$.json"
    local api_url="https://api.github.com/repos/${owner}/${repo}/actions/runs?per_page=20"
    
    local http_code
    http_code=$(curl -s -w "%{http_code}" \
        -H "Authorization: Bearer ${ANKLET_TEST_TRIGGER_GITHUB_PAT}" \
        -H "Accept: application/vnd.github.v3+json" \
        -o "$tmp_runs" \
        "$api_url")

    if [[ "$http_code" != "200" ]]; then
        echo "ERROR: Failed to get workflow runs (HTTP $http_code)" >&2
        cat "$tmp_runs" >&2 2>/dev/null || true
        rm -f "$tmp_runs"
        return 1
    fi

    # Find the run at the specified index matching our pattern
    local run_id
    run_id=$(jq -r --arg pattern "$workflow_pattern" --argjson idx "$run_index" '
        [.workflow_runs[] | select(.path | test($pattern))]
        | .[$idx].id // empty
    ' "$tmp_runs")

    if [[ -z "$run_id" ]]; then
        echo "ERROR: No workflow run found matching pattern '$workflow_pattern' at index $run_index" >&2
        rm -f "$tmp_runs"
        return 1
    fi

    local run_name
    run_name=$(jq -r --arg pattern "$workflow_pattern" --argjson idx "$run_index" '
        [.workflow_runs[] | select(.path | test($pattern))]
        | .[$idx].name // "unknown"
    ' "$tmp_runs")

    local run_conclusion
    run_conclusion=$(jq -r --arg pattern "$workflow_pattern" --argjson idx "$run_index" '
        [.workflow_runs[] | select(.path | test($pattern))]
        | .[$idx].conclusion // "unknown"
    ' "$tmp_runs")

    echo "Found workflow run: id=$run_id name='$run_name' conclusion=$run_conclusion" >&2
    rm -f "$tmp_runs"

    # Get jobs for this run
    local tmp_jobs="/tmp/workflow_jobs_$$.json"
    local jobs_url="https://api.github.com/repos/${owner}/${repo}/actions/runs/${run_id}/jobs"
    
    http_code=$(curl -s -w "%{http_code}" \
        -H "Authorization: Bearer ${ANKLET_TEST_TRIGGER_GITHUB_PAT}" \
        -H "Accept: application/vnd.github.v3+json" \
        -o "$tmp_jobs" \
        "$jobs_url")

    if [[ "$http_code" != "200" ]]; then
        echo "ERROR: Failed to get jobs for run $run_id (HTTP $http_code)" >&2
        cat "$tmp_jobs" >&2 2>/dev/null || true
        rm -f "$tmp_jobs"
        return 1
    fi

    # Get all job IDs
    local job_ids
    job_ids=$(jq -r '.jobs[].id' "$tmp_jobs")

    if [[ -z "$job_ids" ]]; then
        echo "ERROR: No jobs found for run $run_id" >&2
        rm -f "$tmp_jobs"
        return 1
    fi

    local job_count
    job_count=$(jq '.jobs | length' "$tmp_jobs")
    echo "Found $job_count job(s) in run $run_id" >&2

    # Create logs directory for this run
    local logs_dir="/tmp/workflow_logs_${run_id}"
    mkdir -p "$logs_dir"

    # Download logs for each job and save to files
    while read -r job_id; do
        local job_name
        job_name=$(jq -r --argjson jid "$job_id" '.jobs[] | select(.id == $jid) | .name' "$tmp_jobs")
        
        # Sanitize job name for filename (replace spaces and special chars with underscores)
        local safe_job_name
        safe_job_name=$(echo "$job_name" | tr ' /:' '_' | tr -cd '[:alnum:]_-')
        
        local log_file="${logs_dir}/${safe_job_name}_${job_id}.log"
        
        echo "Downloading logs for job: id=$job_id name='$job_name' -> $log_file" >&2
        
        local log_url="https://api.github.com/repos/${owner}/${repo}/actions/jobs/${job_id}/logs"
        
        # GitHub returns a 302 redirect to the actual log content
        http_code=$(curl -s -w "%{http_code}" -L \
            -H "Authorization: Bearer ${ANKLET_TEST_TRIGGER_GITHUB_PAT}" \
            -H "Accept: application/vnd.github.v3+json" \
            -o "$log_file" \
            "$log_url")

        if [[ "$http_code" != "200" ]]; then
            echo "WARNING: Failed to get logs for job $job_id (HTTP $http_code)" >&2
            rm -f "$log_file"
            continue
        fi

        # Output the file path to stdout (for array capture)
        echo "$log_file"
        
    done <<< "$job_ids"

    rm -f "$tmp_jobs"
    
    echo "Log retrieval complete. Logs saved to: $logs_dir" >&2
    return 0
}

# =============================================================================
# Log Assertion Functions
# =============================================================================
# These functions are designed to work with log file arrays from get_workflow_run_logs
# All assertion functions return 0 on success, 1 on failure

# Assert that a pattern exists in ANY of the log files
# Automatically calls record_fail if assertion fails
# Usage: assert_logs_contain <pattern> <log_file1> [log_file2] ...
# Example: assert_logs_contain "hostname: my-vm" "${log_files[@]}"
assert_logs_contain() {
    local pattern="$1"
    shift
    local log_files=("$@")

    if [[ -z "$pattern" ]]; then
        echo "ERROR: pattern is required (arg 1)" >&2
        record_fail "assert_logs_contain: pattern is required"
        return 1
    fi
    if [[ ${#log_files[@]} -eq 0 ]]; then
        echo "ERROR: at least one log file is required" >&2
        record_fail "assert_logs_contain: no log files provided"
        return 1
    fi

    for log_file in "${log_files[@]}"; do
        if [[ ! -f "$log_file" ]]; then
            echo "WARNING: Log file not found: $log_file" >&2
            continue
        fi
        if grep -q "$pattern" "$log_file"; then
            echo "✓ Pattern '$pattern' found in $log_file"
            return 0
        fi
    done

    echo "✗ Pattern '$pattern' not found in any log file" >&2
    record_fail "pattern '$pattern' not found in logs"
    return 1
}

# Assert that a pattern exists in ALL of the log files
# Automatically calls record_fail if assertion fails
# Usage: assert_all_logs_contain <pattern> <log_file1> [log_file2] ...
# Example: assert_all_logs_contain "Runner started" "${log_files[@]}"
assert_all_logs_contain() {
    local pattern="$1"
    shift
    local log_files=("$@")

    if [[ -z "$pattern" ]]; then
        echo "ERROR: pattern is required (arg 1)" >&2
        record_fail "assert_all_logs_contain: pattern is required"
        return 1
    fi
    if [[ ${#log_files[@]} -eq 0 ]]; then
        echo "ERROR: at least one log file is required" >&2
        record_fail "assert_all_logs_contain: no log files provided"
        return 1
    fi

    local failed_files=()
    for log_file in "${log_files[@]}"; do
        if [[ ! -f "$log_file" ]]; then
            echo "WARNING: Log file not found: $log_file" >&2
            failed_files+=("$log_file (not found)")
            continue
        fi
        if ! grep -q "$pattern" "$log_file"; then
            failed_files+=("$log_file")
        fi
    done

    if [[ ${#failed_files[@]} -eq 0 ]]; then
        echo "✓ Pattern '$pattern' found in all ${#log_files[@]} log file(s)"
        return 0
    fi

    echo "✗ Pattern '$pattern' not found in ${#failed_files[@]} log file(s):" >&2
    for f in "${failed_files[@]}"; do
        echo "    - $f" >&2
    done
    record_fail "pattern '$pattern' not found in all logs"
    return 1
}

# Assert that a pattern does NOT exist in ANY of the log files
# Automatically calls record_fail if assertion fails
# Usage: assert_logs_not_contain <pattern> <log_file1> [log_file2] ...
# Example: assert_logs_not_contain "ERROR" "${log_files[@]}"
assert_logs_not_contain() {
    local pattern="$1"
    shift
    local log_files=("$@")

    if [[ -z "$pattern" ]]; then
        echo "ERROR: pattern is required (arg 1)" >&2
        record_fail "assert_logs_not_contain: pattern is required"
        return 1
    fi
    if [[ ${#log_files[@]} -eq 0 ]]; then
        echo "ERROR: at least one log file is required" >&2
        record_fail "assert_logs_not_contain: no log files provided"
        return 1
    fi

    local found_files=()
    for log_file in "${log_files[@]}"; do
        if [[ ! -f "$log_file" ]]; then
            continue
        fi
        if grep -q "$pattern" "$log_file"; then
            found_files+=("$log_file")
        fi
    done

    if [[ ${#found_files[@]} -eq 0 ]]; then
        echo "✓ Pattern '$pattern' not found in any log file (as expected)"
        return 0
    fi

    echo "✗ Pattern '$pattern' unexpectedly found in ${#found_files[@]} log file(s):" >&2
    for f in "${found_files[@]}"; do
        echo "    - $f" >&2
        echo "      Matching lines:" >&2
        grep "$pattern" "$f" | head -3 | sed 's/^/        /' >&2
    done
    record_fail "pattern '$pattern' unexpectedly found in logs"
    return 1
}

# Assert pattern match count in log files
# Automatically calls record_fail if assertion fails
# Usage: assert_logs_match_count <pattern> <expected_count> <log_file1> [log_file2] ...
# Example: assert_logs_match_count "Job completed" 1 "${log_files[@]}"
assert_logs_match_count() {
    local pattern="$1"
    local expected_count="$2"
    shift 2
    local log_files=("$@")

    if [[ -z "$pattern" ]]; then
        echo "ERROR: pattern is required (arg 1)" >&2
        record_fail "assert_logs_match_count: pattern is required"
        return 1
    fi
    if [[ -z "$expected_count" ]]; then
        echo "ERROR: expected_count is required (arg 2)" >&2
        record_fail "assert_logs_match_count: expected_count is required"
        return 1
    fi
    if [[ ${#log_files[@]} -eq 0 ]]; then
        echo "ERROR: at least one log file is required" >&2
        record_fail "assert_logs_match_count: no log files provided"
        return 1
    fi

    local actual_count=0
    for log_file in "${log_files[@]}"; do
        if [[ ! -f "$log_file" ]]; then
            continue
        fi
        local file_count
        file_count=$(grep -c "$pattern" "$log_file" 2>/dev/null || echo "0")
        actual_count=$((actual_count + file_count))
    done

    if [[ "$actual_count" -eq "$expected_count" ]]; then
        echo "✓ Pattern '$pattern' found $actual_count time(s) (expected $expected_count)"
        return 0
    fi

    echo "✗ Pattern '$pattern' found $actual_count time(s), expected $expected_count" >&2
    record_fail "pattern '$pattern' count mismatch: got $actual_count, expected $expected_count"
    return 1
}

# Assert that a specific log file contains a pattern
# Automatically calls record_fail if assertion fails
# Usage: assert_log_file_contains <log_file> <pattern>
# Example: assert_log_file_contains "${log_files[0]}" "hostname: my-vm"
assert_log_file_contains() {
    local log_file="$1"
    local pattern="$2"

    if [[ -z "$log_file" ]]; then
        echo "ERROR: log_file is required (arg 1)" >&2
        record_fail "assert_log_file_contains: log_file is required"
        return 1
    fi
    if [[ -z "$pattern" ]]; then
        echo "ERROR: pattern is required (arg 2)" >&2
        record_fail "assert_log_file_contains: pattern is required"
        return 1
    fi
    if [[ ! -f "$log_file" ]]; then
        echo "✗ Log file not found: $log_file" >&2
        record_fail "log file not found: $log_file"
        return 1
    fi

    if grep -q "$pattern" "$log_file"; then
        echo "✓ Pattern '$pattern' found in $(basename "$log_file")"
        return 0
    fi

    echo "✗ Pattern '$pattern' not found in $(basename "$log_file")" >&2
    record_fail "pattern '$pattern' not found in $(basename "$log_file")"
    return 1
}

# Assert that a specific log file does NOT contain a pattern
# Automatically calls record_fail if assertion fails
# Usage: assert_log_file_not_contains <log_file> <pattern>
# Example: assert_log_file_not_contains "${log_files[0]}" "FATAL"
assert_log_file_not_contains() {
    local log_file="$1"
    local pattern="$2"

    if [[ -z "$log_file" ]]; then
        echo "ERROR: log_file is required (arg 1)" >&2
        record_fail "assert_log_file_not_contains: log_file is required"
        return 1
    fi
    if [[ -z "$pattern" ]]; then
        echo "ERROR: pattern is required (arg 2)" >&2
        record_fail "assert_log_file_not_contains: pattern is required"
        return 1
    fi
    if [[ ! -f "$log_file" ]]; then
        echo "✗ Log file not found: $log_file" >&2
        record_fail "log file not found: $log_file"
        return 1
    fi

    if grep -q "$pattern" "$log_file"; then
        echo "✗ Pattern '$pattern' unexpectedly found in $(basename "$log_file")" >&2
        echo "  Matching lines:" >&2
        grep "$pattern" "$log_file" | head -3 | sed 's/^/    /' >&2
        record_fail "pattern '$pattern' unexpectedly found in $(basename "$log_file")"
        return 1
    fi

    echo "✓ Pattern '$pattern' not found in $(basename "$log_file") (as expected)"
    return 0
}

# Print log file contents for debugging (useful when assertions fail)
# Usage: print_log_files <log_file1> [log_file2] ...
# Example: print_log_files "${log_files[@]}"
print_log_files() {
    local log_files=("$@")

    if [[ ${#log_files[@]} -eq 0 ]]; then
        echo "No log files to print"
        return 0
    fi

    for log_file in "${log_files[@]}"; do
        echo "========== $(basename "$log_file") =========="
        if [[ -f "$log_file" ]]; then
            cat "$log_file"
        else
            echo "(file not found)"
        fi
        echo "========== END $(basename "$log_file") =========="
        echo ""
    done
}

# Clean up log files created by get_workflow_run_logs
# Usage: cleanup_log_files <log_file1> [log_file2] ...
# Example: cleanup_log_files "${log_files[@]}"
cleanup_log_files() {
    local log_files=("$@")

    if [[ ${#log_files[@]} -eq 0 ]]; then
        return 0
    fi

    # Get the directory from the first file and remove the whole directory
    local log_dir
    log_dir=$(dirname "${log_files[0]}")
    
    if [[ "$log_dir" == /tmp/workflow_logs_* ]]; then
        rm -rf "$log_dir"
        echo "Cleaned up log directory: $log_dir"
    else
        # Fallback: remove individual files
        rm -f "${log_files[@]}"
        echo "Cleaned up ${#log_files[@]} log file(s)"
    fi
}

# Move log files to the current test's log directory
# Usage: move_log_files_to_test_dir <log_file1> [log_file2] ...
move_log_files_to_test_dir() {
    local log_files=("$@")

    if [[ ${#log_files[@]} -eq 0 ]]; then
        return 0
    fi

    if [[ -z "${CURRENT_TEST_LOG_DIR}" ]]; then
        echo "Warning: CURRENT_TEST_LOG_DIR not set, cleaning up logs instead"
        cleanup_log_files "${log_files[@]}"
        return 0
    fi

    # Create workflow_logs subdirectory in test folder
    local dest_dir="${CURRENT_TEST_LOG_DIR}/workflow_logs"
    mkdir -p "${dest_dir}"

    # Get the source directory from the first file
    local log_dir
    log_dir=$(dirname "${log_files[0]}")
    
    if [[ "$log_dir" == /tmp/workflow_logs_* ]]; then
        # Move all files from the workflow logs directory
        mv "${log_dir}"/* "${dest_dir}/" 2>/dev/null || true
        rm -rf "$log_dir"
        echo "Moved workflow logs to: ${dest_dir}"
    else
        # Move individual files
        for file in "${log_files[@]}"; do
            if [[ -f "$file" ]]; then
                mv "$file" "${dest_dir}/" 2>/dev/null || true
            fi
        done
        echo "Moved ${#log_files[@]} log file(s) to: ${dest_dir}"
    fi
}

# =============================================================================
# High-Level Test Helper Functions
# =============================================================================
# These functions combine multiple low-level operations for cleaner test files

# Run a workflow and get logs: trigger, wait, get logs
# Run a workflow and check anklet.log for expected pattern (ignores GitHub status)
# Use this for tests where the workflow can't complete due to host resource constraints
# Usage: run_workflow_and_check_anklet_log <owner> <repo> <workflow_name> <log_pattern>
# Example: 
#   run_workflow_and_check_anklet_log "veertuinc" "anklet" "t2-12c20r-1" "not enough resources"
# Returns: 0 if pattern found in anklet.log, 1 otherwise
run_workflow_and_check_anklet_log() {
    local owner="$1"
    local repo="$2"
    local workflow_name="$3"
    local log_pattern="$4"

    # Set expected for test reporting
    CURRENT_TEST_EXPECTED="anklet_log:${log_pattern}"

    if [[ -z "$owner" ]] || [[ -z "$repo" ]] || [[ -z "$workflow_name" ]] || [[ -z "$log_pattern" ]]; then
        echo "ERROR: run_workflow_and_check_anklet_log requires: owner, repo, workflow_name, log_pattern" >&2
        return 1
    fi

    echo "]] Running workflow: $workflow_name (checking anklet.log for: '$log_pattern')"

    # Trigger workflow
    echo "]] Triggering workflow..."
    if ! trigger_workflow_runs "$owner" "$repo" "${workflow_name}.yml" 1; then
        echo "ERROR: Failed to trigger workflow $workflow_name"
        return 1
    fi

    # Wait for workflow to complete (any status) - we don't care about GitHub conclusion
    # Use a shorter timeout and check anklet.log periodically
    local max_wait=300  # 5 minutes
    local wait_interval=10
    local elapsed=0

    echo "]] Waiting for anklet to process workflow (checking for pattern in logs)..."
    while [[ $elapsed -lt $max_wait ]]; do
        sleep $wait_interval
        elapsed=$((elapsed + wait_interval))
        
        # Check if pattern appears in anklet.log
        if [[ -f /tmp/anklet.log ]] && grep -q "$log_pattern" /tmp/anklet.log 2>/dev/null; then
            echo "]] Found pattern '$log_pattern' in anklet.log after ${elapsed}s"
            return 0
        fi
        
        echo "]] Waiting... (${elapsed}s/${max_wait}s)"
    done

    echo "ERROR: Pattern '$log_pattern' not found in anklet.log after ${max_wait}s"
    echo "===== anklet.log tail ====="
    tail -50 /tmp/anklet.log 2>/dev/null || echo "(no log file)"
    echo "==========================="
    return 1
}

# Sets the global WORKFLOW_LOG_FILES array with log file paths
# Usage: run_workflow_and_get_logs <owner> <repo> <workflow_name> <expected_conclusion> [run_count]
# Example: 
#   run_workflow_and_get_logs "veertuinc" "anklet" "t2-6c14r-1" "success"
#   assert_logs_contain "pattern" "${WORKFLOW_LOG_FILES[@]}"
#   cleanup_log_files "${WORKFLOW_LOG_FILES[@]}"
# Returns: 0 on success, 1 on failure
WORKFLOW_LOG_FILES=()
run_workflow_and_get_logs() {
    local owner="$1"
    local repo="$2"
    local workflow_name="$3"
    local expected_conclusion="$4"
    local run_count="${5:-1}"

    # Set the expected conclusion for test reporting
    CURRENT_TEST_EXPECTED="$expected_conclusion"

    # Reset global array
    WORKFLOW_LOG_FILES=()

    if [[ -z "$owner" ]]; then
        echo "ERROR: owner is required (arg 1)" >&2
        return 1
    fi
    if [[ -z "$repo" ]]; then
        echo "ERROR: repo is required (arg 2)" >&2
        return 1
    fi
    if [[ -z "$workflow_name" ]]; then
        echo "ERROR: workflow_name is required (arg 3)" >&2
        return 1
    fi
    if [[ -z "$expected_conclusion" ]]; then
        echo "ERROR: expected_conclusion is required (arg 4)" >&2
        return 1
    fi

    echo "]] Running workflow: $workflow_name x$run_count (expecting: $expected_conclusion)"

    # Trigger workflow runs
    echo "]] Triggering workflow runs..."
    if ! trigger_workflow_runs "$owner" "$repo" "${workflow_name}.yml" "$run_count"; then
        echo "ERROR: Failed to trigger workflow runs for $workflow_name"
        return 1
    fi

    # Wait for workflow runs to complete
    if ! wait_for_workflow_runs_to_complete "$owner" "$repo" "$workflow_name" "$expected_conclusion"; then
        echo "ERROR: Failed to wait for workflow runs to complete for $workflow_name"
        return 1
    fi

    # Skip log retrieval for cancelled workflows - GitHub doesn't provide logs for cancelled jobs
    if [[ "$expected_conclusion" == "cancelled" ]]; then
        echo "]] Workflow $workflow_name completed with expected conclusion: cancelled (no logs available for cancelled jobs)"
        return 0
    fi

    # Get workflow run logs into global array (Bash 3.x compatible)
    while IFS= read -r line; do
        WORKFLOW_LOG_FILES+=("$line")
    done < <(get_workflow_run_logs "$owner" "$repo" "$workflow_name")

    if [[ ${#WORKFLOW_LOG_FILES[@]} -eq 0 ]]; then
        echo "ERROR: No log files retrieved for $workflow_name"
        return 1
    fi

    echo "]] Workflow $workflow_name completed, ${#WORKFLOW_LOG_FILES[@]} log file(s) available"
    return 0
}

# =============================================================================

# Cancel all running workflow runs for test workflows
# Usage: cancel_running_workflow_runs <owner> <repo> [workflow_patterns...]
# If no patterns provided, uses default test workflow patterns (t1-.*, t2-.*)
# Requires: ANKLET_TEST_TRIGGER_GITHUB_PAT environment variable to be set
cancel_running_workflow_runs() {
    local owner="$1"
    local repo="$2"
    shift 2
    local workflow_patterns=("$@")

    # Default patterns if none provided
    if [[ ${#workflow_patterns[@]} -eq 0 ]]; then
        workflow_patterns=("t1-" "t2-")
    fi

    if [[ -z "$owner" ]]; then
        echo "ERROR: owner is required (arg 1)" >&2
        return 1
    fi
    if [[ -z "$repo" ]]; then
        echo "ERROR: repo is required (arg 2)" >&2
        return 1
    fi
    if [[ -z "${ANKLET_TEST_TRIGGER_GITHUB_PAT}" ]]; then
        echo "ERROR: ANKLET_TEST_TRIGGER_GITHUB_PAT environment variable is required" >&2
        return 1
    fi

    echo "Cancelling running workflow runs: owner=$owner repo=$repo patterns=${workflow_patterns[*]}"

    # Get recent workflow runs
    local tmp_runs="/tmp/workflow_runs_cancel_$$.json"
    local api_url="https://api.github.com/repos/${owner}/${repo}/actions/runs?per_page=100"
    
    local http_code
    http_code=$(curl -s -w "%{http_code}" \
        -H "Authorization: Bearer ${ANKLET_TEST_TRIGGER_GITHUB_PAT}" \
        -H "Accept: application/vnd.github.v3+json" \
        -o "$tmp_runs" \
        "$api_url")

    if [[ "$http_code" != "200" ]]; then
        echo "ERROR: Failed to get workflow runs (HTTP $http_code)" >&2
        cat "$tmp_runs" >&2 2>/dev/null || true
        rm -f "$tmp_runs"
        return 1
    fi

    # Find running workflow runs matching any of our patterns
    local cancelled_count=0
    local failed_count=0

    for pattern in "${workflow_patterns[@]}"; do
        # Get runs matching this pattern that are still running
        local running_runs
        running_runs=$(jq -r --arg pattern "$pattern" '
            .workflow_runs[]
            | select(.path | test($pattern))
            | select(.status == "in_progress" or .status == "queued" or .status == "pending" or .status == "waiting")
            | "\(.id) \(.name) \(.status)"
        ' "$tmp_runs" 2>/dev/null || true)

        if [[ -z "$running_runs" ]]; then
            continue
        fi

        echo "Found running workflows matching '$pattern':"
        while IFS=' ' read -r run_id run_name run_status; do
            if [[ -z "$run_id" ]]; then
                continue
            fi
            
            echo "  - Cancelling run $run_id ($run_name) [status: $run_status]"
            
            # Cancel the workflow run
            local cancel_url="https://api.github.com/repos/${owner}/${repo}/actions/runs/${run_id}/cancel"
            local cancel_code
            cancel_code=$(curl -s -w "%{http_code}" -o /dev/null \
                -X POST \
                -H "Authorization: Bearer ${ANKLET_TEST_TRIGGER_GITHUB_PAT}" \
                -H "Accept: application/vnd.github.v3+json" \
                "$cancel_url")

            # 202 = accepted, 409 = already completed (not an error)
            if [[ "$cancel_code" == "202" ]]; then
                echo "    ✓ Cancelled successfully"
                cancelled_count=$((cancelled_count + 1))
            elif [[ "$cancel_code" == "409" ]]; then
                echo "    - Already completed (no action needed)"
            else
                echo "    ✗ Failed to cancel (HTTP $cancel_code)"
                failed_count=$((failed_count + 1))
            fi
        done <<< "$running_runs"
    done

    rm -f "$tmp_runs"

    echo "Workflow cancellation complete: cancelled=$cancelled_count failed=$failed_count"
    
    if [[ $failed_count -gt 0 ]]; then
        return 1
    fi
    return 0
}

# =============================================================================

# Start anklet with nohup, background it, and track PIDs for cleanup
start_anklet() {
    local service_name="${1:-anklet}"
    echo "] Starting $service_name..."
    
    # Start anklet with nohup and background it
    export LOG_LEVEL=${LOG_LEVEL:-dev}
    echo "LOG_LEVEL: $LOG_LEVEL"
    ( 
        # First fork - creates subshell
        cd /
cat > /tmp/setsid.pl 2>&1 <<EOF
#!/usr/bin/perl -w
use strict;
use POSIX qw(setsid);
fork() && exit(0);
setsid() or die "setsid failed: $!";
exec @ARGV;
EOF
        chmod +x /tmp/setsid.pl
        /tmp/setsid.pl nohup /tmp/anklet > /tmp/anklet.log 2>&1 < /dev/null &
    ) &
    
    # Wait a moment to ensure the process starts
    sleep 1
    
    # Find the actual anklet process (not tail or other commands)
    # Use pgrep to find processes with "anklet" in the name, excluding tail and grep
    ANKLET_PID=$(pgrep -f "^/tmp/anklet" | head -1)
    
    if [[ -z "$ANKLET_PID" ]]; then
        echo "ERROR: Could not find anklet process"
        exit 1
    fi
    
    echo "Anklet parent PID: $ANKLET_PID"
    
    # Store only the parent PID for cleanup
    echo "$ANKLET_PID" > /tmp/anklet-pids
    echo "Stored parent PID for cleanup: $ANKLET_PID"
    
    # Give more time for processes to start
    sleep 3
    
    grep "ERROR" /tmp/anklet.log && {
        echo "ERROR: Anklet log contains errors"
        echo "===== /tmp/anklet.log contents ====="
        cat /tmp/anklet.log
        echo "===================================="
        exit 1
    }
    
    ps aux | grep "/tmp/anklet"
    # cat "/tmp/anklet.log"
    echo "Parent PID stored for cleanup:"
    cat /tmp/anklet-pids
}

