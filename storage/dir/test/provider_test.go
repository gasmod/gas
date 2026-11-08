package dir_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir"
)

// errNotConfig is the error of a binder given another type.
var errNotConfig = errors.New("not a *dir.Config")

// binder is a gas.ConfigProvider that binds a directory and a bucket.
type binder struct {
	gas.ConfigProvider

	// path is the directory it binds.
	path string
}

// Bind sets the directory and the bucket of a *dir.Config.
func (b binder) Bind(dest any) error {
	cfg, ok := dest.(*dir.Config)
	if !ok {
		return errNotConfig
	}

	cfg.Storage.Dir = b.path
	cfg.Storage.Bucket = bucket

	return nil
}

// Init binds the `storage` section from the ConfigProvider, creates the
// configured bucket, and logs that it is initialized, without the path or
// the bucket's name.
func TestProvider_InitBinds(t *testing.T) {
	t.Parallel()

	path := t.TempDir()
	log := newRecorder()

	p, err := dir.New()(binder{path: path}, log)
	require.NoError(t, err)
	require.NoError(t, p.Init(t.Context()))

	defer func() { require.NoError(t, p.Close(context.Background())) }()

	assert.Equal(t, dir.ServiceName, p.Name())
	assert.DirExists(t, filepath.Join(path, sha(bucket)))
	assert.FileExists(t, filepath.Join(path, ".gas", "layout"))

	e, ok := log.find("service initialized")
	require.True(t, ok)
	assert.Equal(t, "info", e.level)
	assert.Equal(t, map[string]any{
		"service":  dir.ServiceName,
		"bucket":   true,
		"own_root": true,
	}, e.fields)
}

// A store given a root with WithRoot uses it, never closes it, and a
// restart uses it again.
func TestProvider_WithRoot(t *testing.T) {
	t.Parallel()

	path := t.TempDir()

	root, err := os.OpenRoot(path)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	p, log := build(t, dir.WithRoot(root), dir.WithConfig(config("", bucket)))
	ctx := t.Context()

	require.NoError(t, p.Init(ctx))
	put(t, p, "k", "v")
	require.NoError(t, p.Close(ctx))

	_, err = root.Stat(".gas")
	require.NoError(t, err, "the caller's root stays open")

	require.NoError(t, p.Init(ctx))
	assert.Equal(t, "v", read(t, p, "k"))
	require.NoError(t, p.Close(ctx))

	e, ok := log.find("service initialized")
	require.True(t, ok)
	assert.Equal(t, false, e.fields["own_root"])
	assert.DirExists(t, filepath.Join(path, sha(bucket)))
}

// A caller that closes its root while the store is open gets errors
// wrapping storage.ErrBackend, which hold no path.
func TestProvider_CallerClosedRoot(t *testing.T) {
	t.Parallel()

	path := t.TempDir()

	root, err := os.OpenRoot(path)
	require.NoError(t, err)

	p, _ := build(t, dir.WithRoot(root), dir.WithConfig(config("", bucket)))
	require.NoError(t, p.Init(t.Context()))

	defer func() { require.NoError(t, p.Close(context.Background())) }()

	require.NoError(t, root.Close())

	_, err = p.PutObject(t.Context(), putIn("k", "v"))
	require.ErrorIs(t, err, storage.ErrBackend)
	require.ErrorIs(t, err, os.ErrClosed)
	requireClean(t, err, path)
}

