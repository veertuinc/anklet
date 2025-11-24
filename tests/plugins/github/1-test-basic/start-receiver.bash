#!/usr/bin/env bash
set -exo pipefail

# Source the helper functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/helpers.bash"

if [[ ${CLEAN:-false} == true || "$1" == "--clean" ]]; then
    clean_anklet "receiver"
    exit 0
fi
clean_anklet "receiver"

start_anklet "receiver"

# check that it started properly
check_anklet_process