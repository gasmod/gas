package dir_test

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir"
)

// A PutObject whose body blocked ignored its context while it copied the
// body, and held the key's lock throughout, so other calls on the key
// waited too. It now checks its context between reads of the body: it
// returns the context's error, stores nothing, and gives the key up.
func TestFindings_PutHonorsContextDuringBody(t *testing.T) {
	t.Parallel()

	p, path := open(t)
	body := newSlowBody()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)

	defer cancel()

	in := putIn("k", "")

	in.Body = body

	start := time.Now()

	_, err := p.PutObject(ctx, in)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second)

	quick, cancelQuick := context.WithTimeout(t.Context(), time.Second)
	defer cancelQuick()

	_, err = p.HeadObject(quick, headIn("k"))
	require.ErrorIs(t, err, storage.ErrObjectNotFound, "the key's lock is free")
	assert.Empty(t, dataFiles(t, path, "k"))
}

// A nil *dir.Provider panicked on every method. Every method now returns
// storage.ErrNotInitialized, but Close, which returns nil; see
// TestProvider_Nil.
func TestFindings_NilProvider(t *testing.T) {
	t.Parallel()

	var p *dir.Provider

	_, err := p.PutObject(t.Context(), putIn("k", "v"))
	require.ErrorIs(t, err, storage.ErrNotInitialized)
	require.NoError(t, p.Close(t.Context()))
}

// A page of ListObjects read the metadata of every object of the bucket
// and stated every data file. It now reads the metadata of the objects of
// the page only: an object it cannot read past the page is not even
// looked at.
func TestFindings_ListReadsThePageOnly(t *testing.T) {
	t.Parallel()

	var got skips

	path := t.TempDir()
	p := openAt(t, path, dir.WithSkipHandler(got.handler()))

	for _, k := range []string{"a", "b", "c", "d", "e"} {
		put(t, p, k, "v")
	}

	require.NoError(t, os.WriteFile(metaFile(path, "e"), []byte("{"), 0o600))

	out, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{MaxKeys: new(int32(2))})
	require.NoError(t, err)
	assert.Len(t, out.Objects, 2)
	assert.Empty(t, got.all())

	assert.Equal(t, []string{"a", "b", "c", "d"}, listKeys(t, p, &storage.ListObjectsInput{}))
	assert.Len(t, got.all(), 1)
}

// A continuation token was the last key in base64url: it exposed the key,
// and a forged one started a listing anywhere. A token now tells nothing
// of the key, and one that is changed, forged, from another listing or
// from before a restart is storage.ErrInvalidToken.
func TestFindings_TokensAreOpaque(t *testing.T) {
	t.Parallel()

	p, _ := open(t)
	key := "users/alice@example.com/passport.pdf"

	put(t, p, key, "v")
	put(t, p, "users/bob", "v")

	out, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{MaxKeys: new(int32(1))})
	require.NoError(t, err)

	token := out.NextContinuationToken
	require.NotEmpty(t, token)

	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding, base64.StdEncoding, base64.RawStdEncoding,
	} {
		assert.NotContains(t, token, enc.EncodeToString([]byte(key)))
	}

	raw, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "alice")

	forged := base64.RawURLEncoding.EncodeToString([]byte(key))
	// A character in the middle: the last one may carry only bits that
	// base64 decoding ignores.
	flip := map[bool]string{true: "B", false: "A"}[token[8] == 'A']
	changed := token[:8] + flip + token[9:]

	for _, tok := range []string{forged, changed} {
		_, err = p.ListObjects(t.Context(), &storage.ListObjectsInput{ContinuationToken: &tok})
		require.ErrorIs(t, err, storage.ErrInvalidToken)
		requireClean(t, err, "", tok, "alice")
	}

	require.NoError(t, p.Close(t.Context()))
	require.NoError(t, p.Init(t.Context()))

	_, err = p.ListObjects(t.Context(), &storage.ListObjectsInput{ContinuationToken: &token})
	require.ErrorIs(t, err, storage.ErrInvalidToken)
}

// The constructor and Init quoted the configured bucket and the path in
// their errors. They now name the setting, or give the cause only.
func TestFindings_InitErrorsHoldNoData(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "secret-dir")

	for _, cfg := range []*dir.Config{
		config(t.TempDir(), "Secret_Bucket"),
		config(missing, "secret-bucket"),
	} {
		p, err := dir.New(dir.WithConfig(cfg))(nil, &gas.NopLogger{})
		require.NoError(t, err)

		err = p.Init(t.Context())
		requireClean(t, err, missing, "ecret")
	}
}

// A PutObject that raced a DeleteBucket of its bucket in another process
// failed on darwin with an EINVAL that was not classified, where it should
// be storage.ErrBucketNotFound. TestProvider_CrossProcessBucketLifecycle
// pins it across processes; here, in one process, a write of a new key
// whose bucket is deleted while it reads its body fails with
// storage.ErrBucketNotFound, even when the bucket is created again.
func TestFindings_DeletedBucketUnderWrite(t *testing.T) {
	t.Parallel()

	var got skips

	path := t.TempDir()
	p := openAt(t, path, dir.WithSkipHandler(got.handler()))
	ctx := t.Context()
	gate := make(chan struct{})
	entered := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		in := putIn("k", "")

		in.Body = &gatedBody{entered: entered, release: gate}

		_, err := p.PutObject(ctx, in)
		done <- err
	}()

	<-entered
	require.NoError(t, p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: bucket}))
	require.NoError(t, p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: bucket}))
	close(gate)

	err := <-done
	require.ErrorIs(t, err, storage.ErrBucketNotFound)
	requireClean(t, err, path, bucket)

	_, err = p.HeadObject(ctx, headIn("k"))
	require.ErrorIs(t, err, storage.ErrObjectNotFound)

	var left bool

	for _, s := range got.all() {
		left = left || strings.Contains(s.err.Error(), "data file of a write in progress")
	}

	assert.True(t, left, "DeleteBucket reports the data file of the write it left")
}

// gatedBody is a body whose first Read blocks until release is closed,
// then yields `v` and ends.
type gatedBody struct {
	// entered is closed when the first Read starts.
	entered chan struct{}
	// release ends the wait.
	release chan struct{}
	// read reports whether the body was read.
	read bool
}

// Read blocks until release is closed, then yields one byte and ends.
func (b *gatedBody) Read(p []byte) (int, error) {
	if b.read {
		return 0, io.EOF
	}

	close(b.entered)
	<-b.release

	b.read = true

	return copy(p, "v"), nil
}
