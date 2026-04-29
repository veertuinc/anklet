#!/usr/bin/env bash
# install-agent.bash - download and extract the Azure DevOps Pipelines agent
# inside the Anka VM. Run before register-agent.bash.
#
# Args: none. Optional env vars:
#   AGENT_HOME    override the install directory (default $HOME/agent)
#   AGENT_VERSION pin a specific agent version, e.g. "4.250.0" (default: latest)
set -exo pipefail

AGENT_HOME="${AGENT_HOME:-"$HOME/agent"}"
mkdir -p "${AGENT_HOME}"
cd "${AGENT_HOME}"

ARCH="$(uname -m)"
case "$ARCH" in
  arm64|aarch64) AGENT_ARCH="arm64" ;;
  x86_64|x64)    AGENT_ARCH="x64" ;;
  *) echo "unsupported arch: $ARCH" >&2; exit 1 ;;
esac

# Resolve version. Microsoft does not attach tarballs to GitHub releases, so
# we read the latest tag from the GitHub API and build the CDN URL ourselves.
if [ -z "${AGENT_VERSION:-}" ]; then
  AGENT_VERSION="$(curl -sSL https://api.github.com/repos/microsoft/azure-pipelines-agent/releases/latest \
    | grep '"tag_name":' \
    | head -1 \
    | sed -E 's/.*"v?([^"]+)".*/\1/')"
fi
if [ -z "${AGENT_VERSION}" ]; then
  echo "could not determine agent version" >&2
  exit 1
fi

ARCHIVE="vsts-agent-osx-${AGENT_ARCH}-${AGENT_VERSION}.tar.gz"
# Microsoft retired vstsagentpackage.azureedge.net in 2024. The canonical
# host is now download.agent.dev.azure.com, which is also the URL listed in
# the upstream GitHub release notes.
DOWNLOAD_URL="https://download.agent.dev.azure.com/agent/${AGENT_VERSION}/${ARCHIVE}"

curl -fsSL -o "${ARCHIVE}" "${DOWNLOAD_URL}"
tar -xzf "${ARCHIVE}" 1>/dev/null
rm -f "${ARCHIVE}"
echo "azure devops agent ${AGENT_VERSION} (${AGENT_ARCH}) installed in ${AGENT_HOME}"
