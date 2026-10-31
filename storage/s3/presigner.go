package s3

import (
	"context"
	"crypto/md5" //nolint:gosec // G501: Content-MD5, which S3 checks the body against, not a security check.
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"time"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/middleware"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal/configsrc"
	"github.com/gasmod/gas/storage/internal/lifecycle"
)

// PresignerName is the name of the S3 presigner, returned by
// Presigner.Name and prefixed to every error the presigner returns.
const PresignerName = "gas/storage/s3/presigner"

// The operations of the presigner errors name.
const (
	opPresignUpload   = "presign upload"
	opPresignDownload = "presign download"
)

// amzDateFormat is the form of the X-Amz-Date of a presigned URL, the
// time it was signed at.
const amzDateFormat = "20060102T150405Z"

// errNoSigningTime reports a presigned URL without a valid X-Amz-Date,
// from which its expiry cannot be told.
var errNoSigningTime = fmt.Errorf("%w: no signing time in the URL", errInvalidReply)

// emptyMD5 is the Content-MD5 of an empty body, which a presigned upload
// of 0 bytes signs: the AWS SDK signs no Content-Length of 0, and S3
// refuses a body whose MD5 is not this one.
var emptyMD5 = func() string {
	sum := md5.Sum(nil) //nolint:gosec // G401: Content-MD5.

	return base64.StdEncoding.EncodeToString(sum[:])
}()

// PresignAPI is the part of the S3 presign client the presigner uses.
// *s3.PresignClient implements it.
type PresignAPI interface {
	// PresignPutObject signs a PutObject.
	PresignPutObject(
		ctx context.Context,
		params *s3.PutObjectInput,
		optFns ...func(*s3.PresignOptions),
	) (*v4.PresignedHTTPRequest, error)
	// PresignGetObject signs a GetObject.
	PresignGetObject(
		ctx context.Context,
		params *s3.GetObjectInput,
		optFns ...func(*s3.PresignOptions),
	) (*v4.PresignedHTTPRequest, error)
}

var _ PresignAPI = (*s3.PresignClient)(nil)

// Presigner issues presigned URLs of objects on Amazon S3, or a server
// that speaks its API, as described in the package documentation. It is
// safe for concurrent use.
type Presigner struct {
	// cfgProvider is the ConfigProvider Init binds from, unless a
	// configuration was given to WithPresignerConfig.
	cfgProvider gas.ConfigProvider
	// logger receives the presigner's events, with `service` set.
	logger gas.Logger
	// client is the client given to WithPresignClient.
	client PresignAPI
	// cfgSource records the configuration given to WithPresignerConfig.
	cfgSource configsrc.Source[Config]
	// lc is the lifecycle, whose sessions hold what one Init opened.
	lc lifecycle.Lifecycle[*signer]
	// clientSet reports whether WithPresignClient was given, so that a
	// nil client is told apart from none.
	clientSet bool
}

var (
	_ storage.Presigner = (*Presigner)(nil)
	_ gas.Service       = (*Presigner)(nil)
	_ gas.ReadyReporter = (*Presigner)(nil)
)

// signer is what one Init of a Presigner opened.
type signer struct {
	// api signs the requests.
	api PresignAPI
	// transport is the HTTP transport of a client Init built, or nil.
	transport *http.Transport
	// bucket is the configured bucket, or empty.
	bucket string
	// opTimeout is Settings.OpTimeout.
	opTimeout time.Duration
}

