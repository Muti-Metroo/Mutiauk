package socks5

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Transport type for SOCKS5 connections
const (
	TransportTCP       = "tcp"
	TransportWebSocket = "websocket"
)

// Client connects to a SOCKS5 proxy
type Client struct {
	ServerAddr string
	Auth       Authenticator
	Timeout    time.Duration
	KeepAlive  time.Duration
	Transport  string // "tcp" (default) or "websocket"
	WSPath     string // WebSocket path (default: "/socks5")
	WSUsername string // WebSocket HTTP Basic Auth username (optional)
	WSPassword string // WebSocket HTTP Basic Auth password (optional)
}

// ClientOptions contains optional configuration for the SOCKS5 client
type ClientOptions struct {
	Transport  string // "tcp" (default) or "websocket"
	WSPath     string // WebSocket path (default: "/socks5")
	WSUsername string // WebSocket HTTP Basic Auth username (optional)
	WSPassword string // WebSocket HTTP Basic Auth password (optional)
}

// NewClient creates a new SOCKS5 client
func NewClient(serverAddr string, auth Authenticator, timeout, keepAlive time.Duration) *Client {
	return NewClientWithOptions(serverAddr, auth, timeout, keepAlive, ClientOptions{})
}

// NewClientWithOptions creates a new SOCKS5 client with additional options
func NewClientWithOptions(serverAddr string, auth Authenticator, timeout, keepAlive time.Duration, opts ClientOptions) *Client {
	if auth == nil {
		auth = &NoAuth{}
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if keepAlive == 0 {
		keepAlive = 60 * time.Second
	}

	transport := opts.Transport
	if transport == "" {
		transport = TransportTCP
	}

	wsPath := opts.WSPath
	if wsPath == "" {
		wsPath = "/socks5"
	}

	return &Client{
		ServerAddr: serverAddr,
		Auth:       auth,
		Timeout:    timeout,
		KeepAlive:  keepAlive,
		Transport:  transport,
		WSPath:     wsPath,
		WSUsername: opts.WSUsername,
		WSPassword: opts.WSPassword,
	}
}

// Connect establishes a TCP connection through the SOCKS5 proxy
func (c *Client) Connect(ctx context.Context, addr string) (net.Conn, error) {
	// Parse target address
	target, err := ParseAddress(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid target address: %w", err)
	}

	// Connect to SOCKS5 server
	conn, err := c.dialServer(ctx)
	if err != nil {
		return nil, err
	}

	// Set deadline for handshake
	if c.Timeout > 0 {
		conn.SetDeadline(time.Now().Add(c.Timeout))
	}

	// Perform handshake
	if err := c.handshake(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("handshake failed: %w", err)
	}

	// Send CONNECT request
	if err := c.sendConnect(conn, target); err != nil {
		conn.Close()
		return nil, fmt.Errorf("connect failed: %w", err)
	}

	// Clear deadline after successful connection
	conn.SetDeadline(time.Time{})

	return conn, nil
}

// UDPAssociate establishes a UDP relay through the SOCKS5 proxy
func (c *Client) UDPAssociate(ctx context.Context, localAddr *net.UDPAddr) (*UDPRelay, error) {
	// Connect to SOCKS5 server
	conn, err := c.dialServer(ctx)
	if err != nil {
		return nil, err
	}

	// Set deadline for handshake
	if c.Timeout > 0 {
		conn.SetDeadline(time.Now().Add(c.Timeout))
	}

	// Perform handshake
	if err := c.handshake(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("handshake failed: %w", err)
	}

	// Send UDP ASSOCIATE request
	relayAddr, err := c.sendUDPAssociate(conn, localAddr)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("udp associate failed: %w", err)
	}

	// Clear deadline
	conn.SetDeadline(time.Time{})

	// Fix relay address if server returns localhost/unspecified
	// Some SOCKS5 servers return 0.0.0.0 or 127.0.0.1 as relay address
	// In these cases, use the SOCKS5 server's address instead
	if relayAddr.IP != nil && (relayAddr.IP.IsLoopback() || relayAddr.IP.IsUnspecified()) {
		serverHost, _, _ := net.SplitHostPort(c.ServerAddr)
		serverIP := net.ParseIP(serverHost)
		if serverIP == nil {
			// Hostname - resolve it
			ips, err := net.LookupIP(serverHost)
			if err == nil && len(ips) > 0 {
				serverIP = ips[0]
			}
		}
		if serverIP != nil {
			relayAddr.IP = serverIP
			if serverIP.To4() != nil {
				relayAddr.Type = AddrTypeIPv4
			} else {
				relayAddr.Type = AddrTypeIPv6
			}
		}
	}

	// Create UDP connection to relay (use unconnected socket for flexibility)
	// Use "udp4" for IPv4 relay addresses - dual-stack "udp" sockets can have
	// routing issues in Docker/container environments where IPv6 isn't configured
	network := "udp"
	if relayAddr.Type == AddrTypeIPv4 || (relayAddr.IP != nil && relayAddr.IP.To4() != nil) {
		network = "udp4"
	}
	udpConn, err := net.ListenUDP(network, nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to create UDP socket: %w", err)
	}

	return &UDPRelay{
		controlConn: conn,
		relayConn:   udpConn,
		relayAddr:   relayAddr,
	}, nil
}

