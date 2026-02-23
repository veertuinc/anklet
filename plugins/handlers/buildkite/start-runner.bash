#!/usr/bin/env bash
set -exo pipefail

CONFIG_FILE="${HOME}/.buildkite-agent/anklet.env"
[[ -f "${CONFIG_FILE}" ]] || (echo "Error: missing ${CONFIG_FILE}" && exit 1)
source "${CONFIG_FILE}"

buildkite-agent start \
  --name "${BUILDKITE_AGENT_NAME}" \
  --token "${BUILDKITE_AGENT_TOKEN}" \
  --tags "${BUILDKITE_AGENT_TAGS}" \
  --acquire-job "${BUILDKITE_ACQUIRE_JOB_ID}" \
  --disconnect-after-job