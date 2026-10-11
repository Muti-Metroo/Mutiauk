package cli

import (
	"fmt"
	"net"
	"os"
	"text/tabwriter"

	"github.com/postalsys/mutiauk/internal/api"
	"github.com/postalsys/mutiauk/internal/config"
	"github.com/postalsys/mutiauk/internal/route"
	"github.com/spf13/cobra"
)

func newRouteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "route",
		Short: "Route management commands",
	}

	cmd.AddCommand(newRouteAddCmd())
	cmd.AddCommand(newRouteRemoveCmd())
	cmd.AddCommand(newRouteListCmd())
	cmd.AddCommand(newRoutePlanCmd())
	cmd.AddCommand(newRouteApplyCmd())
	cmd.AddCommand(newRouteCheckCmd())
	cmd.AddCommand(newRouteTraceCmd())

	return cmd
}

func newRouteAddCmd() *cobra.Command {
	var comment string
	var persist bool
	var force bool

	cmd := &cobra.Command{
		Use:   "add <cidr>",
		Short: "Add a route",
		Long: `Add a route to the kernel routing table.

By default, routes are added to the kernel only and will be lost on reboot.
Use --persist to also save the route to the config file.

This command requires the daemon to be running so the in-memory state
stays in sync with the kernel. If the daemon is offline, pass --force
to mutate the kernel and config directly. NOT recommended in
production: when the daemon next starts, its in-memory state is
re-applied and may revert direct edits.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cidr := args[0]

			_, _, err := net.ParseCIDR(cidr)
			if err != nil {
				return fmt.Errorf("invalid CIDR: %w", err)
			}

			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			daemonClient := newDaemonAPIClient(cfg)
			if result, err := daemonClient.AddRoute(cidr, comment, persist); err != nil {
				return err
			} else if result != nil {
				fmt.Printf("Added route: %s\n", cidr)
				if result.Persisted {
					fmt.Printf("Saved to config: %s\n", cfgFile)
				} else {
					printEphemeralRouteWarning(cidr)
				}
				return nil
			}

			if !force {
				return errDaemonOfflineNoForce("route add")
			}
			printDaemonOfflineForceWarning()
			return addRouteDirect(cfg, cidr, comment, persist)
		},
	}

	cmd.Flags().StringVar(&comment, "comment", "", "comment for the route")
	cmd.Flags().BoolVar(&persist, "persist", false, "save route to config file")
	cmd.Flags().BoolVar(&force, "force", false, "mutate kernel + config directly when the daemon is offline (NOT recommended)")

	return cmd
}

func addRouteDirect(cfg *config.Config, cidr, comment string, persist bool) error {
	_, ipNet, _ := net.ParseCIDR(cidr)

	mgr, err := newRouteManager(cfg)
	if err != nil {
		return err
	}

	r := route.Route{
		Destination: ipNet,
		Comment:     comment,
		Enabled:     true,
	}

	if err := mgr.Add(r); err != nil {
		return fmt.Errorf("failed to add route: %w", err)
	}

	fmt.Printf("Added route: %s\n", ipNet.String())

	if persist {
		cfg.Routes = append(cfg.Routes, config.RouteConfig{
			Destination: cidr,
			Comment:     comment,
			Enabled:     true,
		})
		if err := cfg.Save(cfgFile); err != nil {
			return fmt.Errorf("route added but failed to save config: %w", err)
		}
		fmt.Printf("Saved to config: %s\n", cfgFile)
	} else {
		printEphemeralRouteWarning(ipNet.String())
	}

	return nil
}

func printEphemeralRouteWarning(cidr string) {
	fmt.Println("Warning: route is ephemeral and will not survive reboot or daemon restart.")
	fmt.Printf("  To persist, run: mutiauk route add %s --persist\n", cidr)
}

func newRouteRemoveCmd() *cobra.Command {
	var persist bool
	var force bool

	cmd := &cobra.Command{
		Use:   "remove <cidr>",
		Short: "Remove a route",
		Long: `Remove a route from the kernel routing table.

Use --persist to also remove the route from the config file.

This command requires the daemon to be running. If the daemon is
offline, pass --force to mutate the kernel and config directly. NOT
recommended in production (see ` + "`route add --help`" + ` for details).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cidr := args[0]

			_, _, err := net.ParseCIDR(cidr)
			if err != nil {
				return fmt.Errorf("invalid CIDR: %w", err)
			}

			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			daemonClient := newDaemonAPIClient(cfg)
			if result, err := daemonClient.RemoveRoute(cidr, persist); err != nil {
				return err
			} else if result != nil {
				fmt.Printf("Removed route: %s\n", cidr)
				if result.Persisted {
					fmt.Printf("Removed from config: %s\n", cfgFile)
				}
				return nil
			}

			if !force {
				return errDaemonOfflineNoForce("route remove")
			}
			printDaemonOfflineForceWarning()
			return removeRouteDirect(cfg, cidr, persist)
		},
	}

	cmd.Flags().BoolVar(&persist, "persist", false, "remove route from config file")
	cmd.Flags().BoolVar(&force, "force", false, "mutate kernel + config directly when the daemon is offline (NOT recommended)")

	return cmd
}