// ICMPAssociate establishes an ICMP echo relay through the SOCKS5 proxy.
// This is a Muti Metroo extension (command 0x04).
// After successful handshake, the connection becomes a bidirectional ICMP relay.
func (c *Client) ICMPAssociate(ctx context.Context, destIP net.IP) (*ICMPRelay, error) {
	// Connect to SOCKS5 server
	conn, err := c.dialServer(ctx)
	if err != nil {
		return nil, err
	}

	// Set deadline for handshake
	if c.Timeout > 0 {
		conn.SetDeadline(time.Now().Add(c.Timeout))
	}

	// Perform handshake
	if err := c.handshake(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("handshake failed: %w", err)
	}

	// Send ICMP Echo request with destination IP
	// The "port" is ignored for ICMP, but we need to send a valid address
	addr := &Address{
		Type: AddrTypeIPv4,
		IP:   destIP.To4(),
		Port: 0,
	}
	if addr.IP == nil {
		// IPv6
		addr.Type = AddrTypeIPv6
		addr.IP = destIP.To16()
	}

	_, err = c.sendRequest(conn, CmdICMPEcho, addr)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("icmp associate failed: %w", err)
	}

	// Clear deadline
	conn.SetDeadline(time.Time{})

	return &ICMPRelay{
		conn:   conn,
		destIP: destIP,
	}, nil
}

// DialServer establishes a connection to the SOCKS5 server using the
// configured transport, without performing a SOCKS5 handshake. It is meant
// for reachability probes; callers doing actual proxying should use Connect,
// UDPAssociate, or ICMPAssociate instead.
func (c *Client) DialServer(ctx context.Context) (net.Conn, error) {
	return c.dialServer(ctx)
}

// dialServer connects to the SOCKS5 server using the configured transport.
func (c *Client) dialServer(ctx context.Context) (net.Conn, error) {
	// Check if server address is a WebSocket URL
	if c.isWebSocketURL() {
		return dialWebSocket(ctx, c.ServerAddr, c.Timeout, c.WSUsername, c.WSPassword)
	}

	// Use explicit transport setting
	if c.Transport == TransportWebSocket {
		wsURL := c.buildWebSocketURL()
		return dialWebSocket(ctx, wsURL, c.Timeout, c.WSUsername, c.WSPassword)
	}

	// Default: TCP connection
	dialer := &net.Dialer{
		Timeout:   c.Timeout,
		KeepAlive: c.KeepAlive,
	}

	conn, err := dialer.DialContext(ctx, "tcp", c.ServerAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to SOCKS5 server: %w", err)
	}

	return conn, nil
}

