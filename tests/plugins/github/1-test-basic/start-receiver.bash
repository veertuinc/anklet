#!/usr/bin/env bash
set -exo pipefail

clean() {
    echo "] Cleaning up receiver..."
    for pid in $(cat /tmp/anklet-pids); do
        kill -SIGINT $pid || true
        wait $pid || true
    done
    rm -f /tmp/anklet-pids
}
if [[ ${CLEAN:-false} == true || "$1" == "--clean" ]]; then
    clean
    exit 0
fi
clean

echo "] Starting receiver..."

# Start anklet with nohup and background it
eval nohup /tmp/anklet > /tmp/anklet.log 2>&1 &
# Wait a moment to ensure the process starts
sleep 1
NOHUP_PID="$!"
echo "Nohup PID: $NOHUP_PID"

# Find all processes related to anklet
sleep 3  # Give more time for processes to start

grep "ERROR" /tmp/anklet.log && {
    echo "ERROR: Anklet log contains errors"
    exit 1
}

# Find all PIDs related to anklet
PIDS=$(pgrep -f "/tmp/anklet")
echo "Found anklet-related PIDs: $PIDS"

# Store all found PIDs
for pid in $PIDS; do
    echo "Adding PID to cleanup list: $pid"
    echo "$pid" >> /tmp/anklet-pids
done

# Also add the nohup PID if it's still running (if we started anklet with nohup)
if kill -0 "$NOHUP_PID" 2>/dev/null; then
    echo "Adding nohup PID to cleanup list: $NOHUP_PID"
    echo "$NOHUP_PID" >> /tmp/anklet-pids
fi

ps aux | grep "/tmp/anklet"
sleep 3
cat "/tmp/anklet.log"
echo "PIDs stored for cleanup:"
cat /tmp/anklet-pids
disown "$NOHUP_PID"