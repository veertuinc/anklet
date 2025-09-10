#!/usr/bin/env bash
set -eo pipefail

# Function to wait for workflow completion
wait_for_workflow_completion() {
    local owner="$1"
    local repo="$2"
    local workflow_name="$3"
    local timeout_minutes="${4:-5}"  # Default 5 minutes timeout

    echo "] Waiting for workflow '$workflow_name' to complete..."

    local start_time=$(date +%s)
    local timeout_seconds=$((timeout_minutes * 60))

    while true; do
        # Check if we've exceeded the timeout
        local current_time=$(date +%s)
        local elapsed=$((current_time - start_time))
        if [ $elapsed -gt $timeout_seconds ]; then
            echo "ERROR: Timeout waiting for workflow completion after ${timeout_minutes} minutes"
            return 1
        fi

        # Get the latest workflow runs
        local response=$(curl -s \
            -H "Authorization: Bearer ${TRIGGER_PAT}" \
            -H "Accept: application/vnd.github.v3+json" \
            "https://api.github.com/repos/${owner}/${repo}/actions/runs")

        # Find the workflow run that matches our workflow file
        # Look for workflow runs with path ending in our workflow name
        local workflow_data=$(echo "$response" | jq -r ".workflow_runs[] | select(.path | endswith(\"$workflow_name\")) | {status: .status, conclusion: .conclusion, id: .id} | @json" | head -1)

        if [ -z "$workflow_data" ]; then
            echo "No workflow run found for '$workflow_name' yet, waiting..."
            sleep 5
            continue
        fi

        # Parse the workflow data
        local status=$(echo "$workflow_data" | jq -r '.status')
        local conclusion=$(echo "$workflow_data" | jq -r '.conclusion')
        local run_id=$(echo "$workflow_data" | jq -r '.id')

        echo "Workflow run $run_id status: $status (elapsed: ${elapsed}s)"

        case "$status" in
            "completed")
                if [ "$conclusion" = "success" ]; then
                    echo "Workflow completed successfully"
                    return 0
                else
                    echo "ERROR: Workflow completed with conclusion: $conclusion"
                    return 1
                fi
                ;;
            "in_progress"|"queued"|"requested"|"waiting")
                echo "Workflow still running, waiting 30 seconds..."
                sleep 30
                ;;
            "failure"|"error"|"timed_out"|"action_required"|"cancelled"|"skipped"|"stale")
                echo "ERROR: Workflow failed with status: $status"
                return 1
                ;;
            *)
                echo "WARNING: Unknown workflow status: $status"
                sleep 5
                ;;
        esac
    done
}

# If script is called directly with arguments, execute the function
if [ $# -ge 3 ]; then
    wait_for_workflow_completion "$@"
fi
