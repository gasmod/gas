//go:build unix

package vfs

import (
	"errors"
	"syscall"
)

// isNotEmpty reports whether err is the error removing a directory that
// still holds entries returns. POSIX allows EEXIST as well as ENOTEMPTY.
func isNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}
