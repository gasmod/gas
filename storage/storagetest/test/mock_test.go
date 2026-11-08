package storagetest_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/storagetest"
)

// errOffline is what the fields of the tests return.
var errOffline = errors.New("offline")

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	m := &storagetest.MockProvider{
		GetObjectFn: func(context.Context, *storage.GetObjectInput) (*storage.Object, error) {
			return nil, errOffline
		},
	}

	_, err := m.GetObject(t.Context(), &storage.GetObjectInput{})
	require.ErrorIs(t, err, errOffline)
	assert.Equal(t, 1, m.CallCount("GetObject"))
}

// With its fields nil, every method succeeds with a zero output, but
// HeadObject and GetObject, which return ErrObjectNotFound.
func TestMockProvider_Defaults(t *testing.T) {
	t.Parallel()

	var m storagetest.MockProvider

	ctx := t.Context()

	require.NoError(t, m.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "b"}))
	require.NoError(t, m.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: "b"}))
	require.NoError(t, m.DeleteObject(ctx, &storage.DeleteObjectInput{Key: "k"}))

	b, err := m.HeadBucket(ctx, &storage.HeadBucketInput{Bucket: "b"})
	require.NoError(t, err)
	assert.Equal(t, &storage.Bucket{Name: "b"}, b)

	b, err = m.HeadBucket(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, &storage.Bucket{}, b)

	buckets, err := m.ListBuckets(ctx, &storage.ListBucketsInput{})
	require.NoError(t, err)
	assert.Equal(t, &storage.ListBucketsOutput{}, buckets)

	objects, err := m.ListObjects(ctx, &storage.ListObjectsInput{})
	require.NoError(t, err)
	assert.Equal(t, &storage.ListObjectsOutput{}, objects)

	out, err := m.PutObject(
		ctx,
		&storage.PutObjectInput{Key: "k", Body: strings.NewReader("unread")},
	)
	require.NoError(t, err)
	assert.Equal(t, &storage.PutObjectOutput{}, out)

	copied, err := m.CopyObject(
		ctx,
		&storage.CopyObjectInput{Source: storage.BaseObjectInput{Key: "k"}},
	)
	require.NoError(t, err)
	assert.Equal(t, &storage.CopyObjectOutput{}, copied)

	_, err = m.HeadObject(ctx, &storage.HeadObjectInput{Key: "k"})
	require.ErrorIs(t, err, storage.ErrObjectNotFound)

	obj, err := m.GetObject(ctx, &storage.GetObjectInput{Key: "k"})
	require.ErrorIs(t, err, storage.ErrObjectNotFound)
	assert.Nil(t, obj)

	var p storagetest.MockPresigner

	up, err := p.PresignUploadURL(ctx, &storage.PresignUploadURLInput{})
	require.NoError(t, err)
	assert.Equal(t, &storage.PresignURLResponse{Method: "PUT"}, up)

	down, err := p.PresignDownloadURL(ctx, &storage.PresignDownloadURLInput{})
	require.NoError(t, err)
	assert.Equal(t, &storage.PresignURLResponse{Method: "GET"}, down)
}

