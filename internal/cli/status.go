package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Muti-Metroo/Mutiauk/internal/api"
	"github.com/Muti-Metroo/Mutiauk/internal/config"
	"github.com/Muti-Metroo/Mutiauk/internal/daemon"
	"github.com/Muti-Metroo/Mutiauk/internal/socks5"
	"github.com/spf13/cobra"
)

// StatusResult holds the complete status information
type StatusResult struct {
	Daemon       DaemonStatus           `json:"daemon"`
	TUN          TUNStatus              `json:"tun"`
	SOCKS5       SOCKS5Status           `json:"socks5"`
	AutoRoutes   *api.AutoRoutesStatus  `json:"autoroutes,omitempty"`
	Routes       []RouteStatus          `json:"routes"`
	Connectivity ConnectivityStatus     `json:"connectivity"`
}

// DaemonStatus holds daemon process status
type DaemonStatus struct {
	Running        bool   `json:"running"`
	PID            int    `json:"pid,omitempty"`
	Message        string `json:"message"`
	ConfigPath     string `json:"config_path,omitempty"`
	Uptime         string `json:"uptime,omitempty"`
	ConfigMismatch bool   `json:"config_mismatch,omitempty"`
}

// TUNStatus holds TUN interface status
type TUNStatus struct {
	Exists  bool   `json:"exists"`
	Name    string `json:"name"`
	Address string `json:"address,omitempty"`
	Message string `json:"message"`
}

// SOCKS5Status holds SOCKS5 server configuration status
type SOCKS5Status struct {
	Server string `json:"server"`
	Auth   bool   `json:"auth"`
}

// RouteStatus holds individual route status
type RouteStatus struct {
	Destination string `json:"destination"`
	Active      bool   `json:"active"`
	Comment     string `json:"comment,omitempty"`
}

// ConnectivityStatus holds connectivity test results.
//
// Expose probe semantics:
//   - ExposeChecked == false: not run (skipConnTests, daemon offline,
//     or expose disabled).
//   - ExposeChecked == true && ExposeConnected == true: expose stream up.
//   - ExposeChecked == true && ExposeConnected == false: expose stream
//     down; ExposeError carries the daemon-reported reason.
type ConnectivityStatus struct {
	SOCKS5Reachable bool   `json:"socks5_reachable"`
	SOCKS5Latency   string `json:"socks5_latency,omitempty"`
	SOCKS5Error     string `json:"socks5_error,omitempty"`
	TCPRouting      bool   `json:"tcp_routing"`
	TCPError        string `json:"tcp_error,omitempty"`
	UDPRouting      bool   `json:"udp_routing"`
	UDPError        string `json:"udp_error,omitempty"`
	ExposeChecked   bool   `json:"expose_checked"`
	ExposeConnected bool   `json:"expose_connected"`
	ExposeError     string `json:"expose_error,omitempty"`
}

// Component names accepted by `status --check`. Centralised so the
// flag validator can reject typos and the healthy-by-component
// computation has one source of truth.
const (
	componentDaemon = "daemon"
	componentTUN    = "tun"
	componentSOCKS5 = "socks5"
	componentTCP    = "tcp"
	componentUDP    = "udp"
	componentExpose = "expose"
)

func allComponents() []string {
	return []string{
		componentDaemon, componentTUN, componentSOCKS5,
		componentTCP, componentUDP, componentExpose,
	}
}

