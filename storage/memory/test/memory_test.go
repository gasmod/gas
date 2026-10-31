package memory_test

import (
	"context"
	"io"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/memory"
)

// DefaultConfig holds the documented defaults.
func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	assert.Equal(t, &memory.Config{Storage: memory.Settings{
		MaxBytes:   256 << 20,
		MaxObjects: 100000,
	}}, memory.DefaultConfig())
	assert.Equal(t, 1<<20, memory.MinMaxBytes)
	assert.Equal(t, 10000, memory.MaxBuckets)
}

// Init checks the validate tags of a configuration given to WithConfig,
// and never changes it.
func TestInit_ValidateTags(t *testing.T) {
	t.Parallel()

	for _, edit := range []func(*memory.Config){
		func(c *memory.Config) { c.Storage.MaxBytes = memory.MinMaxBytes - 1 },
		func(c *memory.Config) { c.Storage.MaxObjects = 0 },
	} {
		cfg := memory.DefaultConfig()
		edit(cfg)

		before := *cfg

		err := initErr(t, memory.WithConfig(cfg))
		require.ErrorIs(t, err, storage.ErrInvalidConfig)
		require.ErrorContains(t, err, "gas/storage/memory: init: invalid config: validate config: ")
		assert.Equal(t, before, *cfg)
	}
}

// Init binds the `storage` section from the ConfigProvider.
func TestInit_Binds(t *testing.T) {
	t.Parallel()

	p, err := memory.New()(binder{bucket: "photos"}, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, p.Init(t.Context()))

	defer func() { require.NoError(t, p.Close(t.Context())) }()

	_, err = p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: "photos"})
	require.NoError(t, err)
}

// binder is a gas.ConfigProvider that binds a configured bucket.
type binder struct {
	gas.ConfigProvider

	// bucket is the bucket it binds.
	bucket string
}

// Bind sets the bucket of a *memory.Config.
func (b binder) Bind(dest any) error {
	cfg, ok := dest.(*memory.Config)
	if ok {
		cfg.Storage.Bucket = b.bucket
	}

	return nil
}

// Init creates the configured bucket, and logs what it opened without
// the bucket's name; Close logs that it closed.
func TestInit_LogsAndCreatesBucket(t *testing.T) {
	t.Parallel()

	p, log := build(t, memory.WithConfig(withBucket("secret-bucket")))
	require.NoError(t, p.Init(t.Context()))

	b, err := p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: "secret-bucket"})
	require.NoError(t, err)
	assert.Equal(t, &storage.Bucket{Name: "secret-bucket"}, b)

	require.NoError(t, p.Close(t.Context()))

	entries := log.entries()
	require.Len(t, entries, 2)
	assert.Equal(t, "info", entries[0].level)
	assert.Equal(t, "service initialized", entries[0].msg)
	assert.Equal(t, map[string]any{
		"service":     memory.ServiceName,
		"bucket":      true,
		"max_bytes":   int64(256 << 20),
		"max_objects": 100000,
	}, entries[0].fields)
	assert.Equal(t, "service closed", entries[1].msg)

	for _, e := range entries {
		for _, v := range e.fields {
			assert.NotContains(t, strings.ToLower(toString(v)), "secret")
		}
	}
}

// toString returns v as text.
func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}

	return ""
}

// A Close whose context ends first logs a warning; one on a store that is
// not initialized logs nothing.
func TestClose_LogsCanceled(t *testing.T) {
	t.Parallel()

	p, log := build(t, memory.WithConfig(withBucket("photos")))
	require.NoError(t, p.Close(t.Context()))
	assert.Empty(t, log.entries())

	require.NoError(t, p.Init(t.Context()))

	bodyCtx, stop := context.WithCancel(context.Background())
	body := &blockingBody{ctx: bodyCtx, entered: make(chan struct{})}
	done := make(chan error, 1)

	go func() {
		_, err := p.PutObject(context.Background(), &storage.PutObjectInput{Key: "k", Body: body})
		done <- err
	}()

	<-body.entered

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.ErrorIs(t, p.Close(ctx), context.Canceled)
	stop()
	require.ErrorIs(t, <-done, storage.ErrClosed)

	entries := log.entries()
	last := entries[len(entries)-1]
	assert.Equal(t, "warn", last.level)
	assert.Equal(t, "service closed, calls in flight canceled", last.msg)
}

