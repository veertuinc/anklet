#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${1:-}" ]]; then
  echo "Error: Runner name argument is missing." >&2
  exit 1
fi
if [[ -z "${2:-}" ]]; then
  echo "Error: Token argument is missing." >&2
  exit 1
fi
if [[ -z "${3:-}" ]]; then
  echo "Error: URL argument is missing." >&2
  exit 1
fi
if [[ -z "${4:-}" ]]; then
  echo "Error: Labels argument is missing." >&2
  exit 1
fi

RUNNER_HOME="${RUNNER_HOME:-"$HOME/actions-runner"}"
mkdir -p "${RUNNER_HOME}"
cd "${RUNNER_HOME}"

config_args=(
  --name "${1}"
  --token "${2}"
  --url "${3}"
  --no-default-labels
  --labels "${4}"
  --disableupdate
  --unattended
  --ephemeral
)

if [[ -n "${5:-}" ]]; then
  config_args+=(--runnergroup "${5}")
fi

./config.sh "${config_args[@]}"
echo "runner successfully configured"
