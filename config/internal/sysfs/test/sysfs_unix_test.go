//go:build unix

package sysfs_test

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/internal/sysfs"
)

// A FIFO is refused rather than waited on for a writer.
//
// Not parallel: it changes the working directory.
func TestOpen_RefusesFIFO(t *testing.T) {
	work, _ := chdirWork(t)
	require.NoError(t, syscall.Mkfifo(filepath.Join(work, "app.json"), 0o600))

	done := make(chan error, 1)

	go func() {
		_, err := sysfs.SysFS{}.Open("app.json")
		done <- err
	}()

	select {
	case err := <-done:
		require.ErrorIs(t, err, sysfs.ErrNotRegularFile)
	case <-time.After(5 * time.Second):
		t.Fatal("Open blocked on a FIFO")
	}
}