// isWebSocketURL checks if ServerAddr is a WebSocket URL.
func (c *Client) isWebSocketURL() bool {
	return IsWebSocketURL(c.ServerAddr)
}

// IsWebSocketURL reports whether serverAddr is a ws:// or wss:// URL.
func IsWebSocketURL(serverAddr string) bool {
	return strings.HasPrefix(serverAddr, "ws://") || strings.HasPrefix(serverAddr, "wss://")
}

// buildWebSocketURL constructs a WebSocket URL from ServerAddr and WSPath.
// If ServerAddr doesn't have a scheme, defaults to wss://.
func (c *Client) buildWebSocketURL() string {
	if c.isWebSocketURL() {
		return c.ServerAddr
	}

	// Convert http/https URLs to ws/wss
	if strings.Contains(c.ServerAddr, "://") {
		u, err := url.Parse(c.ServerAddr)
		if err == nil {
			if u.Scheme == "http" {
				u.Scheme = "ws"
			} else {
				u.Scheme = "wss"
			}
			if u.Path == "" {
				u.Path = c.WSPath
			}
			return u.String()
		}
	}

	// No scheme - build URL with wss:// and path
	return "wss://" + c.ServerAddr + c.WSPath
}

// handshake performs the SOCKS5 handshake including authentication
func (c *Client) handshake(conn net.Conn) error {
	// Send method selection
	// +----+----------+----------+
	// |VER | NMETHODS | METHODS  |
	// +----+----------+----------+
	// | 1  |    1     | 1 to 255 |
	// +----+----------+----------+
	methods := []byte{Version, 1, c.Auth.Method()}
	if _, err := conn.Write(methods); err != nil {
		return fmt.Errorf("failed to send methods: %w", err)
	}

	// Read server response
	// +----+--------+
	// |VER | METHOD |
	// +----+--------+
	// | 1  |   1    |
	// +----+--------+
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return fmt.Errorf("failed to read method response: %w", err)
	}

	if resp[0] != Version {
		return fmt.Errorf("unexpected SOCKS version: %d", resp[0])
	}

	if resp[1] == AuthNoAccept {
		return fmt.Errorf("no acceptable authentication method")
	}

	if resp[1] != c.Auth.Method() {
		return fmt.Errorf("unexpected authentication method: %d", resp[1])
	}

	// Perform authentication if needed
	if err := c.Auth.Authenticate(conn); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	return nil
}

// sendRequest sends a SOCKS5 request with the given command and address.
// Returns the bound address from the reply.
func (c *Client) sendRequest(conn net.Conn, cmd byte, addr *Address) (*Address, error) {
	addrBytes, err := addr.MarshalBinary()
	if err != nil {
		return nil, err
	}

	// Build request: VER | CMD | RSV | ATYP | DST.ADDR | DST.PORT
	req := make([]byte, 3+len(addrBytes))
	req[0] = Version
	req[1] = cmd
	req[2] = 0x00 // Reserved
	copy(req[3:], addrBytes)

	if _, err := conn.Write(req); err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	return c.readReply(conn)
}

// sendConnect sends a CONNECT request
func (c *Client) sendConnect(conn net.Conn, target *Address) error {
	_, err := c.sendRequest(conn, CmdConnect, target)
	return err
}

// sendUDPAssociate sends a UDP ASSOCIATE request
func (c *Client) sendUDPAssociate(conn net.Conn, localAddr *net.UDPAddr) (*Address, error) {
	return c.sendRequest(conn, CmdUDPAssociate, NewAddressFromUDPAddr(localAddr))
}

