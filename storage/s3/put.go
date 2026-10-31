package s3

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal/lifecycle"
)

// PutObject writes the object in, as described in the package
// documentation, with the AWS SDK's transfer manager: in one PutObject
// when the body holds less than 16 MiB, else in parts of 8 MiB. Each
// request is bounded by OpTimeout, and an upload in parts that fails is
// aborted, on a context of its own bounded by OpTimeout, even when ctx
// has ended.
func (p *Provider) PutObject(
	ctx context.Context,
	in *storage.PutObjectInput,
) (*storage.PutObjectOutput, error) {
	if in == nil {
		return nil, opError(opPutObject, storage.ErrNilInput)
	}

	sess, err := p.begin(ctx)
	if err != nil {
		return nil, opError(opPutObject, err)
	}
	defer sess.End()

	req, err := storage.CheckPut(in, sess.Value.bucket)
	if err != nil {
		return nil, opError(opPutObject, err)
	}

	params, err := uploadInput(&req)
	if err != nil {
		return nil, opError(opPutObject, err)
	}

	body := &putBody{r: req.Body, ctx: ctx, sess: sess.Context()}

	params.Body = body

	upload := &uploadAPI{API: sess.Value.api, opTimeout: sess.Value.opTimeout}

	res, err := upload.run(ctx, sess, params)
	if err != nil {
		err = putFailure(ctx, sess, body, upload, err)
		if errors.Is(err, ErrUploadNotAborted) {
			p.logger.Warn("upload in parts not aborted").Err("error", err).Send()
		}

		return nil, err
	}

	return &storage.PutObjectOutput{
		ETag:    unquote(aws.ToString(res.ETag)),
		Version: aws.ToString(res.VersionID),
		Size:    body.n,
	}, nil
}

// uploadInput returns the transfer manager's input of req, but for its
// body.
func uploadInput(req *storage.PutRequest) (*transfermanager.UploadObjectInput, error) {
	params := &transfermanager.UploadObjectInput{
		Bucket:             new(req.Bucket),
		Key:                new(req.Key),
		CacheControl:       nonEmpty(req.CacheControl),
		ContentDisposition: nonEmpty(req.ContentDisposition),
		ContentEncoding:    nonEmpty(req.ContentEncoding),
		ContentLanguage:    nonEmpty(req.ContentLanguage),
		ContentType:        nonEmpty(req.ContentType),
		Metadata:           req.Metadata,
		IfMatch:            ifMatch(req.IfMatch),
	}

	if req.ContentLength >= 0 {
		params.ContentLength = new(req.ContentLength)
	}

	if req.IfNoneMatch {
		params.IfNoneMatch = new("*")
	}

	var err error

	params.Expires, err = expiresTime(req.Expires)
	if err != nil {
		return nil, err
	}

	return params, nil
}

// ifMatch returns the If-Match header of etag, quoted, as S3 expects, or
// nil for an empty etag.
func ifMatch(etag string) *string {
	if etag == "" {
		return nil
	}

	return new(`"` + etag + `"`)
}

// putFailure returns the error err of the upload of a PutObject: the
// context's error when ctx ended, storage.ErrClosed when a Close canceled
// the session, the body's error when it failed, and else the error
// requestError makes; with the failure of the abort after it, when the
// upload could not be aborted.
func putFailure(
	ctx context.Context,
	sess *lifecycle.Session[*state],
	body *putBody,
	upload *uploadAPI,
	err error,
) error {
	switch {
	case ctx.Err() != nil:
		err = opError(opPutObject, ctx.Err())
	case sess.Context().Err() != nil:
		err = opError(opPutObject, storage.ErrClosed)
	case body.err != nil:
		err = opError(opPutObject, body.err)
	default:
		err = requestError(opPutObject, err)
	}

	abortErr := upload.abortFailure()
	if abortErr == nil {
		return err
	}

	reason := reasonOf(opPutObject, abortErr)

	return &fixedError{
		msg:  err.Error() + "; " + ErrUploadNotAborted.Error() + ": " + reason.msg,
		errs: append([]error{err, ErrUploadNotAborted}, reason.errs...),
	}
}

// putBody is the body of a PutObject as the transfer manager reads it:
// the body held to its length, read while the call goes on.
type putBody struct {
	// r is the body, held to its length by storage.CheckLength.
	r io.Reader
	// ctx is the PutObject's context.
	ctx context.Context //nolint:containedctx // what ends the reads.
	// sess is the session's context, which a Close cancels.
	sess context.Context //nolint:containedctx // what ends the reads.
	// err is the first error of r other than io.EOF, or nil.
	err error
	// n counts the bytes read.
	n int64
}

