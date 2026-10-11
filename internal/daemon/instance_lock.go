package daemon

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// ErrLocked is returned by acquireInstanceLock when another process
// already holds the daemon instance lock. Callers can match it with
// errors.Is to distinguish "already running" from real OS errors.
var ErrLocked = errors.New("another mutiauk daemon instance is already running")

// instanceLock is the kernel-held lock on the daemon PID file that
// prevents two daemon processes from running at once - a second instance
// would otherwise steal the API socket and fight over host DNS state
// (/etc/resolver files, NRPT rules). flock(2) on Unix, LockFileEx on
// Windows; both are released automatically by the kernel when the process
// dies, so a stale lock file never blocks a restart. The file doubles as
// the PID file: the current PID is written into it for the status/stop
// CLI paths and for human forensics.
type instanceLock struct {
	path string
	f    *os.File
}

// acquireInstanceLock opens path (creating it and its parent directory if
// needed), takes an exclusive non-blocking lock on it, and writes the
// current PID. ErrLocked (wrap-matched via errors.Is) is returned when
// another process holds the lock.
func acquireInstanceLock(path string) (*instanceLock, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create PID file directory %s: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open PID file %s: %w", path, err)
	}
	if err := lockExclusiveNB(f); err != nil {
		other := readLockPID(f)
		f.Close()
		if errors.Is(err, ErrLocked) {
			if other != "" {
				return nil, fmt.Errorf("%w (pid %s, lock file %s)", ErrLocked, other, path)
			}
			return nil, fmt.Errorf("%w (lock file %s)", ErrLocked, path)
		}
		return nil, fmt.Errorf("failed to lock PID file %s: %w", path, err)
	}
	// Best effort: write our PID for ps-style forensics.
	f.Truncate(0)
	f.Seek(0, io.SeekStart)
	fmt.Fprintln(f, os.Getpid())
	return &instanceLock{path: path, f: f}, nil
}

// Release drops the lock and closes the file. The file itself stays on
// disk (the caller removes it) so the same inode is reused across runs.
func (l *instanceLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	unlock(l.f)
	err := l.f.Close()
	l.f = nil
	return err
}

// readLockPID reads the PID recorded in the lock file for diagnostics.
func readLockPID(f *os.File) string {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	buf := make([]byte, 32)
	n, _ := f.Read(buf)
	if n == 0 {
		return ""
	}
	s := string(buf[:n])
	for i, c := range s {
		if c == '\n' || c == '\r' || c == ' ' {
			s = s[:i]
			break
		}
	}
	if _, err := strconv.Atoi(s); err != nil {
		return ""
	}
	return s
}
