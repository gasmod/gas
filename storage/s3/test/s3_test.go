package s3_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/storage"
	s3store "github.com/gasmod/gas/storage/s3"
)

// stubAPI is an API whose requests a test sets; a request it does not set
// panics.
type stubAPI struct {
	s3store.API

	// head answers HeadObject.
	head func(context.Context) (*awss3.HeadObjectOutput, error)
	// get answers GetObject.
	get func(context.Context) (*awss3.GetObjectOutput, error)
	// put answers PutObject.
	put func(context.Context) (*awss3.PutObjectOutput, error)
	// create answers CreateBucket, given its input.
	create func(*awss3.CreateBucketInput) (*awss3.CreateBucketOutput, error)
}

// HeadObject calls head.
func (s *stubAPI) HeadObject(
	ctx context.Context,
	_ *awss3.HeadObjectInput,
	_ ...func(*awss3.Options),
) (*awss3.HeadObjectOutput, error) {
	return s.head(ctx)
}

// GetObject calls get.
func (s *stubAPI) GetObject(
	ctx context.Context,
	_ *awss3.GetObjectInput,
	_ ...func(*awss3.Options),
) (*awss3.GetObjectOutput, error) {
	return s.get(ctx)
}

// PutObject calls put.
func (s *stubAPI) PutObject(
	ctx context.Context,
	_ *awss3.PutObjectInput,
	_ ...func(*awss3.Options),
) (*awss3.PutObjectOutput, error) {
	return s.put(ctx)
}

// CreateBucket calls create.
func (s *stubAPI) CreateBucket(
	_ context.Context,
	in *awss3.CreateBucketInput,
	_ ...func(*awss3.Options),
) (*awss3.CreateBucketOutput, error) {
	return s.create(in)
}

// stubbed returns a store on api, with cfg, initialized.
func stubbed(t *testing.T, api s3store.API, cfg *s3store.Config) *s3store.Provider {
	t.Helper()

	p, _ := build(t, s3store.WithConfig(cfg), s3store.WithClient(api))
	require.NoError(t, p.Init(t.Context()))

	return p
}

// keyK is the BaseObjectInput of the key `k` in the configured bucket.
var keyK = storage.BaseObjectInput{Key: "k"}

// The presigner's constructor refuses a nil logger, a nil option and a
// nil configuration, as the store's does.
func TestNewPresigner_Errors(t *testing.T) {
	t.Parallel()

	_, err := s3store.NewPresigner()(nil, nil)
	require.ErrorIs(t, err, storage.ErrNilLogger)
	require.EqualError(t, err, "gas/storage/s3/presigner: logger is nil")

	_, err = s3store.NewPresigner(nil)(nil, newRecorder())
	require.ErrorIs(t, err, storage.ErrNilOption)
	require.EqualError(t, err, "gas/storage/s3/presigner: option is nil: at position 0")

	_, err = s3store.NewPresigner(s3store.WithPresignerConfig(nil))(nil, newRecorder())
	require.ErrorIs(t, err, storage.ErrNilConfig)
}

// Name returns the service names, and a nil store or presigner returns
// ErrNotInitialized from every method but Close and Name, and nil from
// its accessors.
func TestNil(t *testing.T) {
	t.Parallel()

	var (
		p  *s3store.Provider
		sg *s3store.Presigner
	)

	ctx := t.Context()

	assert.Equal(t, s3store.ServiceName, p.Name())
	assert.Equal(t, s3store.PresignerName, sg.Name())

	require.ErrorIs(t, p.Init(ctx), storage.ErrNotInitialized)
	require.ErrorIs(t, sg.Init(ctx), storage.ErrNotInitialized)
	require.ErrorIs(t, p.CheckReady(ctx), storage.ErrNotInitialized)
	require.EqualError(
		t,
		sg.CheckReady(ctx),
		"gas/storage/s3/presigner: ready: service not initialized",
	)
	require.NoError(t, p.Close(ctx))
	require.NoError(t, sg.Close(ctx))
	assert.Nil(t, p.Client())
	assert.Nil(t, sg.PresignClient())

	_, err := p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: keyK})
	require.EqualError(t, err, "gas/storage/s3: head object: service not initialized")

	_, err = sg.PresignUploadURL(ctx, &storage.PresignUploadURLInput{BaseObjectInput: keyK})
	require.ErrorIs(t, err, storage.ErrNotInitialized)
}