// Each method returns what its field returns.
//
//nolint:funlen // one assertion per method.
func TestMockProvider_Fields(t *testing.T) {
	t.Parallel()

	m := &storagetest.MockProvider{
		CreateBucketFn: func(context.Context, *storage.CreateBucketInput) error { return errOffline },
		HeadBucketFn: func(context.Context, *storage.HeadBucketInput) (*storage.Bucket, error) {
			return nil, errOffline
		},
		DeleteBucketFn: func(context.Context, *storage.DeleteBucketInput) error { return errOffline },
		ListBucketsFn: func(context.Context, *storage.ListBucketsInput) (*storage.ListBucketsOutput, error) {
			return nil, errOffline
		},
		HeadObjectFn: func(context.Context, *storage.HeadObjectInput) (*storage.ObjectInfo, error) {
			return &storage.ObjectInfo{Size: 3}, nil
		},
		PutObjectFn: func(context.Context, *storage.PutObjectInput) (*storage.PutObjectOutput, error) {
			return nil, errOffline
		},
		DeleteObjectFn: func(context.Context, *storage.DeleteObjectInput) error { return errOffline },
		ListObjectsFn: func(context.Context, *storage.ListObjectsInput) (*storage.ListObjectsOutput, error) {
			return nil, errOffline
		},
		CopyObjectFn: func(context.Context, *storage.CopyObjectInput) (*storage.CopyObjectOutput, error) {
			return nil, errOffline
		},
	}
	ctx := t.Context()

	require.ErrorIs(t, m.CreateBucket(ctx, nil), errOffline)
	require.ErrorIs(t, m.DeleteBucket(ctx, nil), errOffline)
	require.ErrorIs(t, m.DeleteObject(ctx, nil), errOffline)

	_, err := m.HeadBucket(ctx, nil)
	require.ErrorIs(t, err, errOffline)

	_, err = m.ListBuckets(ctx, nil)
	require.ErrorIs(t, err, errOffline)

	_, err = m.PutObject(ctx, nil)
	require.ErrorIs(t, err, errOffline)

	_, err = m.ListObjects(ctx, nil)
	require.ErrorIs(t, err, errOffline)

	_, err = m.CopyObject(ctx, nil)
	require.ErrorIs(t, err, errOffline)

	info, err := m.HeadObject(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(3), info.Size)

	p := &storagetest.MockPresigner{
		PresignUploadURLFn: func(context.Context, *storage.PresignUploadURLInput) (*storage.PresignURLResponse, error) {
			return nil, errOffline
		},
		PresignDownloadURLFn: func(
			context.Context,
			*storage.PresignDownloadURLInput,
		) (*storage.PresignURLResponse, error) {
			return nil, errOffline
		},
	}

	_, err = p.PresignUploadURL(ctx, nil)
	require.ErrorIs(t, err, errOffline)

	_, err = p.PresignDownloadURL(ctx, nil)
	require.ErrorIs(t, err, errOffline)
	assert.Equal(t, 1, p.CallCount("PresignDownloadURL"))
}

// A recorded call holds a copy of its input, which a caller that reuses
// the input does not change, and Calls returns a copy again; a nil input
// is recorded as nil of its type.
//
//nolint:funlen // the copies of one sequence of calls.
func TestMock_CallsAreCopies(t *testing.T) {
	t.Parallel()

	var m storagetest.MockProvider

	ctx := t.Context()
	token := "t1"
	in := &storage.ListObjectsInput{
		Bucket: new("b"), ContinuationToken: &token, Delimiter: new("/"),
		MaxKeys: new(int32(2)), Prefix: new("p"), StartAfter: new("s"),
	}

	_, err := m.ListObjects(ctx, in)
	require.NoError(t, err)

	token = "t2"

	_, err = m.ListObjects(ctx, in)
	require.NoError(t, err)

	put := &storage.PutObjectInput{
		Key: "k", Bucket: new("b"), ContentLength: new(int64(1)),
		Metadata: map[string]string{"a": "1"}, ContentType: new("text/plain"), IfMatch: new("e"),
	}

	_, err = m.PutObject(ctx, put)
	require.NoError(t, err)

	put.Metadata["a"] = "changed"
	*put.ContentType = "changed"

	_, err = m.GetObject(ctx, nil)
	require.ErrorIs(t, err, storage.ErrObjectNotFound)

	calls := m.Calls()
	require.Len(t, calls, 4)

	first, ok := calls[0].Args[0].(*storage.ListObjectsInput)
	require.True(t, ok)
	assert.Equal(t, "t1", *first.ContinuationToken)

	second, ok := calls[1].Args[0].(*storage.ListObjectsInput)
	require.True(t, ok)
	assert.Equal(t, "t2", *second.ContinuationToken)

	recorded, ok := calls[2].Args[0].(*storage.PutObjectInput)
	require.True(t, ok)
	assert.Equal(t, "1", recorded.Metadata["a"])
	assert.Equal(t, "text/plain", *recorded.ContentType)

	recorded.Metadata["a"] = "changed again"
	*first.Prefix = "changed"

	again := m.Calls()
	assert.Equal(t, "1", again[2].Args[0].(*storage.PutObjectInput).Metadata["a"])
	assert.Equal(t, "p", *again[0].Args[0].(*storage.ListObjectsInput).Prefix)
	assert.Equal(t, (*storage.GetObjectInput)(nil), again[3].Args[0])

	assert.Equal(t, 2, m.CallCount("ListObjects"))

	m.Reset()
	assert.Empty(t, m.Calls())
	assert.Zero(t, m.CallCount("ListObjects"))
}

