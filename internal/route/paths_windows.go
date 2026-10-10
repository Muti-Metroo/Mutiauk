//go:build windows

package route

import (
	"os"
	"path/filepath"
)

// defaultRouteStateFile is where persisted route state lives on Windows:
// under %ProgramData%\Mutiauk (machine-wide, admin-writable).
var defaultRouteStateFile = filepath.Join(programDataDir(), "Mutiauk", "routes.json")

func programDataDir() string {
	if d := os.Getenv("ProgramData"); d != "" {
		return d
	}
	return `C:\ProgramData`
}
