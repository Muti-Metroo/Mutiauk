#!/usr/bin/env python3
"""Test SOCKS5 UDP ASSOCIATE from container to host proxy."""

import socket
import struct
import sys

# DNS query for example.com (type A)
dns_query = bytes([
    0x12, 0x34,  # Transaction ID
    0x01, 0x00,  # Flags: standard query
    0x00, 0x01,  # Questions: 1
    0x00, 0x00,  # Answer RRs: 0
    0x00, 0x00,  # Authority RRs: 0
    0x00, 0x00,  # Additional RRs: 0
    # Query: example.com
    0x07, 0x65, 0x78, 0x61, 0x6d, 0x70, 0x6c, 0x65,  # "example"
    0x03, 0x63, 0x6f, 0x6d,  # "com"
    0x00,        # Root label
    0x00, 0x01,  # Type: A
    0x00, 0x01,  # Class: IN
])

def test_udp_associate(socks_host, socks_port=1080):
    print(f"Connecting to SOCKS5 proxy at {socks_host}:{socks_port}")

    # Connect to SOCKS5 proxy
    proxy = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    proxy.settimeout(10)
    proxy.connect((socks_host, socks_port))

    # SOCKS5 greeting (no auth)
    proxy.send(b'\x05\x01\x00')
    resp = proxy.recv(2)
    print(f"Auth response: {resp.hex()}")
    if resp != b'\x05\x00':
        print("Auth failed!")
        return False

    # UDP ASSOCIATE request (0.0.0.0:0 = any address)
    proxy.send(b'\x05\x03\x00\x01\x00\x00\x00\x00\x00\x00')
    reply = proxy.recv(10)
    print(f"UDP ASSOCIATE reply: {reply.hex()}")

    if reply[1] != 0x00:
        print(f"UDP ASSOCIATE failed with code {reply[1]}")
        return False

    # Parse relay address
    relay_ip = socket.inet_ntoa(reply[4:8])
    relay_port = struct.unpack('>H', reply[8:10])[0]
    print(f"Relay address: {relay_ip}:{relay_port}")

    # If relay is 0.0.0.0 or 127.0.0.1, use socks_host instead
    if relay_ip in ('0.0.0.0', '127.0.0.1'):
        relay_ip = socks_host
        print(f"Using SOCKS host as relay: {relay_ip}:{relay_port}")

    # Create UDP socket
    udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    udp.settimeout(5)

    # Build SOCKS5 UDP header + DNS query
    # RSV(2) + FRAG(1) + ATYP(1) + ADDR(4) + PORT(2) + DATA
    header = b'\x00\x00\x00\x01'  # RSV=0, FRAG=0, ATYP=IPv4
    header += socket.inet_aton('8.8.8.8')  # Destination IP (Google DNS)
    header += struct.pack('>H', 53)  # Destination port (DNS)

    packet = header + dns_query
    print(f"Sending {len(packet)} bytes to relay {relay_ip}:{relay_port}")

    # Send through relay
    udp.sendto(packet, (relay_ip, relay_port))

    # Receive response
    try:
        response, addr = udp.recvfrom(4096)
        print(f"Received {len(response)} bytes from {addr}")

        # Parse SOCKS5 UDP header
        if len(response) > 10:
            src_ip = socket.inet_ntoa(response[4:8])
            src_port = struct.unpack('>H', response[8:10])[0]
            dns_response = response[10:]
            print(f"Response from {src_ip}:{src_port}")
            print(f"DNS response: {dns_response.hex()[:80]}...")

            # Parse DNS response
            if len(dns_response) > 12:
                answers = dns_response[6:8]
                num_answers = struct.unpack('>H', answers)[0]
                print(f"DNS answers: {num_answers}")
            return True
    except socket.timeout:
        print("Timeout waiting for UDP response")
        return False
    finally:
        udp.close()
        proxy.close()

if __name__ == '__main__':
    host = sys.argv[1] if len(sys.argv) > 1 else 'host.docker.internal'
    port = int(sys.argv[2]) if len(sys.argv) > 2 else 1080

    success = test_udp_associate(host, port)
    sys.exit(0 if success else 1)
