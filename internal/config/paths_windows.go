//go:build windows

package config

import (
	"os"
	"path/filepath"
)

// Default filesystem locations on Windows. Config and the PID file live
// under %ProgramData%\Mutiauk (machine-wide, writable only by
// administrators by default), and the control endpoint is a named pipe.
var (
	// DefaultPath is the canonical config-file location.
	DefaultPath = filepath.Join(programData(), "Mutiauk", "config.yaml")

	defaultPIDFile    = filepath.Join(programData(), "Mutiauk", "mutiauk.pid")
	defaultSocketPath = `\\.\pipe\mutiauk`
)

// programData returns %ProgramData%, falling back to the conventional
// C:\ProgramData when the environment variable is unset.
func programData() string {
	if d := os.Getenv("ProgramData"); d != "" {
		return d
	}
	return `C:\ProgramData`
}
