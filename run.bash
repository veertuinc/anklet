#!/usr/bin/env bash
set -exo pipefail
YAML_CONFIG_FILE="${1}"
[ -z "${YAML_CONFIG_FILE}" ] && echo "ARG 1 (Yaml Config File; org-receiver-config.yml) is not set" && exit 1

SCRIPT_DIR=$(cd $(dirname "${BASH_SOURCE[0]}") && pwd)
cd $SCRIPT_DIR

# we use JQ here instead of pretty print in the logging.go so that we can ensure valid JSON is output from go
LOG_LEVEL=${LOG_LEVEL:-dev} go run main.go -c ${YAML_CONFIG_FILE} 2>&1 | tee /tmp/${YAML_CONFIG_FILE}.log | jq &
go_pid=$!
trap "kill -SIGINT $go_pid; wait $go_pid" EXIT
wait $go_pid