#!/usr/bin/env bash
set -exo pipefail

if command -v buildkite-agent >/dev/null 2>&1; then
  echo "buildkite-agent already installed"
  exit 0
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "ERROR: curl is required to install buildkite-agent" >&2
  exit 1
fi

# Buildkite Linux install flow:
# https://buildkite.com/docs/agent/self-hosted/install/linux
if [[ -n "${BUILDKITE_AGENT_TOKEN:-}" ]]; then
  TOKEN="${BUILDKITE_AGENT_TOKEN}" bash -c "$(curl -sL https://raw.githubusercontent.com/buildkite/agent/main/install.sh)"
else
  bash -c "$(curl -sL https://raw.githubusercontent.com/buildkite/agent/main/install.sh)"
fi

if command -v buildkite-agent >/dev/null 2>&1; then
  buildkite-agent --version
  echo "buildkite-agent successfully installed"
  exit 0
fi

if [[ -x "${HOME}/.buildkite-agent/bin/buildkite-agent" ]]; then
  "${HOME}/.buildkite-agent/bin/buildkite-agent" --version
  echo "buildkite-agent successfully installed"
  exit 0
fi

echo "ERROR: buildkite-agent install completed but binary not found" >&2
exit 1