// readReply reads a SOCKS5 reply and returns the bound address.
// The address is always read from the connection to maintain protocol state.
func (c *Client) readReply(conn net.Conn) (*Address, error) {
	// Read reply header
	// +----+-----+-------+------+----------+----------+
	// |VER | REP |  RSV  | ATYP | BND.ADDR | BND.PORT |
	// +----+-----+-------+------+----------+----------+
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, fmt.Errorf("failed to read reply header: %w", err)
	}

	if header[0] != Version {
		return nil, fmt.Errorf("unexpected SOCKS version in reply: %d", header[0])
	}

	if header[1] != ReplySucceeded {
		return nil, &Error{
			Code:    header[1],
			Message: ReplyMessage(header[1]),
		}
	}

	addr := &Address{Type: header[3]}

	switch addr.Type {
	case AddrTypeIPv4:
		buf := make([]byte, 6)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return nil, fmt.Errorf("failed to read IPv4 address: %w", err)
		}
		addr.IP = make(net.IP, 4)
		copy(addr.IP, buf[:4])
		addr.Port = binary.BigEndian.Uint16(buf[4:6])

	case AddrTypeIPv6:
		buf := make([]byte, 18)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return nil, fmt.Errorf("failed to read IPv6 address: %w", err)
		}
		addr.IP = make(net.IP, 16)
		copy(addr.IP, buf[:16])
		addr.Port = binary.BigEndian.Uint16(buf[16:18])

	case AddrTypeDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return nil, fmt.Errorf("failed to read domain length: %w", err)
		}
		domainLen := int(lenBuf[0])
		buf := make([]byte, domainLen+2)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return nil, fmt.Errorf("failed to read domain: %w", err)
		}
		addr.Domain = string(buf[:domainLen])
		addr.Port = binary.BigEndian.Uint16(buf[domainLen:])

	default:
		return nil, fmt.Errorf("unknown address type: %d", addr.Type)
	}

	return addr, nil
}

// UDPRelay handles UDP forwarding through SOCKS5
type UDPRelay struct {
	controlConn net.Conn     // TCP connection to keep UDP association alive
	relayConn   *net.UDPConn // UDP connection to relay
	relayAddr   *Address     // Address of the relay
	monitorOnce sync.Once    // Guards MonitorControlConn
}

// MonitorControlConn starts a goroutine that invokes onClose when the
// relay's control connection terminates (EOF or read error). Per RFC 1928 a
// UDP association ends when its TCP control connection closes, so onClose is
// the signal that the association is dead and must be torn down. The callback
// also fires when the relay is closed locally via Close. Only the first call
// to MonitorControlConn has any effect.
func (r *UDPRelay) MonitorControlConn(onClose func()) {
	r.monitorOnce.Do(func() {
		go func() {
			// A SOCKS5 server never sends data on the control connection;
			// any read result other than a blocking wait means the
			// association is gone.
			buf := make([]byte, 256)
			for {
				if _, err := r.controlConn.Read(buf); err != nil {
					onClose()
					return
				}
			}
		}()
	})
}

// Send sends a UDP datagram through the relay
func (r *UDPRelay) Send(data []byte, target *Address) error {
	// Build UDP relay header
	// +----+------+------+----------+----------+----------+
	// |RSV | FRAG | ATYP | DST.ADDR | DST.PORT |   DATA   |
	// +----+------+------+----------+----------+----------+
	// | 2  |  1   |  1   | Variable |    2     | Variable |
	// +----+------+------+----------+----------+----------+
	addrBytes, err := target.MarshalBinary()
	if err != nil {
		return err
	}

	packet := make([]byte, 3+len(addrBytes)+len(data))
	packet[0] = 0 // RSV
	packet[1] = 0 // RSV
	packet[2] = 0 // FRAG (no fragmentation)
	copy(packet[3:], addrBytes)
	copy(packet[3+len(addrBytes):], data)

	relayUDP := r.relayAddr.ToUDPAddr()
	_, err = r.relayConn.WriteTo(packet, relayUDP)
	return err
}

