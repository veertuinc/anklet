#!/usr/bin/env bash
set -eo pipefail

echo "] Running tests..."

sleep 30 # wait for the handler to start and do a single no queue jobs found for the logs

echo "] Triggering workflow runs..."
# trigger the api call to trigger the workflow run
../trigger-workflow-runs.bash veertuinc anklet 1 "t1-with-tag-1"

# wait for the logs of the handler to show the expected output

