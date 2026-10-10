#!/bin/bash
# TCP load generator - connection churn and throughput test
# Usage: load-tcp.sh <host> <port> <duration_seconds> <concurrency>

HOST=${1:-172.31.0.100}
PORT=${2:-5000}
DURATION=${3:-60}
CONCURRENCY=${4:-10}

echo "TCP Load Test"
echo "  Host: $HOST:$PORT"
echo "  Duration: ${DURATION}s"
echo "  Concurrency: $CONCURRENCY"
echo ""

# Metrics
CONNECTIONS=0
BYTES_SENT=0
BYTES_RECV=0
ERRORS=0
START_TIME=$(date +%s)
END_TIME=$((START_TIME + DURATION))

# Worker function
worker() {
    local id=$1
    local data="LOADTEST_$(printf '%04d' $id)_$(head -c 1000 /dev/urandom | base64 | head -c 1000)"

    while [ $(date +%s) -lt $END_TIME ]; do
        # Send data and receive echo
        result=$(echo "$data" | nc -w 5 "$HOST" "$PORT" 2>/dev/null)
        if [ $? -eq 0 ]; then
            echo "CONN:$id:OK"
        else
            echo "CONN:$id:ERR"
        fi
        # Small delay between connections
        sleep 0.1
    done
}

# Start workers
echo "Starting $CONCURRENCY workers..."
for i in $(seq 1 $CONCURRENCY); do
    worker $i &
done

# Wait for duration
echo "Running for ${DURATION} seconds..."
wait

# Calculate results
ACTUAL_DURATION=$(($(date +%s) - START_TIME))
echo ""
echo "=== Results ==="
echo "Duration: ${ACTUAL_DURATION}s"
echo "Workers: $CONCURRENCY"
echo "==============="
