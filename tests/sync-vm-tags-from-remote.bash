#!/usr/bin/env bash
# Sync Anklet template tags from the internal registry into the local demo registry.
# Source of truth: http://10.8.1.200:8083 (see tests/plugins/AVAILABLE_ANKA_TEMPLATES.md)
#
# Usage:
#   ./tests/sync-vm-tags-from-remote.bash
#   FORCE_SYNC=1 ./tests/sync-vm-tags-from-remote.bash   # re-pull/re-push even if tag exists locally
set -eo pipefail

ANKLET_TEMPLATE_UUID="84266873-da90-4e0d-903b-ed0233471f9f"
REMOTE_REGISTRY="${REMOTE_REGISTRY:-http://10.8.1.200:8083}"
LOCAL_REGISTRY="${LOCAL_REGISTRY:-local-demo}"

# Tags used by .github/workflows and AVAILABLE_ANKA_TEMPLATES.md.
# Push dependent/large tags last: reverting a parent tag can delete child tags.
TAGS=(
  6c14r
  2c4r
  3c6r
  8c14r
  12c20r
  12c50r
  20c20r
  6c14r-40gb
)

registry_has_tag() {
  local registry="$1"
  local tag="$2"
  anka registry -r "${registry}" show "${ANKLET_TEMPLATE_UUID}" tag 2>/dev/null | grep -qE "[[:space:]]${tag}[[:space:]]"
}

sync_tag() {
  local tag="$1"
  echo "INFO: syncing tag ${tag} from ${REMOTE_REGISTRY} -> ${LOCAL_REGISTRY}"
  anka registry -r "${REMOTE_REGISTRY}" pull "${ANKLET_TEMPLATE_UUID}" --tag "${tag}"
  anka registry -r "${LOCAL_REGISTRY}" revert "${ANKLET_TEMPLATE_UUID}" -t "${tag}" --yes 2>/dev/null || true
  anka push -f -t "${tag}" "${ANKLET_TEMPLATE_UUID}" "${LOCAL_REGISTRY}"
  echo "INFO: synced tag ${tag}"
}

for tag in "${TAGS[@]}"; do
  if registry_has_tag "${LOCAL_REGISTRY}" "${tag}" && [[ "${FORCE_SYNC:-}" != "1" ]]; then
    echo "INFO: ${LOCAL_REGISTRY} already has tag ${tag}; set FORCE_SYNC=1 to re-pull/re-push"
    continue
  fi
  sync_tag "${tag}"
done

echo "INFO: removing local template copy ${ANKLET_TEMPLATE_UUID}"
anka delete --yes "${ANKLET_TEMPLATE_UUID}" 2>/dev/null || true

echo "INFO: ${LOCAL_REGISTRY} tags for ${ANKLET_TEMPLATE_UUID}:"
anka registry -r "${LOCAL_REGISTRY}" show "${ANKLET_TEMPLATE_UUID}" tag
