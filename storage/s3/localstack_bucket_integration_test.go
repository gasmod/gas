package s3_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/gasmod/gas/storage"
)

// TestLocalStack_HeadAndDeleteBucket checks HeadBucket and DeleteBucket
// against LocalStack: a head of an existing and of a missing bucket, a
// delete refused while an object remains, and a delete of the emptied
// bucket, after which it is missing.
func TestLocalStack_HeadAndDeleteBucket(t *testing.T) {
	t.Parallel()
	p, bucket := lsProviderWithBucket(t, "ls-bkt")

	b, err := p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: bucket})
	if err != nil || b.Name != bucket {
		t.Fatalf("HeadBucket() = %+v, %v; want %s", b, err, bucket)
	}
	if _, err := p.HeadBucket(
		t.Context(),
		&storage.HeadBucketInput{
			Bucket: uniqueBucket("ls-none"),
		},
	); !errors.Is(err, storage.ErrBucketNotFound) {
		t.Errorf("HeadBucket(missing) error = %v, want ErrBucketNotFound", err)
	}

	in := lsObj(bucket, "k")
	if _, err := p.PutObject(t.Context(), &storage.PutObjectInput{
		BaseObjectInput: in, Body: strings.NewReader("x"),
	}); err != nil {
		t.Fatal(err)
	}
	del := &storage.DeleteBucketInput{Bucket: bucket}
	if err := p.DeleteBucket(t.Context(), del); !errors.Is(err, storage.ErrBucketNotEmpty) {
		t.Errorf("DeleteBucket(not empty) error = %v, want ErrBucketNotEmpty", err)
	}
	if _, err := p.DeleteObject(
		t.Context(),
		&storage.DeleteObjectInput{BaseObjectInput: in},
	); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteBucket(t.Context(), del); err != nil {
		t.Fatalf("DeleteBucket() of the emptied bucket error: %v", err)
	}
	if _, err := p.HeadBucket(
		t.Context(),
		&storage.HeadBucketInput{Bucket: bucket},
	); !errors.Is(
		err,
		storage.ErrBucketNotFound,
	) {
		t.Errorf("HeadBucket() after delete error = %v, want ErrBucketNotFound", err)
	}
	if err := p.DeleteBucket(t.Context(), del); !errors.Is(err, storage.ErrBucketNotFound) {
		t.Errorf("DeleteBucket(missing) error = %v, want ErrBucketNotFound", err)
	}
}
