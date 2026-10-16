//go:build windows

package vfs

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// UnlockFileEx must be given exactly a range that was locked, so every call
// uses this one: a single byte at offset 0 (from the zeroed OVERLAPPED struct).
// One byte is enough, we never read or write the file.
const lockLen = 1

// tryLock takes an exclusive lock on f without blocking. It reports false
// with a nil error when another holder has the lock.
func tryLock(f *os.File) (bool, error) {
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, lockLen, 0,
		new(windows.Overlapped),
	)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("LockFileEx: %w", err)
	}
	return true, nil
}

// lockBlocking takes an exclusive lock on f, waiting for other holders.
func lockBlocking(f *os.File) error {
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, lockLen, 0,
		new(windows.Overlapped),
	)
	if err != nil {
		return fmt.Errorf("LockFileEx: %w", err)
	}
	return nil
}

// unlock releases the lock tryLock or lockBlocking took on f.
func unlock(f *os.File) error {
	err := windows.UnlockFileEx(
		windows.Handle(f.Fd()), 0, lockLen, 0, new(windows.Overlapped),
	)
	if err != nil {
		return fmt.Errorf("UnlockFileEx: %w", err)
	}
	return nil
}
