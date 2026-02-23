#!/usr/bin/env bash
set -exo pipefail
[ -z "$1" ] && (echo "Error: Agent name argument is missing." && exit 1)
[ -z "$2" ] && (echo "Error: Buildkite token argument is missing." && exit 1)
[ -z "$3" ] && (echo "Error: Queue argument is missing." && exit 1)
[ -z "$4" ] && (echo "Error: Labels argument is missing." && exit 1)
[ -z "$5" ] && (echo "Error: Buildkite job id argument is missing." && exit 1)

AGENT_NAME="$1"
AGENT_TOKEN="$2"
QUEUE_NAME="$3"
LABELS_CSV="$4"
ACQUIRE_JOB_ID="$5"

TAGS="queue=${QUEUE_NAME},anklet=true"
IFS=',' read -r -a LABEL_ARRAY <<< "${LABELS_CSV}"
for label in "${LABEL_ARRAY[@]}"; do
  trimmed="$(echo "$label" | xargs)"
  [[ -z "$trimmed" ]] && continue
  [[ "$trimmed" == queue=* ]] && continue
  # Buildkite tags are key=value. Convert anka-template:foo -> anka-template=foo.
  converted="${trimmed/:/=}"
  if [[ "$converted" == *=* ]]; then
    TAGS="${TAGS},${converted}"
  fi
done

CONFIG_DIR="${HOME}/.buildkite-agent"
mkdir -p "${CONFIG_DIR}"
cat > "${CONFIG_DIR}/anklet.env" <<EOF
BUILDKITE_AGENT_NAME=${AGENT_NAME}
BUILDKITE_AGENT_TOKEN=${AGENT_TOKEN}
BUILDKITE_AGENT_TAGS=${TAGS}
BUILDKITE_ACQUIRE_JOB_ID=${ACQUIRE_JOB_ID}
EOF

echo "buildkite agent successfully configured"
