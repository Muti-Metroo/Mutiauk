#!/bin/bash
# Client side of the TUN end-to-end test: runs Mutiauk with a static route
# for 10.99.0.0/24, once over plain SOCKS5 and once over WebSocket, and
# checks TCP, UDP, and ICMP to the server's private address through the TUN.
apt-get update -qq >/dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq iproute2 python3 curl iputils-ping >/dev/null
SERVER=$(getent hosts server | awk '{print $1}')
FAILS=0
check() { if [ "$2" = "$3" ]; then echo "  ok    $1"; else echo "  FAIL  $1 (want '$2' got '$3')"; FAILS=$((FAILS+1)); fi; }
for transport in tcp websocket; do
  if [ $transport = tcp ]; then SRV="$SERVER:1080"; else SRV="ws://$SERVER:8443/socks5"; fi
  mkdir -p /etc/mutiauk
  cat > /etc/mutiauk/config.yaml <<Y
tun: {name: tun0, address: 10.200.200.1/24, mtu: 1400}
socks5: {server: "$SRV"}
routes: [{destination: 10.99.0.0/24}]
autoroutes: {enabled: false}
admin: {enabled: false}
Y
  /bin/mutiauk daemon start -c /etc/mutiauk/config.yaml > /mutiauk-$transport.log 2>&1 &
  PID=$!
  for i in $(seq 1 40); do ip route | grep -q 10.99.0.0 && break; sleep 0.25; done
  echo "== $transport ($SRV)"
  check "route via tun" "tun0" "$(ip route get 10.99.0.1 | grep -o 'dev [a-z0-9]*' | awk '{print $2}')"
  check "HTTP over TUN" "hello through the tun" "$(curl -s --max-time 10 http://10.99.0.1:8000/)"
  check "UDP over TUN" "echo:ping-$transport" "$(python3 -c "
import socket; s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); s.settimeout(10)
s.sendto(b'ping-$transport',('10.99.0.1',9000)); print(s.recvfrom(100)[0].decode())" 2>&1)"
  WANT_SHA=$(curl -s --max-time 10 http://10.99.0.1:8000/big.sha)
  START=$(date +%s%N)
  GOT_SHA=$(curl -s --max-time 60 http://10.99.0.1:8000/big.bin | sha256sum | cut -d' ' -f1)
  check "2 MiB download over TUN ($(( ($(date +%s%N) - START) / 1000000 )) ms)" "$WANT_SHA" "$GOT_SHA"
  check "ping over TUN" "0" "$(ping -c 2 -W 5 10.99.0.1 >/dev/null 2>&1; echo $?)"
  kill $PID; wait $PID 2>/dev/null; sleep 1
done
if [ "$FAILS" -gt 0 ]; then
  echo "$FAILS check(s) failed"
  for f in /mutiauk-*.log; do echo "--- $f"; tail -20 "$f"; done
  exit 1
fi
echo "all checks passed"
