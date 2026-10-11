//go:build !windows

package api

import (
	"os"
	"path/filepath"
	"testing"
)

// testEndpointPath returns a short Unix-socket path. A short parent dir
// (not t.TempDir, whose name embeds the long test name) keeps the path
// under the ~104-char sun_path limit on macOS/BSD.
func testEndpointPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "muti-metroo-api")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}
