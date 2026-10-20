package storage_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/gasmod/gas/storage"
)

// TestContract_Buckets lists the HeadBucket and DeleteBucket behavior both
// backends must share.
func TestContract_Buckets(t *testing.T) {
	t.Parallel()

	cases := map[string]func(t *testing.T, p storage.Provider){
		"head an existing bucket":         contractHeadBucket,
		"head a missing bucket":           contractHeadBucketMissing,
		"delete an empty bucket":          contractDeleteBucket,
		"delete a bucket with an object":  contractDeleteBucketNotEmpty,
		"delete a missing bucket":         contractDeleteBucketMissing,
		"bucket names are validated":      contractBucketNamesValidated,
		"delete then create starts empty": contractDeleteThenCreate,
	}
	for name, check := range cases {
		for _, be := range backends {
			t.Run(name+"/"+be.name, func(t *testing.T) {
				t.Parallel()
				check(t, be.mk(t).p)
			})
		}
	}
}

// headBucket heads bk through p.
func headBucket(p storage.Provider, bk string) (*storage.Bucket, error) {
	return p.HeadBucket(context.Background(), &storage.HeadBucketInput{Bucket: bk})
}

// deleteBucket deletes bk through p.
func deleteBucket(p storage.Provider, bk string) error {
	return p.DeleteBucket(context.Background(), &storage.DeleteBucketInput{Bucket: bk})
}

// listedBuckets returns the names of every bucket p lists.
func listedBuckets(t *testing.T, p storage.Provider) []string {
	t.Helper()
	out, err := p.ListBuckets(context.Background(), &storage.ListBucketsInput{})
	if err != nil {
		t.Fatalf("ListBuckets() error: %v", err)
	}
	names := make([]string, 0, len(out.Buckets))
	for _, b := range out.Buckets {
		names = append(names, b.Name)
	}
	return names
}

// contractHeadBucket heads the configured bucket: it exists and is named.
func contractHeadBucket(t *testing.T, p storage.Provider) {
	b, err := headBucket(p, contractBucket)
	if err != nil {
		t.Fatalf("HeadBucket() error: %v", err)
	}
	if b.Name != contractBucket {
		t.Errorf("HeadBucket() Name = %q, want %q", b.Name, contractBucket)
	}
}

// contractHeadBucketMissing heads a bucket that does not exist.
func contractHeadBucketMissing(t *testing.T, p storage.Provider) {
	b, err := headBucket(p, "no-such-bucket")
	if got := classify(err); got != "ErrBucketNotFound" || b != nil {
		t.Errorf("HeadBucket(missing) = %+v, %s; want nil, ErrBucketNotFound", b, got)
	}
}

// contractDeleteBucket deletes an empty bucket: HeadBucket then reports it
// missing and ListBuckets leaves it out.
func contractDeleteBucket(t *testing.T, p storage.Provider) {
	if err := deleteBucket(p, contractBucket); err != nil {
		t.Fatalf("DeleteBucket() error: %v", err)
	}
	if _, err := headBucket(p, contractBucket); classify(err) != "ErrBucketNotFound" {
		t.Errorf("HeadBucket() after delete = %s, want ErrBucketNotFound", classify(err))
	}
	if names := listedBuckets(t, p); slices.Contains(names, contractBucket) {
		t.Errorf("ListBuckets() after delete = %q, want it without %s", names, contractBucket)
	}
}

// contractDeleteBucketNotEmpty deletes a bucket that holds an object:
// ErrBucketNotEmpty, and the object stays.
func contractDeleteBucketNotEmpty(t *testing.T, p storage.Provider) {
	if _, err := cput(p, key("kept"), strings.NewReader("kept"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := classify(deleteBucket(p, contractBucket)); got != "ErrBucketNotEmpty" {
		t.Errorf("DeleteBucket(not empty) = %s, want ErrBucketNotEmpty", got)
	}
	_, body, err := cget(p, storage.HeadObjectInput{BaseObjectInput: key("kept")})
	if err != nil || string(body) != "kept" {
		t.Errorf("GetObject() after DeleteBucket(not empty) = %q, %v; want kept", body, err)
	}
}

// contractDeleteBucketMissing deletes a bucket that does not exist.
func contractDeleteBucketMissing(t *testing.T, p storage.Provider) {
	if got := classify(deleteBucket(p, "no-such-bucket")); got != "ErrBucketNotFound" {
		t.Errorf("DeleteBucket(missing) = %s, want ErrBucketNotFound", got)
	}
}

// contractBucketNamesValidated checks that both methods reject an invalid
// name, including the empty one: the configured bucket is not a fallback.
func contractBucketNamesValidated(t *testing.T, p storage.Provider) {
	for _, bk := range []string{"", "Bad_Bucket"} {
		if _, err := headBucket(p, bk); classify(err) != "ErrInvalidBucket" {
			t.Errorf("HeadBucket(%q) = %s, want ErrInvalidBucket", bk, classify(err))
		}
		if err := deleteBucket(p, bk); classify(err) != "ErrInvalidBucket" {
			t.Errorf("DeleteBucket(%q) = %s, want ErrInvalidBucket", bk, classify(err))
		}
	}
}

// contractDeleteThenCreate deletes an emptied bucket and creates it again:
// it holds no object from before.
func contractDeleteThenCreate(t *testing.T, p storage.Provider) {
	if _, err := cput(p, key("gone"), strings.NewReader("x"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := cdel(p, key("gone")); err != nil {
		t.Fatal(err)
	}
	if err := deleteBucket(p, contractBucket); err != nil {
		t.Fatalf("DeleteBucket() of an emptied bucket error: %v", err)
	}
	if _, err := p.CreateBucket(context.Background(),
		&storage.CreateBucketInput{Bucket: contractBucket}); err != nil {
		t.Fatalf("CreateBucket() again error: %v", err)
	}
	out, err := p.ListObjects(context.Background(), &storage.ListObjectsInput{})
	if err != nil || len(out.Objects) != 0 {
		t.Errorf("ListObjects() of the recreated bucket = %+v, %v; want empty", out, err)
	}
}

// TestContract_BucketAccessDenied checks, on s3 only, that a bucket another
// account owns is ErrAccessDenied from both methods: dir has no other
// accounts.
func TestContract_BucketAccessDenied(t *testing.T) {
	t.Parallel()
	p := newS3Backend(t).p
	if _, err := headBucket(p, foreignBucket); classify(err) != "ErrAccessDenied" {
		t.Errorf("HeadBucket(foreign) = %s, want ErrAccessDenied", classify(err))
	}
	if err := deleteBucket(p, foreignBucket); classify(err) != "ErrAccessDenied" {
		t.Errorf("DeleteBucket(foreign) = %s, want ErrAccessDenied", classify(err))
	}
}
