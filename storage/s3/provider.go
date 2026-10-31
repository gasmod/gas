package s3

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal/configsrc"
	"github.com/gasmod/gas/storage/internal/lifecycle"
	"github.com/gasmod/gas/storage/internal/listing"
)

// ServiceName is the name of the S3 store, returned by Provider.Name and
// prefixed to every error the store returns.
const ServiceName = "gas/storage/s3"

// The operations errors name.
const (
	opInit         = "init"
	opReady        = "ready"
	opClose        = "close"
	opCreateBucket = "create bucket"
	opHeadBucket   = "head bucket"
	opDeleteBucket = "delete bucket"
	opListBuckets  = "list buckets"
	opHeadObject   = "head object"
	opPutObject    = "put object"
	opGetObject    = "get object"
	opDeleteObject = "delete object"
	opListObjects  = "list objects"
)

var (
	// ErrNoRegion indicates that Init found no AWS region, in the Region
	// setting or the AWS SDK's default chain.
	ErrNoRegion = errors.New("no region")
	// ErrNoCredentials indicates that Init could not retrieve AWS
	// credentials.
	ErrNoCredentials = errors.New("no credentials")
	// ErrNilClient indicates that WithClient or WithPresignClient was
	// given a nil client.
	ErrNilClient = errors.New("client is nil")
	// ErrConnect indicates that a request got no reply from S3: it timed
	// out, its connection was refused, or the endpoint was unreachable, as
	// described in the package documentation. It comes with
	// storage.ErrBackend.
	ErrConnect = errors.New("no reply")
	// ErrThrottled indicates that S3 throttled a request, past the
	// retries. It comes with storage.ErrBackend.
	ErrThrottled = errors.New("throttled")
	// ErrWrongRegion indicates that S3 refused a request sent to another
	// region than the bucket's. It comes with storage.ErrBackend.
	ErrWrongRegion = errors.New("wrong region")
	// ErrConflict indicates that S3 refused a request that conflicted with
	// another in flight, such as two conditional writes of one key. It
	// comes with storage.ErrBackend.
	ErrConflict = errors.New("conflict")
	// ErrUploadNotAborted indicates that a PutObject that failed after it
	// began an upload in parts could not abort the upload, whose parts S3
	// then keeps, as described in the package documentation.
	ErrUploadNotAborted = errors.New("upload not aborted")
)

// API is the part of the S3 client the store uses. *s3.Client implements
// it.
//
//nolint:interfacebloat // the requests of the store and of the transfer manager.
type API interface {
	// CreateBucket creates a bucket.
	CreateBucket(
		ctx context.Context,
		params *s3.CreateBucketInput,
		optFns ...func(*s3.Options),
	) (*s3.CreateBucketOutput, error)
	// HeadBucket describes a bucket.
	HeadBucket(
		ctx context.Context,
		params *s3.HeadBucketInput,
		optFns ...func(*s3.Options),
	) (*s3.HeadBucketOutput, error)
	// DeleteBucket deletes a bucket.
	DeleteBucket(
		ctx context.Context,
		params *s3.DeleteBucketInput,
		optFns ...func(*s3.Options),
	) (*s3.DeleteBucketOutput, error)
	// ListBuckets lists buckets.
	ListBuckets(
		ctx context.Context,
		params *s3.ListBucketsInput,
		optFns ...func(*s3.Options),
	) (*s3.ListBucketsOutput, error)
	// HeadObject describes an object.
	HeadObject(
		ctx context.Context,
		params *s3.HeadObjectInput,
		optFns ...func(*s3.Options),
	) (*s3.HeadObjectOutput, error)
	// GetObject reads an object.
	GetObject(
		ctx context.Context,
		params *s3.GetObjectInput,
		optFns ...func(*s3.Options),
	) (*s3.GetObjectOutput, error)
	// PutObject writes an object in one request.
	PutObject(
		ctx context.Context,
		params *s3.PutObjectInput,
		optFns ...func(*s3.Options),
	) (*s3.PutObjectOutput, error)
	// DeleteObject deletes an object.
	DeleteObject(
		ctx context.Context,
		params *s3.DeleteObjectInput,
		optFns ...func(*s3.Options),
	) (*s3.DeleteObjectOutput, error)
	// ListObjectsV2 lists the objects of a bucket.
	ListObjectsV2(
		ctx context.Context,
		params *s3.ListObjectsV2Input,
		optFns ...func(*s3.Options),
	) (*s3.ListObjectsV2Output, error)
	// CreateMultipartUpload begins an upload in parts.
	CreateMultipartUpload(
		ctx context.Context,
		params *s3.CreateMultipartUploadInput,
		optFns ...func(*s3.Options),
	) (*s3.CreateMultipartUploadOutput, error)
	// UploadPart uploads a part.
	UploadPart(
		ctx context.Context,
		params *s3.UploadPartInput,
		optFns ...func(*s3.Options),
	) (*s3.UploadPartOutput, error)
	// CompleteMultipartUpload completes an upload in parts.
	CompleteMultipartUpload(
		ctx context.Context,
		params *s3.CompleteMultipartUploadInput,
		optFns ...func(*s3.Options),
	) (*s3.CompleteMultipartUploadOutput, error)
	// AbortMultipartUpload aborts an upload in parts.
	AbortMultipartUpload(
		ctx context.Context,
		params *s3.AbortMultipartUploadInput,
		optFns ...func(*s3.Options),
	) (*s3.AbortMultipartUploadOutput, error)
}

