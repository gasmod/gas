//go:build darwin

package vfs_test

import (
	"fmt"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gasmod/gas/storage/dir/internal/vfs"
)

// On darwin IsMissing accepts EINVAL, which an openat that creates a file
// in a directory another process is removing gets.
func TestIsMissing_EINVAL(t *testing.T) {
	t.Parallel()

	assert.True(t, vfs.IsMissing(fmt.Errorf("open: %w", syscall.EINVAL)))
	assert.False(t, vfs.IsMissing(syscall.EPERM))
}
