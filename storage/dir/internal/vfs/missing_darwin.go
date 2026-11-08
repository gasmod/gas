//go:build darwin

package vfs

import (
	"errors"
	"os"
	"syscall"
)

// IsMissing reports whether err is what an operation gets for a file or a
// directory that is not there: os.ErrNotExist, or EINVAL. On darwin, an
// openat that creates a file in a directory that another process is
// removing at that moment fails with EINVAL, where one in a directory
// removed before fails with ENOENT. EINVAL has other causes, so a caller
// confirms with Removed that the bucket is gone before it reports so.
func IsMissing(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.EINVAL)
}

// renameMayHide is false: a rename replaces a file atomically, so a read of
// its name finds the old file or the new one, never neither.
const renameMayHide = false

// isDeletePending is always false: removing a file frees its name at once.
func isDeletePending(error) bool { return false }
