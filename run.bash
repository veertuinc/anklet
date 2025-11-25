#!/usr/bin/env bash
set -exo pipefail
YAML_CONFIG_FILE="${1}"
[ -z "${YAML_CONFIG_FILE}" ] && echo "ARG 1 (Yaml Config File; org-receiver-config.yml) is not set" && exit 1

SCRIPT_DIR=$(cd $(dirname "${BASH_SOURCE[0]}") && pwd)
cd $SCRIPT_DIR

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
    check_duplicate_keys ${ansible_user}@${ansible_host}

    # Clean up the log file
    log_file="/tmp/$(basename ${YAML_CONFIG_FILE}).log"
    if [ -f "$log_file" ]; then
        echo "Log file saved at: $log_file"
        echo "You can review it or remove it manually."
    fi
}

# we use JQ here instead of pretty print in the logging.go so that we can ensure valid JSON is output from go
# Feed stdout through jq so we still validate JSON, but surface time/name/level/msg/attributes for readability (errors red, warnings yellow, debug grey; names get stable colors).
LOG_LEVEL=${LOG_LEVEL:-dev} go run main.go -c ${YAML_CONFIG_FILE} 2>&1 \
    | tee /tmp/$(basename ${YAML_CONFIG_FILE}).log \
    | jq -r '
        def palette: ["\u001b[32m", "\u001b[35m", "\u001b[36m", "\u001b[94m", "\u001b[95m", "\u001b[96m"];
        def name_color($name):
            palette as $palette |
            (reduce ($name | tostring | explode[]) as $c (0; . + $c)) as $sum |
            ($palette | length) as $plen |
            (if $plen > 0 then $palette[$sum % $plen] else "" end);
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
            .
        end' &
go_pid=$!

# Set up trap to run cleanup on exit
trap cleanup EXIT

echo "Anklet is running... Press Ctrl+C to stop and check for duplicate keys."
wait $go_pid
