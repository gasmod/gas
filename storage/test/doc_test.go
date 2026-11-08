package storage_test

import (
	"bufio"
	"bytes"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal/listing"
	"github.com/gasmod/gas/storage/memory"
)

// The example in the package documentation's Usage section, on the
// memory store.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	store, err := memory.New(memory.WithConfig(memory.DefaultConfig()))(nil, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, store.Init(ctx))

	defer func() { require.NoError(t, store.Close(ctx)) }()

	bucket, contentType := "photos", "image/jpeg"
	jpeg := []byte("\xff\xd8\xff\xe0 not really a JPEG")
	size := int64(len(jpeg))

	err = store.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "photos"})
	require.NoError(t, err)

	_, err = store.PutObject(ctx, &storage.PutObjectInput{
		Bucket:        &bucket,
		Key:           "cats/tom.jpg",
		Body:          bytes.NewReader(jpeg),
		ContentLength: &size,
		ContentType:   &contentType,
		Metadata:      map[string]string{"Author": "Ann"},
	})
	require.NoError(t, err)

	obj, err := store.GetObject(ctx, &storage.GetObjectInput{Bucket: &bucket, Key: "cats/tom.jpg"})
	require.NoError(t, err)

	defer func() { require.NoError(t, obj.Body.Close()) }()

	got, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	assert.Equal(t, jpeg, got)
	assert.Equal(t, "image/jpeg", obj.ContentType)
	assert.Equal(t, map[string]string{"author": "Ann"}, obj.Metadata)
}

// The example of the package documentation's Copies section copies an
// object of the configured bucket to another bucket, with its header
// fields and metadata.
func TestDoc_Copy(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	cfg := memory.DefaultConfig()

	cfg.Storage.Bucket = "photos"

	store, err := memory.New(memory.WithConfig(cfg))(nil, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, store.Init(ctx))

	defer func() { require.NoError(t, store.Close(ctx)) }()

	archive := "archive"

	require.NoError(t, store.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: archive}))

	_, err = store.PutObject(ctx, &storage.PutObjectInput{
		Key:         "cats/tom.jpg",
		Body:        strings.NewReader("Tom"),
		ContentType: new("image/jpeg"),
		Metadata:    map[string]string{"Author": "Ann"},
	})
	require.NoError(t, err)

	_, err = store.CopyObject(ctx, &storage.CopyObjectInput{
		Bucket: &archive,
		Key:    "cats/tom.jpg",
		Source: storage.BaseObjectInput{Key: "cats/tom.jpg"},
	})
	require.NoError(t, err)

	info, err := store.HeadObject(
		ctx,
		&storage.HeadObjectInput{Bucket: &archive, Key: "cats/tom.jpg"},
	)
	require.NoError(t, err)
	assert.Equal(t, int64(3), info.Size)
	assert.Equal(t, "image/jpeg", info.ContentType)
	assert.Equal(t, map[string]string{"author": "Ann"}, info.Metadata)
}

// The listing loop of the package documentation's Listing section lists
// every key under the prefix once.
func TestDoc_Listing(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	cfg := memory.DefaultConfig()

	cfg.Storage.Bucket = "photos"

	store, err := memory.New(memory.WithConfig(cfg))(nil, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, store.Init(ctx))

	defer func() { require.NoError(t, store.Close(ctx)) }()

	for _, k := range []string{"cats/a", "cats/b", "cats/c", "dogs/d"} {
		_, err = store.PutObject(ctx, &storage.PutObjectInput{Key: k})
		require.NoError(t, err)
	}

	prefix := "cats/"
	in := &storage.ListObjectsInput{Prefix: &prefix, MaxKeys: new(int32(2))}

	var keys []string

	for {
		out, err := store.ListObjects(ctx, in)
		require.NoError(t, err)

		for _, o := range out.Objects {
			keys = append(keys, o.Key)
		}

		if !out.IsTruncated {
			break
		}

		in.ContinuationToken = &out.NextContinuationToken
	}

	assert.Equal(t, []string{"cats/a", "cats/b", "cats/c"}, keys)
}

// errorLines returns the code lines of the Errors section of the package
// documentation in file.
func errorLines(t *testing.T, file string) []string {
	t.Helper()

	f, err := os.Open(file) //nolint:gosec // G304: the package's own doc.go.
	require.NoError(t, err)

	defer func() { require.NoError(t, f.Close()) }()

	var (
		out    []string
		inside bool
	)

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "// # Errors" {
			inside = true
		}

		code, ok := strings.CutPrefix(line, "//\t")
		if inside && ok {
			out = append(out, code)
		}
	}

	require.NoError(t, sc.Err())

	return out
}

// zeroReader reads no byte and no error, forever.
type zeroReader struct{}

