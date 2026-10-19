//go:build linux

package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateSystemdUnit(t *testing.T) {
	cfg := ServiceConfig{
		Name:        "mutiauk",
		Description: "Mutiauk TUN interface for Muti Metroo",
		ConfigPath:  "/etc/mutiauk/config.yaml",
		WorkingDir:  "/etc/mutiauk",
	}
	execPath := "/usr/local/bin/mutiauk"

	unit := generateSystemdUnit(cfg, execPath)

	// Check required sections
	if !strings.Contains(unit, "[Unit]") {
		t.Error("unit should contain [Unit] section")
	}
	if !strings.Contains(unit, "[Service]") {
		t.Error("unit should contain [Service] section")
	}
	if !strings.Contains(unit, "[Install]") {
		t.Error("unit should contain [Install] section")
	}

	// Check description
	if !strings.Contains(unit, "Description="+cfg.Description) {
		t.Error("unit should contain service description")
	}

	// Check ExecStart
	expectedExec := "ExecStart=/usr/local/bin/mutiauk daemon start -c /etc/mutiauk/config.yaml"
	if !strings.Contains(unit, expectedExec) {
		t.Errorf("unit should contain ExecStart, got:\n%s", unit)
	}

	// Check WorkingDirectory
	if !strings.Contains(unit, "WorkingDirectory="+cfg.WorkingDir) {
		t.Error("unit should contain WorkingDirectory")
	}

	// Check capabilities
	if !strings.Contains(unit, "CAP_NET_ADMIN") {
		t.Error("unit should require CAP_NET_ADMIN capability")
	}

	// Check restart policy
	if !strings.Contains(unit, "Restart=on-failure") {
		t.Error("unit should have restart policy")
	}

	// Check target
	if !strings.Contains(unit, "WantedBy=multi-user.target") {
		t.Error("unit should be wanted by multi-user.target")
	}
}

func TestGenerateSystemdUnit_DifferentConfigs(t *testing.T) {
	tests := []struct {
		name     string
		cfg      ServiceConfig
		execPath string
		wantIn   []string
	}{
		{
			name: "custom service name",
			cfg: ServiceConfig{
				Name:        "my-proxy",
				Description: "Custom Proxy",
				ConfigPath:  "/opt/proxy/config.yaml",
				WorkingDir:  "/opt/proxy",
			},
			execPath: "/opt/proxy/bin/mutiauk",
			wantIn: []string{
				"Description=Custom Proxy",
				"SyslogIdentifier=my-proxy",
				"WorkingDirectory=/opt/proxy",
			},
		},
		{
			name: "path with spaces",
			cfg: ServiceConfig{
				Name:        "mutiauk",
				Description: "Test Service",
				ConfigPath:  "/path/with spaces/config.yaml",
				WorkingDir:  "/path/with spaces",
			},
			execPath: "/usr/bin/mutiauk",
			wantIn: []string{
				"ExecStart=/usr/bin/mutiauk daemon start -c /path/with spaces/config.yaml",
				"WorkingDirectory=/path/with spaces",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unit := generateSystemdUnit(tt.cfg, tt.execPath)

			for _, want := range tt.wantIn {
				if !strings.Contains(unit, want) {
					t.Errorf("unit should contain %q, got:\n%s", want, unit)
				}
			}
		})
	}
}

func TestGenerateSystemdUnit_SecurityHardening(t *testing.T) {
	cfg := DefaultConfig("/etc/mutiauk/config.yaml")
	unit := generateSystemdUnit(cfg, "/usr/bin/mutiauk")

	securitySettings := []string{
		"NoNewPrivileges=true",
		"ProtectSystem=strict",
		"ProtectHome=read-only",
		"PrivateTmp=true",
		"CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE",
		// Resolver binds UDP/TCP 53 on the TUN address.
		"AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE",
		// Auto-creates /var/lib/mutiauk with daemon-writable perms.
		"StateDirectory=mutiauk",
		// Allow the nm-dnsmasq DNS integration to write its drop-in.
		"/etc/NetworkManager/dnsmasq.d",
	}

	for _, setting := range securitySettings {
		if !strings.Contains(unit, setting) {
			t.Errorf("unit should contain security setting %q", setting)
		}
	}
}

