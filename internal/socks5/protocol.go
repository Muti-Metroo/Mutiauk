package socks5

import (
	"encoding/binary"
	"fmt"
	"net"
)

// SOCKS5 version
const Version = 0x05

// Authentication methods
const (
	AuthNone     byte = 0x00 // No authentication
	AuthGSSAPI   byte = 0x01 // GSSAPI
	AuthPassword byte = 0x02 // Username/password
	AuthNoAccept byte = 0xFF // No acceptable methods
)

// Commands
const (
	CmdConnect      byte = 0x01 // CONNECT
	CmdBind         byte = 0x02 // BIND
	CmdUDPAssociate byte = 0x03 // UDP ASSOCIATE
	CmdICMPEcho     byte = 0x04 // ICMP Echo (Muti Metroo extension)
	CmdRouteWatch   byte = 0x05 // Stream dashboard snapshots on change (Muti Metroo extension)
	CmdSnapshot     byte = 0x06 // One-shot dashboard snapshot (Muti Metroo extension)
	CmdExpose       byte = 0x07 // Run an expose session over the raw stream (Muti Metroo extension)
	CmdUDPStream    byte = 0x08 // UDP ASSOCIATE with datagrams framed on the stream (Muti Metroo extension)
)

// Address types
const (
	AddrTypeIPv4   byte = 0x01 // IPv4 address
	AddrTypeDomain byte = 0x03 // Domain name
	AddrTypeIPv6   byte = 0x04 // IPv6 address
)

// Reply codes
const (
	ReplySucceeded           byte = 0x00
	ReplyGeneralFailure      byte = 0x01
	ReplyNotAllowed          byte = 0x02
	ReplyNetworkUnreachable  byte = 0x03
	ReplyHostUnreachable     byte = 0x04
	ReplyConnectionRefused   byte = 0x05
	ReplyTTLExpired          byte = 0x06
	ReplyCommandNotSupported byte = 0x07
	ReplyAddressNotSupported byte = 0x08
)

// Error is a SOCKS5 protocol error
type Error struct {
	Code    byte
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("socks5 error (0x%02x): %s", e.Code, e.Message)
}

// ReplyMessage returns a human-readable message for a reply code
func ReplyMessage(code byte) string {
	switch code {
	case ReplySucceeded:
		return "succeeded"
	case ReplyGeneralFailure:
		return "general SOCKS server failure"
	case ReplyNotAllowed:
		return "connection not allowed by ruleset"
	case ReplyNetworkUnreachable:
		return "network unreachable"
	case ReplyHostUnreachable:
		return "host unreachable"
	case ReplyConnectionRefused:
		return "connection refused"
	case ReplyTTLExpired:
		return "TTL expired"
	case ReplyCommandNotSupported:
		return "command not supported"
	case ReplyAddressNotSupported:
		return "address type not supported"
	default:
		return fmt.Sprintf("unknown error (0x%02x)", code)
	}
}

// Address represents a SOCKS5 address
type Address struct {
	Type   byte
	IP     net.IP
	Domain string
	Port   uint16
}

// String returns the address as a string
func (a *Address) String() string {
	switch a.Type {
	case AddrTypeIPv4, AddrTypeIPv6:
		return net.JoinHostPort(a.IP.String(), fmt.Sprintf("%d", a.Port))
	case AddrTypeDomain:
		return net.JoinHostPort(a.Domain, fmt.Sprintf("%d", a.Port))
	default:
		return fmt.Sprintf("unknown(%d)", a.Type)
	}
}

// ToUDPAddr converts the address to a net.UDPAddr
func (a *Address) ToUDPAddr() *net.UDPAddr {
	return &net.UDPAddr{
		IP:   a.IP,
		Port: int(a.Port),
	}
}

