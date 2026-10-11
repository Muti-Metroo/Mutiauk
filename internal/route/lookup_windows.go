//go:build windows

package route

import (
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// LookupResult contains route lookup information.
type LookupResult struct {
	Destination  string     // Original input (IP or domain)
	ResolvedIP   net.IP     // Resolved IP address
	MatchedRoute *net.IPNet // Best matching route CIDR
	Interface    string     // Interface name
	Gateway      net.IP     // Gateway if any
	IsMutiauk    bool       // True if matches TUN interface
}

var (
	modiphlpapi       = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetBestRoute2 = modiphlpapi.NewProc("GetBestRoute2")
)

// Lookup finds the best matching kernel route for a destination IP using
// the IP Helper GetBestRoute2 API (the authoritative answer the OS would
// use, accounting for prefix length and metric).
func Lookup(ip net.IP, tunName string) (*LookupResult, error) {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return nil, fmt.Errorf("invalid IP %v", ip)
	}
	addr = addr.Unmap()

	row, err := getBestRoute2(addr)
	if err != nil {
		return nil, fmt.Errorf("GetBestRoute2 %s: %w", ip, err)
	}

	result := &LookupResult{
		ResolvedIP:   ip,
		MatchedRoute: prefixToIPNet(row.DestinationPrefix.Prefix()),
		Interface:    interfaceNameByLUID(row.InterfaceLUID, row.InterfaceIndex),
	}
	result.IsMutiauk = result.Interface != "" && result.Interface == tunName
	if gw := row.NextHop.Addr(); gw.IsValid() && !gw.IsUnspecified() {
		result.Gateway = gw.AsSlice()
	}
	return result, nil
}

// ResolveAndLookup resolves a domain/IP and looks up the route.
func ResolveAndLookup(destination string, tunName string) (*LookupResult, error) {
	ip := net.ParseIP(destination)
	if ip == nil {
		ips, err := net.LookupIP(destination)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve %s: %w", destination, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no IP addresses for %s", destination)
		}
		ip = ips[0]
		for _, a := range ips {
			if a.To4() != nil {
				ip = a // prefer IPv4
				break
			}
		}
	}

	result, err := Lookup(ip, tunName)
	if err != nil {
		return nil, err
	}
	result.Destination = destination
	return result, nil
}

// getBestRoute2 calls iphlpapi!GetBestRoute2 for the destination address,
// letting the OS pick the interface. The winipcfg types mirror the Win32
// SOCKADDR_INET / MIB_IPFORWARD_ROW2 layouts, so they are passed by
// pointer directly.
func getBestRoute2(dst netip.Addr) (*winipcfg.MibIPforwardRow2, error) {
	var destSock winipcfg.RawSockaddrInet
	if err := destSock.SetAddr(dst); err != nil {
		return nil, err
	}
	var bestRoute winipcfg.MibIPforwardRow2
	var bestSrc winipcfg.RawSockaddrInet

	r0, _, _ := procGetBestRoute2.Call(
		0, // InterfaceLuid (NULL -> any)
		0, // InterfaceIndex
		0, // SourceAddress (NULL)
		uintptr(unsafe.Pointer(&destSock)),
		0, // AddressSortOptions
		uintptr(unsafe.Pointer(&bestRoute)),
		uintptr(unsafe.Pointer(&bestSrc)),
	)
	if r0 != 0 {
		return nil, syscall.Errno(r0)
	}
	return &bestRoute, nil
}

// interfaceNameByLUID resolves an interface's friendly name, preferring
// the index (cheap) and falling back to the LUID's interface alias.
func interfaceNameByLUID(luid winipcfg.LUID, index uint32) string {
	if index != 0 {
		if iface, err := net.InterfaceByIndex(int(index)); err == nil {
			return iface.Name
		}
	}
	if row, err := luid.Interface(); err == nil {
		return row.Alias()
	}
	return ""
}
