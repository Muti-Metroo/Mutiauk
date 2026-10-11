package cli

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	mdns "github.com/miekg/dns"
	"github.com/spf13/cobra"

	"github.com/Muti-Metroo/Mutiauk/internal/api"
	"github.com/Muti-Metroo/Mutiauk/internal/config"
	meshdns "github.com/Muti-Metroo/Mutiauk/internal/dns"
)

func newDNSCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dns",
		Short: "Mesh DNS commands",
		Long: `Inspect mesh DNS plumbing.

Use this when mesh hostnames don't resolve through the system resolver:
` + "`mutiauk dns status`" + ` shows which integration backend is active,
the active TLD set, host-side state, and live probes against the
resolver and the system stub.`,
	}
	cmd.AddCommand(newDNSStatusCmd())
	return cmd
}

func newDNSStatusCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show mesh DNS plumbing status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			report := buildDNSStatusReport(cfg)
			if jsonOutput {
				return emitJSON(os.Stdout, report)
			}
			printDNSStatusReport(report)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output in JSON format")
	return cmd
}

// dnsStatusReport is the on-screen / JSON shape of `dns status`. The
// Status block is populated from the daemon API when reachable; the
// Host block always reflects what the local host actually shows.
type dnsStatusReport struct {
	DaemonRunning bool                 `json:"daemon_running"`
	DaemonError   string               `json:"daemon_error,omitempty"`
	Status        *api.DNSStatusResult `json:"status,omitempty"`
	HostState     dnsHostStateReport   `json:"host_state"`
	Probes        []dnsProbeResult     `json:"probes,omitempty"`
}

type dnsHostStateReport struct {
	Backend            string `json:"backend"`
	ResolvectlOutput   string `json:"resolvectl_output,omitempty"`
	NMDnsmasqPath      string `json:"nm_dnsmasq_path,omitempty"`
	NMDnsmasqContent   string `json:"nm_dnsmasq_content,omitempty"`
	EtcResolverContent string `json:"etc_resolver_content,omitempty"`
	NRPTContent        string `json:"nrpt_content,omitempty"`
}

type dnsProbeResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func buildDNSStatusReport(cfg *config.Config) *dnsStatusReport {
	report := &dnsStatusReport{}
	if isDaemonRunning(cfg) {
		report.DaemonRunning = true
		client := api.NewClient(cfg.Daemon.SocketPath)
		status, err := client.DNSStatus()
		if err != nil {
			report.DaemonError = err.Error()
		} else {
			report.Status = status
		}
	}

	report.HostState = readDNSHostState(cfg, report.Status)
	report.Probes = runDNSProbes(cfg, report.Status)
	return report
}

// readDNSHostState looks at the host directly: which backend's
// artifacts are present, and what they say. When the daemon told us
// which backend is active, we only read that one's host state; when
// the daemon isn't running (or didn't pick a backend) we probe both
// so the operator can spot leftover state.
func readDNSHostState(cfg *config.Config, status *api.DNSStatusResult) dnsHostStateReport {
	out := dnsHostStateReport{Backend: meshdns.IntegrationNone}
	if status != nil && status.Backend != "" {
		out.Backend = status.Backend
	}

	probeResolvectl := out.Backend == meshdns.IntegrationResolvectl || status == nil
	probeNMDnsmasq := out.Backend == meshdns.IntegrationNMDnsmasq || status == nil
	probeEtcResolver := out.Backend == meshdns.IntegrationEtcResolver || status == nil
	probeNRPT := out.Backend == meshdns.IntegrationNRPT || status == nil

	if probeEtcResolver {
		if content, err := meshdns.EtcResolverHostState(); err == nil && content != "" {
			out.EtcResolverContent = content
			if status == nil && out.Backend == meshdns.IntegrationNone {
				out.Backend = meshdns.IntegrationEtcResolver
			}
		}
	}

	if probeResolvectl {
		resolvectl, err := meshdns.ResolvectlHostState(cfg.TUN.Name)
		if err == nil && resolvectl != "" {
			out.ResolvectlOutput = resolvectl
			if status == nil && (strings.Contains(resolvectl, "DNS Domain") || strings.Contains(resolvectl, "DNS Servers")) {
				out.Backend = meshdns.IntegrationResolvectl
			}
		}
	}
	if probeNMDnsmasq {
		nmContent, _ := meshdns.NMDnsmasqHostState()
		if nmContent != "" {
			out.NMDnsmasqPath = meshdns.NMDnsmasqDropInPath()
			out.NMDnsmasqContent = nmContent
			if status == nil && out.Backend == meshdns.IntegrationNone {
				out.Backend = meshdns.IntegrationNMDnsmasq
			}
		}
	}
	if probeNRPT {
		if content, err := meshdns.NRPTHostState(); err == nil && content != "" {
			out.NRPTContent = content
			if status == nil && out.Backend == meshdns.IntegrationNone {
				out.Backend = meshdns.IntegrationNRPT
			}
		}
	}
	return out
}

