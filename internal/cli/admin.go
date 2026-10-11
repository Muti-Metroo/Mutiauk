package cli

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/spf13/cobra"

	"github.com/Muti-Metroo/Mutiauk/internal/adminhttp"
)

// newAdminCmd prints a login URL for the loopback admin dashboard. The
// dashboard (like every admin endpoint) requires the per-install bearer
// token stored root-only next to the config; the URL carries it as a
// ?token= query parameter that the daemon exchanges for a session cookie.
func newAdminCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "admin",
		Short: "Print the admin dashboard login URL",
		Long: `Print a login URL for the local admin dashboard.

The admin HTTP interface requires the per-install token stored in the
admin-token file next to the config (mode 0600, root-only). Opening the
printed URL in a browser sets a session cookie and redirects to the
dashboard. For scripted access, send the token as
"Authorization: Bearer <token>" on every request.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if !cfg.Admin.AdminEnabled() {
				return fmt.Errorf("admin HTTP interface is disabled in the config (admin.enabled: false)")
			}
			token, err := adminhttp.ReadToken(cfgFile)
			if err != nil {
				if errors.Is(err, fs.ErrPermission) {
					return fmt.Errorf("cannot read admin token: permission denied (file is 0600; rerun with sudo)")
				}
				return fmt.Errorf("cannot read admin token (is the daemon running? it creates the file on first start): %w", err)
			}
			fmt.Println("Admin dashboard login URL:")
			fmt.Printf("  http://%s/?token=%s\n", cfg.Admin.Listen, token)
			fmt.Println()
			fmt.Println("Opening the URL sets a session cookie; the token file is " + adminhttp.TokenPath(cfgFile))
			return nil
		},
	}
}
