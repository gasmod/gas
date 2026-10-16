//go:build unix

package vfs

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// tryLock takes an exclusive flock on f without blocking. It reports false
// with a nil error when another holder has the lock.
func tryLock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("flock: %w", err)
	}
	return true, nil
}

// lockBlocking takes an exclusive flock on f, waiting for other holders and
// retrying when a signal interrupts the wait.
func lockBlocking(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("flock: %w", err)
		}
		return nil
	}
}

// unlock is a no-op: closing the descriptor releases the flock.
func unlock(_ *os.File) error { return nil }
