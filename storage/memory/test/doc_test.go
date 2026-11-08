package memory_test

import (
	"context"
	"crypto/md5" //nolint:gosec // G501: the ETag the documentation states.
	"encoding/hex"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/memory"
)

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	cfg := memory.DefaultConfig()

	cfg.Storage.Bucket = "photos"

	store, err := memory.New(memory.WithConfig(cfg))(nil, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, store.Init(ctx))

	defer func() { require.NoError(t, store.Close(ctx)) }()

	_, err = store.PutObject(ctx, &storage.PutObjectInput{
		Key:  "cats/tom.txt",
		Body: strings.NewReader("Tom"),
	})
	require.NoError(t, err)

	info, err := store.HeadObject(ctx, &storage.HeadObjectInput{Key: "cats/tom.txt"})
	require.NoError(t, err)

	sum := md5.Sum([]byte("Tom")) //nolint:gosec // G401: the ETag.

	assert.Equal(t, int64(3), info.Size)
	assert.Equal(t, "application/octet-stream", info.ContentType)
	assert.Equal(t, hex.EncodeToString(sum[:]), info.ETag)
}

// blockingBody is a body whose Read blocks until its context ends.
type blockingBody struct {
	// ctx ends the Read.
	ctx context.Context //nolint:containedctx // what ends the Read.
	// entered is closed when the first Read starts.
	entered chan struct{}
	// once closes entered once.
	once sync.Once
}

// Read blocks until ctx ends, then fails with its error.
func (b *blockingBody) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.ctx.Done()

	return 0, b.ctx.Err()
}

