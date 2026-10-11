//go:build windows

package cli

import (
	"fmt"

	"github.com/Muti-Metroo/Mutiauk/internal/service"
	"github.com/Muti-Metroo/Mutiauk/internal/tun"
)

// removeTUNAdapter deletes the Wintun adapter left behind by a crashed
// daemon. tun.RemoveAdapter is a no-op when the adapter is absent.
func removeTUNAdapter(name string, dryRun bool) {
	if name == "" {
		name = "mutiauk0"
	}
	if !service.IsRoot() {
		fmt.Printf("TUN adapter %s: skipped (requires Administrator)\n", name)
		fmt.Printf("  Re-run from an elevated prompt.\n")
		return
	}
	if dryRun {
		fmt.Printf("TUN adapter %s: would delete (if present)\n", name)
		return
	}
	if err := tun.RemoveAdapter(name); err != nil {
		fmt.Printf("TUN adapter %s: failed to delete: %v\n", name, err)
		return
	}
	fmt.Printf("TUN adapter %s: deleted (if present)\n", name)
}
