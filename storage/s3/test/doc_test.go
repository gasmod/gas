package s3_test

import (
	"context"
	"crypto/md5" //nolint:gosec // G501: the ETag the documentation states.
	"encoding/hex"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
	s3store "github.com/gasmod/gas/storage/s3"
)

// The example in the package documentation's Usage section, on the fake,
// which stands for S3.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	f := newFake(t)
	ctx := t.Context()

	cfg := s3store.DefaultConfig()

	cfg.Storage.Region = "eu-west-1"
	cfg.Storage.Bucket = "photos"
	cfg.Storage.Endpoint = f.srv.URL
	cfg.Storage.AccessKeyID, cfg.Storage.SecretAccessKey = testKeyID, testSecret

	store, err := s3store.New(s3store.WithConfig(cfg))(nil, newRecorder())
	require.NoError(t, err)
	require.NoError(t, store.Init(ctx))

	defer func() { require.NoError(t, store.Close(ctx)) }()

	require.NoError(t, store.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "photos"}))

	_, err = store.PutObject(ctx, &storage.PutObjectInput{
		Key:  "cats/tom.txt",
		Body: strings.NewReader("Tom"),
	})
	require.NoError(t, err)

	info, err := store.HeadObject(ctx, &storage.HeadObjectInput{
		Key: "cats/tom.txt",
	})
	require.NoError(t, err)

	sum := md5.Sum([]byte("Tom")) //nolint:gosec // G401: the ETag.

	assert.Equal(t, int64(3), info.Size)
	assert.Equal(t, "application/octet-stream", info.ContentType)
	assert.Equal(t, hex.EncodeToString(sum[:]), info.ETag)
}

// errDiskGone is the error of a body that fails.
var errDiskGone = errors.New("disk gone")

// awsError returns a hook that answers every request with an S3 error
// reply of status and code, without a body for a HEAD.
func awsError(status int, code string) func(http.ResponseWriter, *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		fakeError(w, r, status, code)

		return true
	}
}

// replying returns a store on a new fake whose hook answers every
// request, without retries, with the bucket `b-1` configured.
func replying(t *testing.T, hook func(http.ResponseWriter, *http.Request) bool) *s3store.Provider {
	t.Helper()

	f := newFake(t)
	cfg := f.config("b-1")

	cfg.Storage.MaxRetries = 0

	p := open(t, cfg)

	f.setHook(hook)

	return p
}

// getKey returns the error of a GetObject of key k of the configured
// bucket of p.
func getKey(ctx context.Context, t *testing.T, p *s3store.Provider) error {
	t.Helper()

	_, err := p.GetObject(ctx, &storage.GetObjectInput{
		Key: "k",
	})

	return err
}

// truncated answers a GetObject with a Content-Length of 10 and 3 bytes,
// then ends the connection.
func truncated(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}

	w.Header().Set("Content-Length", "10")
	w.WriteHeader(http.StatusOK)

	_, _ = io.WriteString(w, "abc")

	return true
}

// presignNoDate is a PresignAPI whose URLs have no X-Amz-Date.
type presignNoDate struct{}

// PresignPutObject returns a URL without a signing time.
func (presignNoDate) PresignPutObject(
	context.Context,
	*awss3.PutObjectInput,
	...func(*awss3.PresignOptions),
) (*v4.PresignedHTTPRequest, error) {
	return &v4.PresignedHTTPRequest{URL: "https://s3.example/k", Method: http.MethodPut}, nil
}

// PresignGetObject returns a URL without a signing time.
func (presignNoDate) PresignGetObject(
	context.Context,
	*awss3.GetObjectInput,
	...func(*awss3.PresignOptions),
) (*v4.PresignedHTTPRequest, error) {
	return &v4.PresignedHTTPRequest{URL: "https://s3.example/k", Method: http.MethodGet}, nil
}

