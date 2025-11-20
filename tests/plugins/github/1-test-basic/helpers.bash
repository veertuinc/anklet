#!/usr/bin/env bash
set -aeo pipefail
PATH="/usr/local/bin:$PATH" # unable to find anka in path otherwise

# Helper functions for starting and managing anklet processes

# Clean up any running anklet processes tracked in /tmp/anklet-pids
clean_anklet() {
    local service_name="${1:-anklet}"
    echo "] Cleaning up $service_name..."
    if [[ -f /tmp/anklet-pids ]]; then
        for pid in $(cat /tmp/anklet-pids); do
            if ps -p $pid > /dev/null 2>&1; then
                echo "Process info for PID $pid:"
                ps -p $pid -o pid,ppid,comm,args
                echo "Sending SIGINT to PID $pid..."
                kill -SIGINT $pid || true
            else
                echo "Process with PID $pid is not running"
            fi
        done
        rm -f /tmp/anklet-pids
    fi
}

# Start anklet with nohup, background it, and track PIDs for cleanup
start_anklet() {
    local service_name="${1:-anklet}"
    echo "] Starting $service_name..."
    
    # Start anklet with nohup and background it
    nohup /tmp/anklet > /tmp/anklet.log 2>&1 &
    
    # Wait a moment to ensure the process starts
    sleep 1
    
    # Find the actual anklet process (not tail or other commands)
    # Use pgrep to find processes with "anklet" in the name, excluding tail and grep
    ANKLET_PID=$(pgrep -f "^/tmp/anklet" | head -1)
    
    if [[ -z "$ANKLET_PID" ]]; then
        echo "ERROR: Could not find anklet process"
        exit 1
    fi
    
    echo "Anklet parent PID: $ANKLET_PID"
    
    # Store only the parent PID for cleanup
    echo "$ANKLET_PID" > /tmp/anklet-pids
    echo "Stored parent PID for cleanup: $ANKLET_PID"
    
    # Give more time for processes to start
    sleep 3
    
    grep "ERROR" /tmp/anklet.log && {
        echo "ERROR: Anklet log contains errors"
        echo "===== /tmp/anklet.log contents ====="
        cat /tmp/anklet.log
        echo "===================================="
        exit 1
    }
    
    ps aux | grep "/tmp/anklet"
    sleep 3
    cat "/tmp/anklet.log"
    echo "Parent PID stored for cleanup:"
    cat /tmp/anklet-pids
}

