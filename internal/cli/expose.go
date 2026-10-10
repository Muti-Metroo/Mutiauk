package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/postalsys/mutiauk/internal/api"
	"github.com/spf13/cobra"
)

func newExposeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "expose",
		Short: "Manage forwards published to the upstream Muti Metroo agent",
		Long: `Expose lets the daemon publish local services to the mesh over a
persistent SOCKS5 stream (the CmdExpose extension) to the upstream
Muti Metroo agent. Each forward is keyed by a routing key. When a mesh
forward stream arrives for the key, Muti Metroo pipes it back over the
expose stream and Mutiauk serves the local folder or dials the
local TCP target.

Forwards declared in the config file are loaded at startup and on
SIGHUP; "expose add" and "expose remove" let you add or drop runtime
forwards (lost when the daemon restarts).`,
	}
	cmd.AddCommand(newExposeStatusCmd())
	cmd.AddCommand(newExposeListCmd())
	cmd.AddCommand(newExposeAddCmd())
	cmd.AddCommand(newExposeRemoveCmd())
	return cmd
}

func newExposeStatusCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show expose connection state and registered forwards",
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := callExposeStatus()
			if err != nil {
				return err
			}
			return printExposeStatus(result, jsonOutput, true)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output in JSON format")
	return cmd
}

func newExposeListCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered forwards (no connection summary)",
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := callExposeStatus()
			if err != nil {
				return err
			}
			return printExposeStatus(result, jsonOutput, false)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output in JSON format")
	return cmd
}

func newExposeAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a runtime forward",
	}
	cmd.AddCommand(newExposeAddFolderCmd())
	cmd.AddCommand(newExposeAddTCPCmd())
	return cmd
}

func newExposeAddFolderCmd() *cobra.Command {
	var comment string
	cmd := &cobra.Command{
		Use:   "folder <key> <path>",
		Short: "Expose an absolute folder path as a strict static file server",
		Long: `Serve files from <path> when the mesh sends a forward stream for
<key>. The server returns an empty 404 for any request that does not
exactly resolve to a regular file under <path> (no directory listings,
no index.html fallback, no welcome page).`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExposeAdd(args[0], "folder", args[1], "", comment)
		},
	}
	cmd.Flags().StringVar(&comment, "comment", "", "operator-facing description")
	return cmd
}

func newExposeAddTCPCmd() *cobra.Command {
	var comment string
	cmd := &cobra.Command{
		Use:   "tcp <key> <host:port>",
		Short: "Forward a routing key to a local TCP target",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExposeAdd(args[0], "tcp", "", args[1], comment)
		},
	}
	cmd.Flags().StringVar(&comment, "comment", "", "operator-facing description")
	return cmd
}

func newExposeRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <key>",
		Short: "Remove a runtime forward (config-defined keys cannot be removed here)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := exposeClient()
			if err != nil {
				return err
			}
			result, err := client.ExposeRemove(args[0])
			if err != nil {
				return err
			}
			if !result.Success {
				return fmt.Errorf("%s", result.Message)
			}
			fmt.Printf("Removed expose: %s\n", args[0])
			return nil
		},
	}
}

func runExposeAdd(key, ftype, path, target, comment string) error {
	client, err := exposeClient()
	if err != nil {
		return err
	}
	result, err := client.ExposeAdd(key, ftype, path, target, comment)
	if err != nil {
		return err
	}
	if !result.Success {
		return fmt.Errorf("%s", result.Message)
	}
	fmt.Printf("Added expose: %s (%s)\n", key, ftype)
	return nil
}

func callExposeStatus() (*api.ExposeStatusResult, error) {
	client, err := exposeClient()
	if err != nil {
		return nil, err
	}
	return client.ExposeStatus()
}

// exposeClient returns the daemon's Unix-socket client or an error if
// the daemon is unreachable. All expose subcommands require a live
// daemon (no fallback path).
func exposeClient() (*api.Client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	client := api.NewClient(cfg.Daemon.SocketPath)
	if !client.IsRunning() {
		return nil, fmt.Errorf("daemon is not running")
	}
	return client, nil
}

func printExposeStatus(r *api.ExposeStatusResult, jsonOutput, withSummary bool) error {
	if jsonOutput {
		return emitJSON(os.Stdout, r)
	}
	if !r.Enabled {
		fmt.Println("Expose is disabled (set expose.enabled in the config).")
		return nil
	}
	if withSummary {
		state := "disconnected"
		if r.Connected {
			state = "connected"
			if ts := formatTimestamp(r.ConnectedSince); ts != "" {
				state = "connected since " + ts
			}
		}
		fmt.Printf("Expose: %s\n", state)
		if r.LastError != "" {
			fmt.Printf("Last error: %s\n", r.LastError)
		}
	}
	if len(r.Forwards) == 0 {
		fmt.Println("No forwards registered.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KEY\tTYPE\tSOURCE\tTARGET/PATH\tSTATUS\tSTREAMS")
	for _, f := range r.Forwards {
		dest := f.Path
		if dest == "" {
			dest = f.Target
		}
		status := "[" + LabelWARN + "] pending"
		if f.Registered {
			status = "[" + LabelOK + "]"
		} else if f.Error != "" {
			status = "[" + LabelFAIL + "] " + f.Error
		}
		streams := fmt.Sprintf("%d", f.ActiveStreams)
		if r.MaxStreamsPerKey > 0 {
			streams = fmt.Sprintf("%d / %d", f.ActiveStreams, r.MaxStreamsPerKey)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			f.Key, f.Type, f.Source, dest, status, streams)
	}
	return w.Flush()
}
