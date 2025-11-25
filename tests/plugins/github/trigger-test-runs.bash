#!/usr/bin/env bash
set -eo pipefail

OWNER="${1}"
REPO="${2}"
REGEX_PATTERN="${3}"
[[ -n "$OWNER" ]] || (echo "OWNER as ARG1 is required" && exit 1)
[[ -n "$REPO" ]] || (echo "REPO as ARG2 is required" && exit 1)
[[ -n "$REGEX_PATTERN" ]] || (echo "REGEX_PATTERN as ARG3 is required" && exit 1)

[[ -z "${ANKLET_TEST_TRIGGER_GITHUB_PAT}" ]] && (echo "error: no trigger github pat provided" && exit 1)

TRIGGER_RUN_COUNT="${4:-10}"

for ((i=1; i<=TRIGGER_RUN_COUNT; i++)); do
    echo "[run $i] Starting"
    WORKFLOW_FILES=$(ls ./.github/workflows/*.yml | grep "./.github/workflows/t[0-9]-") # | grep -E "matrix|t1-with-tag-1"
    for FILE in $WORKFLOW_FILES; do
        WORKFLOW_ID=$(basename $FILE)
        echo "[run $i] Triggering workflow $WORKFLOW_ID"
        curl \
            -X POST \
            -H "Authorization: Bearer ${ANKLET_TEST_TRIGGER_GITHUB_PAT}" \
            -H "Accept: application/vnd.github.v3+json" \
            https://api.github.com/repos/${OWNER}/${REPO}/actions/workflows/${WORKFLOW_ID}/dispatches \
            -d '{"ref":"main"}'
        sleep 1
    done
done