// NewPresigner returns the constructor of a Presigner with opts applied,
// in the shape the gas container expects. The constructor returns an
// error wrapping storage.ErrNilLogger for a nil logger,
// storage.ErrNilOption for a nil option, and storage.ErrNilConfig for
// WithPresignerConfig(nil); a nil ConfigProvider is accepted, and Init
// then requires WithPresignerConfig.
func NewPresigner(
	opts ...PresignerOption,
) func(gas.ConfigProvider, gas.Logger) (*Presigner, error) {
	return func(cfgProvider gas.ConfigProvider, logger gas.Logger) (*Presigner, error) {
		if logger == nil {
			return nil, fmt.Errorf("%s: %w", PresignerName, storage.ErrNilLogger)
		}

		p := &Presigner{
			cfgProvider: cfgProvider,
			logger:      logger.With().Str("service", PresignerName).Logger(),
		}

		for i, opt := range opts {
			if opt == nil {
				return nil, fmt.Errorf(
					"%s: %w: at position %d",
					PresignerName,
					storage.ErrNilOption,
					i,
				)
			}

			opt(p)
		}

		if p.cfgSource.IsNil() {
			return nil, fmt.Errorf("%s: %w", PresignerName, storage.ErrNilConfig)
		}

		return p, nil
	}
}

// Name returns PresignerName.
func (*Presigner) Name() string { return PresignerName }

// Init resolves and checks the configuration, builds the presign client
// and retrieves the credentials, as a Provider's Init does. On an
// initialized presigner it returns storage.ErrAlreadyInitialized and
// changes nothing. A failed Init leaves the presigner as it was.
func (p *Presigner) Init(ctx context.Context) error {
	if p == nil {
		return signerError(opInit, storage.ErrNotInitialized)
	}

	var (
		set            *Settings
		region, source string
	)

	err := p.lc.Init(ctx, func() (*signer, error) {
		cfg, err := p.cfgSource.Resolve(p.cfgProvider, DefaultConfig, rules)
		if err != nil {
			return nil, err //nolint:wrapcheck // Init adds the name and the operation.
		}

		set = &cfg.Storage

		sg := &signer{api: p.client, bucket: set.Bucket, opTimeout: set.OpTimeout}

		switch {
		case p.clientSet && isNilPresign(p.client):
			return nil, ErrNilClient
		case !p.clientSet:
			warnPartial(p.logger, set)

			b, err := buildClient(ctx, set)
			if err != nil {
				return nil, err
			}

			sg.api, sg.transport = s3.NewPresignClient(b.client), b.transport
			region, source = b.region, sourceOf(set)
		}

		return sg, nil
	})
	if err != nil {
		return signerError(opInit, err)
	}

	event := p.logger.Info("service initialized").
		Str("region", region).
		Bool("endpoint", set.Endpoint != "").
		Bool("path_style", set.pathStyle()).
		Bool("bucket", set.Bucket != "").
		Bool("client", p.clientSet)
	if !p.clientSet {
		event = event.Str("credentials", source)
	}

	event.Duration("op_timeout", set.OpTimeout).Send()

	return nil
}

// sourceOf returns where the client of set takes its credentials from,
// as credentialSource tells.
func sourceOf(set *Settings) string {
	source, _ := credentialSource(set)

	return source
}

// Close makes every later call but Init return an error wrapping
// storage.ErrClosed, and waits for every call in flight to return, as a
// Provider's Close does. Close on a presigner that is not initialized
// returns nil and logs nothing.
func (p *Presigner) Close(ctx context.Context) error {
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
		err = signerError(opClose, err)
		p.logger.Warn("service closed, calls in flight canceled").Err("error", err).Send()

		return err
	}

	p.logger.Info("service closed").Send()

	return nil
}

// CheckReady reports readiness without I/O: an error wrapping
// storage.ErrNotInitialized or storage.ErrClosed when the presigner is
// not initialized, the context's error when ctx has ended, and else nil.
func (p *Presigner) CheckReady(ctx context.Context) error {
	sess, err := p.begin(ctx)
	if err != nil {
		return signerError(opReady, err)
	}

	sess.End()

	return nil
}

// PresignClient returns the *s3.PresignClient the presigner signs with,
// or nil when the presigner is not initialized or was given another
// PresignAPI with WithPresignClient.
func (p *Presigner) PresignClient() *s3.PresignClient {
	sess, err := p.begin(context.Background())
	if err != nil {
		return nil
	}
	defer sess.End()

	c, _ := sess.Value.api.(*s3.PresignClient)

	return c
}

