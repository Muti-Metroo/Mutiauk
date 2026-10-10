package wizard

import (
	"bytes"
	"io"
	"os"
	"runtime"
	"testing"
)

func TestNewServiceInstaller(t *testing.T) {
	configPath := "/etc/mutiauk/config.yaml"
	installer := NewServiceInstaller(configPath)

	if installer == nil {
		t.Fatal("NewServiceInstaller returned nil")
	}
	if installer.configPath != configPath {
		t.Errorf("configPath = %s, want %s", installer.configPath, configPath)
	}
}

func TestNewServiceInstaller_EmptyPath(t *testing.T) {
	installer := NewServiceInstaller("")

	if installer == nil {
		t.Fatal("NewServiceInstaller returned nil")
	}
	if installer.configPath != "" {
		t.Errorf("configPath = %s, want empty string", installer.configPath)
	}
}

func TestNewServiceInstaller_RelativePath(t *testing.T) {
	configPath := "./configs/mutiauk.yaml"
	installer := NewServiceInstaller(configPath)

	if installer.configPath != configPath {
		t.Errorf("configPath = %s, want %s", installer.configPath, configPath)
	}
}

func TestServiceInstaller_supported(t *testing.T) {
	installer := NewServiceInstaller("/test/path")

	result := installer.supported()
	expected := runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows"

	if result != expected {
		t.Errorf("supported() = %v, want %v (runtime.GOOS = %s)", result, expected, runtime.GOOS)
	}
}

func TestServiceInstaller_printManualInstallCommand(t *testing.T) {
	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	installer := NewServiceInstaller("/etc/mutiauk/config.yaml")
	installer.printManualInstallCommand()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	if !containsString(output, "mutiauk service install") {
		t.Errorf("output should contain 'mutiauk service install', got: %s", output)
	}
	if !containsString(output, "/etc/mutiauk/config.yaml") {
		t.Errorf("output should contain config path, got: %s", output)
	}
}

func TestServiceInstaller_printServiceManagementCommands(t *testing.T) {
	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	installer := NewServiceInstaller("/test/path")
	installer.printServiceManagementCommands()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	var expectedCommands []string
	switch runtime.GOOS {
	case "windows":
		expectedCommands = []string{"sc query mutiauk", "sc stop mutiauk", "sc start mutiauk"}
	case "darwin":
		expectedCommands = []string{"launchctl print", "launchctl bootout", "mutiauk"}
	default:
		expectedCommands = []string{
			"systemctl status mutiauk",
			"systemctl stop mutiauk",
			"systemctl restart mutiauk",
			"journalctl -u mutiauk",
		}
	}

	for _, cmd := range expectedCommands {
		if !containsString(output, cmd) {
			t.Errorf("output should contain %q, got: %s", cmd, output)
		}
	}
}

func TestServiceInstaller_printNonRootInstructions(t *testing.T) {
	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	installer := NewServiceInstaller("/etc/mutiauk/config.yaml")
	installer.printNonRootInstructions()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// Should contain the manual install command
	if !containsString(output, "mutiauk service install") {
		t.Errorf("output should contain install command, got: %s", output)
	}
}

func TestServiceInstaller_Run_NonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("skipping non-Linux test on Linux")
	}

	installer := NewServiceInstaller("/test/path")

	// Capture stdout to suppress output
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	err := installer.Run()

	w.Close()
	os.Stdout = old

	// Should return nil on non-Linux (no error, just info message)
	if err != nil {
		t.Errorf("Run() on non-Linux should return nil, got: %v", err)
	}
}

// Helper to check if string contains substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Table-driven tests for different config paths
func TestServiceInstaller_ConfigPaths(t *testing.T) {
	tests := []struct {
		name       string
		configPath string
	}{
		{"absolute path", "/etc/mutiauk/config.yaml"},
		{"home directory", "~/.config/mutiauk/config.yaml"},
		{"relative path", "./configs/mutiauk.yaml"},
		{"with spaces", "/path/with spaces/config.yaml"},
		{"empty path", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installer := NewServiceInstaller(tt.configPath)
			if installer.configPath != tt.configPath {
				t.Errorf("configPath = %s, want %s", installer.configPath, tt.configPath)
			}
		})
	}
}

// Benchmark test
func BenchmarkNewServiceInstaller(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewServiceInstaller("/etc/mutiauk/config.yaml")
	}
}

func BenchmarkServiceInstaller_supported(b *testing.B) {
	installer := NewServiceInstaller("/test/path")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = installer.supported()
	}
}