var (
	_ API                         = (*s3.Client)(nil)
	_ transfermanager.S3APIClient = API(nil)
)

// Provider is an object store on Amazon S3, or a server that speaks its
// API, as described in the package documentation. It is safe for
// concurrent use.
type Provider struct {
	// cfgProvider is the ConfigProvider Init binds from, unless a
	// configuration was given to WithConfig.
	cfgProvider gas.ConfigProvider
	// logger receives the store's events, with `service` set.
	logger gas.Logger
	// client is the client given to WithClient.
	client API
	// cfgSource records the configuration given to WithConfig.
	cfgSource configsrc.Source[Config]
	// lc is the lifecycle, whose sessions hold what one Init opened.
	lc lifecycle.Lifecycle[*state]
	// clientSet reports whether WithClient was given, so that a nil
	// client is told apart from none.
	clientSet bool
}

var (
	_ storage.Provider  = (*Provider)(nil)
	_ gas.Service       = (*Provider)(nil)
	_ gas.ReadyReporter = (*Provider)(nil)
)

// state is what one Init opened, with the settings the calls use.
type state struct {
	// api sends the requests.
	api API
	// uploader writes the objects PutObject writes, through api.
	uploader *transfermanager.Client
	// transport is the HTTP transport of a client Init built, or nil.
	transport *http.Transport
	// codec seals and opens the continuation tokens of this Init.
	codec *listing.Codec
	// bucket is the configured bucket, or empty.
	bucket string
	// region is the region CreateBucket creates buckets in, or empty.
	region string
	// opTimeout is Settings.OpTimeout.
	opTimeout time.Duration
}

// New returns the constructor of a Provider with opts applied, in the
// shape the gas container expects. The constructor returns an error
// wrapping storage.ErrNilLogger for a nil logger, storage.ErrNilOption for
// a nil option, and storage.ErrNilConfig for WithConfig(nil); a nil
// ConfigProvider is accepted, and Init then requires WithConfig.
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Provider, error) {
	return func(cfgProvider gas.ConfigProvider, logger gas.Logger) (*Provider, error) {
		if logger == nil {
			return nil, fmt.Errorf("%s: %w", ServiceName, storage.ErrNilLogger)
		}

		p := &Provider{
			cfgProvider: cfgProvider,
			logger:      logger.With().Str("service", ServiceName).Logger(),
		}

		for i, opt := range opts {
			if opt == nil {
				return nil, fmt.Errorf(
					"%s: %w: at position %d",
					ServiceName,
					storage.ErrNilOption,
					i,
				)
			}

			opt(p)
		}

		if p.cfgSource.IsNil() {
			return nil, fmt.Errorf("%s: %w", ServiceName, storage.ErrNilConfig)
		}

		return p, nil
	}
}

// Name returns ServiceName.
func (*Provider) Name() string { return ServiceName }

// Init resolves and checks the configuration, builds the client and
// retrieves the credentials, as described in the package documentation.
// It sends no request to S3. On an initialized store it returns
// storage.ErrAlreadyInitialized and changes nothing. A failed Init leaves
// the store as it was.
func (p *Provider) Init(ctx context.Context) error {
	if p == nil {
		return opError(opInit, storage.ErrNotInitialized)
	}

	var (
		set            *Settings
		region, source string
	)

	err := p.lc.Init(ctx, func() (*state, error) {
		cfg, err := p.cfgSource.Resolve(p.cfgProvider, DefaultConfig, rules)
		if err != nil {
			return nil, err //nolint:wrapcheck // Init adds the name and the operation.
		}

		set = &cfg.Storage

		st, err := p.open(ctx, set)
		if err != nil {
			return nil, err
		}

		if !p.clientSet {
			source, _ = credentialSource(set)
		}

		region = st.region

		return st, nil
	})
	if err != nil {
		return opError(opInit, err)
	}

	p.logInitialized(set, region, source)

	return nil
}