// Every input type is recorded as a copy of its own type.
//
//nolint:funlen // one call per input type.
func TestMock_EveryInput(t *testing.T) {
	t.Parallel()

	var (
		m storagetest.MockProvider
		p storagetest.MockPresigner
	)

	ctx := t.Context()

	_ = m.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "b"})
	_, _ = m.HeadBucket(ctx, &storage.HeadBucketInput{Bucket: "b"})
	_ = m.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: "b"})
	_, _ = m.ListBuckets(ctx, &storage.ListBucketsInput{Prefix: new("p")})
	_, _ = m.HeadObject(ctx, &storage.HeadObjectInput{Key: "k", Range: new("bytes=0-1")})
	_, _ = m.GetObject(ctx, &storage.GetObjectInput{Key: "k", Version: new("v")})
	_ = m.DeleteObject(ctx, &storage.DeleteObjectInput{Key: "k", Version: new("v")})

	copyIn := &storage.CopyObjectInput{
		Bucket: new("b"), Key: "k",
		Metadata:    map[string]string{"a": "1"},
		ContentType: new("text/plain"),
		Source:      storage.BaseObjectInput{Bucket: new("s"), Key: "src"},
		Replace:     true,
	}

	_, _ = m.CopyObject(ctx, copyIn)

	copyIn.Metadata["a"] = "changed"
	*copyIn.ContentType = "changed"
	*copyIn.Bucket = "changed"
	*copyIn.Source.Bucket = "changed"

	_, _ = p.PresignUploadURL(ctx, &storage.PresignUploadURLInput{Key: "k", TTL: new(time.Minute)})
	_, _ = p.PresignDownloadURL(ctx, &storage.PresignDownloadURLInput{
		Key: "k", ResponseContentType: new("text/plain"),
	})

	want := []string{
		"CreateBucket",
		"HeadBucket",
		"DeleteBucket",
		"ListBuckets",
		"HeadObject",
		"GetObject",
		"DeleteObject",
		"CopyObject",
	}

	calls := m.Calls()
	require.Len(t, calls, len(want))

	for i, c := range calls {
		assert.Equal(t, want[i], c.Method)
		require.Len(t, c.Args, 1)
	}

	assert.Equal(t, &storage.GetObjectInput{Key: "k", Version: new("v")}, calls[5].Args[0])
	assert.Equal(t, &storage.CopyObjectInput{
		Bucket: new("b"), Key: "k",
		Metadata:    map[string]string{"a": "1"},
		ContentType: new("text/plain"),
		Source:      storage.BaseObjectInput{Bucket: new("s"), Key: "src"},
		Replace:     true,
	}, calls[7].Args[0])

	pcalls := p.Calls()
	require.Len(t, pcalls, 2)
	assert.Equal(
		t,
		&storage.PresignDownloadURLInput{Key: "k", ResponseContentType: new("text/plain")},
		pcalls[1].Args[0],
	)
	assert.Equal(
		t,
		&storage.PresignUploadURLInput{Key: "k", TTL: new(time.Minute)},
		pcalls[0].Args[0],
	)
}

// The methods may be called from several goroutines, for -race.
func TestMock_Concurrent(t *testing.T) {
	t.Parallel()

	var (
		m  storagetest.MockProvider
		wg sync.WaitGroup
	)

	for range 8 {
		wg.Go(func() {
			_, _ = m.HeadObject(context.Background(), &storage.HeadObjectInput{Key: "k"})
			_ = m.Calls()
			_ = m.CallCount("HeadObject")
		})
	}

	wg.Wait()
	assert.Equal(t, 8, m.CallCount("HeadObject"))
}

// Survey 2.18: Calls was an exported field read without the lock, the
// recorded inputs were the caller's own pointers, and an unset GetObject
// returned a nil object with a nil error. Calls is now a method returning
// copies, and an unset GetObject returns ErrObjectNotFound.
func TestFindings_Mocks(t *testing.T) {
	t.Parallel()

	var m storagetest.MockProvider

	obj, err := m.GetObject(t.Context(), &storage.GetObjectInput{Key: "k"})
	require.Error(t, err)
	assert.Nil(t, obj)

	in := &storage.GetObjectInput{Key: "k"}

	_, _ = m.GetObject(t.Context(), in)

	assert.NotSame(t, in, m.Calls()[1].Args[0])
}
