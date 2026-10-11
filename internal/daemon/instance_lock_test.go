package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestInstanceLockSingleInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mutiauk.pid")

	first, err := acquireInstanceLock(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// The PID file doubles as the lock file and must be readable.
	pid, err := ReadPIDFile(path)
	if err != nil {
		t.Fatalf("ReadPIDFile: %v", err)
	}
	if pid != os.Getpid() {
		t.Fatalf("PID file = %d, want %d", pid, os.Getpid())
	}

	// A second instance is refused while the first holds the lock.
	if _, err := acquireInstanceLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second acquire = %v, want ErrLocked", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	// After release the lock can be taken again (stale file is reused).
	second, err := acquireInstanceLock(path)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	if _, err := acquireInstanceLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("third acquire = %v, want ErrLocked", err)
	}
	second.Release()
}

func TestInstanceLockCreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "mutiauk.pid")
	lock, err := acquireInstanceLock(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lock.Release()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file not created: %v", err)
	}
}

func TestReadLockPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mutiauk.pid")
	if err := os.WriteFile(path, []byte("4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got := readLockPID(f); got != strconv.Itoa(4242) {
		t.Fatalf("readLockPID = %q, want 4242", got)
	}
}
