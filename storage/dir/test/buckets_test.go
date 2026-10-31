package dir_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir"
)

// A bucket whose name record is missing, or holds another name, is left
// out of ListBuckets and passed to the skip handler with an error wrapping
// ErrUnnamedBucket; its objects stay readable, and CreateBucket of it
// rewrites the record, which lists it again.
func TestListBuckets_Unnamed(t *testing.T) {
	t.Parallel()

	for name, damage := range map[string]func(string) error{
		"missing": func(path string) error { return os.Remove(nameRecord(path, bucket)) },
		"another name": func(path string) error {
			return os.WriteFile(nameRecord(path, bucket), []byte("other"), 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var got skips

			path := t.TempDir()
			p := openAt(t, path, dir.WithSkipHandler(got.handler()))
			ctx := t.Context()

			put(t, p, "k", "v")
			require.NoError(t, damage(path))

			out, err := p.ListBuckets(ctx, &storage.ListBucketsInput{})
			require.NoError(t, err)
			assert.Empty(t, out.Buckets)

			all := got.all()
			require.Len(t, all, 1)
			assert.Equal(t, filepath.Join(".gas", "buckets", sha(bucket)), all[0].path)
			require.ErrorIs(t, all[0].err, dir.ErrUnnamedBucket)
			assert.Equal(t, "v", read(t, p, "k"))

			require.NoError(t, p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: bucket}))

			out, err = p.ListBuckets(ctx, &storage.ListBucketsInput{})
			require.NoError(t, err)
			require.Len(t, out.Buckets, 1)
			assert.Equal(t, bucket, out.Buckets[0].Name)
		})
	}
}

// CreateBucket of a bucket that exists keeps its objects and its
// creation date, and a bucket deleted and created again gets a new one.
func TestCreateBucket_Exists(t *testing.T) {
	t.Parallel()

	p, path := open(t)
	ctx := t.Context()

	put(t, p, "k", "v")

	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(nameRecord(path, bucket), old, old))
	require.NoError(t, p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: bucket}))
	assert.Equal(t, "v", read(t, p, "k"))

	out, err := p.ListBuckets(ctx, &storage.ListBucketsInput{})
	require.NoError(t, err)
	require.Len(t, out.Buckets, 1)
	assert.True(t, old.Equal(out.Buckets[0].CreationDate))

	require.NoError(t, p.DeleteObject(ctx, &storage.DeleteObjectInput{
		Key: "k",
	}))
	require.NoError(t, p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: bucket}))
	require.NoError(t, p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: bucket}))

	out, err = p.ListBuckets(ctx, &storage.ListBucketsInput{})
	require.NoError(t, err)
	require.Len(t, out.Buckets, 1)
	assert.True(t, out.Buckets[0].CreationDate.After(old))
}

// HeadBucket and CreateBucket of a bucket whose metadata directory is a
// file are errors wrapping storage.ErrBackend that hold no path.
func TestBucket_NotADirectory(t *testing.T) {
	t.Parallel()

	p, path := open(t)
	ctx := t.Context()
	other := "other-bucket"

	require.NoError(t, os.WriteFile(filepath.Join(path, sha(other)), nil, 0o600))

	_, err := p.HeadBucket(ctx, &storage.HeadBucketInput{Bucket: other})
	require.ErrorIs(t, err, storage.ErrBackend)
	requireClean(t, err, path, other)

	err = p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: other})
	require.ErrorIs(t, err, storage.ErrBackend)
	requireClean(t, err, path, other)
}

// DeleteBucket removes with the bucket what no read can use, metadata
// that does not decode, metadata whose data file is gone and the temp
// files of crashed writes, passes each to the skip handler, and leaves
// nothing of the bucket.
func TestDeleteBucket_Debris(t *testing.T) {
	t.Parallel()

	var got skips

	path := t.TempDir()
	p := openAt(t, path, dir.WithSkipHandler(got.handler()))
	ctx := t.Context()

	put(t, p, "corrupt", "v")
	put(t, p, "gone", "v")
	require.NoError(t, os.WriteFile(metaFile(path, "corrupt"), []byte("{"), 0o600))

	for _, f := range dataFiles(t, path, "gone") {
		require.NoError(t, os.Remove(f))
	}

	temp := metaFile(path, "temp") + "00000000-0000-4000-8000-000000000000"
	require.NoError(t, os.WriteFile(temp, []byte("{}"), 0o600))

	require.NoError(t, p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: bucket}))

	var removed int

	for _, s := range got.all() {
		if strings.HasPrefix(s.err.Error(), "removed with the bucket") {
			removed++
		}
	}

	assert.Equal(t, 3, removed)

	for _, d := range []string{
		filepath.Join(path, sha(bucket)),
		objectsDir(path, bucket),
		keysDir(path),
		locksDir(path, bucket),
		nameRecord(path, bucket),
	} {
		assert.NoFileExists(t, d)
		assert.NoDirExists(t, d)
	}
}

// DeleteBucket waits for a write in progress of a key the bucket holds,
// until its context ends.
func TestDeleteBucket_WaitsForOverwrite(t *testing.T) {
	t.Parallel()

	p, _ := open(t)
	ctx := t.Context()

	put(t, p, "k", "v")

	bodyCtx, stop := context.WithCancel(context.Background())
	body := newBlockingBody(bodyCtx)
	done := make(chan error, 1)

	go func() {
		in := putIn("k", "")

		in.Body = body

		_, err := p.PutObject(ctx, in)
		done <- err
	}()

	<-body.entered

	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	err := p.DeleteBucket(short, &storage.DeleteBucketInput{Bucket: bucket})
	require.EqualError(t, err, "gas/storage/dir: delete bucket: context deadline exceeded")

	stop()
	require.Error(t, <-done)
	assert.Equal(t, "v", read(t, p, "k"))
}
