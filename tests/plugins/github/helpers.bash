#!/usr/bin/env bash
set -aeo pipefail
PATH="/usr/local/bin:$PATH" # unable to find anka in path otherwise

# Helper functions for starting and managing anklet processes
# This will exist right next to the other bash scripts on the host machine that runs them. Be aware of paths you use.

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
    local timeout_seconds="${4:-600}"  # Default 10 minutes
    local expected_conclusion="${5:-}"  # Optional: success, failure, cancelled, etc.

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

# Start anklet with nohup, background it, and track PIDs for cleanup
start_anklet() {
    local service_name="${1:-anklet}"
    echo "] Starting $service_name..."
    
    # Start anklet with nohup and background it
    export LOG_LEVEL=${LOG_LEVEL:-dev}
    echo "LOG_LEVEL: $LOG_LEVEL"
    nohup /tmp/anklet > /tmp/anklet.log 2>&1 &
    disown "$!"
    
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