// The error messages the package documentation lists are the ones the
// store returns, and it lists every message this test makes.
//
// Not parallel: it sets AWS variables.
//
//nolint:funlen,maintidx,contextcheck // one case per documented message, made with the test helpers.
func TestDoc_Errors(t *testing.T) {
	isolateAWS(t)

	f := newFake(t)
	ctx := t.Context()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	rule := func(edit func(*s3store.Settings)) error {
		cfg := f.config("")
		edit(&cfg.Storage)

		return initErr(t, s3store.WithConfig(cfg))
	}
	built := func() *s3store.Provider {
		p, _ := build(t, s3store.WithConfig(f.config("b-1")))

		return p
	}
	seq := 0
	initial := func() *s3store.Provider {
		seq++

		return f.withBucket(t, "b-doc-"+strconv.Itoa(seq))
	}
	hole := "http://" + blackhole(t)
	noRetries := func(endpoint string) *s3store.Provider {
		cfg := f.config("b-1")

		cfg.Storage.Endpoint, cfg.Storage.MaxRetries = endpoint, 0

		return open(t, cfg)
	}
	put := func(p *s3store.Provider, in *storage.PutObjectInput) error {
		_, err := p.PutObject(ctx, in)

		return err
	}
	key := storage.BaseObjectInput{Key: "k"}

	cases := map[string]func() error{
		"gas/storage/s3: logger is nil": func() error {
			_, err := s3store.New()(nil, nil)

			return err
		},
		"gas/storage/s3: option is nil: at position 0": func() error {
			_, err := s3store.New(nil)(nil, newRecorder())

			return err
		},
		"gas/storage/s3: config is nil": func() error {
			_, err := s3store.New(s3store.WithConfig(nil))(nil, newRecorder())

			return err
		},
		"gas/storage/s3: init: no config provider": func() error {
			return initErr(t)
		},
		"gas/storage/s3: init: invalid config: Storage.Endpoint must be an http or https URL " +
			"with a host and nothing more but /": func() error {
			return rule(func(s *s3store.Settings) { s.Endpoint = "localhost:4566" })
		},
		"gas/storage/s3: init: invalid config: Storage.Bucket must be a valid bucket name": func() error {
			return rule(func(s *s3store.Settings) { s.Bucket = "Not_Valid" })
		},
		"gas/storage/s3: init: invalid config: AWS configuration failed to load": func() error {
			t.Setenv("AWS_PROFILE", "missing")
			defer t.Setenv("AWS_PROFILE", "")

			return rule(func(*s3store.Settings) {})
		},
		"gas/storage/s3: init: no region": func() error {
			return rule(func(s *s3store.Settings) { s.Region = "" })
		},
		"gas/storage/s3: init: no credentials: failed": func() error {
			return rule(func(s *s3store.Settings) { s.AccessKeyID = "" })
		},
		"gas/storage/s3: init: no credentials: timed out": func() error {
			t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", hole+"/creds")
			defer t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "")

			return rule(func(s *s3store.Settings) {
				s.AccessKeyID, s.OpTimeout = "", 200*time.Millisecond
			})
		},
		"gas/storage/s3: init: no credentials: timed out: context deadline exceeded": func() error {
			t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", hole+"/creds")
			defer t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "")

			cfg := f.config("")

			cfg.Storage.AccessKeyID = ""

			p, _ := build(t, s3store.WithConfig(cfg))

			initCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
			defer cancel()

			return p.Init(initCtx)
		},
		"gas/storage/s3: init: client is nil": func() error {
			return initErr(t, s3store.WithConfig(s3store.DefaultConfig()), s3store.WithClient(nil))
		},
		"gas/storage/s3: init: service already initialized": func() error {
			return initial().Init(ctx)
		},
		"gas/storage/s3: init: context canceled": func() error {
			return built().Init(canceled)
		},
		"gas/storage/s3: ready: service not initialized": func() error {
			return built().CheckReady(ctx)
		},
		"gas/storage/s3: ready: bucket not found": func() error {
			return open(t, f.config("missing")).CheckReady(ctx)
		},
		"gas/storage/s3: create bucket: bucket exists": func() error {
			return initial().CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "foreign"})
		},
		"gas/storage/s3: delete bucket: bucket not empty": func() error {
			p := f.withBucket(t, "b-full")
			putString(t, p, "k", "v")

			return p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: "b-full"})
		},
		"gas/storage/s3: list buckets: invalid continuation token: not one this backend " +
			"issued for this listing": func() error {
			_, err := initial().ListBuckets(ctx, &storage.ListBucketsInput{ContinuationToken: new("AAAA")})

			return err
		},
		"gas/storage/s3: get object: input is nil": func() error {
			_, err := initial().GetObject(ctx, nil)

			return err
		},
		"gas/storage/s3: get object: service not initialized": func() error {
			return getKey(ctx, t, built())
		},
		"gas/storage/s3: get object: service closed": func() error {
			p := initial()
			require.NoError(t, p.Close(ctx))

			return getKey(ctx, t, p)
		},
		"gas/storage/s3: get object: context canceled": func() error {
			return getKey(canceled, t, initial())
		},
		"gas/storage/s3: get object: context deadline exceeded": func() error {
			dl, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
			defer cancel()

			return getKey(dl, t, noRetries(hole))
		},
		"gas/storage/s3: get object: invalid key: empty": func() error {
			_, err := initial().GetObject(ctx, &storage.GetObjectInput{})

			return err
		},
		"gas/storage/s3: get object: object not found": func() error {
			return getKey(ctx, t, initial())
		},
		"gas/storage/s3: get object: object not found: bucket not found": func() error {
			return getKey(ctx, t, open(t, f.config("missing")))
		},
		"gas/storage/s3: get object: object not found: version not found": func() error {
			p := initial()
			putString(t, p, "k", "v")

			_, err := p.GetObject(
				ctx,
				&storage.GetObjectInput{
					BaseObjectInput: key,
					Version:         new("v0"),
				},
			)

			return err
		},
		"gas/storage/s3: get object: range not satisfiable": func() error {
			p := initial()
			putString(t, p, "k", "v")

			_, err := p.GetObject(
				ctx,
				&storage.GetObjectInput{
					BaseObjectInput: key,
					Range:           new("bytes=5-"),
				},
			)

			return err
		},
		"gas/storage/s3: get object: access denied (InvalidAccessKeyId)": func() error {
			cfg := f.config("b-1")

			cfg.Storage.AccessKeyID = "AKIDWRONG"

			return getKey(ctx, t, open(t, cfg))
		},
		"gas/storage/s3: get object: throttled (SlowDown)": func() error {
			return getKey(ctx, t, replying(t, awsError(http.StatusServiceUnavailable, "SlowDown")))
		},
		"gas/storage/s3: get object: wrong region (PermanentRedirect)": func() error {
			return getKey(
				ctx,
				t,
				replying(t, awsError(http.StatusMovedPermanently, "PermanentRedirect")),
			)
		},
		"gas/storage/s3: get object: failed (InternalError)": func() error {
			return getKey(
				ctx,
				t,
				replying(t, awsError(http.StatusInternalServerError, "InternalError")),
			)
		},
		"gas/storage/s3: get object: failed": func() error {
			return getKey(ctx, t, replying(t, awsError(http.StatusBadRequest, "zqxUnknownCode")))
		},
		"gas/storage/s3: get object: timed out": func() error {
			cfg := f.config("b-1")

			cfg.Storage.Endpoint, cfg.Storage.OpTimeout = hole, 200*time.Millisecond

			return getKey(ctx, t, open(t, cfg))
		},
		"gas/storage/s3: get object: connection refused": func() error {
			return getKey(ctx, t, noRetries("http://"+closedAddr(t)))
		},
		"gas/storage/s3: get object: unreachable": func() error {
			return getKey(ctx, t, noRetries("http://gas-s3-test.invalid"))
		},
		"gas/storage/s3: get object: read body: unexpected EOF": func() error {
			obj, err := replying(t, truncated).GetObject(ctx, &storage.GetObjectInput{
				BaseObjectInput: key,
			})
			require.NoError(t, err)

			defer func() { require.NoError(t, obj.Body.Close()) }()

			_, err = io.ReadAll(obj.Body)

			return err
		},
		"gas/storage/s3: get object: read body: context canceled": func() error {
			p := initial()
			putString(t, p, "k", strings.Repeat("v", 1<<20))

			getCtx, cancel := context.WithCancel(ctx)

			obj, err := p.GetObject(getCtx, &storage.GetObjectInput{
				BaseObjectInput: key,
			})
			require.NoError(t, err)

			defer func() { require.NoError(t, obj.Body.Close()) }()

			cancel()

			_, err = io.ReadAll(obj.Body)

			return err
		},
		"gas/storage/s3: list objects: invalid reply": func() error {
			p := replying(t, rawReply(http.StatusOK, []byte(
				`<ListBucketResult><IsTruncated>true</IsTruncated></ListBucketResult>`)))

			_, err := p.ListObjects(ctx, &storage.ListObjectsInput{})

			return err
		},
		"gas/storage/s3: put object: content length mismatch: body has 3 bytes, ContentLength is 5": func() error {
			return put(initial(), &storage.PutObjectInput{
				BaseObjectInput: key,
				Body:            strings.NewReader("abc"),
				ContentLength:   new(int64(5)),
			})
		},
		"gas/storage/s3: put object: read body: disk gone": func() error {
			return put(initial(), &storage.PutObjectInput{
				BaseObjectInput: key,
				Body:            iotest.ErrReader(errDiskGone),
			})
		},
		"gas/storage/s3: put object: precondition failed": func() error {
			p := initial()
			putString(t, p, "k", "v")

			return put(p, &storage.PutObjectInput{
				BaseObjectInput: key,
				IfNoneMatch:     new("*"),
			})
		},
		"gas/storage/s3: put object: conflict (ConditionalRequestConflict)": func() error {
			p := replying(t, awsError(http.StatusConflict, "ConditionalRequestConflict"))

			return put(p, &storage.PutObjectInput{BaseObjectInput: key})
		},
		"gas/storage/s3: put object: context canceled; upload not aborted: failed (InternalError)": func() error {
			return canceledUpload(t, f)
		},
		"gas/storage/s3: close: context deadline exceeded": func() error {
			cfg := f.config("b-1")

			cfg.Storage.Endpoint, cfg.Storage.OpTimeout = hole, time.Minute

			p := open(t, cfg)
			got := make(chan error, 1)

			go func() { got <- getKey(context.Background(), t, p) }()

			time.Sleep(100 * time.Millisecond)

			closeCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()

			err := p.Close(closeCtx)

			require.ErrorIs(t, <-got, storage.ErrClosed)

			return err
		},
		"gas/storage/s3/presigner: presign upload: invalid argument: ContentLength is required": func() error {
			_, err := openSigner(t, f.config("b-1")).PresignUploadURL(ctx,
				&storage.PresignUploadURLInput{BaseObjectInput: key})

			return err
		},
		"gas/storage/s3/presigner: presign upload: invalid reply": func() error {
			p, _ := buildSigner(t, s3store.WithPresignerConfig(s3store.DefaultConfig()),
				s3store.WithPresignClient(presignNoDate{}))
			require.NoError(t, p.Init(ctx))

			_, err := p.PresignUploadURL(ctx, &storage.PresignUploadURLInput{
				Bucket: new("b-1"), Key: "k",
				ContentLength: new(int64(1)),
			})

			return err
		},
		"gas/storage/s3/presigner: presign download: service closed": func() error {
			p := openSigner(t, f.config("b-1"))
			require.NoError(t, p.Close(ctx))

			_, err := p.PresignDownloadURL(ctx, &storage.PresignDownloadURLInput{
				BaseObjectInput: key,
			})

			return err
		},
	}

	for _, text := range slices.Sorted(maps.Keys(cases)) {
		require.EqualError(t, cases[text](), text, text)
	}

	documented := docLines(t, "../doc.go", "gas/storage/s3")
	assert.ElementsMatch(t, slices.Collect(maps.Keys(cases)), documented)

	cfg := f.config("")

	cfg.Storage.OpTimeout = 0

	err := initErr(t, s3store.WithConfig(cfg))
	require.EqualError(t, err, "gas/storage/s3: init: invalid config: validate config: "+
		docLines(t, "../doc.go", "Key: ")[0])
}

// canceledUpload starts a PutObject in parts on f whose context it cancels
// once a part is sent, with every abort failing, and returns its error.
func canceledUpload(t *testing.T, f *fakeS3) error {
	t.Helper()

	p := f.withBucket(t, "b-up")
	ctx, cancel := context.WithCancel(t.Context())

	defer cancel()

	f.setHook(func(w http.ResponseWriter, r *http.Request) bool {
		q := r.URL.Query()

		switch {
		case r.Method == http.MethodDelete && q.Has("uploadId"):
			fakeError(w, r, http.StatusInternalServerError, "InternalError")

			return true
		case r.Method == http.MethodPut && q.Has("partNumber"):
			cancel()
		}

		return false
	})
	defer f.setHook(nil)

	_, err := p.PutObject(ctx, &storage.PutObjectInput{
		Key:  "big",
		Body: &endless{},
	})

	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, s3store.ErrUploadNotAborted)
	require.ErrorIs(t, err, storage.ErrBackend)

	return err
}

// endless is a body that never ends.
type endless struct{}

// Read fills p with `x`.
func (*endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}

	return len(p), nil
}
