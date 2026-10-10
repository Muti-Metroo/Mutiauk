package expose

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// runRequest issues one HTTP request through folderHandler.Serve over
// an in-memory pipe and returns the parsed response. Caller must
// close the response body.
func runRequest(t *testing.T, root, method, target string) *http.Response {
	t.Helper()

	logger := zap.NewNop()
	h, err := newFolderHandler(root, logger)
	if err != nil {
		t.Fatalf("newFolderHandler: %v", err)
	}

	clientSide, serverSide := net.Pipe()

	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(done)
		_ = h.Serve(ctx, serverSide)
	}()

	// Write the request and signal we're done writing so the server
	// processes it promptly. With keep-alive we'd loop here.
	if err := func() error {
		r, err := http.NewRequest(method, target, nil)
		if err != nil {
			return err
		}
		r.Host = "test"
		r.Close = true // force Connection: close so Serve returns after one request
		return r.Write(clientSide)
	}(); err != nil {
		t.Fatalf("write request: %v", err)
	}

	_ = clientSide.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(clientSide), nil)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}

	t.Cleanup(func() {
		resp.Body.Close()
		clientSide.Close()
		cancel()
		<-done
	})
	return resp
}

func TestFolderHandlerServesExistingFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	resp := runRequest(t, dir, "GET", "/a.txt")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
}

func TestFolderHandlerStrictNotFound(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subdir", "nested.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, target string
	}{
		{"missing", "/missing.txt"},
		{"root index", "/"},
		{"root with double slash", "//"},
		{"directory plain", "/subdir"},
		{"directory trailing slash", "/subdir/"},
		{"traversal", "/../etc/passwd"},
		{"traversal embedded", "/foo/../../etc/passwd"},
		{"empty path", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := tc.target
			if target == "" {
				target = "/"
			}
			resp := runRequest(t, dir, "GET", target)
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("status: got %d want 404 (target=%q)", resp.StatusCode, tc.target)
			}
		})
	}
}

func TestFolderHandlerRejectsNonGet(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"POST", "PUT", "DELETE"} {
		resp := runRequest(t, dir, m, "/a.txt")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: got %d want 404", m, resp.StatusCode)
		}
	}
}

func TestFolderHandlerSymlinkOutOfRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks unreliable on windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("nope"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "leak.txt")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), link); err != nil {
		t.Skipf("symlink not permitted: %v", err)
	}
	resp := runRequest(t, root, "GET", "/leak.txt")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("symlink-out-of-root: got %d want 404", resp.StatusCode)
	}
}

func TestFolderHandlerSymlinkInsideRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks unreliable on windows")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "alias.txt")); err != nil {
		t.Skipf("symlink not permitted: %v", err)
	}
	resp := runRequest(t, root, "GET", "/alias.txt")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("in-root symlink: got %d want 200", resp.StatusCode)
	}
}

func TestFolderHandlerNullByteRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	resp := runRequest(t, dir, "GET", "/a.txt%00b")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("null byte: got %d want 404", resp.StatusCode)
	}
}

func TestNewFolderHandlerRequiresExistingRoot(t *testing.T) {
	_, err := newFolderHandler(filepath.Join(os.TempDir(), "nope-"+randomTag()), zap.NewNop())
	if err == nil {
		t.Fatal("expected error for missing root")
	}
}

// randomTag is a small helper to dodge collisions across parallel
// runs of the missing-root test.
func randomTag() string {
	const chars = "abcdefghijklmnop"
	now := time.Now().UnixNano()
	var b strings.Builder
	for i := 0; i < 8; i++ {
		b.WriteByte(chars[(now>>(i*4))&0xF])
	}
	return b.String()
}
