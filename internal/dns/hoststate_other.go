//go:build !linux && !darwin

package dns

// ResolvectlHostState is a no-op on platforms without resolvectl.
func ResolvectlHostState(_ string) (string, error) { return "", nil }

// NMDnsmasqHostState is a no-op on platforms without NetworkManager.
func NMDnsmasqHostState() (string, error) { return "", nil }

// NMDnsmasqDropInPath is unused on platforms without NetworkManager.
func NMDnsmasqDropInPath() string { return "" }
