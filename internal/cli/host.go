package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/Muti-Metroo/Mutiauk/internal/api"
	"github.com/spf13/cobra"
)

func newHostCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host",
		Short: "Mesh hostname commands",
		Long: `Inspect mesh hostnames known to the daemon.

Mesh hostnames are managed centrally on the Muti Metroo agent's HTTP API.
The local daemon polls /api/dashboard alongside autoroutes and serves
A/AAAA records for the suffixes the agent advertises in
mesh_hosts_suffixes. Use this command to inspect what the daemon
currently serves; to add or remove a hostname or a suffix, talk to
the Muti Metroo agent.`,
	}

	cmd.AddCommand(newHostListCmd())
	return cmd
}

func newHostListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List mesh hostnames served by the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			client := api.NewClient(cfg.Daemon.SocketPath)
			if !client.IsRunning() {
				return fmt.Errorf("daemon is not running")
			}

			result, err := client.HostList()
			if err != nil {
				return fmt.Errorf("failed to list hosts: %w", err)
			}
			return printHostList(result, jsonOutput)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output in JSON format")
	return cmd
}

func printHostList(result *api.HostListResult, jsonOutput bool) error {
	if jsonOutput {
		return emitJSON(os.Stdout, result)
	}

	if !result.Enabled {
		fmt.Println("Mesh DNS resolver is not running (autoroutes is disabled).")
		return nil
	}

	if len(result.Suffixes) == 0 {
		fmt.Println("Active suffixes: (none advertised by agent)")
	} else {
		dotted := make([]string, len(result.Suffixes))
		for i, s := range result.Suffixes {
			dotted[i] = "." + s
		}
		fmt.Printf("Active suffixes: %s\n", strings.Join(dotted, " "))
	}

	if len(result.Hosts) == 0 {
		fmt.Println("No mesh hosts registered.")
		return nil
	}

	hosts := make([]api.MeshHostInfo, len(result.Hosts))
	copy(hosts, result.Hosts)
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tIPV4\tIPV6\tCOMMENT")
	for _, h := range hosts {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", h.Name, h.IPv4, h.IPv6, h.Comment)
	}
	return w.Flush()
}
