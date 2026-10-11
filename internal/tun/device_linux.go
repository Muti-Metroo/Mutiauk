//go:build linux

package tun

import (
	"fmt"
	"net"
	"os"
	"unsafe"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	tunDevice = "/dev/net/tun"
	ifnamsiz  = 16
)

// Flags for TUNSETIFF
const (
	cIFF_TUN   = 0x0001
	cIFF_NO_PI = 0x1000 // No packet info header
)

// linuxDevice implements Device for Linux
type linuxDevice struct {
	file *os.File
	name string
	mtu  int
}

// ifReq is the struct for TUNSETIFF ioctl
type ifReq struct {
	Name  [ifnamsiz]byte
	Flags uint16
	_     [22]byte // padding
}

// New creates a new TUN device on Linux
func New(cfg Config) (Device, error) {
	// Open /dev/net/tun
	file, err := os.OpenFile(tunDevice, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", tunDevice, err)
	}

	// Prepare the interface request
	var req ifReq
	req.Flags = cIFF_TUN | cIFF_NO_PI

	// Copy interface name if specified
	if cfg.Name != "" {
		copy(req.Name[:], cfg.Name)
	}

	// Create the TUN interface
	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		file.Fd(),
		unix.TUNSETIFF,
		uintptr(unsafe.Pointer(&req)),
	)
	if errno != 0 {
		file.Close()
		return nil, fmt.Errorf("TUNSETIFF ioctl failed: %w", errno)
	}

	// Get the actual interface name
	name := string(req.Name[:])
	for i, b := range req.Name {
		if b == 0 {
			name = string(req.Name[:i])
			break
		}
	}

	dev := &linuxDevice{
		file: file,
		name: name,
		mtu:  cfg.MTU,
	}

	// Set up the interface
	if err := dev.configure(cfg); err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to configure interface: %w", err)
	}

	return dev, nil
}

// configure sets up the TUN interface with addresses and brings it up
func (d *linuxDevice) configure(cfg Config) error {
	// Get the link
	link, err := netlink.LinkByName(d.name)
	if err != nil {
		return fmt.Errorf("failed to get link: %w", err)
	}

	// Set MTU
	if cfg.MTU > 0 {
		if err := netlink.LinkSetMTU(link, cfg.MTU); err != nil {
			return fmt.Errorf("failed to set MTU: %w", err)
		}
		d.mtu = cfg.MTU
	}

	// Add IPv4 address
	if cfg.Address != nil && cfg.Netmask != nil {
		addr := &netlink.Addr{
			IPNet: &net.IPNet{
				IP:   cfg.Address,
				Mask: cfg.Netmask,
			},
		}
		if err := netlink.AddrAdd(link, addr); err != nil {
			return fmt.Errorf("failed to add IPv4 address: %w", err)
		}
	}

	// Add IPv6 address
	if cfg.Address6 != nil && cfg.Netmask6 != nil {
		addr := &netlink.Addr{
			IPNet: &net.IPNet{
				IP:   cfg.Address6,
				Mask: cfg.Netmask6,
			},
		}
		if err := netlink.AddrAdd(link, addr); err != nil {
			return fmt.Errorf("failed to add IPv6 address: %w", err)
		}
	}

	// Bring interface up
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to bring interface up: %w", err)
	}

	return nil
}

// Read reads a packet from the TUN device
func (d *linuxDevice) Read(p []byte) (int, error) {
	return d.file.Read(p)
}

// Write writes a packet to the TUN device
func (d *linuxDevice) Write(p []byte) (int, error) {
	return d.file.Write(p)
}

// Close closes the TUN device
func (d *linuxDevice) Close() error {
	// Get the link and bring it down
	link, err := netlink.LinkByName(d.name)
	if err == nil {
		netlink.LinkSetDown(link)
	}
	return d.file.Close()
}

// Name returns the interface name
func (d *linuxDevice) Name() string {
	return d.name
}

// MTU returns the interface MTU
func (d *linuxDevice) MTU() int {
	return d.mtu
}

// SetMTU sets the interface MTU
func (d *linuxDevice) SetMTU(mtu int) error {
	link, err := netlink.LinkByName(d.name)
	if err != nil {
		return fmt.Errorf("failed to get link: %w", err)
	}

	if err := netlink.LinkSetMTU(link, mtu); err != nil {
		return fmt.Errorf("failed to set MTU: %w", err)
	}

	d.mtu = mtu
	return nil
}

// File returns the file descriptor
func (d *linuxDevice) File() int {
	return int(d.file.Fd())
}
