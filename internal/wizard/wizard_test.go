package wizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Muti-Metroo/Mutiauk/internal/config"
)

// TestWriteConfigMode confirms the wizard's writeConfig produces a
// 0600 file. Pairs with the cfg.Save tests in internal/config; the
// wizard takes its own path because it prepends a header before
// marshaling, so it doesn't go through Save.
func TestWriteConfigMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wizard-config.yaml")

	// Pre-create with a permissive mode to confirm writeConfig
	// tightens it (chmod-after-WriteFile).
	if err := os.WriteFile(path, []byte("placeholder: true\n"), 0644); err != nil {
		t.Fatalf("pre-create: %v", err)
	}

	w := New()
	cfg := &config.Config{
		TUN: config.TUNConfig{
			Name:    "tun0",
			MTU:     1400,
			Address: "10.200.200.1/24",
		},
		SOCKS5: config.SOCKS5Config{
			Server:   "127.0.0.1:1080",
			Username: "user",
			Password: "secret",
		},
	}

	if err := w.writeConfig(cfg, path); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0600); got != want {
		t.Errorf("file mode = %#o, want %#o (config holds secrets)", got, want)
	}
}

// TestBuildConfig_EditPreservesRawAndDoesNotFreezeDefaults confirms
// that re-editing an existing config clones the RAW parse (no defaults)
// as the base: operator-set sections the wizard never asks about
// survive verbatim, and the file is not re-written with frozen default
// values (which would prevent picking up new defaults on upgrade).
func TestBuildConfig_EditPreservesRawAndDoesNotFreezeDefaults(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "config.yaml")
	content := "" +
		"tun:\n  name: tun0\n  address: 10.200.200.1/24\n" +
		"socks5:\n  server: 127.0.0.1:1080\n" +
		"routes:\n  - destination: 10.0.0.0/8\n    comment: corp\n" +
		"expose:\n  enabled: true\n  forwards:\n    - key: site\n      type: folder\n      path: " + dir + "\n" +
		"nat:\n  table_size: 12345\n"
	if err := os.WriteFile(src, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	loaded, err := config.Load(src)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rawData, _ := os.ReadFile(src)
	raw, err := config.ParseRaw(rawData)
	if err != nil {
		t.Fatalf("ParseRaw: %v", err)
	}

	w := New()
	w.existingCfg = loaded
	w.existingRaw = raw

	cfg := w.buildConfig(loaded.TUN, loaded.SOCKS5, config.AutoRoutesConfig{}, loaded.Routes)
	out := filepath.Join(dir, "out.yaml")
	if err := WriteConfigFile(cfg, out); err != nil {
		t.Fatalf("WriteConfigFile: %v", err)
	}
	b, _ := os.ReadFile(out)
	s := string(b)

	// Operator-set sections survive an edit.
	if !strings.Contains(s, "key: site") {
		t.Error("expose forward 'site' was dropped on edit")
	}
	if !strings.Contains(s, "12345") {
		t.Error("hand-set nat.table_size 12345 was lost on edit")
	}
	// Defaults must NOT be frozen into the file: the daemon refills them
	// on Load, so the file should not carry the defaulted values.
	if strings.Contains(s, "65536") {
		t.Error("file gained the frozen NAT default 65536")
	}
	if strings.Contains(s, "level: info") || strings.Contains(s, "format: json") {
		t.Error("file gained frozen logging defaults (info/json)")
	}
	if strings.Contains(s, "/var/run/mutiauk") {
		t.Error("file gained frozen daemon path defaults")
	}
}
