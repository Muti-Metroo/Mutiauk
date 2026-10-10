#!/bin/bash
# UDP load generator - packet throughput test
# Usage: load-udp.sh <host> <port> <duration_seconds> <concurrency>

HOST=${1:-172.31.0.100}
PORT=${2:-5001}
DURATION=${3:-60}
CONCURRENCY=${4:-10}

echo "UDP Load Test"
echo "  Host: $HOST:$PORT"
echo "  Duration: ${DURATION}s"
echo "  Concurrency: $CONCURRENCY"
echo ""

START_TIME=$(date +%s)
END_TIME=$((START_TIME + DURATION))

# Worker function
worker() {
    local id=$1
    local count=0

    while [ $(date +%s) -lt $END_TIME ]; do
        # Generate random payload (512 bytes)
        payload="UDP_PACKET_$(printf '%04d' $id)_$(printf '%06d' $count)_$(head -c 450 /dev/urandom | base64 | head -c 450)"

        # Send UDP packet
        echo "$payload" | nc -u -w 1 "$HOST" "$PORT" 2>/dev/null &

        count=$((count + 1))

        # Small delay to prevent overwhelming
        sleep 0.01
    done

    echo "WORKER:$id:PACKETS:$count"
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
