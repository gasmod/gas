//go:build windows

package vfs

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isNotEmpty reports whether err is the error removing a directory that
// still holds entries returns.
func isNotEmpty(err error) bool {
	return errors.Is(err, windows.ERROR_DIR_NOT_EMPTY)
}
