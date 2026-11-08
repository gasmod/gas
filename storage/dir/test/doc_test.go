package dir_test

import (
	"context"
	"crypto/md5" //nolint:gosec // G501: the ETag the documentation states.
	"encoding/hex"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir"
)

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	cfg := dir.DefaultConfig()

	cfg.Storage.Dir = t.TempDir()
	cfg.Storage.Bucket = "photos"

	store, err := dir.New(dir.WithConfig(cfg))(nil, &gas.NopLogger{})
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

// The error messages the package documentation lists are the ones the
// store returns, and it lists every message this test makes.
//
//nolint:funlen,maintidx // one case per documented message.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	nop := &gas.NopLogger{}
	ctx := t.Context()
	opened := func() (*dir.Provider, string) { return open(t) }
	built := func() *dir.Provider {
		p, err := dir.New(dir.WithConfig(config(t.TempDir(), bucket)))(nil, nop)
		require.NoError(t, err)

		return p
	}
	initErr := func(opts ...dir.Option) error {
		p, err := dir.New(opts...)(nil, nop)
		require.NoError(t, err)

		return p.Init(ctx)
	}
	putErr := func(p *dir.Provider, in *storage.PutObjectInput) error {
		_, err := p.PutObject(ctx, in)

		return err
	}
	getErr := func(in *storage.GetObjectInput) error {
		p, _ := opened()
		require.NoError(t, putErr(p, putIn("k", "v")))

		_, err := p.GetObject(ctx, in)

		return err
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, root.Close()) })

	cases := map[string]func() error{
		"gas/storage/dir: logger is nil": func() error {
			_, err := dir.New()(nil, nil)

			return err
		},
		"gas/storage/dir: option is nil: at position 0": func() error {
			_, err := dir.New(nil)(nil, nop)

			return err
		},
		"gas/storage/dir: config is nil": func() error {
			_, err := dir.New(dir.WithConfig(nil))(nil, nop)

			return err
		},
		"gas/storage/dir: root is nil": func() error {
			_, err := dir.New(dir.WithRoot(nil))(nil, nop)

			return err
		},
		"gas/storage/dir: init: no config provider": func() error { return initErr() },
		"gas/storage/dir: init: invalid config: Storage.Bucket must be a valid bucket name": func() error {
			return initErr(dir.WithConfig(config(t.TempDir(), "Not_Valid")))
		},
		"gas/storage/dir: init: invalid config: Storage.Dir is required without WithRoot": func() error {
			return initErr(dir.WithConfig(config("", "")))
		},
		"gas/storage/dir: init: invalid config: Storage.Dir must be empty with WithRoot": func() error {
			return initErr(dir.WithRoot(root), dir.WithConfig(config(t.TempDir(), "")))
		},
		"gas/storage/dir: init: backend failed: open dir: no such file or directory": func() error {
			return initErr(dir.WithConfig(config(filepath.Join(t.TempDir(), "missing"), "")))
		},
		"gas/storage/dir: init: backend failed: the root holds buckets of an earlier layout": func() error {
			path := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(path, sha("old")), 0o750))

			return initErr(dir.WithConfig(config(path, "")))
		},
		"gas/storage/dir: init: service already initialized": func() error {
			p, _ := opened()

			return p.Init(ctx)
		},
		"gas/storage/dir: init: context canceled": func() error { return built().Init(canceled) },
		"gas/storage/dir: ready: service not initialized": func() error {
			return built().CheckReady(ctx)
		},
		"gas/storage/dir: ready: backend failed: stat layout marker: no such file or directory": func() error {
			p, path := opened()
			require.NoError(t, os.RemoveAll(path))

			return p.CheckReady(ctx)
		},
		"gas/storage/dir: put object: input is nil": func() error {
			p, _ := opened()

			return putErr(p, nil)
		},
		"gas/storage/dir: put object: service not initialized": func() error {
			return putErr(built(), putIn("k", "v"))
		},
		"gas/storage/dir: put object: service closed": func() error {
			p := built()
			require.NoError(t, p.Init(ctx))
			require.NoError(t, p.Close(ctx))

			return putErr(p, putIn("k", "v"))
		},
		"gas/storage/dir: put object: context canceled": func() error {
			p, _ := opened()

			_, err := p.PutObject(canceled, putIn("k", "v"))

			return err
		},
		"gas/storage/dir: put object: invalid key: empty": func() error {
			p, _ := opened()

			return putErr(p, putIn("", "v"))
		},
		"gas/storage/dir: put object: bucket not found": func() error {
			p, _ := opened()
			in := putIn("k", "v")

			in.Bucket = new("missing")

			return putErr(p, in)
		},
		"gas/storage/dir: put object: precondition failed": func() error {
			p, _ := opened()
			require.NoError(t, putErr(p, putIn("k", "v")))

			in := putIn("k", "v")

			in.IfNoneMatch = new("*")

			return putErr(p, in)
		},
		"gas/storage/dir: copy object: source: invalid key: empty": func() error {
			p, _ := opened()

			_, err := p.CopyObject(ctx, &storage.CopyObjectInput{})

			return err
		},
		"gas/storage/dir: copy object: invalid argument: copies an object onto itself without Replace": func() error { //nolint:lll // the documented text.
			p, _ := opened()

			_, err := p.CopyObject(ctx, &storage.CopyObjectInput{
				Key:    "k",
				Source: storage.BaseObjectInput{Key: "k"},
			})

			return err
		},
		"gas/storage/dir: put object: object not found": func() error {
			p, _ := opened()
			in := putIn("k", "v")

			in.IfMatch = new("abc")

			return putErr(p, in)
		},
		"gas/storage/dir: get object: object not found": func() error {
			return getErr(getIn("missing"))
		},
		"gas/storage/dir: get object: object not found: bucket not found": func() error {
			in := getIn("k")

			in.Bucket = new("missing")

			return getErr(in)
		},
		"gas/storage/dir: get object: not supported: the store keeps no versions": func() error {
			in := getIn("k")

			in.Version = new("v1")

			return getErr(in)
		},
		"gas/storage/dir: get object: not supported: the store keeps no parts": func() error {
			in := getIn("k")

			in.PartNumber = new(int32(1))

			return getErr(in)
		},
		"gas/storage/dir: get object: range not satisfiable": func() error {
			in := getIn("k")

			in.Range = new("bytes=5-")

			return getErr(in)
		},
		"gas/storage/dir: get object: backend failed: object cannot be read: decode meta file: syntax error at offset 1": func() error { //nolint:lll // the documented text.
			p, path := opened()
			require.NoError(t, putErr(p, putIn("k", "v")))
			require.NoError(t, os.WriteFile(metaFile(path, "k"), []byte("x"), 0o600))

			_, err := p.GetObject(ctx, getIn("k"))

			return err
		},
		"gas/storage/dir: get object: backend failed: object cannot be read: data file is gone": func() error {
			p, path := opened()
			require.NoError(t, putErr(p, putIn("k", "v")))

			for _, f := range dataFiles(t, path, "k") {
				require.NoError(t, os.Remove(f))
			}

			_, err := p.GetObject(ctx, getIn("k"))

			return err
		},
		"gas/storage/dir: delete bucket: bucket not empty": func() error {
			p, _ := opened()
			require.NoError(t, putErr(p, putIn("k", "v")))

			return p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: bucket})
		},
		"gas/storage/dir: delete bucket: backend failed: meta dir: file this backend did not write": func() error {
			p, path := opened()
			require.NoError(
				t,
				os.WriteFile(filepath.Join(path, sha(bucket), "notes.txt"), nil, 0o600),
			)

			return p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: bucket})
		},
		"gas/storage/dir: list objects: invalid continuation token: not one this backend issued for this listing": func() error { //nolint:lll // the documented text.
			p, _ := opened()

			_, err := p.ListObjects(
				ctx,
				&storage.ListObjectsInput{ContinuationToken: new("forged")},
			)

			return err
		},
		"gas/storage/dir: close: context deadline exceeded": func() error {
			p := built()
			require.NoError(t, p.Init(ctx))

			bodyCtx, stop := context.WithCancel(ctx)
			defer stop()

			body := newBlockingBody(bodyCtx)
			done := make(chan error, 1)

			go func() {
				in := putIn("k", "")

				in.Body = body

				done <- putErr(p, in)
			}()

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
		// Windows words this cause its own way, and the case of a removed
		// directory needs a removal that an open store prevents there.
		if runtime.GOOS == "windows" && strings.HasSuffix(text, "no such file or directory") {
			continue
		}

		require.EqualError(t, cases[text](), text)
	}

	assert.ElementsMatch(
		t,
		slices.Collect(maps.Keys(cases)),
		docLines(t, "../doc.go", "gas/storage/dir: "),
	)
}
