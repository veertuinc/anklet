#!/usr/bin/env bash
set -eo pipefail

PAT="${1}"
OWNER="${2}"
REPO="${3}"
[[ -n "$PAT" ]] || (echo "PAT as ARG1 is required" && exit 1)
[[ -n "$OWNER" ]] || (echo "OWNER as ARG2 is required" && exit 1)
[[ -n "$REPO" ]] || (echo "REPO as ARG3 is required" && exit 1)

TRIGGER_RUN_COUNT="${4:-10}"

for ((i=1; i<=TRIGGER_RUN_COUNT; i++)); do
    echo "Triggering run $i"
    WORKFLOW_FILES=$(ls ./.github/workflows/*.yml | grep -v "check-pr") # | grep -E "matrix|t1-with-tag-1"
    for FILE in $WORKFLOW_FILES; do
        WORKFLOW_ID=$(basename $FILE)
        curl \
            -X POST \
            -H "Authorization: Bearer ${PAT}" \
            -H "Accept: application/vnd.github.v3+json" \
            https://api.github.com/repos/${OWNER}/${REPO}/actions/workflows/${WORKFLOW_ID}/dispatches \
            -d '{"ref":"main"}'
        sleep 1
    done
done