// errDaemonOfflineNoForce builds the error returned by route
// add/remove when the daemon is unreachable and the operator did
// not pass --force. Callers in route.go and any future
// route-mutating subcommands should use this so the message stays
// consistent across the binary.
func errDaemonOfflineNoForce(op string) error {
	return fmt.Errorf(
		"%s refused: daemon is not running, so kernel and in-memory state cannot be kept in sync.\n"+
			"  - start the daemon: sudo mutiauk daemon start\n"+
			"  - or pass --force to mutate kernel + config directly (not recommended in production)",
		op,
	)
}

// printDaemonOfflineForceWarning prints a single highly-visible
// warning before the direct-kernel fallback runs. Kept loud so
// scripts piping the output cannot easily miss it.
func printDaemonOfflineForceWarning() {
	fmt.Fprintln(os.Stderr, "WARNING: Daemon is offline. Direct changes may be reverted on next daemon start.")
}

func removeRouteDirect(cfg *config.Config, cidr string, persist bool) error {
	_, ipNet, _ := net.ParseCIDR(cidr)

	mgr, err := newRouteManager(cfg)
	if err != nil {
		return err
	}

	r := route.Route{
		Destination: ipNet,
	}

	if err := mgr.Remove(r); err != nil {
		return fmt.Errorf("failed to remove route: %w", err)
	}

	fmt.Printf("Removed route: %s\n", ipNet.String())

	if persist {
		for i, rc := range cfg.Routes {
			if rc.Destination == cidr {
				cfg.Routes = append(cfg.Routes[:i], cfg.Routes[i+1:]...)
				break
			}
		}
		if err := cfg.Save(cfgFile); err != nil {
			return fmt.Errorf("route removed but failed to save config: %w", err)
		}
		fmt.Printf("Removed from config: %s\n", cfgFile)
	}

	return nil
}

func newRouteListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List managed routes",
		Long: `List routes managed by Mutiauk.

When the daemon is running, the listing is fetched over the API and
includes a SOURCE column tagging each route as:
  config  - declared in the config file
  auto    - received via autoroutes route-watch
  manual  - added at runtime via "route add" without --persist

When the daemon is not running, falls back to reading the local state
file (no source information).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			daemonClient := newDaemonAPIClient(cfg)
			if daemonClient.IsRunning() {
				result, err := daemonClient.ListRoutes()
				if err != nil {
					return err
				}
				return printDaemonRouteList(result, jsonOutput)
			}

			return printDirectRouteList(cfg, jsonOutput)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output in JSON format")

	return cmd
}

func printDaemonRouteList(result *api.RouteListResult, jsonOutput bool) error {
	if jsonOutput {
		return emitJSON(os.Stdout, result)
	}

	routes := result.Routes
	if len(routes) == 0 {
		fmt.Println("No routes configured")
	} else {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "DESTINATION\tSOURCE\tINTERFACE\tCOMMENT")
		for _, r := range routes {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Destination, r.Source, r.Interface, r.Comment)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}

	printRefusedRoutes(result.Refused)
	return nil
}

// printRefusedRoutes warns about routes the daemon declined to install because
// they cover the SOCKS5 server (would loop). Shown after the table so operators
// who never read the daemon logs still see the misconfiguration.
func printRefusedRoutes(refused []api.RefusedRoute) {
	if len(refused) == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("[%s] %d route(s) refused (cover the SOCKS5 server; not applied - fix on the Muti Metroo agent):\n", LabelWARN, len(refused))
	for _, r := range refused {
		src := r.Source
		if src == "" {
			src = "unknown"
		}
		fmt.Printf("  %s (%s): %s\n", r.Destination, src, r.Reason)
	}
}

func printDirectRouteList(cfg *config.Config, jsonOutput bool) error {
	mgr, err := newRouteManager(cfg)
	if err != nil {
		return err
	}

	routes, err := mgr.List()
	if err != nil {
		return fmt.Errorf("failed to list routes: %w", err)
	}

	// Wrap kernel-route entries in api.RouteInfo so the JSON shape
	// matches the daemon path. SOURCE is "unknown" because we cannot
	// distinguish config/auto/manual without the daemon's in-memory
	// state.
	infos := make([]api.RouteInfo, 0, len(routes))
	for _, r := range routes {
		dest := ""
		if r.Destination != nil {
			dest = r.Destination.String()
		}
		infos = append(infos, api.RouteInfo{
			Destination: dest,
			Interface:   r.Interface,
			Comment:     r.Comment,
			Source:      "unknown",
			Enabled:     r.Enabled,
		})
	}

	if jsonOutput {
		return emitJSON(os.Stdout, infos)
	}

	if len(infos) == 0 {
		fmt.Println("No routes configured")
		return nil
	}

	fmt.Println("(daemon offline; source attribution unavailable - SOURCE shown as \"unknown\")")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DESTINATION\tSOURCE\tINTERFACE\tCOMMENT")
	for _, r := range infos {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Destination, r.Source, r.Interface, r.Comment)
	}
	return w.Flush()
}

func newRoutePlanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "plan",
		Short: "Show pending route changes",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			mgr, err := newRouteManager(cfg)
			if err != nil {
				return err
			}

			desired, err := configRoutesToRoutes(cfg)
			if err != nil {
				return err
			}

			plan, err := mgr.Diff(desired)
			if err != nil {
				return fmt.Errorf("failed to generate plan: %w", err)
			}

			if len(plan.ToAdd) == 0 && len(plan.ToRemove) == 0 {
				fmt.Println("No changes required")
				return nil
			}

			printRoutePlan(plan)
			return nil
		},
	}
}

func printRoutePlan(plan *route.Plan) {
	if len(plan.ToAdd) > 0 {
		fmt.Println("Routes to add:")
		for _, r := range plan.ToAdd {
			fmt.Printf("  + %s\n", r.Destination.String())
		}
	}

	if len(plan.ToRemove) > 0 {
		fmt.Println("Routes to remove:")
		for _, r := range plan.ToRemove {
			fmt.Printf("  - %s\n", r.Destination.String())
		}
	}
}

func newRouteApplyCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply routes from configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			mgr, err := newRouteManager(cfg)
			if err != nil {
				return err
			}

			desired, err := configRoutesToRoutes(cfg)
			if err != nil {
				return err
			}

			plan, err := mgr.Diff(desired)
			if err != nil {
				return fmt.Errorf("failed to generate plan: %w", err)
			}

			if len(plan.ToAdd) == 0 && len(plan.ToRemove) == 0 {
				fmt.Println("No changes required")
				return nil
			}

			if dryRun {
				fmt.Println("Dry run - would apply:")
				printRoutePlan(plan)
				return nil
			}

			if _, err := mgr.Apply(plan); err != nil {
				return fmt.Errorf("failed to apply routes: %w", err)
			}

			fmt.Printf("Applied %d additions, %d removals\n", len(plan.ToAdd), len(plan.ToRemove))
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be done without making changes")

	return cmd
}

func newRouteCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Check for route conflicts",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			mgr, err := newRouteManager(cfg)
			if err != nil {
				return err
			}

			routes, err := configRoutesToRoutesAll(cfg)
			if err != nil {
				return err
			}

			conflicts, err := mgr.DetectConflicts(routes)
			if err != nil {
				return fmt.Errorf("failed to check conflicts: %w", err)
			}

			if len(conflicts) == 0 {
				fmt.Println("No conflicts detected")
				return nil
			}

			fmt.Printf("Found %d conflict(s):\n", len(conflicts))
			for _, c := range conflicts {
				fmt.Printf("  - %s conflicts with %s: %s\n",
					c.Proposed.Destination.String(),
					c.Existing.Destination.String(),
					c.Reason,
				)
			}

			return nil
		},
	}
}