// A nil Provider returns ErrNotInitialized, and its Close returns nil.
func TestNilProvider(t *testing.T) {
	t.Parallel()

	var p *memory.Provider

	ctx := t.Context()

	assert.Equal(t, memory.ServiceName, p.Name())
	require.NoError(t, p.Close(ctx))
	require.ErrorIs(t, p.Init(ctx), storage.ErrNotInitialized)
	require.ErrorIs(t, p.CheckReady(ctx), storage.ErrNotInitialized)

	_, err := p.PutObject(ctx, &storage.PutObjectInput{Key: "k"})
	require.EqualError(t, err, "gas/storage/memory: put object: service not initialized")

	_, err = p.HeadObject(ctx, &storage.HeadObjectInput{Key: "k"})
	require.ErrorIs(t, err, storage.ErrNotInitialized)

	_, err = p.ListBuckets(ctx, &storage.ListBucketsInput{})
	require.ErrorIs(t, err, storage.ErrNotInitialized)
}

// A body is also an io.Seeker, and stays readable after its object is
// replaced or deleted, and after it is closed.
func TestGetObject_Body(t *testing.T) {
	t.Parallel()

	p := open(t, withBucket("photos"))
	ctx := t.Context()

	_, err := p.PutObject(
		ctx,
		&storage.PutObjectInput{Key: "k", Body: strings.NewReader("0123456789")},
	)
	require.NoError(t, err)

	obj, err := p.GetObject(ctx, &storage.GetObjectInput{Key: "k", Range: new("bytes=2-5")})
	require.NoError(t, err)

	seeker, ok := obj.Body.(io.Seeker)
	require.True(t, ok)

	_, err = p.PutObject(ctx, &storage.PutObjectInput{Key: "k", Body: strings.NewReader("new")})
	require.NoError(t, err)
	require.NoError(t, p.DeleteObject(ctx, &storage.DeleteObjectInput{Key: "k"}))
	require.NoError(t, obj.Body.Close())

	_, err = seeker.Seek(1, io.SeekStart)
	require.NoError(t, err)

	b, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	assert.Equal(t, "345", string(b))
}

// A write that would take the store past MaxBytes is ErrStorageFull, and
// changes nothing; replacing and deleting objects frees what they held.
func TestPutObject_MaxBytes(t *testing.T) {
	t.Parallel()

	cfg := withBucket("photos")

	cfg.Storage.MaxBytes = memory.MinMaxBytes

	p := open(t, cfg)
	ctx := t.Context()
	half := strings.Repeat("v", memory.MinMaxBytes/2)
	write := func(key, body string) error {
		_, err := p.PutObject(ctx, &storage.PutObjectInput{Key: key, Body: strings.NewReader(body)})

		return err
	}

	require.ErrorIs(t, write("big", half+half+"v"), memory.ErrStorageFull)
	require.NoError(t, write("a", half))
	require.ErrorIs(t, write("b", half+"v"), memory.ErrStorageFull)

	_, err := p.HeadObject(ctx, &storage.HeadObjectInput{Key: "b"})
	require.ErrorIs(t, err, storage.ErrObjectNotFound)

	require.NoError(t, write("a", "small"))
	require.NoError(t, write("b", half))
	require.NoError(t, p.DeleteObject(ctx, &storage.DeleteObjectInput{Key: "b"}))
	require.NoError(t, write("c", half))

	md := map[string]string{"n": strings.Repeat("m", 2000)}

	_, err = p.PutObject(ctx, &storage.PutObjectInput{
		Key:      "d",
		Body:     strings.NewReader(strings.Repeat("v", memory.MinMaxBytes/2-1000)),
		Metadata: md,
	})
	require.ErrorIs(t, err, memory.ErrStorageFull)
}

