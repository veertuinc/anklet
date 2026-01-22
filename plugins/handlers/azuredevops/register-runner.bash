#!/usr/bin/env bash
set -exo pipefail
[ -z "$1" ] && (echo "Error: Agent name argument is missing." && exit 1)
[ -z "$2" ] && (echo "Error: Token argument is missing." && exit 1)
[ -z "$3" ] && (echo "Error: URL argument is missing." && exit 1)
[ -z "$4" ] && (echo "Error: Pool argument is missing." && exit 1)
AGENT_HOME="${AGENT_HOME:-"$HOME/azp-agent"}"
mkdir -p "${AGENT_HOME}"
cd "${AGENT_HOME}"
./config.sh \
  --agent "${1}" \
  --auth PAT \
  --token "${2}" \
  --url "${3}" \
  --pool "${4}" \
  --replace \
  --unattended \
  --once
echo "agent successfully configured"