// Close makes every later call but Init return an error wrapping
// storage.ErrClosed, and waits for every call in flight to return, as
// described in the package documentation. When ctx ends first, it
// cancels the calls in flight, which return storage.ErrClosed, and
// returns an error wrapping ctx's error; the store stays closed. Close
// then closes the idle connections of a client Init built. Close on a
// store that is not initialized returns nil and logs nothing.
func (p *Provider) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}

	sess, err := p.lc.Close(ctx)
	if sess == nil {
		return nil
	}

	if t := sess.Value.transport; t != nil {
		t.CloseIdleConnections()
	}

	if err != nil {
		err = opError(opClose, err)
		p.logger.Warn("service closed, calls in flight canceled").Err("error", err).Send()

		return err
	}

	p.logger.Info("service closed").Send()

	return nil
}

// CheckReady reports readiness: an error wrapping
// storage.ErrNotInitialized or storage.ErrClosed when the store is not
// initialized, the context's error when ctx has ended, and else, with a
// configured bucket, the error of a HeadBucket of it, or nil. Without a
// configured bucket it does no I/O.
func (p *Provider) CheckReady(ctx context.Context) error {
	sess, err := p.begin(ctx)
	if err != nil {
		return opError(opReady, err)
	}
	defer sess.End()

	st := sess.Value
	if st.bucket == "" {
		return nil
	}

	return run(ctx, sess, opReady, func(ctx context.Context) error {
		_, err := st.api.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: new(st.bucket)})

		return err //nolint:wrapcheck // run classifies it.
	})
}

// Client returns the *s3.Client the store sends its requests with, or nil
// when the store is not initialized or was given another API with
// WithClient, as described in the package documentation.
func (p *Provider) Client() *s3.Client {
	sess, err := p.begin(context.Background())
	if err != nil {
		return nil
	}
	defer sess.End()

	c, _ := sess.Value.api.(*s3.Client)

	return c
}

// open returns the state of an Init with the settings set: the client
// given to WithClient, or one buildClient builds, after a warning naming
// the keys missing from a partial set of static credentials.
func (p *Provider) open(ctx context.Context, set *Settings) (*state, error) {
	codec, err := listing.NewCodec()
	if err != nil {
		return nil, err //nolint:wrapcheck // Init adds the name and the operation.
	}

	st := &state{
		api:       p.client,
		codec:     codec,
		bucket:    set.Bucket,
		region:    set.Region,
		opTimeout: set.OpTimeout,
	}

	switch {
	case p.clientSet && isNil(p.client):
		return nil, ErrNilClient
	case p.clientSet:
		if c, ok := p.client.(*s3.Client); ok {
			st.region = c.Options().Region
		}
	default:
		warnPartial(p.logger, set)

		b, err := buildClient(ctx, set)
		if err != nil {
			return nil, err
		}

		st.api, st.transport, st.region = b.client, b.transport, b.region
	}

	st.uploader = transfermanager.New(st.api, func(o *transfermanager.Options) {
		// A failed upload in parts is aborted on a context of its own,
		// bounded by OpTimeout, so that a canceled PutObject aborts it.
		o.FailTimeout = set.OpTimeout
	})

	return st, nil
}

// warnPartial logs a warning naming the keys missing from a partial set
// of static credentials, as described in the package documentation.
func warnPartial(logger gas.Logger, set *Settings) {
	_, missing := credentialSource(set)
	if len(missing) > 0 {
		logger.Warn("static credentials incomplete, using the default chain").
			Str("missing", strings.Join(missing, ", ")).
			Send()
	}
}

// logInitialized logs `service initialized`, as described in the package
// documentation, with the credential source unless a client was given.
func (p *Provider) logInitialized(set *Settings, region, source string) {
	event := p.logger.Info("service initialized").
		Str("region", region).
		Bool("endpoint", set.Endpoint != "").
		Bool("path_style", set.pathStyle()).
		Bool("bucket", set.Bucket != "").
		Bool("client", p.clientSet)
	if !p.clientSet {
		event = event.Str("credentials", source)
	}

	event.Duration("op_timeout", set.OpTimeout).Int("max_retries", set.MaxRetries).Send()
}

// begin starts a call, as lifecycle.Lifecycle.Begin does, on a Provider
// that may be nil.
func (p *Provider) begin(ctx context.Context) (*lifecycle.Session[*state], error) {
	if p == nil {
		return nil, storage.ErrNotInitialized
	}

	return p.lc.Begin(ctx) //nolint:wrapcheck // the method adds the name and the operation.
}

// isNil reports whether client is nil, or a nil *s3.Client.
func isNil(client API) bool {
	c, ok := client.(*s3.Client)

	return client == nil || ok && c == nil
}

// opError returns err as the failure of op, after ServiceName.
func opError(op string, err error) error {
	return fmt.Errorf("%s: %w", opPrefix(op), err)
}

// opPrefix returns the start of the errors of op: ServiceName and the
// operation.
func opPrefix(op string) string {
	return ServiceName + ": " + op
}
