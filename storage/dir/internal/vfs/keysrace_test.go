package vfs

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// TestKeys_ConcurrentWritesAreNotCorrupt checks that Keys never reports a
// live object as skipped while another goroutine overwrites or deletes it.
// An overwrite renames new metadata in and then removes the old data file,
// and a delete removes the metadata before the data file, so a Keys that
// read the old metadata can find its data file gone; that is a write in
// progress, not a damaged object.
func TestKeys_ConcurrentWritesAreNotCorrupt(t *testing.T) {
	for name, write := range map[string]func(b *Bucket) error{
		"overwrite": func(b *Bucket) error {
			_, err := b.CreateObject(context.Background(), &CreateObjectInput{
				Key: "k", Body: strings.NewReader("body"),
			})
			return err
		},
		"delete and write": func(b *Bucket) error {
			if err := b.DeleteObject(context.Background(), &DeleteObjectInput{Key: "k"}); err != nil {
				return err
			}
			_, err := b.CreateObject(context.Background(), &CreateObjectInput{
				Key: "k", Body: strings.NewReader("body"),
			})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			v, _ := newTestVFS(t)
			b := newTestBucket(t, v)
			putObject(t, b, "k", "body", "")

			ctx, cancel := context.WithCancel(t.Context())
			var wg sync.WaitGroup
			wg.Go(func() {
				writer := newOpenBucket(t, v)
				for ctx.Err() == nil {
					if err := write(writer); err != nil && ctx.Err() == nil {
						t.Errorf("write: %v", err)
						return
					}
				}
			})
			defer wg.Wait()
			defer cancel()

			for i := range 3000 {
				_, skipped, err := b.Keys(t.Context())
				if err != nil {
					t.Fatalf("Keys %d: %v", i, err)
				}
				if len(skipped) != 0 {
					t.Fatalf(
						"Keys %d skipped %v while the object was being written",
						i,
						skipped[0].Err,
					)
				}
			}
		})
	}
}
