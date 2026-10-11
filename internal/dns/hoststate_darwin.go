//go:build darwin

package dns

// On macOS the resolvectl and nm-dnsmasq backends do not exist; their host-state
// readers report nothing. The active backend is /etc/resolver, reported by
// EtcResolverHostState (in resolver_darwin.go).

// ResolvectlHostState is a no-op on macOS.
func ResolvectlHostState(_ string) (string, error) { return "", nil }

// NMDnsmasqHostState is a no-op on macOS.
func NMDnsmasqHostState() (string, error) { return "", nil }

// NMDnsmasqDropInPath is unused on macOS.
func NMDnsmasqDropInPath() string { return "" }
