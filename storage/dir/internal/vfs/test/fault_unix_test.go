//go:build unix

package vfs_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/gasmod/gas/storage/dir/internal/vfs"
)

// A blocking file lock that a signal interrupts is taken again, and Lock
// gets the name once its holder gives it up.
func TestFault_Lock_InterruptedWaitRetries(t *testing.T) {
	dir := t.TempDir()

	holder := newLock(t, dir)
	require.NoError(t, holder.Lock(t.Context(), "n"))

	// The first flock is the try, which the holder makes fail.
	rule := &faultRule{op: vfs.OpFlock, match: at(filepath.Join(dir, "n")), nth: 2, err: unix.EINTR}
	inject(t, rule)

	l := newLock(t, dir)
	done := lockAsync(t.Context(), l, "n")

	requireBlocked(t, done)
	require.NoError(t, holder.Unlock("n"))
	require.NoError(t, recv(t, done))
	requireFired(t, rule)
}
