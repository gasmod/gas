package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/storage"
	storagedir "github.com/gasmod/gas/storage/dir"
	"github.com/gasmod/gas/storage/storagetest"
)

// #region mock
func TestReadMissingFileIsNotFound(t *testing.T) {
	mock := &storagetest.MockProvider{
		GetObjectFn: func(context.Context, *storage.GetObjectInput) (*storage.Object, error) {
			return nil, storage.ErrKeyNotFound
		},
	}

	_, err := NewFiles(mock, &storagetest.MockPresigner{}).Read(t.Context(), "missing.txt")

	var gasErr *gas.Error
	if !errors.As(err, &gasErr) || gasErr.Status != http.StatusNotFound {
		t.Fatalf("Read error = %v, want a 404 gas.Error", err)
	}
	if got := mock.CallCount("GetObject"); got != 1 {
		t.Errorf("GetObject called %d times, want 1", got)
	}
}

// #endregion mock

// #region dir-test
// newDirProvider builds the directory backend on a temporary directory, with
// "files" as the configured bucket, and creates that bucket.
func newDirProvider(t *testing.T) storage.Provider {
	t.Helper()

	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})

	cfg := config.New()
	cfg.Set("storage.bucket", "files")

	p, err := storagedir.NewProvider(storagedir.WithRoot(root))(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: "files"}); err != nil {
		t.Fatal(err)
	}
	return p
}

// #endregion dir-test

func TestFilesOnDir(t *testing.T) {
	ctx := t.Context()
	p := newDirProvider(t)
	files := NewFiles(p, &storagetest.MockPresigner{})

	etag, err := files.Upload(ctx, "notes/a.txt", "one\n")
	if err != nil {
		t.Fatal(err)
	}
	if etag == "" || strings.Contains(etag, `"`) {
		t.Errorf("ETag = %q, want a non-empty unquoted ETag", etag)
	}

	if err := files.Append(ctx, "notes/a.txt", "two"); err != nil {
		t.Fatal(err)
	}
	got, err := files.Read(ctx, "notes/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "one\ntwo\n" {
		t.Errorf("Read = %q", got)
	}

	// A write conditioned on the old ETag fails and writes nothing.
	_, err = p.PutObject(ctx, &storage.PutObjectInput{
		Body:            strings.NewReader("lost"),
		WriteCondition:  storage.WriteCondition{IfMatch: &etag},
		BaseObjectInput: storage.BaseObjectInput{Key: "notes/a.txt"},
	})
	if !errors.Is(err, storage.ErrPreconditionFailed) {
		t.Fatalf("stale IfMatch = %v, want ErrPreconditionFailed", err)
	}

	// A body shorter than ContentLength stores nothing.
	ten := int64(10)
	_, err = p.PutObject(ctx, &storage.PutObjectInput{
		Body:            strings.NewReader("short"),
		ContentLength:   &ten,
		BaseObjectInput: storage.BaseObjectInput{Key: "notes/b.txt"},
	})
	if !errors.Is(err, storage.ErrContentLengthMismatch) {
		t.Fatalf("short body = %v, want ErrContentLengthMismatch", err)
	}

	if _, err := files.Upload(ctx, "notes/c.txt", "three"); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Upload(ctx, "other.txt", "x"); err != nil {
		t.Fatal(err)
	}
	keys, err := files.Keys(ctx, "notes/")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(keys, []string{"notes/a.txt", "notes/c.txt"}) {
		t.Errorf("Keys = %v", keys)
	}

	// HeadObject reports a missing key as ErrNotFound, GetObject as
	// ErrKeyNotFound, and DeleteObject of it succeeds.
	missing := storage.BaseObjectInput{Key: "nope"}
	if _, err := p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: missing}); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("HeadObject missing = %v", err)
	}
	if _, err := p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: missing}); err != nil {
		t.Errorf("DeleteObject missing = %v", err)
	}

	// DeleteBucket refuses a bucket that holds objects.
	if err := p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: "files"}); !errors.Is(err, storage.ErrBucketNotEmpty) {
		t.Errorf("DeleteBucket non-empty = %v", err)
	}
}

func TestKeysPaginates(t *testing.T) {
	ctx := t.Context()
	p := newDirProvider(t)

	one := int32(1)
	prefix := "k/"
	for _, k := range []string{"k/1", "k/2", "k/3"} {
		if _, err := p.PutObject(ctx, &storage.PutObjectInput{
			BaseObjectInput: storage.BaseObjectInput{Key: k},
		}); err != nil {
			t.Fatal(err)
		}
	}

	var keys []string
	in := &storage.ListObjectsInput{Prefix: &prefix, MaxKeys: &one}
	for {
		out, err := p.ListObjects(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if (out.NextContinuationToken == "") == out.IsTruncated {
			t.Fatalf("token %q with IsTruncated %v", out.NextContinuationToken, out.IsTruncated)
		}
		for _, o := range out.Objects {
			keys = append(keys, o.Key)
		}
		if !out.IsTruncated {
			break
		}
		in.ContinuationToken = &out.NextContinuationToken
	}
	if !slices.Equal(keys, []string{"k/1", "k/2", "k/3"}) {
		t.Errorf("keys = %v", keys)
	}
}
