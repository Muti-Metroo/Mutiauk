//go:build windows

package tun

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

const (
	// wintunRingCapacity is the send/receive ring size in bytes. 4 MiB
	// is the value WireGuard uses; it must be a power of two between
	// 128 KiB and 64 MiB.
	wintunRingCapacity = 0x400000

	// wintunTunnelType is the adapter type label shown in Windows
	// network UIs.
	wintunTunnelType = "Mutiauk"
)

// windowsDevice implements Device on a Wintun adapter. Wintun delivers
// raw IP packets through a ring-buffer session (no BSD address-family
// header, unlike the macOS utun device), so Read/Write copy packets
// straight in and out. Shutdown follows the WireGuard pattern: a close
// flag plus a WaitGroup so Close drains in-flight Read/Write calls before
// ending the session.
type windowsDevice struct {
	adapter  *wintun.Adapter
	session  wintun.Session
	luid     winipcfg.LUID
	readWait windows.Handle
	name     string
	mtu      int

	closed    atomic.Bool
	running   sync.WaitGroup
	closeOnce sync.Once
}

// New creates (or reclaims and replaces) a Wintun adapter and configures
// its addresses and MTU.
func New(cfg Config) (Device, error) {
	mtu := cfg.MTU
	if mtu <= 0 {
		mtu = 1400
	}
	name := cfg.Name
	if name == "" {
		name = "mutiauk0"
	}

	if err := ensureWintunDLL(); err != nil {
		return nil, fmt.Errorf("wintun driver unavailable: %w", err)
	}

	adapter, err := createAdapter(name)
	if err != nil {
		return nil, err
	}

	dev := &windowsDevice{
		adapter: adapter,
		luid:    winipcfg.LUID(adapter.LUID()),
		name:    name,
		mtu:     mtu,
	}

	if err := dev.configure(cfg); err != nil {
		adapter.Close()
		return nil, fmt.Errorf("failed to configure interface: %w", err)
	}

	session, err := adapter.StartSession(wintunRingCapacity)
	if err != nil {
		adapter.Close()
		return nil, fmt.Errorf("wintun: start session: %w", err)
	}
	dev.session = session
	dev.readWait = session.ReadWaitEvent()

	return dev, nil
}

// createAdapter creates the named Wintun adapter. The common path
// (no pre-existing adapter) is tried first to avoid the driver's noisy
// "adapter not found" log on every start; only if creation fails do we
// reclaim a stale adapter of the same name left by a prior unclean exit
// (closing a Wintun adapter handle removes it) and retry.
func createAdapter(name string) (*wintun.Adapter, error) {
	adapter, err := wintun.CreateAdapter(name, wintunTunnelType, nil)
	if err == nil {
		return adapter, nil
	}

	if existing, oerr := wintun.OpenAdapter(name); oerr == nil {
		existing.Close()
		if adapter, err = wintun.CreateAdapter(name, wintunTunnelType, nil); err == nil {
			return adapter, nil
		}
	}
	return nil, fmt.Errorf("wintun: create adapter %q: %w", name, err)
}

// configure assigns the IPv4 (required) and IPv6 (optional) addresses and
// sets the interface MTU. IPv6 failures are tolerated so the device still
// works on hosts where IPv6 is disabled.
func (d *windowsDevice) configure(cfg Config) error {
	if p, ok := toPrefix(cfg.Address, cfg.Netmask); ok {
		if err := d.luid.SetIPAddressesForFamily(winipcfg.AddressFamily(windows.AF_INET), []netip.Prefix{p}); err != nil {
			return fmt.Errorf("set ipv4 address: %w", err)
		}
		if err := d.setFamilyMTU(windows.AF_INET, d.mtu); err != nil {
			return fmt.Errorf("set ipv4 mtu: %w", err)
		}
	}

	if p, ok := toPrefix(cfg.Address6, cfg.Netmask6); ok {
		// Tolerate IPv6 being disabled host-wide: log nothing here (the
		// device has no logger), just keep IPv4 working.
		if err := d.luid.SetIPAddressesForFamily(winipcfg.AddressFamily(windows.AF_INET6), []netip.Prefix{p}); err == nil {
			_ = d.setFamilyMTU(windows.AF_INET6, d.mtu)
		}
	}

	return nil
}