// Read returns the context's error once ctx has ended, and
// storage.ErrClosed once a Close canceled the session, so that a body
// that never ends cannot hold the call; and else reads from the body.
func (b *putBody) Read(p []byte) (int, error) {
	err := b.ctx.Err()
	if err != nil {
		return 0, err //nolint:wrapcheck // PutObject reports the context's error.
	}

	if b.sess.Err() != nil {
		return 0, storage.ErrClosed
	}

	n, err := b.r.Read(p)

	b.n += int64(n)
	if err != nil && !errors.Is(err, io.EOF) && b.err == nil {
		b.err = err
	}

	return n, err //nolint:wrapcheck // storage.CheckLength wraps it.
}

// uploadAPI is the API the transfer manager uploads one object with: each
// request bounded by OpTimeout, its errors classified, so that no error
// the transfer manager returns or logs holds the AWS SDK's text, and the
// failure of an abort recorded.
type uploadAPI struct {
	API

	// abortErr is the error of the abort of the upload, or nil.
	abortErr error
	// opTimeout bounds each request.
	opTimeout time.Duration
	// mu guards abortErr.
	mu sync.Mutex
}

var _ transfermanager.S3APIClient = (*uploadAPI)(nil)

// bounded calls fn with in within ctx and opTimeout, and returns its
// output, or its error classified.
func bounded[I, O any](
	ctx context.Context,
	opTimeout time.Duration,
	fn func(context.Context, *I, ...func(*s3.Options)) (*O, error),
	in *I,
	optFns []func(*s3.Options),
) (*O, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	out, err := fn(ctx, in, optFns...)
	if err == nil && out == nil {
		err = errNoOutput
	}

	if err != nil {
		return nil, reasonOf(opPutObject, err)
	}

	return out, nil
}

// PutObject writes an object in one request.
func (u *uploadAPI) PutObject(
	ctx context.Context,
	in *s3.PutObjectInput,
	optFns ...func(*s3.Options),
) (*s3.PutObjectOutput, error) {
	return bounded(ctx, u.opTimeout, u.API.PutObject, in, optFns)
}

// CreateMultipartUpload begins an upload in parts.
func (u *uploadAPI) CreateMultipartUpload(
	ctx context.Context,
	in *s3.CreateMultipartUploadInput,
	optFns ...func(*s3.Options),
) (*s3.CreateMultipartUploadOutput, error) {
	return bounded(ctx, u.opTimeout, u.API.CreateMultipartUpload, in, optFns)
}

// UploadPart uploads a part.
func (u *uploadAPI) UploadPart(
	ctx context.Context,
	in *s3.UploadPartInput,
	optFns ...func(*s3.Options),
) (*s3.UploadPartOutput, error) {
	return bounded(ctx, u.opTimeout, u.API.UploadPart, in, optFns)
}

// CompleteMultipartUpload completes an upload in parts.
func (u *uploadAPI) CompleteMultipartUpload(
	ctx context.Context,
	in *s3.CompleteMultipartUploadInput,
	optFns ...func(*s3.Options),
) (*s3.CompleteMultipartUploadOutput, error) {
	return bounded(ctx, u.opTimeout, u.API.CompleteMultipartUpload, in, optFns)
}

// AbortMultipartUpload aborts an upload in parts, and records its error.
func (u *uploadAPI) AbortMultipartUpload(
	ctx context.Context,
	in *s3.AbortMultipartUploadInput,
	optFns ...func(*s3.Options),
) (*s3.AbortMultipartUploadOutput, error) {
	out, err := bounded(ctx, u.opTimeout, u.API.AbortMultipartUpload, in, optFns)
	if err != nil {
		u.mu.Lock()
		defer u.mu.Unlock()

		u.abortErr = err
	}

	return out, err
}

// run uploads the object of params, within ctx and the session's
// context.
func (u *uploadAPI) run(
	ctx context.Context,
	sess *lifecycle.Session[*state],
	params *transfermanager.UploadObjectInput,
) (*transfermanager.UploadObjectOutput, error) {
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	//nolint:contextcheck // the session's context is one Init's, which Close cancels.
	stop := context.AfterFunc(sess.Context(), cancel)
	defer stop()

	res, err := sess.Value.uploader.UploadObject(
		callCtx,
		params,
		func(o *transfermanager.Options) { o.S3 = u },
	)
	if err == nil && res == nil {
		err = errNoOutput
	}

	return res, err
}

// abortFailure returns the error of the abort of the upload, or nil when
// there was none or it succeeded.
func (u *uploadAPI) abortFailure() error {
	u.mu.Lock()
	defer u.mu.Unlock()

	return u.abortErr
}
