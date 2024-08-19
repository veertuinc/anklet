#!/usr/bin/env bash
set -exo pipefail
RUNNER_HOME="${RUNNER_HOME:-"$HOME/actions-runner"}"
mkdir -p "${RUNNER_HOME}"
cd "${RUNNER_HOME}"
GITHUB_RUNNER_VERSION="2.319.0"
RUNNER_ARCH=${RUNNER_ARCH:-"$(uname -m)"}
if [ "$RUNNER_ARCH" = "x86_64" ] || [ "$RUNNER_ARCH" = "x64" ]; then
  RUNNER_ARCH="x64"
fi
curl -o "actions-runner-osx-${RUNNER_ARCH}-${GITHUB_RUNNER_VERSION}.tar.gz" \
  -L "https://github.com/actions/runner/releases/download/v${GITHUB_RUNNER_VERSION}/actions-runner-osx-${RUNNER_ARCH}-${GITHUB_RUNNER_VERSION}.tar.gz"
tar -xzf "./actions-runner-osx-${RUNNER_ARCH}-${GITHUB_RUNNER_VERSION}.tar.gz" 1>/dev/null
echo "runner successfully installed"
