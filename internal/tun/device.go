package tun

import (
	"io"
	"net"
)

// Device represents a TUN interface
type Device interface {
	io.ReadWriteCloser

	// Name returns the interface name
	Name() string

	// MTU returns the interface MTU
	MTU() int

	// SetMTU sets the interface MTU
	SetMTU(mtu int) error

	// File returns the underlying file descriptor
	File() int
}

// Config holds TUN device configuration
type Config struct {
	Name     string     // Interface name (e.g., "tun0")
	MTU      int        // Maximum transmission unit
	Address  net.IP     // IPv4 address
	Netmask  net.IPMask // IPv4 netmask
	Address6 net.IP     // IPv6 address
	Netmask6 net.IPMask // IPv6 prefix length (as mask)
	Persist  bool       // Keep interface after process exit
}

// DefaultConfig returns a default TUN configuration
func DefaultConfig() Config {
	return Config{
		Name:    "tun0",
		MTU:     1400,
		Address: net.ParseIP("10.200.200.1"),
		Netmask: net.CIDRMask(24, 32),
		Persist: false,
	}
}
