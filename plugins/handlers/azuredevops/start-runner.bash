#!/usr/bin/env bash
set -exo pipefail
AGENT_HOME="${AGENT_HOME:-"$HOME/azp-agent"}"
cd "${AGENT_HOME}"
./svc.sh install
./svc.sh start
./svc.sh status
echo "agent started"