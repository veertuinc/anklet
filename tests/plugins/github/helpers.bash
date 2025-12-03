#!/usr/bin/env bash
set -aeo pipefail
PATH="/usr/local/bin:/opt/homebrew/bin:$PATH" # unable to find anka in path otherwise

# Source shared functions if available (written by run-test-on-hosts.bash)
# This file contains all test functions: Redis, test reporting, log assertions, 
# GitHub workflow functions, and high-level test helpers.
[[ -f /tmp/redis-functions.bash ]] && source /tmp/redis-functions.bash