func newStatusCmd() *cobra.Command {
	var jsonOutput bool
	var skipConnTests bool
	var exitCode bool
	var quiet bool
	var checkScope []string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show system status and connectivity tests",
		Long: `Show comprehensive status including:
- Daemon process status
- TUN interface status
- Configured routes
- SOCKS5 proxy connectivity tests
- Expose stream health (when enabled)

Use --skip-tests to skip connectivity tests (faster).
Use --json for machine-readable output.
Use --exit-code to return non-zero when any component is unhealthy
(suitable for monitoring and CI).
Use --quiet to suppress all output, leaving only the exit code (implies
--exit-code; intended for monitoring scripts).
Use --check <component> (repeatable) to scope the exit code to specific
components: tun, socks5, tcp, udp, expose, daemon. With --check, other
components still print but don't affect the exit code -- pair with
--exit-code or --quiet for monitoring that needs to page a specific
team on a specific component.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			scope, err := resolveCheckScope(checkScope)
			if err != nil {
				return err
			}

			result := gatherStatus(cfg, skipConnTests)

			// --quiet implies --exit-code: there's no point computing
			// status silently if we don't surface it via the exit code.
			if quiet {
				exitCode = true
			} else if jsonOutput {
				if err := printJSONStatus(result); err != nil {
					return err
				}
			} else if err := printHumanStatus(result, cfg); err != nil {
				return err
			}

			if exitCode && !result.healthy(scope, skipConnTests) {
				os.Exit(1)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	cmd.Flags().BoolVar(&skipConnTests, "skip-tests", false, "Skip connectivity tests")
	cmd.Flags().BoolVar(&exitCode, "exit-code", false, "Exit non-zero if any component is unhealthy")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Suppress output (implies --exit-code)")
	cmd.Flags().StringSliceVar(&checkScope, "check", nil,
		"limit exit-code computation to these components (repeatable): "+
			strings.Join(allComponents(), ", "))

	return cmd
}

// resolveCheckScope validates the user-supplied component list. An
// empty list means "all components" (the legacy behavior). Returned
// as a set keyed by canonical component name so healthy() can do O(1)
// membership tests without an inner loop.
func resolveCheckScope(names []string) (map[string]bool, error) {
	all := allComponents()
	valid := make(map[string]bool, len(all))
	for _, n := range all {
		valid[n] = true
	}
	if len(names) == 0 {
		return valid, nil
	}
	scope := make(map[string]bool, len(names))
	for _, n := range names {
		n = strings.TrimSpace(strings.ToLower(n))
		if !valid[n] {
			return nil, fmt.Errorf("--check: unknown component %q (valid: %s)",
				n, strings.Join(all, ", "))
		}
		scope[n] = true
	}
	return scope, nil
}

// healthy reports whether every component in scope is OK. With
// skipConnTests, the network-probe components (socks5/tcp/udp/expose)
// pass automatically since we never measured them. Daemon and TUN are
// always evaluated against the actual probe results.
func (r *StatusResult) healthy(scope map[string]bool, skipConnTests bool) bool {
	if scope[componentDaemon] && !r.Daemon.Running {
		return false
	}
	if scope[componentTUN] && !r.TUN.Exists {
		return false
	}
	if skipConnTests {
		return true
	}
	if scope[componentSOCKS5] && !r.Connectivity.SOCKS5Reachable {
		return false
	}
	if scope[componentTCP] && !r.Connectivity.TCPRouting {
		return false
	}
	if scope[componentUDP] && !r.Connectivity.UDPRouting {
		return false
	}
	// Expose participates in the exit code only when the daemon
	// actually ran the probe (ExposeChecked). Disabled-expose
	// deployments never fail this gate.
	if scope[componentExpose] && r.Connectivity.ExposeChecked && !r.Connectivity.ExposeConnected {
		return false
	}
	return true
}

func gatherStatus(cfg *config.Config, skipConnTests bool) *StatusResult {
	// The daemon is the source of truth for live state (TUN, etc.) when it is
	// running. Fetch its API status once and thread it into the sub-checks so
	// they don't each re-query or fall back to local guesswork.
	apiStatus := daemonAPIStatus(cfg)

	result := &StatusResult{
		Daemon:       checkDaemonStatus(cfg, apiStatus),
		TUN:          checkTUNStatus(cfg, apiStatus),
		SOCKS5:       getSocks5Config(cfg),
		Routes:       checkRoutesStatus(cfg),
		Connectivity: ConnectivityStatus{},
	}

	// Surface autoroutes staleness: during an agent outage the last-snapshot
	// age is the only sign that auto routes no longer reflect the mesh.
	if apiStatus != nil && apiStatus.AutoRoutes.Enabled {
		ar := apiStatus.AutoRoutes
		result.AutoRoutes = &ar
	}

	if !skipConnTests {
		result.Connectivity = runConnectivityTests(cfg)
	}

	return result
}

// daemonAPIStatus returns the daemon's API status when it is reachable over the
// control socket, or nil when the daemon is not running (or the API is
// unreachable). Callers treat nil as "daemon offline".
func daemonAPIStatus(cfg *config.Config) *api.StatusResult {
	client := api.NewClient(cfg.Daemon.SocketPath)
	if !client.IsRunning() {
		return nil
	}
	apiStatus, err := client.Status()
	if err != nil {
		return nil
	}
	return apiStatus
}

func checkDaemonStatus(cfg *config.Config, apiStatus *api.StatusResult) DaemonStatus {
	status := DaemonStatus{
		Running: false,
		Message: "not running",
	}

	if apiStatus != nil {
		status.Running = true
		status.PID = apiStatus.PID
		status.ConfigPath = apiStatus.ConfigPath
		status.Uptime = apiStatus.Uptime
		status.Message = fmt.Sprintf("running (PID %d)", apiStatus.PID)

		if apiStatus.ConfigPath != cfgFile {
			status.ConfigMismatch = true
		}
		return status
	}

	// API unreachable: fall back to a PID file check so a wedged-but-alive
	// daemon still shows as running.
	pid, err := daemon.ReadPIDFile(cfg.Daemon.PIDFile)
	if err != nil {
		return status
	}

	if !pidAlive(pid) {
		status.Message = "not running (stale PID file)"
		return status
	}

	status.Running = true
	status.PID = pid
	status.Message = fmt.Sprintf("running (PID %d)", pid)
	return status
}

// checkTUNStatus reports the TUN interface. When the daemon is running it owns
// a live TUN device and reports its real kernel-assigned name (utunN on macOS,
// the Wintun adapter on Windows, the configured name on Linux), so trust that
// over local introspection. When the daemon is offline, fall back to a local
// `ip` lookup on Linux (a leftover interface may persist) and an honest
// "daemon not running" elsewhere -- the daemon owns the device, so without it
// there is nothing to introspect off Linux.
func checkTUNStatus(cfg *config.Config, apiStatus *api.StatusResult) TUNStatus {
	if apiStatus != nil {
		return TUNStatus{
			Exists:  true,
			Name:    apiStatus.TUNName,
			Address: apiStatus.TUNAddress,
			Message: "up",
		}
	}

	status := TUNStatus{
		Name:    cfg.TUN.Name,
		Exists:  false,
		Message: "interface not found",
	}

	if runtime.GOOS != "linux" {
		status.Message = "daemon not running"
		return status
	}

	out, err := exec.Command("ip", "link", "show", cfg.TUN.Name).Output()
	if err != nil {
		return status
	}

	if len(out) > 0 {
		status.Exists = true
		status.Message = "up"
	}

	out, err = exec.Command("ip", "-4", "addr", "show", cfg.TUN.Name).Output()
	if err == nil {
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "inet ") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					status.Address = parts[1]
				}
				break
			}
		}
	}

	return status
}

func getSocks5Config(cfg *config.Config) SOCKS5Status {
	return SOCKS5Status{
		Server: cfg.SOCKS5.Server,
		Auth:   cfg.HasAuth(),
	}
}

func checkRoutesStatus(cfg *config.Config) []RouteStatus {
	routes := make([]RouteStatus, 0)

	mgr, err := newRouteManager(cfg)
	if err != nil {
		return configRoutesToStatus(cfg)
	}

	activeRoutes, err := mgr.List()
	if err != nil {
		return configRoutesToStatus(cfg)
	}

	activeMap := make(map[string]bool)
	for _, r := range activeRoutes {
		if r.Destination != nil {
			activeMap[r.Destination.String()] = true
		}
	}

	for _, r := range cfg.Routes {
		if r.Enabled {
			routes = append(routes, RouteStatus{
				Destination: r.Destination,
				Active:      activeMap[r.Destination],
				Comment:     r.Comment,
			})
		}
	}

	return routes
}

func configRoutesToStatus(cfg *config.Config) []RouteStatus {
	routes := make([]RouteStatus, 0)
	for _, r := range cfg.Routes {
		if r.Enabled {
			routes = append(routes, RouteStatus{
				Destination: r.Destination,
				Active:      false,
				Comment:     r.Comment,
			})
		}
	}
	return routes
}

func runConnectivityTests(cfg *config.Config) ConnectivityStatus {
	status := ConnectivityStatus{}

	var auth socks5.Authenticator
	if cfg.HasAuth() {
		auth = &socks5.UserPassAuth{
			Username: cfg.SOCKS5.Username,
			Password: cfg.SOCKS5.Password,
		}
	}
	client := socks5.NewClientWithOptions(
		cfg.SOCKS5.Server,
		auth,
		cfg.SOCKS5.Timeout,
		cfg.SOCKS5.KeepAlive,
		socks5.ClientOptions{
			Transport: cfg.SOCKS5.Transport,
			WSPath:    cfg.SOCKS5.WSPath,
		},
	)

	start := time.Now()
	var conn net.Conn
	var err error
	if socks5.IsWebSocketURL(cfg.SOCKS5.Server) {
		// URL-form endpoints cannot be dialed raw ("too many colons in
		// address"); probe through the same WebSocket upgrade the data
		// path uses.
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, err = client.DialServer(probeCtx)
		probeCancel()
	} else {
		conn, err = net.DialTimeout("tcp", cfg.SOCKS5.Server, 5*time.Second)
	}
	if err != nil {
		status.SOCKS5Reachable = false
		status.SOCKS5Error = err.Error()
		return status
	}
	conn.Close()
	status.SOCKS5Reachable = true
	// Round to microseconds so sub-millisecond connects don't print
	// "0ms" (which used to mask broken-but-fast cases).
	status.SOCKS5Latency = time.Since(start).Round(time.Microsecond).String()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	probeTarget := cfg.Daemon.StatusProbeTarget
	if probeTarget == "" {
		// Defensive fallback for callers (e.g. tests) that build a
		// Config struct directly without going through Load().
		probeTarget = "example.com:80"
	}
	tcpConn, err := client.Connect(ctx, probeTarget)
	if err != nil {
		status.TCPRouting = false
		status.TCPError = fmt.Sprintf("%s: %s", probeTarget, err)
	} else {
		tcpConn.Close()
		status.TCPRouting = true
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	relay, err := client.UDPAssociate(ctx2, nil)
	if err != nil {
		status.UDPRouting = false
		status.UDPError = err.Error()
	} else {
		relay.Close()
		status.UDPRouting = true
	}

	probeExpose(cfg, &status)

	return status
}

// probeExpose surfaces the daemon's view of the expose stream so
// `status --check expose` can fail fast on a stale connection. We do
// not open a new stream here; we just ask the running daemon. If
// the daemon is unreachable or expose is disabled, the probe is
// reported as not run (ExposeChecked=false) so it doesn't influence
// the exit code.
func probeExpose(cfg *config.Config, status *ConnectivityStatus) {
	apiClient := api.NewClient(cfg.Daemon.SocketPath)
	if !apiClient.IsRunning() {
		return
	}
	st, err := apiClient.ExposeStatus()
	if err != nil || st == nil {
		return
	}
	if !st.Enabled {
		return
	}
	status.ExposeChecked = true
	status.ExposeConnected = st.Connected
	if !st.Connected {
		status.ExposeError = st.LastError
		if status.ExposeError == "" {
			status.ExposeError = "expose stream not connected"
		}
	}
}

// printExposeSummary prints a one-line summary of the expose
// subsystem after the routes table. Silent when expose is disabled
// or the daemon doesn't answer (status already reported elsewhere).
func printExposeSummary(cfg *config.Config) {
	client := api.NewClient(cfg.Daemon.SocketPath)
	st, err := client.ExposeStatus()
	if err != nil || st == nil || !st.Enabled {
		return
	}
	state := "disconnected"
	if st.Connected {
		state = "connected"
	}
	fmt.Println()
	fmt.Printf("Expose:        %s, %d forwards\n", state, len(st.Forwards))
}

func printJSONStatus(result *StatusResult) error {
	return emitJSON(os.Stdout, result)
}

func printHumanStatus(result *StatusResult, cfg *config.Config) error {
	const title = "Mutiauk Status"
	fmt.Println(title)
	fmt.Println(strings.Repeat("=", len(title)))
	fmt.Println()

	daemonInfo := result.Daemon.Message
	if result.Daemon.Uptime != "" {
		daemonInfo += fmt.Sprintf(", uptime %s", result.Daemon.Uptime)
	}
	fmt.Printf("Daemon:        %s\n", daemonInfo)
	if result.Daemon.ConfigPath != "" {
		fmt.Printf("Config:        %s\n", result.Daemon.ConfigPath)
	}

	tunInfo := result.TUN.Name
	if result.TUN.Address != "" {
		tunInfo = fmt.Sprintf("%s (%s)", result.TUN.Name, result.TUN.Address)
	}
	if result.TUN.Exists {
		fmt.Printf("TUN Interface: %s\n", tunInfo)
	} else {
		fmt.Printf("TUN Interface: %s - %s\n", result.TUN.Name, result.TUN.Message)
	}

	socks5Info := result.SOCKS5.Server
	if result.SOCKS5.Auth {
		socks5Info += " (with auth)"
	}
	fmt.Printf("SOCKS5 Server: %s\n", socks5Info)

	if result.AutoRoutes != nil {
		arInfo := fmt.Sprintf("enabled, %d routes", result.AutoRoutes.Count)
		if result.AutoRoutes.LastSnapshotAge != "" {
			arInfo += ", last snapshot " + result.AutoRoutes.LastSnapshotAge + " ago"
		} else {
			arInfo += ", no snapshot yet"
		}
		fmt.Printf("Autoroutes:      %s\n", arInfo)
	}

	fmt.Println()
	fmt.Println("Routes:")
	if len(result.Routes) == 0 {
		fmt.Println("  (no routes configured)")
	} else {
		fmt.Printf("  %-20s  %s\n", "DESTINATION", "STATUS")
		for _, r := range result.Routes {
			status := "inactive"
			if r.Active {
				status = "active"
			}
			fmt.Printf("  %-20s  %s\n", r.Destination, status)
		}
	}

	if result.Daemon.Running {
		printExposeSummary(cfg)
	}

	fmt.Println()
	fmt.Println("Connectivity:")

	if result.Connectivity.SOCKS5Reachable {
		fmt.Printf("  SOCKS5 Connection: [%s]   (%s)\n", LabelOK, result.Connectivity.SOCKS5Latency)
	} else if result.Connectivity.SOCKS5Error != "" {
		fmt.Printf("  SOCKS5 Connection: [%s] %s\n", LabelFAIL, result.Connectivity.SOCKS5Error)
	} else {
		fmt.Printf("  SOCKS5 Connection: [%s]\n", LabelSKIP)
	}

	if result.Connectivity.TCPRouting {
		fmt.Printf("  TCP Routing:       [%s]\n", LabelOK)
	} else if result.Connectivity.TCPError != "" {
		fmt.Printf("  TCP Routing:       [%s] %s\n", LabelFAIL, result.Connectivity.TCPError)
	} else {
		fmt.Printf("  TCP Routing:       [%s]\n", LabelSKIP)
	}

	if result.Connectivity.UDPRouting {
		fmt.Printf("  UDP Routing:       [%s]\n", LabelOK)
	} else if result.Connectivity.UDPError != "" {
		fmt.Printf("  UDP Routing:       [%s] %s\n", LabelFAIL, result.Connectivity.UDPError)
	} else {
		fmt.Printf("  UDP Routing:       [%s]\n", LabelSKIP)
	}

	switch {
	case !result.Connectivity.ExposeChecked:
		fmt.Printf("  Expose Stream:     [%s]\n", LabelSKIP)
	case result.Connectivity.ExposeConnected:
		fmt.Printf("  Expose Stream:     [%s]\n", LabelOK)
	default:
		fmt.Printf("  Expose Stream:     [%s] %s\n", LabelFAIL, result.Connectivity.ExposeError)
	}

	fmt.Println()
	if result.Daemon.ConfigMismatch {
		fmt.Printf("Warning: CLI config (%s) differs from daemon config (%s)\n", cfgFile, result.Daemon.ConfigPath)
		fmt.Println("         Commands may behave unexpectedly. Consider using: -c " + result.Daemon.ConfigPath)
	}
	if !result.Daemon.Running {
		fmt.Println("Note: Daemon is not running. Start with: sudo mutiauk daemon start")
	} else if !result.Connectivity.SOCKS5Reachable {
		fmt.Println("Warning: Cannot reach SOCKS5 server. Check if it is running.")
	} else if !result.Connectivity.TCPRouting {
		fmt.Println("Warning: TCP routing through SOCKS5 failed. Check SOCKS5 server configuration.")
	}

	return nil
}
