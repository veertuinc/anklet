#!/usr/bin/env bash
set -exo pipefail
AGENT_HOME="${AGENT_HOME:-"$HOME/azp-agent"}"
mkdir -p "${AGENT_HOME}"
cd "${AGENT_HOME}"
AGENT_ARCH=${AGENT_ARCH:-"$(uname -m)"}
if [ "$AGENT_ARCH" = "x86_64" ]; then
  AGENT_ARCH="x64"
elif [ "$AGENT_ARCH" = "arm64" ]; then
  AGENT_ARCH="arm64"
fi
LATEST_VERSION="$(curl -s https://api.github.com/repos/microsoft/azure-pipelines-agent/releases/latest | grep '"tag_name":' | sed -E 's/.*"v([^"]+)".*/\1/')"
DOWNLOAD_URL="https://download.agent.dev.azure.com/agent/${LATEST_VERSION}/vsts-agent-osx-${AGENT_ARCH}-${LATEST_VERSION}.tar.gz"
FULL_FILE_NAME="vsts-agent-osx-${AGENT_ARCH}-${LATEST_VERSION}.tar.gz"
curl -sL -o "$FULL_FILE_NAME" "$DOWNLOAD_URL"
tar -xzf "$FULL_FILE_NAME" 1>/dev/null
rm -f "$FULL_FILE_NAME"
echo "agent successfully installed"