// Receive receives a UDP datagram from the relay
func (r *UDPRelay) Receive(buf []byte) (int, *Address, error) {
	n, _, err := r.relayConn.ReadFrom(buf)
	if err != nil {
		return 0, nil, err
	}

	if n < 4 {
		return 0, nil, fmt.Errorf("packet too short: %d bytes", n)
	}

	// Parse header
	if buf[2] != 0 {
		return 0, nil, fmt.Errorf("fragmented packets not supported")
	}

	// Parse source address
	addr, consumed, err := ReadAddress(buf[3:n])
	if err != nil {
		return 0, nil, fmt.Errorf("failed to parse address: %w", err)
	}

	dataStart := 3 + consumed
	dataLen := n - dataStart

	// Move data to beginning of buffer
	copy(buf, buf[dataStart:n])

	return dataLen, addr, nil
}

// LocalAddr returns the local UDP address
func (r *UDPRelay) LocalAddr() *net.UDPAddr {
	return r.relayConn.LocalAddr().(*net.UDPAddr)
}

// RemoteAddr returns the relay address we're sending to
func (r *UDPRelay) RemoteAddr() string {
	return r.relayAddr.String()
}

// RelayUDPAddr returns the relay address as *net.UDPAddr
func (r *UDPRelay) RelayUDPAddr() *net.UDPAddr {
	return r.relayAddr.ToUDPAddr()
}

// SetReadDeadline sets the read deadline on the relay connection
func (r *UDPRelay) SetReadDeadline(t time.Time) error {
	return r.relayConn.SetReadDeadline(t)
}

// Close closes the UDP relay
func (r *UDPRelay) Close() error {
	if r.relayConn != nil {
		r.relayConn.Close()
	}
	if r.controlConn != nil {
		return r.controlConn.Close()
	}
	return nil
}

// ICMPRelay handles ICMP echo relay through SOCKS5.
// Wire format for echo request (client -> server):
//
//	[Identifier:2][Sequence:2][PayloadLen:2][Payload:N]
//
// Wire format for echo reply (server -> client):
//
//	[Identifier:2][Sequence:2][PayloadLen:2][Payload:N]
//
// ICMPRelay is not safe for concurrent use. Callers must serialize access
// (e.g., via icmpSession.mu in the ICMP forwarder).
type ICMPRelay struct {
	conn   net.Conn
	destIP net.IP
}

// SendEcho sends an ICMP echo request through the relay.
func (r *ICMPRelay) SendEcho(id, seq uint16, payload []byte) error {
	buf := make([]byte, 6+len(payload))
	binary.BigEndian.PutUint16(buf[0:2], id)
	binary.BigEndian.PutUint16(buf[2:4], seq)
	binary.BigEndian.PutUint16(buf[4:6], uint16(len(payload)))
	copy(buf[6:], payload)

	_, err := r.conn.Write(buf)
	return err
}

// ReceiveEcho receives an ICMP echo reply from the relay.
// Returns the reply payload and sequence number.
func (r *ICMPRelay) ReceiveEcho() (payload []byte, seq uint16, err error) {
	header := make([]byte, 6)
	if _, err := io.ReadFull(r.conn, header); err != nil {
		return nil, 0, err
	}

	seq = binary.BigEndian.Uint16(header[2:4])
	payloadLen := binary.BigEndian.Uint16(header[4:6])

	// Read payload
	if payloadLen > 0 {
		payload = make([]byte, payloadLen)
		if _, err := io.ReadFull(r.conn, payload); err != nil {
			return nil, seq, err
		}
	}

	return payload, seq, nil
}

// SetReadDeadline sets the read deadline on the relay connection.
func (r *ICMPRelay) SetReadDeadline(t time.Time) error {
	return r.conn.SetReadDeadline(t)
}

// Close closes the ICMP relay.
func (r *ICMPRelay) Close() error {
	return r.conn.Close()
}
