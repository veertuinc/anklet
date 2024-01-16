#!/usr/bin/env bash
set -exo pipefail
[ -z "$1" ] && (echo "Error: Runner name argument is missing." && exit 1)
[ -z "$2" ] && (echo "Error: Token argument is missing." && exit 1)
[ -z "$3" ] && (echo "Error: URL argument is missing." && exit 1)
[ -z "$4" ] && (echo "Error: Labels argument is missing." && exit 1)
RUNNER_HOME="${RUNNER_HOME:-"$HOME/actions-runner"}"
mkdir -p "${RUNNER_HOME}"
cd "${RUNNER_HOME}"
./config.sh --name "${1}" \
  --token "${2}" \
  --url "${3}" \
  --no-default-labels --labels "${4}" \
  --disableupdate \
  --unattended \
  --ephemeral
echo "runner successfully configured"