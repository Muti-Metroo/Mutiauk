//go:build !windows

package daemon

import (
	"errors"
	"os"
	"syscall"
)

// lockExclusiveNB takes an exclusive non-blocking flock(2) so the second
// process gets a clean ErrLocked rather than hanging.
func lockExclusiveNB(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrLocked
	}
	return err
}

func unlock(f *os.File) {
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