// setFamilyMTU sets the link MTU for one address family.
func (d *windowsDevice) setFamilyMTU(family winipcfg.AddressFamily, mtu int) error {
	row, err := d.luid.IPInterface(family)
	if err != nil {
		return err
	}
	row.NLMTU = uint32(mtu)
	return row.Set()
}

// Read returns one raw IP packet from the adapter. It blocks on the
// session read-wait event when the ring is empty, and returns os.ErrClosed
// once the device is closing. Single-reader (driven by the link pump).
func (d *windowsDevice) Read(p []byte) (int, error) {
	d.running.Add(1)
	defer d.running.Done()

	for {
		if d.closed.Load() {
			return 0, os.ErrClosed
		}
		packet, err := d.session.ReceivePacket()
		switch err {
		case nil:
			n := copy(p, packet)
			d.session.ReleaseReceivePacket(packet)
			return n, nil
		case windows.ERROR_NO_MORE_ITEMS:
			windows.WaitForSingleObject(d.readWait, windows.INFINITE)
			continue
		case windows.ERROR_HANDLE_EOF:
			return 0, os.ErrClosed
		default:
			return 0, fmt.Errorf("wintun read: %w", err)
		}
	}
}

// Write sends one raw IP packet to the adapter. A full ring drops the
// packet (best-effort, matching the macOS device: gVisor/TCP retransmit).
func (d *windowsDevice) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	d.running.Add(1)
	defer d.running.Done()
	if d.closed.Load() {
		return 0, os.ErrClosed
	}

	packet, err := d.session.AllocateSendPacket(len(p))
	switch err {
	case nil:
		copy(packet, p)
		d.session.SendPacket(packet)
		return len(p), nil
	case windows.ERROR_BUFFER_OVERFLOW:
		return len(p), nil // ring full: drop
	case windows.ERROR_HANDLE_EOF:
		return 0, os.ErrClosed
	default:
		return 0, fmt.Errorf("wintun write: %w", err)
	}
}

// Close drains in-flight Read/Write calls, ends the session, and removes
// the adapter.
func (d *windowsDevice) Close() error {
	d.closeOnce.Do(func() {
		d.closed.Store(true)
		if d.readWait != 0 {
			windows.SetEvent(d.readWait) // wake a blocked Read
		}
		d.running.Wait()
		d.session.End()
		d.adapter.Close()
	})
	return nil
}

func (d *windowsDevice) Name() string { return d.name }

func (d *windowsDevice) MTU() int { return d.mtu }

// SetMTU updates the interface MTU for both address families.
func (d *windowsDevice) SetMTU(mtu int) error {
	if err := d.setFamilyMTU(windows.AF_INET, mtu); err != nil {
		return err
	}
	_ = d.setFamilyMTU(windows.AF_INET6, mtu) // tolerate IPv6 disabled
	d.mtu = mtu
	return nil
}

// File has no meaning on Windows (Wintun exposes no file descriptor; the
// link layer is driven by the pump via Read/Write). Returns -1.
func (d *windowsDevice) File() int { return -1 }

// RemoveAdapter deletes a Wintun adapter by name if present. Used by
// `mutiauk cleanup` to reclaim a leftover adapter after a crash.
func RemoveAdapter(name string) error {
	if name == "" {
		name = "mutiauk0"
	}
	if err := ensureWintunDLL(); err != nil {
		return fmt.Errorf("wintun driver unavailable: %w", err)
	}
	adapter, err := wintun.OpenAdapter(name)
	if err != nil {
		return nil // not present: nothing to do
	}
	return adapter.Close()
}

// toPrefix converts a net.IP + net.IPMask into a netip.Prefix. ok is
// false when either is nil/invalid.
func toPrefix(ip net.IP, mask net.IPMask) (netip.Prefix, bool) {
	if ip == nil || mask == nil {
		return netip.Prefix{}, false
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Prefix{}, false
	}
	addr = addr.Unmap()
	ones, _ := mask.Size()
	return netip.PrefixFrom(addr, ones), true
}
