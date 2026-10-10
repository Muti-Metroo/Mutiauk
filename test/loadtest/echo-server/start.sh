#!/bin/sh
echo "Starting iperf3 server on port 5201..."
iperf3 -s -p 5201 &

echo "Starting echo server..."
exec /usr/local/bin/echo-server