// The error messages the package documentation lists are the ones the
// store returns, and it lists every message this test makes.
//
//nolint:funlen // one case per documented message.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	nop := &gas.NopLogger{}
	ctx := t.Context()
	bucket := "photos"
	cfg := func() *memory.Config { return withBucket(bucket) }
	opened := func(c *memory.Config) *memory.Provider { return open(t, c) }
	built := func() *memory.Provider {
		p, err := memory.New(memory.WithConfig(cfg()))(nil, nop)
		require.NoError(t, err)

		return p
	}
	putErr := func(p *memory.Provider, in *storage.PutObjectInput) error {
		_, err := p.PutObject(ctx, in)

		return err
	}
	getErr := func(in *storage.GetObjectInput) error {
		p := opened(cfg())
		require.NoError(
			t,
			putErr(p, &storage.PutObjectInput{Key: "k", Body: strings.NewReader("v")}),
		)

		_, err := p.GetObject(ctx, in)

		return err
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	cases := map[string]func() error{
		"gas/storage/memory: logger is nil": func() error {
			_, err := memory.New()(nil, nil)

			return err
		},
		"gas/storage/memory: option is nil: at position 0": func() error {
			_, err := memory.New(nil)(nil, nop)

			return err
		},
		"gas/storage/memory: config is nil": func() error {
			_, err := memory.New(memory.WithConfig(nil))(nil, nop)

			return err
		},
		"gas/storage/memory: init: no config provider": func() error {
			p, err := memory.New()(nil, nop)
			require.NoError(t, err)

			return p.Init(ctx)
		},
		"gas/storage/memory: init: invalid config: Storage.Bucket must be a valid bucket name": func() error {
			return initErr(t, memory.WithConfig(withBucket("Not_Valid")))
		},
		"gas/storage/memory: init: service already initialized": func() error {
			return opened(cfg()).Init(ctx)
		},
		"gas/storage/memory: init: context canceled": func() error { return built().Init(canceled) },
		"gas/storage/memory: ready: service not initialized": func() error {
			return built().CheckReady(ctx)
		},
		"gas/storage/memory: put object: input is nil": func() error {
			return putErr(opened(cfg()), nil)
		},
		"gas/storage/memory: put object: service not initialized": func() error {
			return putErr(built(), &storage.PutObjectInput{Key: "k"})
		},
		"gas/storage/memory: put object: service closed": func() error {
			p := built()
			require.NoError(t, p.Init(ctx))
			require.NoError(t, p.Close(ctx))

			return putErr(p, &storage.PutObjectInput{Key: "k"})
		},
		"gas/storage/memory: put object: context canceled": func() error {
			_, err := opened(cfg()).PutObject(canceled, &storage.PutObjectInput{Key: "k"})

			return err
		},
		"gas/storage/memory: put object: invalid key: empty": func() error {
			return putErr(opened(cfg()), &storage.PutObjectInput{})
		},
		"gas/storage/memory: put object: bucket not found": func() error {
			return putErr(opened(cfg()), &storage.PutObjectInput{Bucket: new("missing"), Key: "k"})
		},
		"gas/storage/memory: put object: precondition failed": func() error {
			p := opened(cfg())
			require.NoError(t, putErr(p, &storage.PutObjectInput{Key: "k"}))

			return putErr(p, &storage.PutObjectInput{Key: "k", IfNoneMatch: new("*")})
		},
		"gas/storage/memory: put object: object not found": func() error {
			return putErr(opened(cfg()), &storage.PutObjectInput{Key: "k", IfMatch: new("abc")})
		},
		"gas/storage/memory: put object: storage full": func() error {
			c := cfg()

			c.Storage.MaxObjects = 1

			p := opened(c)
			require.NoError(t, putErr(p, &storage.PutObjectInput{Key: "a"}))

			return putErr(p, &storage.PutObjectInput{Key: "b"})
		},
		"gas/storage/memory: copy object: source: invalid key: empty": func() error {
			_, err := opened(cfg()).CopyObject(ctx, &storage.CopyObjectInput{})

			return err
		},
		"gas/storage/memory: copy object: invalid argument: copies an object onto itself without Replace": func() error { //nolint:lll // the documented text.
			_, err := opened(cfg()).CopyObject(ctx, &storage.CopyObjectInput{
				Key:    "k",
				Source: storage.BaseObjectInput{Key: "k"},
			})

			return err
		},
		"gas/storage/memory: create bucket: storage full": func() error {
			p := opened(memory.DefaultConfig())

			for i := range memory.MaxBuckets {
				require.NoError(
					t,
					p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "b-" + strconv.Itoa(i)}),
				)
			}

			return p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "one-more"})
		},
		"gas/storage/memory: get object: object not found": func() error {
			return getErr(&storage.GetObjectInput{Key: "missing"})
		},
		"gas/storage/memory: get object: object not found: bucket not found": func() error {
			return getErr(&storage.GetObjectInput{Bucket: new("missing"), Key: "k"})
		},
		"gas/storage/memory: get object: not supported: the store keeps no versions": func() error {
			return getErr(&storage.GetObjectInput{Key: "k", Version: new("v1")})
		},
		"gas/storage/memory: get object: not supported: the store keeps no parts": func() error {
			return getErr(&storage.GetObjectInput{Key: "k", PartNumber: new(int32(1))})
		},
		"gas/storage/memory: get object: range not satisfiable": func() error {
			return getErr(&storage.GetObjectInput{Key: "k", Range: new("bytes=5-")})
		},
		"gas/storage/memory: delete bucket: bucket not empty": func() error {
			p := opened(cfg())
			require.NoError(t, putErr(p, &storage.PutObjectInput{Key: "k"}))

			return p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: bucket})
		},
		"gas/storage/memory: list objects: invalid continuation token: not one this backend issued for this listing": func() error { //nolint:lll // the documented text.
			_, err := opened(
				cfg(),
			).ListObjects(ctx, &storage.ListObjectsInput{ContinuationToken: new("forged")})

			return err
		},
		"gas/storage/memory: close: context deadline exceeded": func() error {
			p := built()
			require.NoError(t, p.Init(ctx))

			bodyCtx, stop := context.WithCancel(context.Background())
			defer stop()

			body := &blockingBody{ctx: bodyCtx, entered: make(chan struct{})}
			done := make(chan error, 1)

			go func() { done <- putErr(p, &storage.PutObjectInput{Key: "k", Body: body}) }()

			<-body.entered

			closeCtx, cancelClose := context.WithTimeout(ctx, 50*time.Millisecond)
			defer cancelClose()

			err := p.Close(closeCtx)

			stop()
			require.Error(t, <-done)

			return err
		},
	}

	for _, text := range slices.Sorted(maps.Keys(cases)) {
		require.EqualError(t, cases[text](), text)
	}

	assert.ElementsMatch(
		t,
		slices.Collect(maps.Keys(cases)),
		docLines(t, "../doc.go", "gas/storage/memory: "),
	)
}