// runDNSProbes does live DNS queries against the local resolver and
// against the system stub to confirm each link in the chain.
func runDNSProbes(cfg *config.Config, status *api.DNSStatusResult) []dnsProbeResult {
	if status == nil || !status.Enabled || len(status.Suffixes) == 0 || status.Listen == "" {
		return nil
	}

	suffix := status.Suffixes[0]
	probeName := mdns.Fqdn("mutiauk-status-probe." + suffix)

	probes := []dnsProbeResult{
		probeServer("resolver direct", status.Listen, probeName),
		probeServer("system stub", systemStubAddr(), probeName),
	}
	return probes
}

// probeServer sends an A query for `name` to `addr` (host:port). NXDOMAIN
// counts as success: it proves the resolver is reachable and answering
// (it doesn't have a record for the synthetic name, which is the
// expected outcome).
func probeServer(label, addr, name string) dnsProbeResult {
	if addr == "" {
		return dnsProbeResult{Name: label, OK: false, Detail: "no address available to query"}
	}
	c := &mdns.Client{Net: "udp", Timeout: 2 * time.Second}
	msg := new(mdns.Msg)
	msg.SetQuestion(name, mdns.TypeA)
	resp, _, err := c.Exchange(msg, addr)
	if err != nil {
		return dnsProbeResult{Name: label, OK: false, Detail: fmt.Sprintf("%s: %v", addr, err)}
	}
	switch resp.Rcode {
	case mdns.RcodeSuccess, mdns.RcodeNameError:
		return dnsProbeResult{
			Name:   label,
			OK:     true,
			Detail: fmt.Sprintf("%s -> %s", addr, mdns.RcodeToString[resp.Rcode]),
		}
	default:
		return dnsProbeResult{
			Name:   label,
			OK:     false,
			Detail: fmt.Sprintf("%s -> %s", addr, mdns.RcodeToString[resp.Rcode]),
		}
	}
}

// systemStubAddr returns the first nameserver from /etc/resolv.conf,
// or an empty string when the file can't be read or has no entries.
func systemStubAddr() string {
	cc, err := mdns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil || len(cc.Servers) == 0 {
		return ""
	}
	port := cc.Port
	if port == "" {
		port = "53"
	}
	return net.JoinHostPort(cc.Servers[0], port)
}

func printDNSStatusReport(r *dnsStatusReport) {
	if !r.DaemonRunning {
		fmt.Println("Daemon is not running; showing host-side state only.")
		fmt.Println()
	} else if r.DaemonError != "" {
		fmt.Printf("Daemon API error: %s\n\n", r.DaemonError)
	}

	if r.Status != nil {
		s := r.Status
		fmt.Printf("Backend:    %s", s.Backend)
		if s.Preference != "" && s.Preference != "auto" {
			fmt.Printf(" (configured: %s)", s.Preference)
		} else if s.Backend != meshdns.IntegrationNone {
			fmt.Print(" (auto-detected)")
		}
		fmt.Println()
		if s.Listen != "" {
			fmt.Printf("Listen:     %s\n", s.Listen)
		}
		if len(s.Suffixes) > 0 {
			dotted := make([]string, len(s.Suffixes))
			for i, suf := range s.Suffixes {
				dotted[i] = "." + suf
			}
			fmt.Printf("Active TLDs: %s", strings.Join(dotted, " "))
			if ts := formatTimestamp(s.AppliedAt); ts != "" {
				fmt.Printf("    applied %s", ts)
			}
			fmt.Println()
		}
		fmt.Printf("Hosts:      %d registered\n", s.HostCount)
		fmt.Println()
	}

	hs := r.HostState
	switch hs.Backend {
	case meshdns.IntegrationResolvectl:
		fmt.Println("Host state (resolvectl):")
		printIndented(hs.ResolvectlOutput)
	case meshdns.IntegrationNMDnsmasq:
		fmt.Printf("Host state (%s):\n", hs.NMDnsmasqPath)
		printIndented(hs.NMDnsmasqContent)
	case meshdns.IntegrationEtcResolver:
		fmt.Println("Host state (/etc/resolver):")
		printIndented(hs.EtcResolverContent)
	case meshdns.IntegrationNRPT:
		fmt.Println("Host state (NRPT):")
		printIndented(hs.NRPTContent)
	default:
		fmt.Println("Host state: no integration artifacts present.")
	}

	if len(r.Probes) > 0 {
		fmt.Println()
		fmt.Println("Probes:")
		for _, p := range r.Probes {
			marker := LabelOK
			if !p.OK {
				marker = LabelFAIL
			}
			fmt.Printf("  [%s] %s -- %s\n", marker, p.Name, p.Detail)
		}
	}
}

func printIndented(text string) {
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if line == "" {
			continue
		}
		fmt.Printf("  %s\n", line)
	}
}
