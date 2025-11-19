#!/usr/bin/env bash
set -eo pipefail

clean() {
    echo "] Cleaning up handler..."
    exit 0
}
[[ ${CLEAN:-false} == true || "$1" == "--clean" ]] && clean

echo "] Starting handler..."
echo "]] Running tests..."

sleep 300 # wait for the handler to start and do a single no queue jobs found for the logs

echo "]] Triggering workflow runs..."
# # trigger the api call to trigger the workflow run
# ../trigger-workflow-runs.bash "veertuinc" "anklet" 1 "t2-6c14r-1.yml"

# # wait for the workflow to run to finish
# ../wait-workflow-completion.bash "veertuinc" "anklet" "t2-6c14r-1.yml" 3

# wait for the logs of the handler to show the expected output

