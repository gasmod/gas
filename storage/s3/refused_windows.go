//go:build windows

package s3

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isRefused reports whether err is a refused TCP connection, which
// Winsock reports with an error of its own, not syscall.ECONNREFUSED.
func isRefused(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED)
}
