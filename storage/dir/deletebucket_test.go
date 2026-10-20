package dir

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir/internal/vfs"
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

// TestProvider_IfMatchPutIntoBucketDeletedMidCall checks that a Put with
// IfMatch whose bucket is deleted while it waits for the key's lock fails
// with storage.ErrBucketNotFound, not storage.ErrKeyNotFound: the key is
// missing only because the bucket is. It is not parallel: the lock-wait
// hook it sets is process-wide.
func TestProvider_IfMatchPutIntoBucketDeletedMidCall(t *testing.T) {
	p, dir := newTestProvider(t)

	// Another holder of the key's lock keeps the Put waiting.
	locksRoot, err := os.OpenRoot(lockDir(dir, testBucket))
	if err != nil {
		t.Fatal(err)
	}
	holder := vfs.NewLock(locksRoot)
	t.Cleanup(func() { _ = holder.Close() })
	name := sha("k")
	if lErr := holder.Lock(context.Background(), name); lErr != nil {
		t.Fatal(lErr)
	}

	waiting := make(chan struct{})
	var once sync.Once
	vfs.SetLockWaitHook(func(n string) {
		if n == name {
			once.Do(func() { close(waiting) })
		}
	})
	t.Cleanup(func() { vfs.SetLockWaitHook(nil) })

	done := make(chan error, 1)
	go func() {
		_, pErr := p.PutObject(context.Background(), &storage.PutObjectInput{
			BaseObjectInput: base("k"),
			Body:            strings.NewReader("x"),
			WriteCondition:  storage.WriteCondition{IfMatch: new("abc")},
		})
		done <- pErr
	}()
	select {
	case <-waiting:
	case pErr := <-done:
		t.Fatalf("PutObject() returned %v before waiting for the key's lock", pErr)
	case <-time.After(10 * time.Second):
		t.Fatal("PutObject() never waited for the key's lock")
	}

	if dErr := p.DeleteBucket(context.Background(),
		&storage.DeleteBucketInput{Bucket: testBucket}); dErr != nil {
		t.Fatalf("DeleteBucket(): %v", dErr)
	}
	if uErr := holder.Unlock(name); uErr != nil {
		t.Fatal(uErr)
	}
	if pErr := <-done; !errors.Is(pErr, storage.ErrBucketNotFound) {
		t.Errorf("PutObject(IfMatch) into the deleted bucket = %v, want ErrBucketNotFound", pErr)
	}
}
