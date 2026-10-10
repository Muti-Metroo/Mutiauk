package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/postalsys/mutiauk/internal/api"
	"github.com/postalsys/mutiauk/internal/autoroutes"
	"github.com/postalsys/mutiauk/internal/config"
	"github.com/postalsys/mutiauk/internal/route"
	"github.com/postalsys/mutiauk/internal/socks5"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// Mesh path status values for TraceResult.MeshPathStatus.
const (
	meshPathOK          = "ok"
	meshPathUnreachable = "unreachable"
	meshPathNotInMesh   = "not_in_mesh"
)

// TraceResult contains the result of a route trace
type TraceResult struct {
	Destination    string   `json:"destination"`
	Resolution     string   `json:"resolution,omitempty"`
	ResolvedIP     string   `json:"resolved_ip"`
	MatchedRoute   string   `json:"matched_route"`
	Interface      string   `json:"interface"`
	Gateway        string   `json:"gateway,omitempty"`
	IsMutiauk      bool     `json:"is_mutiauk"`
	MeshPath       []string `json:"mesh_path,omitempty"`
	Origin         string   `json:"origin,omitempty"`
	OriginID       string   `json:"origin_id,omitempty"`
	HopCount       int      `json:"hop_count,omitempty"`
	MeshPathStatus string   `json:"mesh_path_status,omitempty"`
	MeshPathError  string   `json:"mesh_path_error,omitempty"`
}

func newRouteTraceCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "trace <ip|domain>",
		Short: "Analyze routing for a destination",
		Long: `Trace the route path for an IP address or domain name.

Shows which interface handles the traffic and, if routed through Mutiauk,
displays the mesh path through Muti Metroo agents.

Examples:
  mutiauk route trace 10.10.5.100
  mutiauk route trace internal.corp.local
  mutiauk route trace 8.8.8.8 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			destination := args[0]

			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			result, err := traceWithDaemonOrFallback(cmd, cfg, destination)
			if err != nil {
				return err
			}

			if jsonOutput {
				return emitJSON(os.Stdout, result)
			}

			printTraceResult(result)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func buildTraceResult(destination string, lookup *route.LookupResult) *TraceResult {
	result := &TraceResult{
		Destination:  destination,
		ResolvedIP:   lookup.ResolvedIP.String(),
		MatchedRoute: lookup.MatchedRoute.String(),
		Interface:    lookup.Interface,
		IsMutiauk:    lookup.IsMutiauk,
	}

	if lookup.Gateway != nil && !lookup.Gateway.IsUnspecified() {
		result.Gateway = lookup.Gateway.String()
	}

	return result
}

// traceWithDaemonOrFallback prefers the daemon API (which has
// access to the in-memory mesh-host map and the active suffix set).
// When the daemon is not running the call falls through to a direct
// route lookup against the system resolver; mesh names that are only
// known to the daemon will fail with NXDOMAIN there.
func traceWithDaemonOrFallback(cmd *cobra.Command, cfg *config.Config, destination string) (*TraceResult, error) {
	daemon := newDaemonAPIClient(cfg)

	apiResult, err := daemon.TraceRoute(destination)
	if err != nil {
		return nil, err
	}
	if apiResult != nil {
		return traceResultFromAPI(apiResult), nil
	}

	lookup, err := route.ResolveAndLookup(destination, cfg.TUN.Name)
	if err != nil {
		return nil, err
	}

	result := buildTraceResult(destination, lookup)
	if net.ParseIP(destination) != nil {
		result.Resolution = api.ResolutionIP
	} else {
		result.Resolution = api.ResolutionSystem
	}

	if lookup.IsMutiauk {
		// The mesh path is read over the SOCKS5 endpoint, which always has an
		// address; no separate configuration is required.
		populateMeshPath(cmd, cfg, lookup, result, GetLogger())
	}

	return result, nil
}

// traceResultFromAPI flattens the wire-format daemon response into
// the CLI's display struct.
func traceResultFromAPI(r *api.RouteTraceResult) *TraceResult {
	out := &TraceResult{
		Destination:  r.Destination,
		Resolution:   r.Resolution,
		ResolvedIP:   r.ResolvedIP,
		MatchedRoute: r.MatchedRoute,
		Interface:    r.Interface,
		Gateway:      r.Gateway,
		IsMutiauk:    r.IsMutiauk,
		MeshPath:     r.MeshPath,
		Origin:       r.Origin,
		OriginID:     r.OriginID,
		HopCount:     r.HopCount,
	}
	if r.IsMutiauk {
		if len(r.MeshPath) > 0 {
			out.MeshPathStatus = meshPathOK
		} else {
			out.MeshPathStatus = meshPathNotInMesh
		}
	}
	return out
}

func populateMeshPath(cmd *cobra.Command, cfg *config.Config, lookup *route.LookupResult, result *TraceResult, logger *zap.Logger) {
	path, err := lookupMeshPath(cmd.Context(), cfg, lookup.ResolvedIP)
	if err != nil {
		logger.Warn("failed to lookup mesh path", zap.Error(err))
		result.MeshPathStatus = meshPathUnreachable
		result.MeshPathError = err.Error()
		return
	}
	if path == nil {
		result.MeshPathStatus = meshPathNotInMesh
		return
	}
	result.MeshPathStatus = meshPathOK
	result.MeshPath = path.PathDisplay
	result.Origin = path.Origin
	result.OriginID = path.OriginID
	result.HopCount = path.HopCount
}

// lookupMeshPath resolves the mesh path for ip from a one-shot SOCKS5 snapshot
// (CmdSnapshot) over the agent's SOCKS5 endpoint.
func lookupMeshPath(ctx context.Context, cfg *config.Config, ip net.IP) (*autoroutes.PathResult, error) {
	sc := socks5.NewClientWithOptions(
		cfg.SOCKS5.Server,
		socks5.NewAuthenticator(cfg.SOCKS5.Username, cfg.SOCKS5.Password),
		cfg.SOCKS5.Timeout,
		cfg.SOCKS5.KeepAlive,
		socks5.ClientOptions{
			Transport:  cfg.SOCKS5.Transport,
			WSPath:     cfg.SOCKS5.WSPath,
			WSUsername: cfg.SOCKS5.Username,
			WSPassword: cfg.SOCKS5.Password,
		},
	)
	snap, err := sc.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return autoroutes.LookupPathFromSnapshot(snap, ip)
}

func printTraceResult(r *TraceResult) {
	fmt.Printf("Destination:  %s\n", r.Destination)
	if r.Destination != r.ResolvedIP {
		fmt.Printf("Resolved:     %s%s\n", r.ResolvedIP, resolutionSuffix(r.Resolution))
	} else if r.Resolution == api.ResolutionIP {
		fmt.Println("Resolved:     (literal IP)")
	}
	fmt.Printf("Match:        %s", r.MatchedRoute)
	if r.Gateway != "" {
		fmt.Printf(" via %s", r.Gateway)
	}
	fmt.Println()
	fmt.Printf("Interface:    %s", r.Interface)
	if r.IsMutiauk {
		fmt.Print(" (Mutiauk)")
	}
	fmt.Println()

	if !r.IsMutiauk {
		fmt.Printf("Note:         %s\n", coverageNote(r.Interface))
		return
	}

	switch r.MeshPathStatus {
	case meshPathOK:
		fmt.Printf("Mesh Path:    %s\n", strings.Join(r.MeshPath, " -> "))
		fmt.Printf("Origin:       %s [%s]\n", r.Origin, r.OriginID)
		fmt.Printf("Hop Count:    %d\n", r.HopCount)
	case meshPathUnreachable:
		fmt.Printf("Mesh Path:    (snapshot unreachable: %s)\n", r.MeshPathError)
	case meshPathNotInMesh:
		fmt.Println("Mesh Path:    (not in mesh - no matching route advertised)")
	default:
		fmt.Println("Mesh Path:    (unknown)")
	}
}

// coverageNote renders the Note: line shown when the resolved IP
// isn't routed through Mutiauk, naming the interface the
// kernel actually selected.
func coverageNote(iface string) string {
	if iface == "" {
		return "Resolved IP has no matching kernel route; traffic will not reach Mutiauk."
	}
	return fmt.Sprintf("Resolved IP is not in any mutiauk route; kernel will send it via %s. Add a static route or autoroute to forward it through the mesh.", iface)
}

// resolutionSuffix renders a short tag describing how the
// destination was resolved, to be appended to the "Resolved:" line.
func resolutionSuffix(source string) string {
	switch source {
	case api.ResolutionMesh:
		return " (via mesh DNS)"
	case api.ResolutionSystem:
		return " (via system DNS)"
	default:
		return ""
	}
}