func TestGenerateSystemdUnit_Logging(t *testing.T) {
	cfg := DefaultConfig("/etc/mutiauk/config.yaml")
	unit := generateSystemdUnit(cfg, "/usr/bin/mutiauk")

	loggingSettings := []string{
		"StandardOutput=journal",
		"StandardError=journal",
		"SyslogIdentifier=mutiauk",
	}

	for _, setting := range loggingSettings {
		if !strings.Contains(unit, setting) {
			t.Errorf("unit should contain logging setting %q", setting)
		}
	}
}

func TestGenerateSystemdUnit_NetworkDependency(t *testing.T) {
	cfg := DefaultConfig("/etc/mutiauk/config.yaml")
	unit := generateSystemdUnit(cfg, "/usr/bin/mutiauk")

	networkSettings := []string{
		"After=network-online.target",
		"Wants=network-online.target",
	}

	for _, setting := range networkSettings {
		if !strings.Contains(unit, setting) {
			t.Errorf("unit should contain network dependency %q", setting)
		}
	}
}

func TestIsRootImpl(t *testing.T) {
	result := isRootImpl()

	// Check against actual UID
	expectedRoot := os.Getuid() == 0
	if result != expectedRoot {
		t.Errorf("isRootImpl() = %v, want %v (uid=%d)", result, expectedRoot, os.Getuid())
	}
}

func TestIsInstalledImpl_NonExistent(t *testing.T) {
	result := isInstalledImpl("nonexistent-service-test-12345")

	if result {
		t.Error("isInstalledImpl should return false for non-existent service")
	}
}

func TestIsInstalledImpl_ChecksCorrectPath(t *testing.T) {
	// Create a temporary service file to test detection
	tmpDir := t.TempDir()

	// We can't easily test the actual /etc/systemd/system path without root,
	// but we can verify the path construction logic
	serviceName := "test-service"
	expectedPath := filepath.Join(systemdUnitPath, serviceName+".service")

	// Verify the expected path format
	if !strings.HasPrefix(expectedPath, "/etc/systemd/system/") {
		t.Errorf("expected path should be in /etc/systemd/system/, got %s", expectedPath)
	}
	if !strings.HasSuffix(expectedPath, ".service") {
		t.Errorf("expected path should end with .service, got %s", expectedPath)
	}

	_ = tmpDir // Used for potential future tests
}

func TestStatusImpl_NonExistentService(t *testing.T) {
	status, err := statusImpl("nonexistent-service-test-12345")

	// Should return "unknown" or similar for non-existent service
	if err != nil && status == "" {
		// Some error is expected for non-existent service
		return
	}
	if status != "unknown" && status != "inactive" && err == nil {
		t.Errorf("statusImpl should return 'unknown' or 'inactive' for non-existent service, got %q", status)
	}
}

func TestInstall_NotRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping non-root test when running as root")
	}

	cfg := DefaultConfig("/etc/mutiauk/config.yaml")
	err := Install(cfg)

	if err == nil {
		t.Error("Install should return error when not running as root")
	}
	if !strings.Contains(err.Error(), "root") {
		t.Errorf("error should mention root requirement, got: %v", err)
	}
}

func TestUninstall_NotRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping non-root test when running as root")
	}

	err := Uninstall("mutiauk")

	if err == nil {
		t.Error("Uninstall should return error when not running as root")
	}
	if !strings.Contains(err.Error(), "root") {
		t.Errorf("error should mention root requirement, got: %v", err)
	}
}

func TestSystemdUnitPath(t *testing.T) {
	// Verify the constant is set correctly
	if systemdUnitPath != "/etc/systemd/system" {
		t.Errorf("systemdUnitPath = %s, want /etc/systemd/system", systemdUnitPath)
	}
}

// Benchmark tests

func BenchmarkGenerateSystemdUnit(b *testing.B) {
	cfg := DefaultConfig("/etc/mutiauk/config.yaml")
	execPath := "/usr/bin/mutiauk"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = generateSystemdUnit(cfg, execPath)
	}
}

func BenchmarkIsRootImpl(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = isRootImpl()
	}
}

func BenchmarkIsInstalledImpl(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = isInstalledImpl("mutiauk")
	}
}
