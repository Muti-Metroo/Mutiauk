#!/bin/bash
# Load test runner for Mutiauk TUN via Muti Metroo tunnel
# Run this from the kali container

set -e

ECHO_SERVER="172.31.0.100"
TCP_PORT=5000
UDP_PORT=5001
IPERF_PORT=5201
SOCKS_PROXY="mm-ingress:1080"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_phase() {
    echo ""
    echo "========================================"
    echo -e "${GREEN}PHASE: $1${NC}"
    echo "========================================"
    echo ""
}

wait_for_service() {
    local host=$1
    local port=$2
    local timeout=${3:-30}
    local elapsed=0

    log_info "Waiting for $host:$port..."
    while ! nc -z "$host" "$port" 2>/dev/null; do
        sleep 1
        elapsed=$((elapsed + 1))
        if [ $elapsed -ge $timeout ]; then
            log_error "Timeout waiting for $host:$port"
            return 1
        fi
    done
    log_info "$host:$port is available"
}

# Phase 1: Direct Connectivity
phase1_direct() {
    log_phase "1: Direct Connectivity"

    log_info "Testing TCP echo server..."
    echo "Hello TCP" | nc -w 5 "$ECHO_SERVER" "$TCP_PORT"
    if [ $? -eq 0 ]; then
        log_info "TCP direct: OK"
    else
        log_error "TCP direct: FAILED"
        return 1
    fi

    log_info "Testing UDP echo server..."
    echo "Hello UDP" | nc -u -w 2 "$ECHO_SERVER" "$UDP_PORT"
    if [ $? -eq 0 ]; then
        log_info "UDP direct: OK"
    else
        log_warn "UDP direct: May have succeeded (nc exits regardless)"
    fi

    log_info "Testing iperf3 server..."
    iperf3 -c "$ECHO_SERVER" -p "$IPERF_PORT" -t 2 -J > /dev/null 2>&1
    if [ $? -eq 0 ]; then
        log_info "iperf3 direct: OK"
    else
        log_error "iperf3 direct: FAILED"
        return 1
    fi
}

# Phase 2: SOCKS5 via Muti Metroo
phase2_socks5() {
    log_phase "2: SOCKS5 via Muti Metroo"

    log_info "Testing TCP via SOCKS5..."
    echo "Hello SOCKS5 TCP" | nc -X 5 -x "$SOCKS_PROXY" -w 10 "$ECHO_SERVER" "$TCP_PORT"
    if [ $? -eq 0 ]; then
        log_info "TCP via SOCKS5: OK"
    else
        log_error "TCP via SOCKS5: FAILED"
        return 1
    fi

    log_info "Testing curl via SOCKS5..."
    curl -s --socks5 "$SOCKS_PROXY" --connect-timeout 10 "http://$ECHO_SERVER:$TCP_PORT/" || true
    log_info "curl via SOCKS5: Completed (may show connection reset, that's OK for echo server)"
}

# Phase 3: Through TUN Interface
phase3_tun() {
    log_phase "3: Through TUN Interface"

    log_info "Starting Mutiauk TUN daemon..."
    mutiauk daemon start -c /etc/mutiauk/config.yaml &
    sleep 3

    log_info "Checking TUN interface..."
    if ip addr show tun0 2>/dev/null; then
        log_info "TUN interface tun0: UP"
    else
        log_error "TUN interface tun0: NOT FOUND"
        return 1
    fi

    log_info "Checking routes..."
    ip route | grep -E "172.31.0.0|tun0" || true

    log_info "Testing TCP through TUN..."
    echo "Hello TUN TCP" | nc -w 5 "$ECHO_SERVER" "$TCP_PORT"
    if [ $? -eq 0 ]; then
        log_info "TCP via TUN: OK"
    else
        log_error "TCP via TUN: FAILED"
        return 1
    fi

    log_info "Testing UDP through TUN..."
    echo "Hello TUN UDP" | nc -u -w 2 "$ECHO_SERVER" "$UDP_PORT"
    log_info "UDP via TUN: Sent (response may timeout, checking logs)"
}

# Phase 4: Load Testing
phase4_load() {
    log_phase "4: Load Testing (60s, 10 concurrent)"

    log_info "Ensuring TUN is running..."
    if ! ip addr show tun0 2>/dev/null; then
        log_info "Starting TUN daemon..."
        mutiauk daemon start -c /etc/mutiauk/config.yaml &
        sleep 3
    fi

    log_info "Running TCP throughput test with iperf3..."
    iperf3 -c "$ECHO_SERVER" -p "$IPERF_PORT" -t 60 -P 10 2>&1 | tee /tmp/iperf3_tcp.log

    log_info "Running UDP throughput test with iperf3..."
    iperf3 -c "$ECHO_SERVER" -p "$IPERF_PORT" -u -b 100M -t 60 -P 10 2>&1 | tee /tmp/iperf3_udp.log

    log_info "Running TCP connection churn test..."
    /scripts/load-tcp.sh "$ECHO_SERVER" "$TCP_PORT" 60 10 2>&1 | tee /tmp/load_tcp.log

    log_info "Running UDP packet load test..."
    /scripts/load-udp.sh "$ECHO_SERVER" "$UDP_PORT" 60 10 2>&1 | tee /tmp/load_udp.log
}

# Summary
print_summary() {
    log_phase "Summary"

    echo "Test logs saved to:"
    echo "  /tmp/iperf3_tcp.log"
    echo "  /tmp/iperf3_udp.log"
    echo "  /tmp/load_tcp.log"
    echo "  /tmp/load_udp.log"
    echo ""

    if [ -f /tmp/iperf3_tcp.log ]; then
        log_info "TCP iperf3 results:"
        grep -E "sender|receiver" /tmp/iperf3_tcp.log | tail -4 || true
    fi

    if [ -f /tmp/iperf3_udp.log ]; then
        log_info "UDP iperf3 results:"
        grep -E "sender|receiver|Lost" /tmp/iperf3_udp.log | tail -4 || true
    fi
}

# Main
main() {
    log_info "Starting load test suite..."
    log_info "Echo server: $ECHO_SERVER"
    log_info "SOCKS5 proxy: $SOCKS_PROXY"

    # Wait for services
    wait_for_service "$ECHO_SERVER" "$TCP_PORT" 60
    wait_for_service "mm-ingress" 1080 60

    # Run phases
    phase1_direct
    phase2_socks5
    phase3_tun
    phase4_load

    print_summary

    log_info "All tests completed!"
}

# Handle arguments
case "${1:-all}" in
    1|direct)
        wait_for_service "$ECHO_SERVER" "$TCP_PORT" 60
        phase1_direct
        ;;
    2|socks5)
        wait_for_service "mm-ingress" 1080 60
        phase2_socks5
        ;;
    3|tun)
        wait_for_service "$ECHO_SERVER" "$TCP_PORT" 60
        wait_for_service "mm-ingress" 1080 60
        phase3_tun
        ;;
    4|load)
        wait_for_service "$ECHO_SERVER" "$TCP_PORT" 60
        wait_for_service "mm-ingress" 1080 60
        phase4_load
        ;;
    all)
        main
        ;;
    *)
        echo "Usage: $0 [1|direct|2|socks5|3|tun|4|load|all]"
        exit 1
        ;;
esac
