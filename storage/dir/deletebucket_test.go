package dir

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/gasmod/gas/storage"
)

// TestProvider_PutIntoBucketDeletedMidWrite checks that a Put whose bucket
// is deleted while it writes the body fails with storage.ErrBucketNotFound,
// and that the delete does not cut the write short: it neither waits for
// the Put (the bucket holds no object yet) nor removes the Put's data file
// under it. The same holds when the bucket is created again before the Put
// finishes: the bucket the Put was writing to is gone, and the new one does
// not receive its object.
func TestProvider_PutIntoBucketDeletedMidWrite(t *testing.T) {
	t.Parallel()
	for _, recreate := range []bool{false, true} {
		name := "deleted"
		if recreate {
			name = "deleted and created again"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p, _ := newTestProvider(t)

			pr, pw := io.Pipe()
			done := make(chan error, 1)
			go func() {
				_, err := p.PutObject(context.Background(), &storage.PutObjectInput{
					BaseObjectInput: base("k"), Body: pr,
				})
				done <- err
			}()
			// Once the Put has read these bytes it holds the key's lock and
			// is copying the body into its data file.
			if _, err := pw.Write(make([]byte, 600)); err != nil {
				t.Fatal(err)
			}

			if err := p.DeleteBucket(context.Background(),
				&storage.DeleteBucketInput{Bucket: testBucket}); err != nil {
				t.Fatalf("DeleteBucket() during a Put of a new key: %v", err)
			}
			if recreate {
				createBucket(t, p, testBucket)
			}
			if err := pw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, storage.ErrBucketNotFound) {
				t.Errorf("PutObject() into the deleted bucket = %v, want ErrBucketNotFound", err)
			}
			if recreate {
				if _, err := head(p, "k"); !errors.Is(err, storage.ErrNotFound) {
					t.Errorf("HeadObject() in the new bucket = %v, want ErrNotFound", err)
				}
			}
		})
	}
}
