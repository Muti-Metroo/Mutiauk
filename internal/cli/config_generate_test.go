package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Muti-Metroo/Mutiauk/internal/config"
)

const minimalAnswers = `tun:
  name: tun0
  mtu: 1400
  address: 10.200.200.1/24
socks5:
  server: 127.0.0.1:1080
`

func TestConfigGenerate_FromAnswers(t *testing.T) {
	dir := t.TempDir()
	answers := filepath.Join(dir, "answers.yaml")
	if err := os.WriteFile(answers, []byte(minimalAnswers), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "config.yaml")

	if err := runConfigGenerate(generateOpts{
		answersPath: answers,
		outputPath:  out,
	}); err != nil {
		t.Fatalf("runConfigGenerate: %v", err)
	}

	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0600); got != want {
		t.Errorf("mode = %#o, want %#o", got, want)
	}

	cfg, err := config.Load(out)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TUN.Name != "tun0" {
		t.Errorf("TUN.Name = %q, want tun0", cfg.TUN.Name)
	}
	if cfg.SOCKS5.Server != "127.0.0.1:1080" {
		t.Errorf("SOCKS5.Server = %q", cfg.SOCKS5.Server)
	}
}

func TestConfigGenerate_FlagOverridesAnswers(t *testing.T) {
	dir := t.TempDir()
	answers := filepath.Join(dir, "answers.yaml")
	if err := os.WriteFile(answers, []byte(minimalAnswers), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "config.yaml")

	if err := runConfigGenerate(generateOpts{
		answersPath:    answers,
		outputPath:     out,
		socks5Server:   "override.local:1080",
		socks5Username: "ops",
	}); err != nil {
		t.Fatalf("runConfigGenerate: %v", err)
	}

	cfg, err := config.Load(out)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SOCKS5.Server != "override.local:1080" {
		t.Errorf("SOCKS5.Server = %q, want override.local:1080", cfg.SOCKS5.Server)
	}
	if cfg.SOCKS5.Username != "ops" {
		t.Errorf("SOCKS5.Username = %q, want ops", cfg.SOCKS5.Username)
	}
}

func TestConfigGenerate_AutoroutesEnabledFlag(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "config.yaml")

	if err := runConfigGenerate(generateOpts{
		outputPath:        out,
		tunAddress:        "10.7.0.1/24",
		socks5Server:      "127.0.0.1:1080",
		autoroutesEnabled: true,
	}); err != nil {
		t.Fatalf("runConfigGenerate: %v", err)
	}

	cfg, err := config.Load(out)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.AutoRoutes.Enabled {
		t.Error("--autoroutes-enabled should have enabled autoroutes")
	}
}

func TestConfigGenerate_StrictAnswersRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	answers := filepath.Join(dir, "answers.yaml")
	bad := minimalAnswers + "unknownkey: oops\n"
	if err := os.WriteFile(answers, []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "config.yaml")

	err := runConfigGenerate(generateOpts{
		answersPath: answers,
		outputPath:  out,
	})
	if err == nil {
		t.Fatal("expected error for unknown YAML key")
	}
	if !strings.Contains(err.Error(), "unknownkey") &&
		!strings.Contains(err.Error(), "field unknownkey") {
		t.Errorf("error should name the unknown field: %v", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Error("config file should not be written when answers parsing fails")
	}
}

func TestConfigGenerate_RefusesOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(out, []byte("placeholder: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	answers := filepath.Join(dir, "answers.yaml")
	if err := os.WriteFile(answers, []byte(minimalAnswers), 0600); err != nil {
		t.Fatal(err)
	}

	err := runConfigGenerate(generateOpts{
		answersPath: answers,
		outputPath:  out,
	})
	if err == nil {
		t.Fatal("expected refusal when overwriting without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force: %v", err)
	}

	// Now retry with force; this time it must succeed.
	if err := runConfigGenerate(generateOpts{
		answersPath: answers,
		outputPath:  out,
		force:       true,
	}); err != nil {
		t.Fatalf("with --force: %v", err)
	}
}

func TestConfigGenerate_ValidationFailureLeavesNoFile(t *testing.T) {
	dir := t.TempDir()
	answers := filepath.Join(dir, "answers.yaml")
	// MTU below the validator's floor.
	bad := `tun:
  name: tun0
  mtu: 100
socks5:
  server: 127.0.0.1:1080
`
	if err := os.WriteFile(answers, []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "config.yaml")

	err := runConfigGenerate(generateOpts{
		answersPath: answers,
		outputPath:  out,
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Error("config file should not be written when validation fails")
	}
}

func TestGenerateReport_JSONShape(t *testing.T) {
	var buf bytes.Buffer
	r := generateReport{
		Path:      "/etc/mutiauk/config.yaml",
		Mode:      "0600",
		Validated: true,
	}
	if err := printGenerateReport(&buf, r, true); err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	for _, key := range []string{"path", "mode", "validated"} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON missing %q key (got %v)", key, got)
		}
	}
}
