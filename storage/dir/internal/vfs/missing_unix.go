//go:build unix && !darwin

package vfs

import (
	"errors"
	"os"
)

// IsMissing reports whether err is what an operation gets for a file or a
// directory that is not there: os.ErrNotExist, including for a file
// created in a directory that was removed while it was open. A caller
// confirms with Removed that the bucket is gone before it reports so.
func IsMissing(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}

// renameMayHide is false: a rename replaces a file atomically, so a read of
// its name finds the old file or the new one, never neither.
const renameMayHide = false

// isDeletePending is always false: removing a file frees its name at once.
func isDeletePending(error) bool { return false }