// PresignUploadURL returns a presigned PUT of the object in, as described
// in the package documentation: the ContentLength, the header fields, the
// metadata and the condition are signed, so the client must send them as
// SignedHeader holds them.
func (p *Presigner) PresignUploadURL(
	ctx context.Context,
	in *storage.PresignUploadURLInput,
) (*storage.PresignURLResponse, error) {
	if in == nil {
		return nil, signerError(opPresignUpload, storage.ErrNilInput)
	}

	sess, err := p.begin(ctx)
	if err != nil {
		return nil, signerError(opPresignUpload, err)
	}
	defer sess.End()

	req, err := storage.CheckPresignUpload(in, sess.Value.bucket)
	if err != nil {
		return nil, signerError(opPresignUpload, err)
	}

	params := &s3.PutObjectInput{
		Bucket:             new(req.Bucket),
		Key:                new(req.Key),
		ContentLength:      new(req.ContentLength),
		CacheControl:       nonEmpty(req.CacheControl),
		ContentDisposition: nonEmpty(req.ContentDisposition),
		ContentEncoding:    nonEmpty(req.ContentEncoding),
		ContentLanguage:    nonEmpty(req.ContentLanguage),
		ContentType:        nonEmpty(req.ContentType),
		Metadata:           req.Metadata,
		IfMatch:            ifMatch(req.IfMatch),
	}

	if req.ContentLength == 0 {
		params.ContentMD5 = new(emptyMD5)
	}

	if req.IfNoneMatch {
		params.IfNoneMatch = new("*")
	}

	params.Expires, err = expiresTime(req.Expires)
	if err != nil {
		return nil, signerError(opPresignUpload, err)
	}

	return presign(ctx, sess, opPresignUpload, req.TTL,
		func(ctx context.Context, opt func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
			return sess.Value.api.PresignPutObject(ctx, params, opt, signContentType)
		})
}

// removeContentTypeID is the ID of the AWS SDK's build middleware that
// removes the Content-Type of a presigned PutObject whose length is 0.
const removeContentTypeID = "RemoveContentTypeHeader"

// signContentType keeps the Content-Type of a presigned PutObject of 0
// bytes in the signed header fields: the AWS SDK removes it when the
// length is 0, which would let a client send any. It fails the signing
// when the SDK no longer has the middleware it removes.
func signContentType(o *s3.PresignOptions) {
	o.ClientOptions = append(o.ClientOptions, func(o *s3.Options) {
		o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
			_, err := stack.Build.Remove(removeContentTypeID)
			if err != nil {
				return fmt.Errorf("keep content type: %w", err)
			}

			return nil
		})
	})
}

// PresignDownloadURL returns a presigned GET of the object in, as
// described in the package documentation, with the Range, PartNumber,
// Version and response header fields it sets.
func (p *Presigner) PresignDownloadURL(
	ctx context.Context,
	in *storage.PresignDownloadURLInput,
) (*storage.PresignURLResponse, error) {
	if in == nil {
		return nil, signerError(opPresignDownload, storage.ErrNilInput)
	}

	sess, err := p.begin(ctx)
	if err != nil {
		return nil, signerError(opPresignDownload, err)
	}
	defer sess.End()

	req, err := storage.CheckPresignDownload(in, sess.Value.bucket)
	if err != nil {
		return nil, signerError(opPresignDownload, err)
	}

	params := &s3.GetObjectInput{
		Bucket:                     new(req.Bucket),
		Key:                        new(req.Key),
		PartNumber:                 nonZero(req.PartNumber),
		Range:                      rangeHeader(req.Range),
		VersionId:                  nonEmpty(req.Version),
		ResponseCacheControl:       nonEmpty(req.Response.CacheControl),
		ResponseContentDisposition: nonEmpty(req.Response.ContentDisposition),
		ResponseContentEncoding:    nonEmpty(req.Response.ContentEncoding),
		ResponseContentLanguage:    nonEmpty(req.Response.ContentLanguage),
		ResponseContentType:        nonEmpty(req.Response.ContentType),
	}

	params.ResponseExpires, err = expiresTime(req.Response.Expires)
	if err != nil {
		return nil, signerError(opPresignDownload, err)
	}

	return presign(ctx, sess, opPresignDownload, req.TTL,
		func(ctx context.Context, opt func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
			return sess.Value.api.PresignGetObject(ctx, params, opt)
		})
}

