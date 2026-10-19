#!/usr/bin/env bash
# End-to-end test of Mutiauk's TUN data path against real Muti Metroo agents.
#
# Builds Linux binaries of Mutiauk and of the agent (from --mm-src, default
# ../Muti-Metroo), then runs two containers on a private Docker network:
# the server runs an exit and an ingress agent and serves HTTP and UDP on
# 10.99.0.1; the client runs Mutiauk and reaches 10.99.0.1 only through the
# TUN interface. See server.sh and client.sh for the checks.
#
# Usage: test/tun-e2e/run.sh [--mm-src DIR]

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
MM_SRC="$REPO/../Muti-Metroo"
[ "${1:-}" = "--mm-src" ] && MM_SRC="$2"

ARCH="$(docker version --format '{{.Server.Arch}}')"
NET="mutiauk-tun-e2e-$$"
BIN="$(mktemp -d "${TMPDIR:-/tmp}/mutiauk-tun-e2e.XXXXXX")"
cleanup() {
  docker rm -f "$NET-server" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  rm -rf "$BIN"
}
trap cleanup EXIT

echo "building linux/$ARCH binaries"
(cd "$REPO" && CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -o "$BIN/mutiauk" ./cmd/mutiauk)
# The agent picks its own toolchain from its go.mod.
(cd "$MM_SRC" && env -u GOTOOLCHAIN CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -o "$BIN/muti-metroo" ./cmd/muti-metroo)

IMAGE="debian:bookworm-slim"
docker network create "$NET" >/dev/null
docker run -d --name "$NET-server" --hostname server --network "$NET" --network-alias server --platform "linux/$ARCH" \
  --cap-add NET_ADMIN -v "$BIN/muti-metroo:/bin/muti-metroo:ro" -v "$HERE/server.sh:/server.sh:ro" \
  "$IMAGE" /server.sh >/dev/null

echo "waiting for the server agents"
for _ in $(seq 1 180); do
  docker exec "$NET-server" test -f /ready 2>/dev/null && break
  sleep 1
done
if ! docker exec "$NET-server" test -f /ready 2>/dev/null; then
  echo "server did not become ready"; docker logs "$NET-server" | tail -20
  docker exec "$NET-server" sh -c 'tail -20 /exit.log /ingress.log' || true
  exit 1
fi

docker run --rm --network "$NET" --platform "linux/$ARCH" --cap-add NET_ADMIN --device /dev/net/tun \
  -v "$BIN/mutiauk:/bin/mutiauk:ro" -v "$HERE/client.sh:/client.sh:ro" \
  "$IMAGE" /client.sh
