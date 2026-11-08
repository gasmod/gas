//go:build unix

package vfs_test

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage/dir/internal/vfs"
)

// A FIFO in place of a metadata file or a data file never blocks a read:
// the object is ErrCorrupt at once.
func TestOpenObject_FIFO(t *testing.T) {
	t.Parallel()

	for name, target := range map[string]func(dir string) string{
		"meta": func(dir string) string { return metaPath(dir, testBucket, "k") },
		"data": func(dir string) string { return dataFiles(t, dir, "k")[0] },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, dir := newVFS(t)
			bk := newBucket(t, v)

			put(t, bk, "k", "v")

			path := target(dir)
			require.NoError(t, os.Remove(path))
			require.NoError(t, syscall.Mkfifo(path, 0o600))

			done := make(chan error, 1)

			go func() {
				_, err := bk.OpenObject(t.Context(), "k")
				done <- err
			}()

			select {
			case err := <-done:
				require.ErrorIs(t, err, vfs.ErrCorrupt)
				requireNoPath(t, err, dir)
			case <-time.After(5 * time.Second):
				// Unblock the read so the test can end.
				f, err := os.OpenFile( //nolint:gosec // G304: the FIFO this test made.
					path, os.O_WRONLY|syscall.O_NONBLOCK, 0,
				)
				if err == nil {
					_ = f.Close()
				}

				require.Error(t, <-done)
				require.FailNow(t, "a read blocked on a FIFO")
			}
		})
	}
}