// NewAddressFromUDPAddr creates an Address from a net.UDPAddr.
// If addr is nil or has no IP, returns a zero address (0.0.0.0:0).
func NewAddressFromUDPAddr(addr *net.UDPAddr) *Address {
	if addr == nil || addr.IP == nil {
		return &Address{Type: AddrTypeIPv4, IP: net.IPv4zero, Port: 0}
	}
	if ip4 := addr.IP.To4(); ip4 != nil {
		return &Address{Type: AddrTypeIPv4, IP: ip4, Port: uint16(addr.Port)}
	}
	return &Address{Type: AddrTypeIPv6, IP: addr.IP, Port: uint16(addr.Port)}
}

// ToTCPAddr converts the address to a net.TCPAddr
func (a *Address) ToTCPAddr() *net.TCPAddr {
	return &net.TCPAddr{
		IP:   a.IP,
		Port: int(a.Port),
	}
}

// ParseAddress parses a host:port string into an Address
func ParseAddress(addr string) (*Address, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address format: %w", err)
	}

	port, err := net.LookupPort("tcp", portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid port: %w", err)
	}

	a := &Address{Port: uint16(port)}

	// Try to parse as IP
	ip := net.ParseIP(host)
	if ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			a.Type = AddrTypeIPv4
			a.IP = ip4
		} else {
			a.Type = AddrTypeIPv6
			a.IP = ip
		}
	} else {
		// It's a domain
		a.Type = AddrTypeDomain
		a.Domain = host
	}

	return a, nil
}

// MarshalBinary encodes the address to bytes
func (a *Address) MarshalBinary() ([]byte, error) {
	var buf []byte

	switch a.Type {
	case AddrTypeIPv4:
		buf = make([]byte, 1+4+2)
		buf[0] = AddrTypeIPv4
		copy(buf[1:5], a.IP.To4())
		binary.BigEndian.PutUint16(buf[5:], a.Port)

	case AddrTypeIPv6:
		buf = make([]byte, 1+16+2)
		buf[0] = AddrTypeIPv6
		copy(buf[1:17], a.IP.To16())
		binary.BigEndian.PutUint16(buf[17:], a.Port)

	case AddrTypeDomain:
		buf = make([]byte, 1+1+len(a.Domain)+2)
		buf[0] = AddrTypeDomain
		buf[1] = byte(len(a.Domain))
		copy(buf[2:], a.Domain)
		binary.BigEndian.PutUint16(buf[2+len(a.Domain):], a.Port)

	default:
		return nil, fmt.Errorf("unknown address type: %d", a.Type)
	}

	return buf, nil
}

// ReadAddress reads a SOCKS5 address from bytes
func ReadAddress(data []byte) (*Address, int, error) {
	if len(data) < 1 {
		return nil, 0, fmt.Errorf("data too short")
	}

	a := &Address{Type: data[0]}
	var consumed int

	switch a.Type {
	case AddrTypeIPv4:
		if len(data) < 1+4+2 {
			return nil, 0, fmt.Errorf("data too short for IPv4 address")
		}
		a.IP = make(net.IP, 4)
		copy(a.IP, data[1:5])
		a.Port = binary.BigEndian.Uint16(data[5:7])
		consumed = 7

	case AddrTypeIPv6:
		if len(data) < 1+16+2 {
			return nil, 0, fmt.Errorf("data too short for IPv6 address")
		}
		a.IP = make(net.IP, 16)
		copy(a.IP, data[1:17])
		a.Port = binary.BigEndian.Uint16(data[17:19])
		consumed = 19

	case AddrTypeDomain:
		if len(data) < 2 {
			return nil, 0, fmt.Errorf("data too short for domain length")
		}
		domainLen := int(data[1])
		if len(data) < 1+1+domainLen+2 {
			return nil, 0, fmt.Errorf("data too short for domain")
		}
		a.Domain = string(data[2 : 2+domainLen])
		a.Port = binary.BigEndian.Uint16(data[2+domainLen : 4+domainLen])
		consumed = 4 + domainLen

	default:
		return nil, 0, fmt.Errorf("unknown address type: %d", a.Type)
	}

	return a, consumed, nil
}
