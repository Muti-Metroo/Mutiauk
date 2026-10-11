//go:build darwin

package tun

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// macOS utun control constants. SYSPROTO_CONTROL and UTUN_OPT_IFNAME are not
// exported by golang.org/x/sys/unix, so they are defined here.
const (
	sysprotoControl = 2 // SYSPROTO_CONTROL
	utunOptIfname   = 2 // UTUN_OPT_IFNAME, read with getsockopt at SYSPROTO_CONTROL
	utunControlName = "com.apple.net.utun_control"

	// afHeaderLen is the 4-byte BSD address-family header that the kernel
	// prepends to every utun packet (there is no IFF_NO_PI equivalent).
	afHeaderLen = 4
)

// darwinDevice implements Device using a macOS utun interface. Unlike Linux,
// utun frames each packet with a 4-byte address-family header; this type adds
// it on Write and strips it on Read so the rest of the stack deals only in raw
// IP packets, exactly like the Linux device.
type darwinDevice struct {
	file *os.File
	name string
	mtu  int

	// rbuf backs Read. Read is single-reader (driven by the link pump), so a
	// shared buffer is safe and avoids a per-packet allocation.
	rbuf []byte

	// wmu and wbuf back Write: the framed packet (AF header + IP packet) is
	// assembled in a reusable buffer instead of allocating per packet. Write
	// is called from several goroutines (the outbound pump, UDP/ICMP reply
	// paths), so the buffer is mutex-guarded.
	wmu  sync.Mutex
	wbuf []byte
}

// New creates a new utun device on macOS.
func New(cfg Config) (Device, error) {
	mtu := cfg.MTU
	if mtu <= 0 {
		mtu = 1400
	}

	fd, name, err := openUtun(cfg.Name)
	if err != nil {
		return nil, err
	}

	// Non-blocking so os.File integrates with the Go network poller instead of
	// pinning an OS thread per read.
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("set utun non-blocking: %w", err)
	}

	dev := &darwinDevice{
		file: os.NewFile(uintptr(fd), name),
		name: name,
		mtu:  mtu,
		rbuf: make([]byte, mtu+afHeaderLen),
	}

	if err := dev.configure(cfg); err != nil {
		dev.Close()
		return nil, fmt.Errorf("failed to configure interface: %w", err)
	}

	return dev, nil
}

// openUtun opens a utun device via an AF_SYSTEM control socket and returns the
// fd plus the kernel-assigned interface name (always "utunN").
func openUtun(name string) (int, string, error) {
	fd, err := unix.Socket(unix.AF_SYSTEM, unix.SOCK_DGRAM, sysprotoControl)
	if err != nil {
		return -1, "", fmt.Errorf("open AF_SYSTEM socket: %w", err)
	}

	ci := &unix.CtlInfo{}
	copy(ci.Name[:], utunControlName)
	if err := unix.IoctlCtlInfo(fd, ci); err != nil {
		unix.Close(fd)
		return -1, "", fmt.Errorf("query utun control id: %w", err)
	}

	unit, err := utunUnit(name)
	if err != nil {
		unix.Close(fd)
		return -1, "", err
	}

	if err := unix.Connect(fd, &unix.SockaddrCtl{ID: ci.Id, Unit: unit}); err != nil {
		unix.Close(fd)
		return -1, "", fmt.Errorf("create utun interface: %w", err)
	}

	ifname, err := unix.GetsockoptString(fd, sysprotoControl, utunOptIfname)
	if err != nil {
		unix.Close(fd)
		return -1, "", fmt.Errorf("read utun interface name: %w", err)
	}

	return fd, ifname, nil
}

// utunUnit maps a requested interface name to a kernel control unit. The kernel
// names utun interfaces "utunN" and selects unit N+1; unit 0 lets the kernel
// pick the next free interface. Any name that is not "utunN" (including the
// cross-platform default "tun0") means "let the kernel choose".
func utunUnit(name string) (uint32, error) {
	num, ok := strings.CutPrefix(name, "utun")
	if !ok || num == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid utun interface name %q", name)
	}
	return uint32(n + 1), nil
}

// configure assigns addresses and brings the interface up via ifconfig. utun is
// point-to-point, so the IPv4 address is set as its own peer; specific CIDRs are
// installed separately by the route manager.
func (d *darwinDevice) configure(cfg Config) error {
	if v4 := cfg.Address.To4(); v4 != nil {
		if err := ifconfig(d.name, "inet", v4.String(), v4.String(), "mtu", strconv.Itoa(d.mtu), "up"); err != nil {
			return err
		}
	} else if err := ifconfig(d.name, "mtu", strconv.Itoa(d.mtu), "up"); err != nil {
		return err
	}

	if cfg.Address6 != nil && cfg.Netmask6 != nil {
		ones, _ := cfg.Netmask6.Size()
		if err := ifconfig(d.name, "inet6", fmt.Sprintf("%s/%d", cfg.Address6.String(), ones)); err != nil {
			return err
		}
	}

	return nil
}

// ifconfig runs /sbin/ifconfig with the given arguments.
func ifconfig(args ...string) error {
	out, err := exec.Command("/sbin/ifconfig", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ifconfig %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Read reads one packet from the device and strips the BSD address-family
// header, returning a raw IP packet. It is not safe for concurrent use.
func (d *darwinDevice) Read(p []byte) (int, error) {
	n, err := d.file.Read(d.rbuf)
	if err != nil {
		return 0, err
	}
	if n <= afHeaderLen {
		return 0, nil
	}
	return copy(p, d.rbuf[afHeaderLen:n]), nil
}

// Write prepends the BSD address-family header (chosen from the IP version) and
// writes the packet to the device. The returned count is the number of IP bytes
// written, excluding the header.
func (d *darwinDevice) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	af := uint32(unix.AF_INET)
	if p[0]>>4 == 6 {
		af = uint32(unix.AF_INET6)
	}

	d.wmu.Lock()
	if cap(d.wbuf) < afHeaderLen+len(p) {
		d.wbuf = make([]byte, afHeaderLen+len(p))
	}
	out := d.wbuf[:afHeaderLen+len(p)]
	binary.BigEndian.PutUint32(out[:afHeaderLen], af)
	copy(out[afHeaderLen:], p)

	n, err := d.file.Write(out)
	d.wmu.Unlock()
	if err != nil {
		return 0, err
	}
	if n < afHeaderLen {
		return 0, nil
	}
	return n - afHeaderLen, nil
}

// Close closes the device, which removes the utun interface.
func (d *darwinDevice) Close() error {
	return d.file.Close()
}

// Name returns the kernel-assigned interface name (e.g. "utun4").
func (d *darwinDevice) Name() string {
	return d.name
}

// MTU returns the interface MTU.
func (d *darwinDevice) MTU() int {
	return d.mtu
}

// SetMTU sets the interface MTU.
func (d *darwinDevice) SetMTU(mtu int) error {
	if err := ifconfig(d.name, "mtu", strconv.Itoa(mtu)); err != nil {
		return err
	}
	d.mtu = mtu
	return nil
}

// File returns the underlying file descriptor. On macOS the network stack
// drives the device through Read/Write rather than the fd, so this exists only
// for interface compatibility.
func (d *darwinDevice) File() int {
	return int(d.file.Fd())
}