// Every method of a nil Provider returns storage.ErrNotInitialized, but
// Close, which returns nil, and Name.
func TestProvider_Nil(t *testing.T) {
	t.Parallel()

	var p *dir.Provider

	ctx := t.Context()
	b := storage.BaseObjectInput{Bucket: new(bucket), Key: "k"}

	_, headBucket := p.HeadBucket(ctx, &storage.HeadBucketInput{Bucket: bucket})
	_, listBuckets := p.ListBuckets(ctx, &storage.ListBucketsInput{})
	_, head := p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: b})
	_, putErr := p.PutObject(ctx, &storage.PutObjectInput{BaseObjectInput: b})
	_, getErr := p.GetObject(ctx, getIn("k"))
	_, list := p.ListObjects(ctx, &storage.ListObjectsInput{})
	_, copyErr := p.CopyObject(ctx, &storage.CopyObjectInput{BaseObjectInput: b, Source: b})

	for _, err := range []error{
		p.Init(ctx),
		p.CheckReady(ctx),
		p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: bucket}),
		headBucket,
		p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: bucket}),
		listBuckets,
		head,
		putErr,
		getErr,
		p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: b}),
		list,
		copyErr,
	} {
		require.ErrorIs(t, err, storage.ErrNotInitialized)
	}

	require.NoError(t, p.Close(ctx))
	assert.Equal(t, dir.ServiceName, p.Name())
}

// Init refuses a directory that is a file, and a layout marker of another
// layout, with errors wrapping storage.ErrBackend that hold no path, and
// stays not initialized.
func TestProvider_InitRefuses(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	other := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(other, ".gas"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(other, ".gas", "layout"), []byte("1\n"), 0o600))

	for path, want := range map[string]string{
		file:  "gas/storage/dir: init: backend failed: open dir: not a directory",
		other: "gas/storage/dir: init: backend failed: layout marker holds another layout",
	} {
		p, _ := build(t, dir.WithConfig(config(path, "")))

		err := p.Init(t.Context())
		require.ErrorIs(t, err, storage.ErrBackend)
		require.EqualError(t, err, want)
		requireClean(t, err, path)
		require.ErrorIs(t, p.CheckReady(t.Context()), storage.ErrNotInitialized)
	}
}

// An Init that fails after it opened the directory, here on a configured
// bucket it cannot create, closes it and can be tried again.
func TestProvider_InitFailureRetried(t *testing.T) {
	t.Parallel()

	path := t.TempDir()

	// A file where the bucket's metadata directory goes.
	require.NoError(t, os.WriteFile(filepath.Join(path, sha(bucket)), nil, 0o600))

	p, _ := build(t, dir.WithConfig(config(path, bucket)))

	err := p.Init(t.Context())
	require.ErrorIs(t, err, storage.ErrBackend)
	requireClean(t, err, path, bucket)

	require.NoError(t, os.Remove(filepath.Join(path, sha(bucket))))
	require.NoError(t, p.Init(t.Context()))
	require.NoError(t, p.Close(t.Context()))
}

// Close logs that the store is closed; a Close whose context ends first
// logs that it canceled the calls in flight.
func TestProvider_CloseLogs(t *testing.T) {
	t.Parallel()

	p, log := build(t, dir.WithConfig(config(t.TempDir(), bucket)))
	require.NoError(t, p.Init(t.Context()))
	require.NoError(t, p.Close(t.Context()))

	e, ok := log.find("service closed")
	require.True(t, ok)
	assert.Equal(t, "info", e.level)

	require.NoError(t, p.Init(t.Context()))

	bodyCtx, stop := context.WithCancel(context.Background())
	defer stop()

	body := newBlockingBody(bodyCtx)
	done := make(chan error, 1)

	go func() {
		in := putIn("k", "")

		in.Body = body

		_, err := p.PutObject(context.Background(), in)
		done <- err
	}()

	<-body.entered

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.ErrorIs(t, p.Close(ctx), context.Canceled)
	stop()
	require.ErrorIs(t, <-done, storage.ErrClosed)

	e, ok = log.find("service closed, calls in flight canceled")
	require.True(t, ok)
	assert.Equal(t, "warn", e.level)
}

// A store whose directory is removed while open is not ready.
func TestProvider_CheckReady(t *testing.T) {
	t.Parallel()

	p, path := open(t)
	require.NoError(t, p.CheckReady(t.Context()))

	if runtime.GOOS == "windows" {
		t.Skip("Windows does not let an open store's directory be removed")
	}

	require.NoError(t, os.RemoveAll(path))

	err := p.CheckReady(t.Context())
	require.ErrorIs(t, err, storage.ErrBackend)
	requireClean(t, err, path)
}
