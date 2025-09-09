#!/usr/bin/env bash
set -eo pipefail

OWNER="${1}"
REPO="${2}"
TRIGGER_RUN_COUNT="${3}"
REGEX_PATTERN="${4}"

[[ -n "$TRIGGER_PAT" ]] || (echo "TRIGGER_PAT env is required" && exit 1)
[[ -n "$OWNER" ]] || (echo "OWNER as ARG1 is required" && exit 1)
[[ -n "$REPO" ]] || (echo "REPO as ARG2 is required" && exit 1)
[[ -n "$TRIGGER_RUN_COUNT" ]] || (echo "TRIGGER_RUN_COUNT as ARG3 is required" && exit 1)

# Check if .github/workflows directory exists
if [[ ! -d "./.github/workflows" ]]; then
    echo "ERROR: ./.github/workflows directory not found"
    exit 1
fi

for ((i=1; i<=TRIGGER_RUN_COUNT; i++)); do
    echo "[run $i] Starting"
    WORKFLOW_FILES=$(ls ./.github/workflows/*.yml | grep "./.github/workflows/t[0-9]-" | grep -E "$REGEX_PATTERN")

    # Check if any workflow files match the pattern
    if [[ -z "$WORKFLOW_FILES" ]]; then
        echo "[run $i] WARNING: No workflow files match the pattern '$REGEX_PATTERN'"
        continue
    fi

    echo "[run $i] Found $(echo "$WORKFLOW_FILES" | wc -l) workflow(s) matching pattern"

    for FILE in $WORKFLOW_FILES; do
        WORKFLOW_ID=$(basename $FILE)
        echo "[run $i] Triggering workflow $WORKFLOW_ID"
        curl \
            -X POST \
            -H "Authorization: Bearer ${TRIGGER_PAT}" \
            -H "Accept: application/vnd.github.v3+json" \
            https://api.github.com/repos/${OWNER}/${REPO}/actions/workflows/${WORKFLOW_ID}/dispatches \
            -d "{\"ref\":\"${GITHUB_REF_NAME:-"main"}\"}"
        sleep 1
        exit
    done
done