// Read returns 0 and nil.
func (zeroReader) Read([]byte) (int, error) { return 0, nil }

// The error messages the package documentation lists are the ones the
// checks return, and it lists every message this test makes.
//
//nolint:funlen // one case per documented message.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	put := func(edit func(*storage.PutObjectInput)) func() error {
		return func() error {
			in := &storage.PutObjectInput{Key: "k"}
			edit(in)

			_, err := storage.CheckPut(in, "photos")

			return err
		}
	}
	read := func(edit func(*storage.HeadObjectInput)) func() error {
		return func() error {
			in := &storage.HeadObjectInput{Key: "k"}
			edit(in)

			_, err := storage.CheckRead(in, "photos")

			return err
		}
	}
	upload := func(edit func(*storage.PresignUploadURLInput)) func() error {
		return func() error {
			in := &storage.PresignUploadURLInput{Key: "k", ContentLength: new(int64(1))}
			edit(in)

			_, err := storage.CheckPresignUpload(in, "photos")

			return err
		}
	}
	copyIn := func(edit func(*storage.CopyObjectInput)) func() error {
		return func() error {
			in := &storage.CopyObjectInput{
				Key:    "dst",
				Source: storage.BaseObjectInput{Key: "src"},
			}
			edit(in)

			_, err := storage.CheckCopy(in, "photos")

			return err
		}
	}
	md := func(m map[string]string) func() error {
		return put(func(in *storage.PutObjectInput) { in.Metadata = m })
	}
	cond := func(c storage.WriteCondition) func() error {
		return put(func(in *storage.PutObjectInput) { in.WriteCondition = c })
	}
	body := func(r io.Reader, n int64) func() error {
		return func() error {
			l, err := storage.CheckLength(r, &n)
			require.NoError(t, err)

			_, err = io.ReadAll(l)

			return err
		}
	}
	bucket := func(name string) func() error { return func() error { return storage.CheckBucket(name) } }
	key := func(k string) func() error { return func() error { return storage.CheckKey(k) } }
	codec, err := listing.NewCodec()
	require.NoError(t, err)

	cases := map[string]func() error{
		"invalid key: empty": key(""),
		"invalid key: longer than 1024 bytes": key(
			strings.Repeat("k", 1025),
		),
		"invalid key: not valid UTF-8":                                    key("\xff"),
		"invalid key: holds a control character":                          key("a\nb"),
		`invalid key: its last path element is "."`:                       key("a/."),
		"invalid bucket: not 3 to 63 bytes":                               bucket("ab"),
		"invalid bucket: holds two dots in a row":                         bucket("a..b"),
		"invalid bucket: shaped like an IPv4 address":                     bucket("10.0.0.1"),
		"invalid bucket: holds a character other than a-z, 0-9, . and -":  bucket("a_b"),
		"invalid bucket: does not start and end with a letter or a digit": bucket("ab-"),
		"invalid bucket: none given and none configured": func() error {
			_, err := storage.ResolveBucket(nil, "")

			return err
		},
		"invalid argument: ContentLength is negative": func() error {
			_, err := storage.CheckLength(nil, new(int64(-1)))

			return err
		},
		"invalid argument: ContentLength is required": upload(
			func(in *storage.PresignUploadURLInput) { in.ContentLength = nil }),
		"invalid argument: ContentLength is not 0 to 5368709120": upload(
			func(in *storage.PresignUploadURLInput) { in.ContentLength = new(int64(-1)) }),
		"invalid header: ContentType is empty": put(
			func(in *storage.PutObjectInput) { in.ContentType = new("") }),
		"invalid header: CacheControl holds a character other than printable ASCII, space and tab": put(
			func(in *storage.PutObjectInput) { in.CacheControl = new("a\r\nb") },
		),
		"invalid header: ContentLanguage starts or ends with a space or a tab": put(
			func(in *storage.PutObjectInput) { in.ContentLanguage = new("en ") }),
		"invalid header: ContentDisposition is longer than 1024 bytes": put(
			func(in *storage.PutObjectInput) {
				in.ContentDisposition = new(strings.Repeat("a", 1025))
			}),
		"invalid header: Expires is not an IMF-fixdate": put(
			func(in *storage.PutObjectInput) { in.Expires = new("tomorrow") }),
		"invalid header: ResponseContentDisposition holds a character other than printable ASCII, space and tab": func() error { //nolint:lll // the documented text.
			_, err := storage.CheckPresignDownload(&storage.PresignDownloadURLInput{
				Key:                        "k",
				ResponseContentDisposition: new("é"),
			}, "photos")

			return err
		},
		"invalid metadata: a name is not an HTTP token": md(map[string]string{"a b": "v"}),
		"invalid metadata: a value is empty":            md(map[string]string{"n": ""}),
		"invalid metadata: a value holds a character other than printable ASCII, space and tab": md(
			map[string]string{"n": "\x00"}),
		"invalid metadata: a value starts or ends with a space or a tab": md(
			map[string]string{"n": "v\t"},
		),
		"invalid metadata: two names differ only in case": md(
			map[string]string{"A": "v", "a": "v"},
		),
		"invalid metadata: 2049 bytes, more than 2048": md(
			map[string]string{"n": strings.Repeat("v", 2048)},
		),
		"invalid condition: IfMatch and IfNoneMatch are both set": cond(
			storage.WriteCondition{IfMatch: new("e"), IfNoneMatch: new("*")}),
		`invalid condition: IfNoneMatch is not "*"`: cond(
			storage.WriteCondition{IfNoneMatch: new("e")},
		),
		"invalid condition: IfMatch is not one entity tag": cond(
			storage.WriteCondition{IfMatch: new("*")},
		),
		"invalid range: not one byte range": read(
			func(in *storage.HeadObjectInput) { in.Range = new("bytes=2-1") },
		),
		"invalid argument: PartNumber is not 1 to 10000": read(
			func(in *storage.HeadObjectInput) { in.PartNumber = new(int32(0)) }),
		"invalid argument: PartNumber and Range are both set": read(
			func(in *storage.HeadObjectInput) {
				in.PartNumber, in.Range = new(int32(1)), new("bytes=0-0")
			},
		),
		"invalid argument: Version is empty": read(
			func(in *storage.HeadObjectInput) { in.Version = new("") },
		),
		"invalid argument: MaxKeys is not 1 to 1000": func() error {
			_, err := storage.CheckListObjects(
				&storage.ListObjectsInput{MaxKeys: new(int32(0))},
				"photos",
			)

			return err
		},
		"invalid argument: MaxBuckets is not 1 to 10000": func() error {
			_, err := storage.CheckListBuckets(&storage.ListBucketsInput{MaxBuckets: new(int32(0))})

			return err
		},
		"invalid argument: Prefix is not valid UTF-8 of at most 1024 bytes": func() error {
			_, err := storage.CheckListObjects(
				&storage.ListObjectsInput{Prefix: new("\xff")},
				"photos",
			)

			return err
		},
		"invalid argument: Delimiter is empty": func() error {
			_, err := storage.CheckListObjects(
				&storage.ListObjectsInput{Delimiter: new("")},
				"photos",
			)

			return err
		},
		"invalid argument: TTL is not 1s to 168h0m0s": func() error {
			_, err := storage.CheckPresignTTL(new(time.Millisecond))

			return err
		},
		"source: invalid key: empty": copyIn(
			func(in *storage.CopyObjectInput) { in.Source.Key = "" }),
		"destination: invalid bucket: none given and none configured": func() error {
			_, err := storage.CheckCopy(&storage.CopyObjectInput{
				Key:    "k",
				Source: storage.BaseObjectInput{Bucket: new("photos"), Key: "k"},
			}, "")

			return err
		},
		"invalid argument: header fields or metadata set without Replace": copyIn(
			func(in *storage.CopyObjectInput) { in.ContentType = new("text/plain") }),
		"invalid argument: copies an object onto itself without Replace": copyIn(
			func(in *storage.CopyObjectInput) { in.Key = "src" }),
		"invalid argument: source is longer than 5368709120 bytes": func() error {
			return storage.CheckCopySize(storage.MaxCopySize + 1)
		},
		"invalid continuation token: empty": func() error {
			_, err := storage.CheckListBuckets(
				&storage.ListBucketsInput{ContinuationToken: new("")},
			)

			return err
		},
		"content length mismatch: body has 3 bytes, ContentLength is 5": body(
			strings.NewReader("abc"),
			5,
		),
		"content length mismatch: body is longer than ContentLength 5": body(
			strings.NewReader("abcdef"),
			5,
		),
		"read body: Read returned an invalid count":              body(badCount{}, 5),
		"read body: multiple Read calls return no data or error": body(zeroReader{}, 0),
		"range not satisfiable": func() error {
			r, err := storage.ParseRange("bytes=5-")
			require.NoError(t, err)

			_, _, err = r.Resolve(5)

			return err
		},
		"invalid continuation token: not one this backend issued for this listing": func() error {
			_, err := codec.Open(listing.Scope("photos"), "forged")

			return err
		},
	}

	for _, text := range slices.Sorted(maps.Keys(cases)) {
		require.EqualError(t, cases[text](), text)
	}

	assert.ElementsMatch(t, slices.Collect(maps.Keys(cases)), errorLines(t, "../doc.go"))
}