// The key counts toward MaxBytes.
func TestPutObject_MaxBytesCountsKey(t *testing.T) {
	t.Parallel()

	cfg := withBucket("photos")

	cfg.Storage.MaxBytes = memory.MinMaxBytes

	p := open(t, cfg)
	write := func(key string, n int) error {
		_, err := p.PutObject(t.Context(), &storage.PutObjectInput{
			Key:  key,
			Body: strings.NewReader(strings.Repeat("v", n)),
		})

		return err
	}

	// The body and the default Content-Type fill MaxBytes but for three
	// bytes: room for a key of three bytes, not four.
	n := memory.MinMaxBytes - len("application/octet-stream") - len("key")

	require.ErrorIs(t, write("keys", n), memory.ErrStorageFull)
	require.NoError(t, write("key", n))
}

// gatedBody yields n bytes, closes read once it has, then blocks until
// gate is closed and ends.
type gatedBody struct {
	// gate ends the body once closed.
	gate chan struct{}
	// read is closed once n bytes are read.
	read chan struct{}
	// n is how many bytes are left before the gate.
	n int
}

// Read yields the bytes before the gate, then waits for it.
func (g *gatedBody) Read(b []byte) (int, error) {
	if g.n > 0 {
		k := min(len(b), g.n)

		g.n -= k

		for i := range k {
			b[i] = 'v'
		}

		return k, nil
	}

	close(g.read)
	<-g.gate

	return 0, io.EOF
}

// countingBody is an endless body that counts the bytes read from it.
type countingBody struct {
	// n is how many bytes were read.
	n int64
}

// Read fills b.
func (c *countingBody) Read(b []byte) (int, error) {
	c.n += int64(len(b))

	return len(b), nil
}

// The bodies writes in flight have read count toward MaxBytes, so
// concurrent writes cannot hold more than the limit between them.
func TestPutObject_MaxBytesInFlight(t *testing.T) {
	t.Parallel()

	cfg := withBucket("photos")

	cfg.Storage.MaxBytes = memory.MinMaxBytes

	p := open(t, cfg)
	ctx := t.Context()
	half := memory.MinMaxBytes / 2
	body := &gatedBody{gate: make(chan struct{}), read: make(chan struct{}), n: half + 1}
	first := make(chan error, 1)

	go func() {
		_, err := p.PutObject(ctx, &storage.PutObjectInput{Key: "a", Body: body})
		first <- err
	}()

	<-body.read

	// A second write stops reading once the bodies of both would not fit:
	// it never holds more than the room the first one left.
	endless := &countingBody{}

	_, err := p.PutObject(ctx, &storage.PutObjectInput{Key: "b", Body: endless})
	require.ErrorIs(t, err, memory.ErrStorageFull)
	assert.LessOrEqual(t, endless.n, int64(memory.MinMaxBytes-half-1+64<<10))

	close(body.gate)
	require.NoError(t, <-first)

	// Neither write holds room once it returned.
	require.NoError(t, p.DeleteObject(ctx, &storage.DeleteObjectInput{Key: "a"}))

	_, err = p.PutObject(ctx, &storage.PutObjectInput{
		Key:  "b",
		Body: strings.NewReader(strings.Repeat("v", memory.MinMaxBytes-1024)),
	})
	require.NoError(t, err)
}

// onlyReader hides every method of its Reader but Read.
type onlyReader struct{ io.Reader }

