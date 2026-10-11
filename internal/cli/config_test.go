package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestConfigCheckReport(t *testing.T) {
	dir := t.TempDir()

	// Valid, 0600 config -> OK with no warnings.
	clean := writeFile(t, dir, "clean.yaml", `
tun:
  name: tun0
  mtu: 1400
  address: 10.200.200.1/24
socks5:
  server: 127.0.0.1:1080
routes:
  - destination: 10.0.0.0/8
    enabled: true
`, 0600)

	r := buildConfigCheckReport(clean, false)
	if !r.OK {
		t.Fatalf("clean config returned OK=false: errors=%v warnings=%v", r.Errors, r.Warnings)
	}
	if len(r.Errors) != 0 {
		t.Errorf("clean config returned errors: %v", r.Errors)
	}
	if len(r.Warnings) != 0 {
		t.Errorf("clean config returned warnings: %v", r.Warnings)
	}

	// Invalid MTU -> failing error.
	bad := writeFile(t, dir, "bad.yaml", `
tun:
  name: tun0
  mtu: 100
socks5:
  server: 127.0.0.1:1080
`, 0600)

	r = buildConfigCheckReport(bad, false)
	if r.OK {
		t.Error("invalid config returned OK=true")
	}
	if len(r.Errors) == 0 {
		t.Error("invalid config returned no errors")
	}

	// Loose file mode -> warning, not error (unless --strict).
	loose := writeFile(t, dir, "loose.yaml", `
tun:
  name: tun0
  mtu: 1400
socks5:
  server: 127.0.0.1:1080
routes:
  - destination: 10.0.0.0/8
    enabled: true
`, 0644)

	r = buildConfigCheckReport(loose, false)
	if !r.OK {
		t.Errorf("loose-mode config without --strict: OK=false (errors=%v)", r.Errors)
	}
	if len(r.Warnings) == 0 {
		t.Error("loose-mode config produced no warnings")
	}

	r = buildConfigCheckReport(loose, true)
	if r.OK {
		t.Error("loose-mode config with --strict: OK=true; expected failure")
	}

	// Defaulted route enabled -> warning.
	defaulted := writeFile(t, dir, "defaulted.yaml", `
tun:
  name: tun0
  mtu: 1400
socks5:
  server: 127.0.0.1:1080
routes:
  - destination: 10.0.0.0/8
`, 0600)

	r = buildConfigCheckReport(defaulted, false)
	if !r.OK {
		t.Errorf("defaulted-enabled config: OK=false (errors=%v)", r.Errors)
	}
	found := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "missing explicit 'enabled'") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("defaulted-enabled config: no warning about missing enabled key; got %v", r.Warnings)
	}
}
