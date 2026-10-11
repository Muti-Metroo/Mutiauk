//go:build windows

package dns

import (
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	// nrptConfigKey is the machine-wide Name Resolution Policy Table.
	// Each rule is a GUID-named subkey.
	nrptConfigKey = `SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig`

	// nrptMarkerName/Value tag the rules we own so we never touch a
	// foreign NRPT rule (the analogue of the macOS resolver-file marker).
	nrptMarkerName  = "MutiaukManaged"
	nrptMarkerValue = "Managed by Mutiauk"

	// nrptGUIDNamespace seeds the deterministic per-suffix GUID so the
	// same suffix always maps to the same rule subkey across restarts.
	nrptGUIDNamespace = "mutiauk-nrpt"

	// nrptConfigUseGenericServers (0x8) tells the resolver to forward
	// matching queries to GenericDNSServers. Version 2 is the Win10+
	// NRPT rule schema.
	nrptConfigUseGenericServers = 0x8
	nrptRuleVersion             = 2
)

var (
	moddnsapi                 = windows.NewLazySystemDLL("dnsapi.dll")
	procDnsFlushResolverCache = moddnsapi.NewProc("DnsFlushResolverCache")
)

func nrptAvailable() bool { return true }

func nrptPresent() bool {
	managed, _ := managedNRPTRules()
	return len(managed) > 0
}

// nrptIntegration drives Windows split DNS by writing one NRPT rule per
// active mesh TLD, each forwarding that namespace to the local Muti Metroo
// Entry resolver. Rules are reconciled to exactly the active suffix set
// on every Apply, and only rules carrying our marker are ever modified
// or removed.
type nrptIntegration struct {
	listenIP string
	logger   *zap.Logger
}

func newNRPTIntegration(listenIP string, logger *zap.Logger) Integration {
	return &nrptIntegration{listenIP: listenIP, logger: logger}
}

func (n *nrptIntegration) Name() string { return IntegrationNRPT }

// Apply reconciles the NRPT to the requested suffixes: it writes a rule
// for each and removes any of our stale rules.
func (n *nrptIntegration) Apply(suffixes []string) error {
	if n.listenIP == "" {
		return errors.New("nrpt: listenIP is required")
	}

	keep := make(map[string]bool, len(suffixes))
	for _, suf := range suffixes {
		ns := normalizeSuffix(suf)
		if ns == "" {
			continue
		}
		guid := nrptGUID(ns)
		keep[strings.ToLower(guid)] = true
		if err := n.writeRule(guid, ns); err != nil {
			return err
		}
	}

	if err := removeManagedNRPT(keep); err != nil {
		return err
	}

	flushDNSCache(n.logger)
	n.logger.Info("nrpt applied", zap.Strings("suffixes", suffixes), zap.String("resolver", n.listenIP))
	return nil
}

// Revert removes every NRPT rule Mutiauk created.
func (n *nrptIntegration) Revert() error {
	if err := removeManagedNRPT(nil); err != nil {
		return err
	}
	flushDNSCache(n.logger)
	return nil
}

