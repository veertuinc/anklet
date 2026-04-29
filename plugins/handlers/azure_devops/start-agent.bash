#!/usr/bin/env bash
# start-agent.bash - launch the configured Azure DevOps agent for exactly
# one job, in the background, and return immediately so the handler can
# proceed to its watch loop.
#
# Positional args (both required, passed by the handler):
#   $1 ANKA_TEMPLATE     UUID of the Anka template the VM was cloned from
#   $2 ANKA_TEMPLATE_TAG tag of that template (may be empty for "latest")
#
# Capabilities: the Azure DevOps agent picks up its system capabilities
# from the env vars present at run.sh startup. We expose
# anka-template / anka-template-tag so ADO's dispatcher can match the
# job's pool demands against this agent. Without them the build sits in
# queue forever even though we have a registered, online agent.
#
# Why background: run.sh --once blocks until the agent finishes its one
# job, which can be many minutes. The handler watches run state via the
# REST API instead, so this script must return as soon as the agent is
# launched - otherwise AnkaRun blocks and the watch loop never starts.
set -exo pipefail

[ -z "${1:-}" ] && { echo "Error: ANKA_TEMPLATE argument is missing." >&2; exit 1; }
ANKA_TEMPLATE="$1"
ANKA_TEMPLATE_TAG="${2:-}"

AGENT_HOME="${AGENT_HOME:-"$HOME/agent"}"
cd "${AGENT_HOME}"

# Bash cannot export env-var names that contain dashes via `export`, but
# `env name=value cmd` accepts them and the agent reads them via .NET's
# Environment APIs which don't care about identifier syntax.
nohup env \
  "anka-template=${ANKA_TEMPLATE}" \
  "anka-template-tag=${ANKA_TEMPLATE_TAG}" \
  ./run.sh --once </dev/null >"${AGENT_HOME}/run.log" 2>&1 &
disown

echo "azure devops agent started in background (pid $!) with capabilities anka-template=${ANKA_TEMPLATE} anka-template-tag=${ANKA_TEMPLATE_TAG}"
