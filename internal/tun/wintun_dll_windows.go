//go:build windows

package tun

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows"
)

// loadWithAlteredSearchPath is the LoadLibraryEx flag that loads a DLL
// from an absolute path (and resolves its dependencies relative to that
// directory). We pre-load our verified copy this way; the wintun
// package's later by-name LoadLibraryEx then resolves to the
// already-loaded module (Windows matches loaded modules by base name).
const loadWithAlteredSearchPath = 0x00000008

// wintunDLLDir is the directory the embedded wintun.dll is extracted to.
// A var so tests can redirect it to a temp dir.
var wintunDLLDir = filepath.Join(programData(), "Mutiauk")

var (
	ensureDLLOnce sync.Once
	ensureDLLErr  error
)

// programData returns %ProgramData%, falling back to the conventional
// C:\ProgramData when the environment variable is unset.
func programData() string {
	if d := os.Getenv("ProgramData"); d != "" {
		return d
	}
	return `C:\ProgramData`
}

// ensureWintunDLL extracts the embedded, SHA-256-verified wintun.dll to a
// machine-wide directory and pre-loads it by absolute path so the wintun
// package's later by-name load resolves to our verified copy. It runs at
// most once per process; the result is cached.
func ensureWintunDLL() error {
	ensureDLLOnce.Do(func() { ensureDLLErr = doEnsureWintunDLL() })
	return ensureDLLErr
}

func doEnsureWintunDLL() error {
	if len(wintunDLLBytes) == 0 || wintunDLLSHA256 == "" {
		return fmt.Errorf("no embedded wintun.dll for this architecture")
	}
	if err := os.MkdirAll(wintunDLLDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", wintunDLLDir, err)
	}

	path := filepath.Join(wintunDLLDir, "wintun.dll")
	if err := writeVerifiedDLL(path); err != nil {
		return err
	}

	if _, err := windows.LoadLibraryEx(path, 0, loadWithAlteredSearchPath); err != nil {
		return fmt.Errorf("load %s: %w", path, err)
	}
	return nil
}

// writeVerifiedDLL makes sure the on-disk DLL matches the embedded bytes
// by hash. An existing good copy is reused (so we never rewrite a DLL a
// running daemon may have mapped); otherwise the embedded bytes are
// written atomically (temp + rename) and re-hashed. A hash mismatch
// after writing is fatal -- we refuse to load an unverified DLL.
func writeVerifiedDLL(path string) error {
	if sum, err := fileSHA256(path); err == nil && sum == wintunDLLSHA256 {
		return nil
	}

	tmp, err := os.CreateTemp(wintunDLLDir, ".wintun-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp dll: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(wintunDLLBytes); err != nil {
		tmp.Close()
		return fmt.Errorf("write dll: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close dll: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install dll: %w", err)
	}

	sum, err := fileSHA256(path)
	if err != nil {
		return fmt.Errorf("verify dll: %w", err)
	}
	if sum != wintunDLLSHA256 {
		return fmt.Errorf("wintun.dll hash mismatch after write: got %s, want %s", sum, wintunDLLSHA256)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
