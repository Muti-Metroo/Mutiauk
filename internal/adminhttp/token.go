package adminhttp

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// TokenFileName is the per-install admin API token file, stored next to
// the daemon config. The file is created with 0600 permissions: anyone
// who can read it (root, and the operator via sudo) may drive the admin
// HTTP interface.
const TokenFileName = "admin-token"

// TokenPath returns the token file path for the given config file path.
func TokenPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), TokenFileName)
}

// LoadOrCreateToken returns the admin token for this install: it reads
// the token file next to the config, generating a fresh random token and
// persisting it (0600) on first use. A read of an existing file never
// modifies it.
func LoadOrCreateToken(configPath string) (string, error) {
	path := TokenPath(configPath)
	if data, err := os.ReadFile(path); err == nil {
		if tok := strings.TrimSpace(string(data)); tok != "" {
			return tok, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("read admin token %s: %w", path, err)
	}

	tok, err := generateToken()
	if err != nil {
		return "", err
	}
	// Write with O_EXCL so a concurrent first-start never truncates a
	// token another daemon instance just created.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			data, rerr := os.ReadFile(path)
			if rerr == nil {
				if existing := strings.TrimSpace(string(data)); existing != "" {
					return existing, nil
				}
			}
		}
		return "", fmt.Errorf("create admin token %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(tok + "\n"); err != nil {
		return "", fmt.Errorf("write admin token %s: %w", path, err)
	}
	return tok, nil
}

// ReadToken reads the token file for the given config path. It is used by
// local consumers (the CLI) that must present the token to the admin API.
func ReadToken(configPath string) (string, error) {
	path := TokenPath(configPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read admin token %s: %w", path, err)
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", fmt.Errorf("admin token %s is empty", path)
	}
	return tok, nil
}

// generateToken returns 32 crypto-random bytes, hex encoded.
func generateToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate admin token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
