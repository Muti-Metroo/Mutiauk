//go:build !windows

package cli

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/postalsys/mutiauk/internal/service"
)

// removeTUNAdapter deletes the kernel TUN interface. On Linux it uses
// `ip link del` (which also clears the interface's routes); on macOS the
// utun interface is removed automatically when the daemon closes its
// device, so there is nothing to do here.
func removeTUNAdapter(name string, dryRun bool) {
	if runtime.GOOS != "linux" {
		fmt.Printf("TUN interface %s: removal not required on %s (auto-removed on close)\n", name, runtime.GOOS)
		return
	}
	if exec.Command("ip", "link", "show", name).Run() != nil {
		fmt.Printf("TUN interface %s: not present (skipped)\n", name)
		return
	}
	if !service.IsRoot() {
		fmt.Printf("TUN interface %s: skipped (requires root)\n", name)
		fmt.Printf("  Re-run: sudo mutiauk cleanup\n")
		return
	}
	if dryRun {
		fmt.Printf("TUN interface %s: would delete\n", name)
		return
	}
	if out, err := exec.Command("ip", "link", "del", name).CombinedOutput(); err != nil {
		fmt.Printf("TUN interface %s: failed to delete: %v (%s)\n", name, err, out)
		return
	}
	fmt.Printf("TUN interface %s: deleted\n", name)
}
