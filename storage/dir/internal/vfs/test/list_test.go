package vfs_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Stat reads without the key's lock, so a write may rename new metadata
// over the file it opened; it then looks again under the lock, and never
// reports a healthy object as corrupt while it is overwritten.
func TestStat_DuringOverwrite(t *testing.T) {
	t.Parallel()

	v, _ := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "k", "x")

	stop := make(chan struct{})

	var wg sync.WaitGroup

	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}

			_, err := bk.CreateObject(t.Context(), write("k", "y"))
			if err != nil {
				t.Errorf("unexpected error: %v", err)

				return
			}
		}
	})

	defer func() {
		close(stop)
		wg.Wait()
	}()

	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		_, err := bk.Stat(t.Context(), "k")
		require.NoError(t, err)
	}
}