// Init logs what it resolved and Close that it closed, and neither logs a
// bucket name, the endpoint or a credential.
func TestInit_Logs(t *testing.T) {
	t.Parallel()

	cfg := newFake(t).config("zqx-bucket")

	cfg.Storage.SessionToken = "zqxtoken"

	p, log := build(t, s3store.WithConfig(cfg))
	require.NoError(t, p.Init(t.Context()))
	require.NoError(t, p.Close(t.Context()))

	entries := log.entries()
	require.Len(t, entries, 2)
	assert.Equal(t, "service initialized", entries[0].msg)
	assert.Equal(t, map[string]any{
		"service":     s3store.ServiceName,
		"region":      testRegion,
		"endpoint":    true,
		"path_style":  true,
		"bucket":      true,
		"client":      false,
		"credentials": "static",
		"op_timeout":  s3store.DefaultOpTimeout,
		"max_retries": s3store.DefaultMaxRetries,
	}, entries[0].fields)
	assert.Equal(t, "service closed", entries[1].msg)
	assertNoSecrets(t, log.text(), testSecret)

	sg, slog := buildSigner(t, s3store.WithPresignerConfig(cfg))
	require.NoError(t, sg.Init(t.Context()))
	require.NoError(t, sg.CheckReady(t.Context()))
	assert.NotNil(t, sg.PresignClient())
	require.NoError(t, sg.Close(t.Context()))
	require.ErrorIs(t, sg.CheckReady(t.Context()), storage.ErrClosed)
	assert.Equal(t, s3store.PresignerName, slog.entries()[0].fields["service"])
	assertNoSecrets(t, slog.text(), testSecret)
}

// Without WithConfig, Init binds the `storage` section from the
// ConfigProvider over DefaultConfig.
func TestInit_Binds(t *testing.T) {
	t.Parallel()

	f := newFake(t)

	cp, err := configtest.NewMockConfigWithValues(map[string]any{
		"storage": map[string]any{
			"region":          testRegion,
			"endpoint":        f.srv.URL,
			"usepathstyle":    false,
			"bucket":          "b-1",
			"accesskeyid":     testKeyID,
			"secretaccesskey": testSecret,
			"maxretries":      1,
		},
	})
	require.NoError(t, err)

	p, err := s3store.New()(cp, newRecorder())
	require.NoError(t, err)
	require.NoError(t, p.Init(t.Context()))

	defer func() { require.NoError(t, p.Close(t.Context())) }()

	assert.False(t, p.Client().Options().UsePathStyle)
	assert.Equal(t, testRegion, p.Client().Options().Region)

	sg, err := s3store.NewPresigner()(cp, newRecorder())
	require.NoError(t, err)
	require.NoError(t, sg.Init(t.Context()))
	require.NoError(t, sg.Close(t.Context()))
}

// CreateBucket sends a LocationConstraint of the region of the client:
// the one Init built, an *s3.Client given, or else the Region setting;
// none in us-east-1.
//
//nolint:funlen // the regions of a built client, an *s3.Client given and another API.
func TestCreateBucket_Region(t *testing.T) {
	t.Parallel()

	f := newFake(t)

	var (
		mu     sync.Mutex
		bodies []string
	)

	f.setHook(func(_ http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPut && strings.Count(r.URL.Path, "/") == 1 {
			b, err := io.ReadAll(r.Body)
			assert.NoError(t, err)

			mu.Lock()

			bodies = append(bodies, string(b))
			mu.Unlock()

			r.Body = io.NopCloser(strings.NewReader(string(b)))
		}

		return false
	})

	for i, region := range []string{"eu-west-1", testRegion} {
		cfg := f.config("")

		cfg.Storage.Region = region

		require.NoError(t, open(t, cfg).CreateBucket(t.Context(),
			&storage.CreateBucketInput{Bucket: "b-" + region}))

		mu.Lock()
		assert.Equal(
			t,
			region == "eu-west-1",
			strings.Contains(
				bodies[i],
				"<LocationConstraint>eu-west-1</LocationConstraint>",
			),
			region,
		)
		mu.Unlock()
	}

	client := awss3.New(awss3.Options{Region: "ap-south-1"})
	given := open(t, f.config(""))

	require.NoError(t, given.Close(t.Context()))

	p, _ := build(t, s3store.WithConfig(f.config("")), s3store.WithClient(client))
	require.NoError(t, p.Init(t.Context()))
	assert.Same(t, client, p.Client())

	cfg := s3store.DefaultConfig()

	cfg.Storage.Region = "eu-central-1"

	var sent *awss3.CreateBucketInput

	stub := stubbed(
		t,
		&stubAPI{create: func(in *awss3.CreateBucketInput) (*awss3.CreateBucketOutput, error) {
			sent = in

			return &awss3.CreateBucketOutput{}, nil
		}},
		cfg,
	)
	require.NoError(t, stub.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: "b-1"}))
	assert.Equal(t, "eu-central-1", string(sent.CreateBucketConfiguration.LocationConstraint))
	assert.Nil(t, stub.Client())
}

