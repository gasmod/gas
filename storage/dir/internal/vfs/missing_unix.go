//go:build unix

package vfs

import (
	"errors"
	"os"
)

// IsMissing reports whether err is what an operation gets for a file or
// directory that is not there: os.ErrNotExist, including for a file created
// in a directory that was removed while it was open.
func IsMissing(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