// expiresTime returns the time of an Expires header field the rules
// checked, or nil for an empty one. The AWS SDK sends it as a time, which
// it writes as an IMF-fixdate, the only form the rules let through.
func expiresTime(expires string) (*time.Time, error) {
	if expires == "" {
		return nil, nil
	}

	t, err := storage.ParseExpires(expires)
	if err != nil {
		return nil, err //nolint:wrapcheck // the method adds the name and the operation.
	}

	return &t, nil
}

// presign signs with fn, for ttl, within ctx, the session's OpTimeout and
// its context, and returns the presigned request, which expires ttl after
// the time it was signed at, or the failure of op: the context's error
// when ctx ended, storage.ErrClosed when a Close canceled the session, and
// else the reason classify finds.
func presign(
	ctx context.Context,
	sess *lifecycle.Session[*signer],
	op string,
	ttl time.Duration,
	fn func(context.Context, func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error),
) (*storage.PresignURLResponse, error) {
	opCtx, cancel := context.WithTimeout(ctx, sess.Value.opTimeout)
	defer cancel()

	//nolint:contextcheck // the session's context is one Init's, which Close cancels.
	stop := context.AfterFunc(sess.Context(), cancel)
	defer stop()

	res, err := fn(opCtx, s3.WithPresignExpires(ttl))
	if err == nil && res == nil {
		err = errNoOutput
	}

	var signed time.Time

	if err == nil {
		signed, err = signingTime(res.URL)
	}

	switch {
	case err == nil:
		return &storage.PresignURLResponse{
			SignedHeader: res.SignedHeader.Clone(),
			Expires:      signed.Add(ttl),
			URL:          res.URL,
			Method:       res.Method,
		}, nil
	case ctx.Err() != nil:
		return nil, signerError(op, ctx.Err())
	case sess.Context().Err() != nil:
		return nil, signerError(op, storage.ErrClosed)
	}

	reason := reasonOf(op, err)

	return nil, &fixedError{msg: PresignerName + ": " + op + ": " + reason.msg, errs: reason.errs}
}

// signingTime returns the time the presigned URL raw was signed at, its
// X-Amz-Date.
func signingTime(raw string) (time.Time, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return time.Time{}, errNoSigningTime
	}

	t, err := time.Parse(amzDateFormat, u.Query().Get("X-Amz-Date"))
	if err != nil {
		return time.Time{}, errNoSigningTime
	}

	return t, nil
}

// begin starts a call, as lifecycle.Lifecycle.Begin does, on a Presigner
// that may be nil.
func (p *Presigner) begin(ctx context.Context) (*lifecycle.Session[*signer], error) {
	if p == nil {
		return nil, storage.ErrNotInitialized
	}

	return p.lc.Begin(ctx) //nolint:wrapcheck // the method adds the name and the operation.
}

// isNilPresign reports whether client is nil, or a nil *s3.PresignClient.
func isNilPresign(client PresignAPI) bool {
	c, ok := client.(*s3.PresignClient)

	return client == nil || ok && c == nil
}

// signerError returns err as the failure of op, after PresignerName.
func signerError(op string, err error) error {
	return fmt.Errorf("%s: %s: %w", PresignerName, op, err)
}
