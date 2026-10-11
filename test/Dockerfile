FROM kalilinux/kali-rolling

# Install basic tools
RUN apt-get update && apt-get install -y \
    curl \
    wget \
    nmap \
    netcat-openbsd \
    iproute2 \
    iptables \
    dnsutils \
    tcpdump \
    && rm -rf /var/lib/apt/lists/*

# Create app directory
WORKDIR /app

# Copy binary (built for Linux)
COPY bin/mutiauk-linux-amd64 /app/bin/mutiauk-linux-amd64

# Copy configs
COPY configs/ /etc/mutiauk/

# Create test config that uses Docker networking
RUN echo 'daemon:\n\
  pid_file: /var/run/mutiauk.pid\n\
  socket_path: /var/run/mutiauk.sock\n\
tun:\n\
  name: tun0\n\
  mtu: 1400\n\
  address: 10.200.200.1/24\n\
socks5:\n\
  server: socks5:1080\n\
  timeout: 30s\n\
routes:\n\
  - destination: 172.31.0.100/32\n\
    comment: "Target nginx container"\n\
    enabled: true\n\
nat:\n\
  tcp_timeout: 1h\n\
  udp_timeout: 5m\n\
logging:\n\
  level: debug\n\
  format: console\n' > /etc/mutiauk/test-config.yaml

# Default command
CMD ["/app/bin/mutiauk-linux-amd64", "daemon", "start", "-c", "/etc/mutiauk/test-config.yaml", "-v"]