// writeRule creates or updates the NRPT rule subkey for one namespace,
// refusing to clobber a rule we do not own.
func (n *nrptIntegration) writeRule(guid, namespace string) error {
	path := nrptConfigKey + `\` + guid
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, path, registry.ALL_ACCESS)
	if err != nil {
		return fmt.Errorf("nrpt: create rule %s: %w", guid, err)
	}
	defer key.Close()

	if v, _, err := key.GetStringValue(nrptMarkerName); err == nil && v != nrptMarkerValue {
		return fmt.Errorf("nrpt: rule %s exists and is not managed by Mutiauk", guid)
	}

	if err := key.SetStringsValue("Name", []string{namespace}); err != nil {
		return fmt.Errorf("nrpt: set Name: %w", err)
	}
	if err := key.SetStringValue("GenericDNSServers", n.listenIP); err != nil {
		return fmt.Errorf("nrpt: set GenericDNSServers: %w", err)
	}
	if err := key.SetDWordValue("ConfigOptions", nrptConfigUseGenericServers); err != nil {
		return fmt.Errorf("nrpt: set ConfigOptions: %w", err)
	}
	if err := key.SetDWordValue("Version", nrptRuleVersion); err != nil {
		return fmt.Errorf("nrpt: set Version: %w", err)
	}
	if err := key.SetStringValue(nrptMarkerName, nrptMarkerValue); err != nil {
		return fmt.Errorf("nrpt: set marker: %w", err)
	}
	return nil
}

// removeManagedNRPT deletes our rules whose GUID is not in keep. A nil
// keep removes all of them.
func removeManagedNRPT(keep map[string]bool) error {
	managed, err := managedNRPTRules()
	if err != nil {
		return err
	}
	for _, name := range managed {
		if keep[strings.ToLower(name)] {
			continue
		}
		if err := registry.DeleteKey(registry.LOCAL_MACHINE, nrptConfigKey+`\`+name); err != nil {
			return fmt.Errorf("nrpt: delete rule %s: %w", name, err)
		}
	}
	return nil
}

// managedNRPTRules returns the subkey names of NRPT rules that carry our
// management marker. A missing/inaccessible config key is treated as
// "no managed rules" (best-effort, as this drives revert and diagnostics).
func managedNRPTRules() ([]string, error) {
	parent, err := registry.OpenKey(registry.LOCAL_MACHINE, nrptConfigKey, registry.READ)
	if err != nil {
		return nil, nil
	}
	defer parent.Close()

	names, err := parent.ReadSubKeyNames(-1)
	if err != nil {
		return nil, fmt.Errorf("nrpt: read rules: %w", err)
	}

	var managed []string
	for _, name := range names {
		sub, err := registry.OpenKey(registry.LOCAL_MACHINE, nrptConfigKey+`\`+name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := sub.GetStringValue(nrptMarkerName)
		sub.Close()
		if err == nil && v == nrptMarkerValue {
			managed = append(managed, name)
		}
	}
	return managed, nil
}

// NRPTHostState renders our NRPT rules for `dns status`.
func NRPTHostState() (string, error) {
	managed, err := managedNRPTRules()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, name := range managed {
		sub, err := registry.OpenKey(registry.LOCAL_MACHINE, nrptConfigKey+`\`+name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		ns, _, _ := sub.GetStringsValue("Name")
		servers, _, _ := sub.GetStringValue("GenericDNSServers")
		sub.Close()
		fmt.Fprintf(&b, "%s: Name=%s GenericDNSServers=%s\n", name, strings.Join(ns, ","), servers)
	}
	return b.String(), nil
}

// normalizeSuffix lowercases a suffix and gives it the single leading dot
// that NRPT namespaces require (e.g. "mesh." -> ".mesh").
func normalizeSuffix(s string) string {
	s = strings.ToLower(strings.Trim(strings.TrimSpace(s), "."))
	if s == "" {
		return ""
	}
	return "." + s
}

// nrptGUID derives a stable, brace-wrapped GUID string for a namespace so
// each suffix maps to a deterministic rule subkey.
func nrptGUID(namespace string) string {
	sum := sha1.Sum([]byte(nrptGUIDNamespace + namespace))
	return fmt.Sprintf("{%08X-%04X-%04X-%04X-%012X}",
		binary.BigEndian.Uint32(sum[0:4]),
		binary.BigEndian.Uint16(sum[4:6]),
		binary.BigEndian.Uint16(sum[6:8]),
		binary.BigEndian.Uint16(sum[8:10]),
		sum[10:16],
	)
}

// flushDNSCache best-effort flushes the Windows resolver cache so NRPT
// changes take effect immediately.
func flushDNSCache(logger *zap.Logger) {
	if err := procDnsFlushResolverCache.Find(); err != nil {
		logger.Debug("nrpt: DnsFlushResolverCache unavailable", zap.Error(err))
		return
	}
	if r, _, err := procDnsFlushResolverCache.Call(); r == 0 {
		logger.Debug("nrpt: DnsFlushResolverCache failed", zap.Error(err))
	}
}
