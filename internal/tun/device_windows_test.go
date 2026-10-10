//go:build windows

package tun

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestToPrefix(t *testing.T) {
	tests := []struct {
		name string
		ip   net.IP
		mask net.IPMask
		want string
		ok   bool
	}{
		{"ipv4 /24", net.ParseIP("10.200.200.1"), net.CIDRMask(24, 32), "10.200.200.1/24", true},
		{"ipv6 /64", net.ParseIP("fd00::1"), net.CIDRMask(64, 128), "fd00::1/64", true},
		{"nil ip", nil, net.CIDRMask(24, 32), "", false},
		{"nil mask", net.ParseIP("10.0.0.1"), nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := toPrefix(tt.ip, tt.mask)
			if ok != tt.ok {
				t.Fatalf("toPrefix ok = %v, want %v", ok, tt.ok)
			}
			if ok && p.String() != tt.want {
				t.Errorf("toPrefix = %q, want %q", p.String(), tt.want)
			}
		})
	}
}

// TestWriteVerifiedDLL_FreshWrite checks the extract+verify path writes
// the embedded bytes and that a matching on-disk copy is reused.
func TestWriteVerifiedDLL_FreshWrite(t *testing.T) {
	dir := t.TempDir()
	orig := wintunDLLDir
	wintunDLLDir = dir
	t.Cleanup(func() { wintunDLLDir = orig })

	path := filepath.Join(dir, "wintun.dll")
	if err := writeVerifiedDLL(path); err != nil {
		t.Fatalf("writeVerifiedDLL: %v", err)
	}

	got, err := fileSHA256(path)
	if err != nil {
		t.Fatalf("fileSHA256: %v", err)
	}
	if got != wintunDLLSHA256 {
		t.Errorf("written dll hash = %s, want %s", got, wintunDLLSHA256)
	}

	// A second call must be a no-op reuse (no error, hash unchanged).
	if err := writeVerifiedDLL(path); err != nil {
		t.Fatalf("writeVerifiedDLL (reuse): %v", err)
	}
}

// TestWriteVerifiedDLL_RewritesCorruptCopy checks that a tampered/short
// on-disk DLL is overwritten with the verified embedded bytes.
func TestWriteVerifiedDLL_RewritesCorruptCopy(t *testing.T) {
	dir := t.TempDir()
	orig := wintunDLLDir
	wintunDLLDir = dir
	t.Cleanup(func() { wintunDLLDir = orig })

	path := filepath.Join(dir, "wintun.dll")
	if err := os.WriteFile(path, []byte("not a dll"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeVerifiedDLL(path); err != nil {
		t.Fatalf("writeVerifiedDLL: %v", err)
	}
	got, err := fileSHA256(path)
	if err != nil {
		t.Fatalf("fileSHA256: %v", err)
	}
	if got != wintunDLLSHA256 {
		t.Errorf("after rewrite hash = %s, want %s", got, wintunDLLSHA256)
	}
}

// TestEmbeddedDLLMatchesConstant guards against the embedded DLL and its
// declared SHA-256 constant drifting apart (e.g. a DLL refresh without a
// hash update).
func TestEmbeddedDLLMatchesConstant(t *testing.T) {
	sum := sha256.Sum256(wintunDLLBytes)
	if got := hex.EncodeToString(sum[:]); got != wintunDLLSHA256 {
		t.Errorf("embedded wintun.dll hash = %s, want constant %s", got, wintunDLLSHA256)
	}
}
