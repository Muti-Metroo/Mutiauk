//go:build windows

package daemon

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockExclusiveNB takes an exclusive non-blocking lock via the Win32
// LockFileEx API. Without LOCKFILE_FAIL_IMMEDIATELY a second instance
// would block; ERROR_LOCK_VIOLATION is the contention sentinel.
func lockExclusiveNB(f *os.File) error {
	var ol windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 0xFFFFFFFF, 0xFFFFFFFF,
		&ol,
	)
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrLocked
	}
	return err
}

func unlock(f *os.File) {
	var ol windows.Overlapped
	windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 0xFFFFFFFF, 0xFFFFFFFF, &ol)
}