// The store keeps no more memory than it counts toward MaxBytes: a body
// read in chunks is not kept in a buffer of up to twice its length.
//
// Not parallel: it measures the heap of the process.
func TestPutObject_MemoryMatchesCount(t *testing.T) {
	const size = 4<<20 + 32<<10

	cfg := withBucket("photos")

	cfg.Storage.MaxBytes = 64 << 20

	p := open(t, cfg)

	var ms runtime.MemStats

	runtime.GC()
	runtime.ReadMemStats(&ms)

	base := ms.HeapAlloc
	stored := 0

	for i := range 14 {
		_, err := p.PutObject(t.Context(), &storage.PutObjectInput{
			Key:           "k" + strconv.Itoa(i),
			Body:          onlyReader{strings.NewReader(strings.Repeat("v", size))},
			ContentLength: new(int64(size)),
		})
		require.NoError(t, err)

		stored += size
	}

	runtime.GC()
	runtime.ReadMemStats(&ms)
	runtime.KeepAlive(p)

	limit := uint64(stored + stored/8)
	assert.Less(t, ms.HeapAlloc, base+limit,
		"heap grew by %d bytes for %d bytes stored", ms.HeapAlloc-base, stored)
}

// MaxObjects counts the objects across buckets; a replacement is not one
// more.
func TestPutObject_MaxObjects(t *testing.T) {
	t.Parallel()

	cfg := withBucket("photos")

	cfg.Storage.MaxObjects = 2

	p := open(t, cfg)
	ctx := t.Context()

	require.NoError(t, p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "other"}))

	for _, in := range []*storage.PutObjectInput{
		{Key: "a"}, {Key: "a"}, {Bucket: new("other"), Key: "b"},
	} {
		_, err := p.PutObject(ctx, in)
		require.NoError(t, err)
	}

	_, err := p.PutObject(ctx, &storage.PutObjectInput{Key: "c"})
	require.ErrorIs(t, err, memory.ErrStorageFull)
}

// A token from before a restart is refused, as the key that sealed it is
// gone.
func TestListObjects_TokenAfterRestart(t *testing.T) {
	t.Parallel()

	p := open(t, withBucket("photos"))
	ctx := t.Context()

	for i := range 3 {
		_, err := p.PutObject(ctx, &storage.PutObjectInput{Key: "k" + strconv.Itoa(i)})
		require.NoError(t, err)
	}

	out, err := p.ListObjects(ctx, &storage.ListObjectsInput{MaxKeys: new(int32(1))})
	require.NoError(t, err)
	require.NoError(t, p.Close(ctx))
	require.NoError(t, p.Init(ctx))

	_, err = p.ListObjects(ctx, &storage.ListObjectsInput{
		MaxKeys:           new(int32(1)),
		ContinuationToken: &out.NextContinuationToken,
	})
	require.ErrorIs(t, err, storage.ErrInvalidToken)
}

// A ListBuckets token never opens for ListObjects, and Init fails with
// ErrStorageFull when the configured bucket would be one past MaxBuckets.
func TestInit_BucketsFull(t *testing.T) {
	t.Parallel()

	p, _ := build(t, memory.WithConfig(withBucket("one-more")))
	ctx := t.Context()

	require.NoError(t, p.Init(ctx))

	for i := range memory.MaxBuckets - 1 {
		require.NoError(
			t,
			p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "b-" + strconv.Itoa(i)}),
		)
	}

	out, err := p.ListBuckets(ctx, &storage.ListBucketsInput{MaxBuckets: new(int32(1))})
	require.NoError(t, err)

	_, err = p.ListObjects(ctx, &storage.ListObjectsInput{
		Bucket:            new("b-0"),
		ContinuationToken: &out.NextContinuationToken,
	})
	require.ErrorIs(t, err, storage.ErrInvalidToken)

	require.NoError(t, p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: "one-more"}))
	require.NoError(t, p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "last"}))
	require.NoError(t, p.Close(ctx))

	err = p.Init(ctx)
	require.EqualError(t, err, "gas/storage/memory: init: storage full")
	require.ErrorIs(t, err, memory.ErrStorageFull)
}
