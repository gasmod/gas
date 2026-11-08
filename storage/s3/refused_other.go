//go:build !windows

package s3

import (
	"errors"
	"syscall"
)

// isRefused reports whether err is a refused TCP connection.
func isRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}
