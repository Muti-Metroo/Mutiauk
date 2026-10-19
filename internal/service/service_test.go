package service

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig("/etc/mutiauk/config.yaml")

	if cfg.Name != "mutiauk" {
		t.Errorf("Name = %s, want mutiauk", cfg.Name)
	}
	if cfg.Description != "Mutiauk TUN interface for Muti Metroo" {
		t.Errorf("Description = %s, want 'Mutiauk TUN interface for Muti Metroo'", cfg.Description)
	}
	if cfg.ConfigPath == "" {
		t.Error("ConfigPath should not be empty")
	}
	if cfg.WorkingDir == "" {
		t.Error("WorkingDir should not be empty")
	}
}

func TestDefaultConfig_AbsolutePath(t *testing.T) {
	cfg := DefaultConfig("/etc/mutiauk/config.yaml")

	// ConfigPath should be absolute
	if !filepath.IsAbs(cfg.ConfigPath) {
		t.Errorf("ConfigPath should be absolute, got %s", cfg.ConfigPath)
	}
	// WorkingDir should be the directory containing the config
	expectedDir := "/etc/mutiauk"
	if cfg.WorkingDir != expectedDir {
		t.Errorf("WorkingDir = %s, want %s", cfg.WorkingDir, expectedDir)
	}
}

func TestDefaultConfig_RelativePath(t *testing.T) {
	cfg := DefaultConfig("./configs/mutiauk.yaml")

	// ConfigPath should be converted to absolute
	if !filepath.IsAbs(cfg.ConfigPath) {
		t.Errorf("ConfigPath should be absolute even for relative input, got %s", cfg.ConfigPath)
	}
	// WorkingDir should be absolute
	if !filepath.IsAbs(cfg.WorkingDir) {
		t.Errorf("WorkingDir should be absolute, got %s", cfg.WorkingDir)
	}
}

func TestDefaultConfig_DifferentPaths(t *testing.T) {
	tests := []struct {
		name       string
		configPath string
		wantDir    string
	}{
		{
			name:       "root config",
			configPath: "/config.yaml",
			wantDir:    "/",
		},
		{
			name:       "nested path",
			configPath: "/var/lib/mutiauk/configs/prod.yaml",
			wantDir:    "/var/lib/mutiauk/configs",
		},
		{
			name:       "home directory",
			configPath: "/home/user/.config/mutiauk/config.yaml",
			wantDir:    "/home/user/.config/mutiauk",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig(tt.configPath)
			if cfg.WorkingDir != tt.wantDir {
				t.Errorf("WorkingDir = %s, want %s", cfg.WorkingDir, tt.wantDir)
			}
		})
	}
}

func TestServiceConfig_Fields(t *testing.T) {
	cfg := ServiceConfig{
		Name:        "test-service",
		Description: "Test service description",
		ConfigPath:  "/path/to/config.yaml",
		WorkingDir:  "/path/to",
	}

	if cfg.Name != "test-service" {
		t.Errorf("Name = %s, want test-service", cfg.Name)
	}
	if cfg.Description != "Test service description" {
		t.Errorf("Description = %s, want 'Test service description'", cfg.Description)
	}
	if cfg.ConfigPath != "/path/to/config.yaml" {
		t.Errorf("ConfigPath = %s, want /path/to/config.yaml", cfg.ConfigPath)
	}
	if cfg.WorkingDir != "/path/to" {
		t.Errorf("WorkingDir = %s, want /path/to", cfg.WorkingDir)
	}
}

func TestIsRoot(t *testing.T) {
	// Just verify it doesn't panic and returns a boolean
	result := IsRoot()
	_ = result // We can't assert the value as it depends on how tests are run
}

func TestIsInstalled_NonExistent(t *testing.T) {
	// Test with a service name that definitely doesn't exist
	result := IsInstalled("nonexistent-service-12345")

	if runtime.GOOS == "linux" {
		// On Linux, it should check for the actual service file
		if result {
			t.Error("IsInstalled should return false for non-existent service")
		}
	} else {
		// On non-Linux, always returns false
		if result {
			t.Error("IsInstalled should return false on non-Linux platforms")
		}
	}
}

// supportedPlatform reports whether service management is implemented here.
func supportedPlatform() bool {
	return runtime.GOOS == "linux" || runtime.GOOS == "darwin"
}

func TestStatus_UnsupportedPlatform(t *testing.T) {
	if supportedPlatform() {
		t.Skip("service management is supported on this platform")
	}

	_, err := Status("any-service")

	if err == nil {
		t.Error("Status should return error on unsupported platforms")
	}
	if !strings.Contains(err.Error(), "Linux and macOS") {
		t.Errorf("error should mention supported platforms, got: %v", err)
	}
}

func TestInstall_UnsupportedPlatform(t *testing.T) {
	if supportedPlatform() {
		t.Skip("service management is supported on this platform")
	}

	cfg := DefaultConfig("/etc/mutiauk/config.yaml")
	err := Install(cfg)

	if err == nil {
		t.Error("Install should return error on unsupported platforms")
	}
	if !strings.Contains(err.Error(), "Linux and macOS") {
		t.Errorf("error should mention supported platforms, got: %v", err)
	}
}

func TestUninstall_UnsupportedPlatform(t *testing.T) {
	if supportedPlatform() {
		t.Skip("service management is supported on this platform")
	}

	err := Uninstall("mutiauk")

	if err == nil {
		t.Error("Uninstall should return error on unsupported platforms")
	}
	if !strings.Contains(err.Error(), "Linux and macOS") {
		t.Errorf("error should mention supported platforms, got: %v", err)
	}
}

func TestRunCommand(t *testing.T) {
	// Test with a simple command that exists on all platforms
	output, err := runCommand("echo", "hello")

	if err != nil {
		t.Errorf("runCommand('echo', 'hello') error = %v", err)
	}
	if !strings.Contains(output, "hello") {
		t.Errorf("output should contain 'hello', got: %s", output)
	}
}

func TestRunCommand_NonExistent(t *testing.T) {
	_, err := runCommand("nonexistent-command-12345")

	if err == nil {
		t.Error("runCommand should return error for non-existent command")
	}
}

func TestRunCommand_WithArgs(t *testing.T) {
	output, err := runCommand("echo", "arg1", "arg2", "arg3")

	if err != nil {
		t.Errorf("runCommand error = %v", err)
	}
	if !strings.Contains(output, "arg1") || !strings.Contains(output, "arg2") {
		t.Errorf("output should contain all args, got: %s", output)
	}
}

// Benchmark tests

func BenchmarkDefaultConfig(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = DefaultConfig("/etc/mutiauk/config.yaml")
	}
}

func BenchmarkIsRoot(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = IsRoot()
	}
}

func BenchmarkIsInstalled(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = IsInstalled("mutiauk")
	}
}
