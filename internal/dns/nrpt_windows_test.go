//go:build windows

package dns

import (
	"strings"
	"testing"
)

func TestNormalizeSuffix(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"mesh", ".mesh"},
		{".mesh", ".mesh"},
		{"MESH", ".mesh"},
		{"  mesh.  ", ".mesh"},
		{"corp.internal", ".corp.internal"},
		{"", ""},
		{".", ""},
	}
	for _, tt := range tests {
		if got := normalizeSuffix(tt.in); got != tt.want {
			t.Errorf("normalizeSuffix(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNRPTGUID(t *testing.T) {
	// Deterministic: same namespace -> same GUID.
	a := nrptGUID(".mesh")
	b := nrptGUID(".mesh")
	if a != b {
		t.Errorf("nrptGUID not deterministic: %q != %q", a, b)
	}
	// Distinct namespaces -> distinct GUIDs.
	if nrptGUID(".mesh") == nrptGUID(".corp") {
		t.Error("nrptGUID collision across namespaces")
	}
	// Brace-wrapped GUID shape: {8-4-4-4-12}.
	if !strings.HasPrefix(a, "{") || !strings.HasSuffix(a, "}") {
		t.Errorf("nrptGUID = %q, want brace-wrapped", a)
	}
	if n := strings.Count(a, "-"); n != 4 {
		t.Errorf("nrptGUID = %q, want 4 hyphens, got %d", a, n)
	}
}
