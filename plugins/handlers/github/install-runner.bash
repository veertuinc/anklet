#!/usr/bin/env bash
set -euo pipefail

RUNNER_HOME="${RUNNER_HOME:-"$HOME/actions-runner"}"
mkdir -p "${RUNNER_HOME}"
cd "${RUNNER_HOME}"

RUNNER_ARCH="${RUNNER_ARCH:-"$(uname -m)"}"
if [[ "${RUNNER_ARCH}" == "x86_64" || "${RUNNER_ARCH}" == "x64" ]]; then
  RUNNER_ARCH="x64"
elif [[ "${RUNNER_ARCH}" == "arm64" || "${RUNNER_ARCH}" == "aarch64" ]]; then
  RUNNER_ARCH="arm64"
else
  echo "Error: unsupported runner architecture: ${RUNNER_ARCH}" >&2
  exit 1
fi

RELEASE_JSON="$(curl -fsSL -H "Accept: application/vnd.github+json" https://api.github.com/repos/actions/runner/releases/latest)"

# Extract the macOS archive download URL and expected SHA-256 from release metadata.
read -r DOWNLOAD_URL EXPECTED_SHA256 < <(
  RUNNER_ARCH="${RUNNER_ARCH}" python3 -c '
import json, os, re, sys
arch = os.environ["RUNNER_ARCH"]
data = json.load(sys.stdin)
pattern = re.compile(rf"^actions-runner-osx-{re.escape(arch)}-[0-9.]+\.tar\.gz$")
asset_url = ""
asset_name = ""
asset_digest = ""
for asset in data.get("assets", []):
    name = asset.get("name", "")
    if pattern.match(name):
        asset_name = name
        asset_url = asset.get("browser_download_url", "")
        digest = asset.get("digest") or ""
        if digest.startswith("sha256:"):
            asset_digest = digest.split(":", 1)[1]
        break
if not asset_url:
    sys.stderr.write(f"Error: no macOS runner asset found for arch {arch}\n")
    sys.exit(1)
if not asset_digest:
    body = data.get("body") or ""
    m = re.search(rf"([a-fA-F0-9]{{64}})\s+\*?{re.escape(asset_name)}", body)
    if not m:
        m = re.search(rf"{re.escape(asset_name)}[^a-fA-F0-9]+([a-fA-F0-9]{{64}})", body)
    if m:
        asset_digest = m.group(1)
print(asset_url, asset_digest)
' <<< "${RELEASE_JSON}"
)

if [[ -z "${DOWNLOAD_URL}" || "${DOWNLOAD_URL}" != https://github.com/actions/runner/releases/download/* ]]; then
  echo "Error: invalid runner download URL: ${DOWNLOAD_URL}" >&2
  exit 1
fi

ARCHIVE_NAME="$(basename "${DOWNLOAD_URL}")"
curl -fsSL -o "${ARCHIVE_NAME}" "${DOWNLOAD_URL}"

if [[ -n "${EXPECTED_SHA256}" ]]; then
  echo "${EXPECTED_SHA256}  ${ARCHIVE_NAME}" | shasum -a 256 -c -
else
  echo "Warning: SHA-256 checksum not published in release metadata for ${ARCHIVE_NAME}" >&2
fi

tar -xzf "${ARCHIVE_NAME}" 1>/dev/null
rm -f "${ARCHIVE_NAME}"
echo "runner successfully installed"
