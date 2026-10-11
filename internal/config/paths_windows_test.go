//go:build windows

package config

import (
	"strings"
	"testing"
)

func TestWindowsDefaults(t *testing.T) {
	if !strings.HasSuffix(DefaultPath, `Mutiauk\config.yaml`) {
		t.Errorf("DefaultPath = %q, want it under Mutiauk", DefaultPath)
	}
	if defaultSocketPath != `\\.\pipe\mutiauk` {
		t.Errorf("defaultSocketPath = %q, want named pipe", defaultSocketPath)
	}
	if !strings.HasSuffix(defaultPIDFile, "mutiauk.pid") {
		t.Errorf("defaultPIDFile = %q, want a .pid file", defaultPIDFile)
	}
}