// HeadBucket returns the region S3 reports; a version and a part number
// are sent to S3, which keeps both.
func TestObjects_VersionsAndParts(t *testing.T) {
	t.Parallel()

	f := newFake(t)
	p := f.withBucket(t, "b-1")

	b, err := p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: "b-1"})
	require.NoError(t, err)
	assert.Equal(t, storage.Bucket{Name: "b-1", Region: testRegion}, *b)

	out := putString(t, p, "k", "v")
	require.NotEmpty(t, out.Version)

	info, err := p.HeadObject(t.Context(), &storage.HeadObjectInput{
		BaseObjectInput: keyK,
		Version:         &out.Version,
		PartNumber:      new(int32(1)),
	})
	require.NoError(t, err)
	assert.Equal(t, out.Version, info.Version)

	err = p.DeleteObject(t.Context(), &storage.DeleteObjectInput{
		BaseObjectInput: keyK,
		Version:         &out.Version,
	})
	require.NoError(t, err)
}

// A write in parts stores the whole body, with an ETag of parts.
func TestPutObject_Parts(t *testing.T) {
	t.Parallel()

	f := newFake(t)
	p := f.withBucket(t, "b-1")
	body := strings.Repeat("0123456789abcdef", partBody/16)

	out, err := p.PutObject(t.Context(), &storage.PutObjectInput{
		BaseObjectInput: keyK,
		Body:            strings.NewReader(body),
		ContentLength:   new(int64(len(body))),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(partBody), out.Size)
	assert.True(t, strings.HasSuffix(out.ETag, "-3"), out.ETag)

	obj, err := p.GetObject(t.Context(), &storage.GetObjectInput{
		BaseObjectInput: keyK,
	})
	require.NoError(t, err)

	got, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	require.NoError(t, obj.Body.Close())
	assert.Equal(t, body, string(got))
}

// A page whose keys, or common prefixes, are not valid URL encoding is an
// invalid reply.
func TestListObjects_BadEncoding(t *testing.T) {
	t.Parallel()

	for _, page := range []string{
		`<ListBucketResult><Contents><Key>%zz</Key></Contents></ListBucketResult>`,
		`<ListBucketResult><CommonPrefixes><Prefix>%zz</Prefix></CommonPrefixes></ListBucketResult>`,
	} {
		p := replying(t, rawReply(http.StatusOK, []byte(page)))

		_, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{})
		require.ErrorIs(t, err, storage.ErrBackend)
		require.EqualError(t, err, "gas/storage/s3: list objects: invalid reply")
	}
}

// A client of WithClient that returns neither an output nor an error
// fails the call as an invalid reply.
func TestStub_NoOutput(t *testing.T) {
	t.Parallel()

	p := stubbed(t, &stubAPI{
		head: func(context.Context) (*awss3.HeadObjectOutput, error) { return nil, nil },
		get:  func(context.Context) (*awss3.GetObjectOutput, error) { return nil, nil },
		put:  func(context.Context) (*awss3.PutObjectOutput, error) { return nil, nil },
	}, s3store.DefaultConfig())

	in := storage.BaseObjectInput{Bucket: new("b-1"), Key: "k"}

	_, head := p.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: in})
	_, get := p.GetObject(t.Context(), &storage.GetObjectInput{
		BaseObjectInput: in,
	})
	_, put := p.PutObject(t.Context(), &storage.PutObjectInput{BaseObjectInput: in})

	for op, err := range map[string]error{"head object": head, "get object": get, "put object": put} {
		require.ErrorIs(t, err, storage.ErrBackend, op)
		require.EqualError(t, err, "gas/storage/s3: "+op+": invalid reply")
	}
}

