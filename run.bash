#!/usr/bin/env bash
set -exo pipefail
YAML_CONFIG_FILE="${1}"
[ -z "${YAML_CONFIG_FILE}" ] && echo "ARG 1 (Yaml Config File; org-receiver-config.yml) is not set" && exit 1

SCRIPT_DIR=$(cd $(dirname "${BASH_SOURCE[0]}") && pwd)
cd $SCRIPT_DIR

# Function to check for duplicate JSON keys in the log file
# Usage: check_duplicate_keys <log_file>
check_duplicate_keys() {
    local log_file="$1"

    if [[ -z "$log_file" ]]; then
        echo "ERROR: log_file parameter is required for check_duplicate_keys"
        return 1
    fi

    if [[ ! -f "$log_file" ]]; then
        echo "ERROR: log file $log_file does not exist"
        return 0
    fi

    echo "Checking for duplicate JSON keys in log file..."

    LOG_FILE="$log_file" python3 << 'EOF'
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

# Cleanup function to check logs and clean up
cleanup() {
    echo ""
    echo "Performing cleanup and validation..."

    # Kill the Go process if it's still running
    if kill -0 $go_pid 2>/dev/null; then
        echo "Stopping Go process (PID: $go_pid)..."
        kill -SIGINT $go_pid 2>/dev/null || true
        wait $go_pid 2>/dev/null || true
    fi

    # Check for duplicate keys in the log file
    log_file="/tmp/$(basename ${YAML_CONFIG_FILE}).log"
    check_duplicate_keys "$log_file"

    # Log file location
    if [ -f "$log_file" ]; then
        echo "Log file saved at: $log_file"
        echo "You can review it or remove it manually."
    fi
}

# we use JQ here instead of pretty print in the logging.go so that we can ensure valid JSON is output from go
# Feed stdout through jq so we still validate JSON, but surface time/name/level/msg/attributes for readability (errors red, warnings yellow, debug grey; names get stable colors).
# Using -Rr to handle non-JSON lines gracefully (e.g., startup messages, errors)
LOG_LEVEL=${LOG_LEVEL:-dev} go run main.go -c ${YAML_CONFIG_FILE} 2>&1 \
    | tee /tmp/$(basename ${YAML_CONFIG_FILE}).log \
    | jq -Rr '
        def palette: ["\u001b[32m", "\u001b[35m", "\u001b[36m", "\u001b[94m", "\u001b[95m", "\u001b[96m"];
        def name_color($name):
            palette as $palette |
            (reduce ($name | tostring | explode[]) as $c (0; . + $c)) as $sum |
            ($palette | length) as $plen |
            (if $plen > 0 then $palette[$sum % $plen] else "" end);
        # Try to parse as JSON, if it fails, output the raw line
        . as $raw |
        try (fromjson |
            if type == "object" then
                (.level // .severity // "") as $level |
                (.time // .ts // .timestamp // "") as $time |
                (.msg // "") as $msg |
                (.attributes // {}) as $attrs |
                (if ($attrs | type) == "object" and ($attrs | has("name")) then ($attrs.name // "") else "" end) as $rawName |
                (if ($rawName | tostring | length) > 0 then ($rawName | tostring) else "-" end) as $nameStr |
                (if ($rawName | tostring | length) > 0 then name_color($rawName) else "\u001b[37m" end) as $nameColor |
                ($nameColor + $nameStr + "\u001b[0m") as $coloredName |
                ($level | tostring) as $levelStr |
                ($time | tostring) as $timeStr |
                ($msg | tostring) as $msgStr |
                ($attrs | tojson) as $attrsJson |
                (if ($attrs | type) == "object" and ($attrs | has("workflowJobRunID")) then " workflowJobRunID=" + ($attrs.workflowJobRunID | tostring) else "" end) as $workflowJobRunIDStr |
                (if has("error") then " error=" + (.error | tojson)
                 elif has("err") then " error=" + (.err | tojson)
                 else "" end) as $errorStr |
                ($levelStr | ascii_upcase) as $levelUpper |
                (if $levelUpper == "ERROR" then
                    "\u001b[31mtime=" + $timeStr + " name=" + $coloredName + "\u001b[31m level=" + $levelStr + " msg=" + $msgStr + $workflowJobRunIDStr + $errorStr + " attributes=" + $attrsJson + "\u001b[0m"
                elif ($levelUpper == "WARN" or $levelUpper == "WARNING") then
                    "\u001b[33mtime=" + $timeStr + " name=" + $coloredName + "\u001b[33m level=" + $levelStr + " msg=" + $msgStr + $workflowJobRunIDStr + $errorStr + " attributes=" + $attrsJson + "\u001b[0m"
                elif $levelUpper == "DEBUG" then
                    "\u001b[90mtime=" + $timeStr + " name=" + $coloredName + "\u001b[90m level=" + $levelStr + " msg=" + $msgStr + $workflowJobRunIDStr + $errorStr + " attributes=" + $attrsJson + "\u001b[0m"
                else
                    "\u001b[33mtime\u001b[0m=\u001b[37m" + $timeStr + "\u001b[0m name=" + $coloredName + " level=" + $levelStr + " msg=" + $msgStr + $workflowJobRunIDStr + $errorStr + " attributes=" + $attrsJson
                end)
            else
                $raw
            end
        ) catch $raw' &
go_pid=$!

# Set up trap to run cleanup on exit
trap cleanup EXIT

echo "Anklet is running... Press Ctrl+C to stop and check for duplicate keys."
wait $go_pid
