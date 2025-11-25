#!/usr/bin/env bash
set -aeo pipefail
PATH="/usr/local/bin:$PATH" # unable to find anka in path otherwise

# Helper functions for starting and managing anklet processes
# This will exist right next to the other bash scripts on the host machine that runs them. Be aware of paths you use.


# check if the anklet process is running
check_anklet_process() {
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
                echo "Sending SIGINT to PID $pid..."
                kill -SIGINT $pid || true
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

