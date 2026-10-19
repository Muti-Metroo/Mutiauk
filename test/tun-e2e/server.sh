#!/bin/bash
# Server side of the TUN end-to-end test: an exit agent and an ingress agent
# (SOCKS5 over TCP and WebSocket), plus an HTTP server and a UDP echo server
# on 10.99.0.1, an address only this container has. The client can reach it
# only through Mutiauk's TUN interface and the mesh.
set -e
apt-get update -qq >/dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq iproute2 python3 >/dev/null
ip link add dummy0 type dummy && ip addr add 10.99.0.1/24 dev dummy0 && ip link set dummy0 up
mkdir -p /www && echo "hello through the tun" > /www/index.html
head -c 2097152 /dev/urandom > /www/big.bin && sha256sum /www/big.bin | cut -d' ' -f1 > /www/big.sha
(cd /www && python3 -m http.server 8000 --bind 10.99.0.1 >/dev/null 2>&1 &)
python3 - <<'PY' &
import socket
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); s.bind(("10.99.0.1",9000))
while True:
    d,a=s.recvfrom(65535); s.sendto(b"echo:"+d,a)
PY
cat > /exit.yaml <<'Y'
agent: {data_dir: /data-exit}
listeners: [{transport: quic, address: "127.0.0.1:4433"}]
exit: {enabled: true, routes: ["10.99.0.0/24"]}
icmp: {enabled: true, allowed_cidrs: ["10.99.0.0/24"]}
Y
cat > /ingress.yaml <<'Y'
agent: {data_dir: /data-ingress}
peers: [{id: auto, transport: quic, address: "127.0.0.1:4433"}]
socks5:
  enabled: true
  address: "0.0.0.0:1080"
  websocket: {enabled: true, address: "0.0.0.0:8443", path: /socks5, plaintext: true}
http: {enabled: true, address: "127.0.0.1:8080"}
Y
/bin/muti-metroo run -c /exit.yaml > /exit.log 2>&1 &
sleep 1
/bin/muti-metroo run -c /ingress.yaml > /ingress.log 2>&1 &
# Ready once the ingress agent has learned the exit route.
until python3 -c "import json,urllib.request,sys; sys.exit(0 if json.load(urllib.request.urlopen('http://127.0.0.1:8080/healthz'))['route_count'] > 0 else 1)" 2>/dev/null; do sleep 0.5; done
touch /ready
sleep infinity
