#!/usr/bin/env bash
set -exo pipefail
RUNNER_HOME="${RUNNER_HOME:-"$HOME/actions-runner"}"
cd "${RUNNER_HOME}"
./svc.sh install
./svc.sh start
./svc.sh status
echo "runner started"