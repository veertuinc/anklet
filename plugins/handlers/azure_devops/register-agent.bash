#!/usr/bin/env bash
# register-agent.bash - configure the freshly installed Azure DevOps agent
# against an organization + pool using a PAT. Run after install-agent.bash
# and before start-agent.bash.
#
# Positional args (must all be set; passed by the handler):
#   $1 ORG_URL    e.g. https://dev.azure.com/my-org
#   $2 POOL_NAME  agent pool the agent will register into (must already exist)
#   $3 AGENT_NAME globally unique agent display name (the handler uses the
#                 Anka VM name, which already includes a UUID suffix)
#   $4 PAT        Personal Access Token with "Agent Pools: Manage" scope
#
# --replace lets us re-register if a previous unclean shutdown left an agent
# of the same name behind; --acceptTeeEula is required by config.sh on macOS
# even though TEE itself is not used.
set -exo pipefail

[ -z "${1:-}" ] && { echo "Error: ORG_URL argument is missing." >&2; exit 1; }
[ -z "${2:-}" ] && { echo "Error: POOL_NAME argument is missing." >&2; exit 1; }
[ -z "${3:-}" ] && { echo "Error: AGENT_NAME argument is missing." >&2; exit 1; }
[ -z "${4:-}" ] && { echo "Error: PAT argument is missing." >&2; exit 1; }

ORG_URL="$1"
POOL_NAME="$2"
AGENT_NAME="$3"
PAT="$4"

AGENT_HOME="${AGENT_HOME:-"$HOME/agent"}"
cd "${AGENT_HOME}"

./config.sh \
  --unattended \
  --auth pat \
  --token "${PAT}" \
  --url "${ORG_URL}" \
  --pool "${POOL_NAME}" \
  --agent "${AGENT_NAME}" \
  --acceptTeeEula \
  --replace

echo "azure devops agent '${AGENT_NAME}' registered into pool '${POOL_NAME}'"