// An error code with a namespace or a fault is told by its bare code, and
// a timeout of the network while the caller's deadline is the one in
// force is the context's error.
func TestStub_Codes(t *testing.T) {
	t.Parallel()

	var reply error

	p := stubbed(t, &stubAPI{
		head: func(context.Context) (*awss3.HeadObjectOutput, error) { return nil, reply },
	}, s3store.DefaultConfig())

	head := func(ctx context.Context) error {
		_, err := p.HeadObject(ctx, &storage.HeadObjectInput{
			Bucket: new("b-1"), Key: "k",
		})

		return err
	}

	reply = &smithy.GenericAPIError{Code: "com.amazonaws.s3#SlowDown:Client", Message: "zqx"}

	err := head(t.Context())
	require.ErrorIs(t, err, s3store.ErrThrottled)
	require.EqualError(t, err, "gas/storage/s3: head object: throttled (SlowDown)")

	reply = errors.Join(&smithy.GenericAPIError{Code: "SlowDown"}, context.DeadlineExceeded)

	err = head(t.Context())
	require.ErrorIs(t, err, s3store.ErrThrottled)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	reply = context.DeadlineExceeded

	ctx, cancel := context.WithTimeout(t.Context(), s3store.DefaultOpTimeout/2)
	defer cancel()

	err = head(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, s3store.ErrConnect)
	require.EqualError(t, err, "gas/storage/s3: head object: context deadline exceeded")
}

// A GetObject whose response comes after OpTimeout, or after a Close
// canceled it, fails, and its body is let go.
func TestStub_GetObjectLate(t *testing.T) {
	t.Parallel()

	cfg := s3store.DefaultConfig()

	cfg.Storage.OpTimeout = 100 * time.Millisecond

	body := &closeBody{Reader: strings.NewReader("v")}
	p := stubbed(t, &stubAPI{get: func(ctx context.Context) (*awss3.GetObjectOutput, error) {
		<-ctx.Done()

		return &awss3.GetObjectOutput{Body: body}, nil
	}}, cfg)

	get := func(ctx context.Context) error {
		_, err := p.GetObject(ctx, &storage.GetObjectInput{
			Bucket: new("b-1"), Key: "k",
		})

		return err
	}

	err := get(t.Context())
	require.ErrorIs(t, err, s3store.ErrConnect)
	require.EqualError(t, err, "gas/storage/s3: get object: timed out")
	assert.True(t, body.isClosed())

	cfg.Storage.OpTimeout = time.Minute
	body = &closeBody{Reader: strings.NewReader("v")}

	got := make(chan error, 1)

	p = stubbed(t, &stubAPI{get: func(ctx context.Context) (*awss3.GetObjectOutput, error) {
		<-ctx.Done()

		return &awss3.GetObjectOutput{Body: body}, nil
	}}, cfg)

	go func() { got <- get(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	require.ErrorIs(t, p.Close(ctx), context.DeadlineExceeded)
	require.EqualError(t, <-got, "gas/storage/s3: get object: service closed")
	assert.True(t, body.isClosed())
}

// A Body whose Close fails reports it, classified.
func TestStub_BodyCloseFails(t *testing.T) {
	t.Parallel()

	p := stubbed(t, &stubAPI{get: func(context.Context) (*awss3.GetObjectOutput, error) {
		return &awss3.GetObjectOutput{
			Body:          &closeBody{Reader: strings.NewReader("v"), err: errZqx},
			ContentLength: aws.Int64(1),
		}, nil
	}}, s3store.DefaultConfig())

	obj, err := p.GetObject(t.Context(), &storage.GetObjectInput{
		Bucket: new("b-1"), Key: "k",
	})
	require.NoError(t, err)

	err = obj.Body.Close()
	require.ErrorIs(t, err, storage.ErrBackend)
	require.EqualError(t, err, "gas/storage/s3: get object: read body: failed")
}

// closeBody is a response body that records its Close, which returns err.
type closeBody struct {
	io.Reader

	// err is what Close returns.
	err error
	// mu guards closed.
	mu sync.Mutex
	// closed reports whether Close was called.
	closed bool
}

// Close records the call and returns err.
func (b *closeBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.closed = true

	return b.err
}

// isClosed reports whether Close was called.
func (b *closeBody) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.closed
}
