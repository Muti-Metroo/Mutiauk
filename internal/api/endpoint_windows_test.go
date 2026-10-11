//go:build windows

package api

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"
)

var pipeCounter int64

// testEndpointPath returns a unique named-pipe path per test so parallel
// tests do not collide on the pipe namespace.
func testEndpointPath(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf(`\\.\pipe\mutiauk-test-%d-%d`, os.Getpid(), atomic.AddInt64(&pipeCounter, 1))
}
