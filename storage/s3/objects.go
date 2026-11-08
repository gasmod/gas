package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal/lifecycle"
	"github.com/gasmod/gas/storage/internal/listing"
)

// objectScope is the first part of the scope of a ListObjects token, so
// that it never opens as a ListBuckets one.
const objectScope = "objects"

// The replies that are not ones S3 sends, which a request fails with as
// an invalid reply.
var (
	// errBadKey reports a listed key or common prefix that is not valid
	// URL encoding.
	errBadKey = fmt.Errorf("%w: a key is not valid URL encoding", errInvalidReply)
	// errBadPage reports a page whose IsTruncated and continuation token
	// disagree.
	errBadPage = fmt.Errorf("%w: IsTruncated and the token disagree", errInvalidReply)
	// errNoOutput reports a client that returned neither an output nor an
	// error.
	errNoOutput = fmt.Errorf("%w: no output", errInvalidReply)
)

// HeadObject describes the object in, as described in the package
// documentation.
func (p *Provider) HeadObject(
	ctx context.Context,
	in *storage.HeadObjectInput,
) (*storage.ObjectInfo, error) {
	if in == nil {
		return nil, opError(opHeadObject, storage.ErrNilInput)
	}

	sess, err := p.begin(ctx)
	if err != nil {
		return nil, opError(opHeadObject, err)
	}
	defer sess.End()

	req, err := storage.CheckRead(in, sess.Value.bucket)
	if err != nil {
		return nil, opError(opHeadObject, err)
	}

	params := &s3.HeadObjectInput{
		Bucket:     new(req.Bucket),
		Key:        new(req.Key),
		PartNumber: nonZero(req.PartNumber),
		Range:      rangeHeader(req.Range),
		VersionId:  nonEmpty(req.Version),
	}

	res, err := call(ctx, sess, opHeadObject,
		func(ctx context.Context) (*s3.HeadObjectOutput, error) {
			return sess.Value.api.HeadObject(ctx, params)
		})
	if err != nil {
		return nil, err
	}

	info := objectInfo(&fields{
		cacheControl:       res.CacheControl,
		contentDisposition: res.ContentDisposition,
		contentEncoding:    res.ContentEncoding,
		contentLanguage:    res.ContentLanguage,
		contentType:        res.ContentType,
		expires:            res.ExpiresString,
		etag:               res.ETag,
		version:            res.VersionId,
		lastModified:       res.LastModified,
		size:               res.ContentLength,
		metadata:           res.Metadata,
	})

	return &info, nil
}

// GetObject returns the object in, as described in the package
// documentation. OpTimeout and a Close bound the call until the response
// headers arrive; the Body is then governed by ctx alone.
func (p *Provider) GetObject(
	ctx context.Context,
	in *storage.GetObjectInput,
) (*storage.Object, error) {
	if in == nil {
		return nil, opError(opGetObject, storage.ErrNilInput)
	}

	sess, err := p.begin(ctx)
	if err != nil {
		return nil, opError(opGetObject, err)
	}
	defer sess.End()

	req, err := storage.CheckRead(&in.HeadObjectInput, sess.Value.bucket)
	if err != nil {
		return nil, opError(opGetObject, err)
	}

	res, cancel, err := getObject(ctx, sess, &s3.GetObjectInput{
		Bucket:     new(req.Bucket),
		Key:        new(req.Key),
		PartNumber: nonZero(req.PartNumber),
		Range:      rangeHeader(req.Range),
		VersionId:  nonEmpty(req.Version),
	})
	if err != nil {
		return nil, err
	}

	return &storage.Object{
		Body: &objectBody{ctx: ctx, body: res.Body, cancel: cancel},
		ObjectInfo: objectInfo(&fields{
			cacheControl:       res.CacheControl,
			contentDisposition: res.ContentDisposition,
			contentEncoding:    res.ContentEncoding,
			contentLanguage:    res.ContentLanguage,
			contentType:        res.ContentType,
			expires:            res.ExpiresString,
			etag:               res.ETag,
			version:            res.VersionId,
			lastModified:       res.LastModified,
			size:               res.ContentLength,
			metadata:           res.Metadata,
		}),
	}, nil
}

// getObject sends the GetObject of params within ctx, and within the
// session's OpTimeout and context until the response headers arrive. It
// returns the response, whose body is then governed by ctx alone, and
// what frees the request's context, which the body's Close calls.
func getObject(
	ctx context.Context,
	sess *lifecycle.Session[*state],
	params *s3.GetObjectInput,
) (*s3.GetObjectOutput, context.CancelFunc, error) {
	reqCtx, cancel := context.WithCancel(ctx)

	//nolint:contextcheck // the session's context is one Init's, which Close cancels.
	stop := context.AfterFunc(sess.Context(), cancel)
	timer := time.AfterFunc(sess.Value.opTimeout, cancel)

	res, err := sess.Value.api.GetObject(reqCtx, params)
	inTime, open := timer.Stop(), stop()

	if err == nil && res == nil {
		err = errNoOutput
	}

	if err == nil && (!inTime || !open) {
		// The response came as OpTimeout or a Close ended the request, so
		// its body is cut off: the call fails, and the body is let go.
		_ = res.Body.Close()

		err = context.Canceled
	}

	if err != nil {
		cancel()

		if !inTime {
			err = errors.Join(err, context.DeadlineExceeded)
		}

		return nil, nil, failure(ctx, sess, opGetObject, time.Time{}, err)
	}

	return res, cancel, nil
}

// objectBody is the Body of an object GetObject returns: the response's,
// whose errors it classifies, and whose Close frees the request's
// context.
type objectBody struct {
	// ctx is the GetObject's context, which governs the body.
	ctx context.Context //nolint:containedctx // what governs the body.
	// body is the response body.
	body io.ReadCloser
	// cancel frees the request's context.
	cancel context.CancelFunc
}

// Read reads the body. io.EOF is returned as it is; any other error is
// returned as the failure of GetObject to read the body.
func (b *objectBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, b.bodyError(err)
	}

	//nolint:wrapcheck // io.EOF must stay as it is.
	return n, err
}

// Close closes the body and frees the request's context.
func (b *objectBody) Close() error {
	err := b.body.Close()

	b.cancel()

	if err != nil {
		return b.bodyError(err)
	}

	return nil
}

// bodyError returns the error err of the body as the failure of GetObject
// to read it: `read body: ` and the context's error when the GetObject's
// context ended, else the reason classify finds.
func (b *objectBody) bodyError(err error) error {
	ctxErr := b.ctx.Err()
	if ctxErr != nil {
		return opError(opGetObject, fmt.Errorf("read body: %w", ctxErr))
	}

	reason := reasonOf(opGetObject, err)

	return &fixedError{msg: opPrefix(opGetObject) + ": read body: " + reason.msg, errs: reason.errs}
}

// DeleteObject deletes the object in, or one version of it, as described
// in the package documentation. Deleting a key that holds no object is not
// an error.
func (p *Provider) DeleteObject(ctx context.Context, in *storage.DeleteObjectInput) error {
	if in == nil {
		return opError(opDeleteObject, storage.ErrNilInput)
	}

	sess, err := p.begin(ctx)
	if err != nil {
		return opError(opDeleteObject, err)
	}
	defer sess.End()

	ref, err := storage.CheckDelete(in, sess.Value.bucket)
	if err != nil {
		return opError(opDeleteObject, err)
	}

	return run(ctx, sess, opDeleteObject, func(ctx context.Context) error {
		_, err := sess.Value.api.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket:    new(ref.Bucket),
			Key:       new(ref.Key),
			VersionId: nonEmpty(ref.Version),
		})

		return err //nolint:wrapcheck // run classifies it.
	})
}

// ListObjects returns one page of the objects of a bucket, as described
// in the package documentation, with ListObjectsV2. It asks S3 to
// URL-encode the keys, so that keys XML cannot carry are listed, and
// decodes them; S3's continuation token is sealed in the token the page
// returns.
func (p *Provider) ListObjects(
	ctx context.Context,
	in *storage.ListObjectsInput,
) (*storage.ListObjectsOutput, error) {
	if in == nil {
		return nil, opError(opListObjects, storage.ErrNilInput)
	}

	sess, err := p.begin(ctx)
	if err != nil {
		return nil, opError(opListObjects, err)
	}
	defer sess.End()

	req, err := storage.CheckListObjects(in, sess.Value.bucket)
	if err != nil {
		return nil, opError(opListObjects, err)
	}

	st := sess.Value
	scope := listing.Scope(objectScope, req.Bucket, req.Prefix, req.Delimiter, req.StartAfter)
	params := &s3.ListObjectsV2Input{
		Bucket:       new(req.Bucket),
		Delimiter:    nonEmpty(req.Delimiter),
		EncodingType: types.EncodingTypeUrl,
		//nolint:gosec // G115: CheckListObjects bounds it to MaxListKeys.
		MaxKeys:    new(int32(req.MaxKeys)),
		Prefix:     nonEmpty(req.Prefix),
		StartAfter: nonEmpty(req.StartAfter),
	}

	params.ContinuationToken, err = openToken(st.codec, scope, req.ContinuationToken)
	if err != nil {
		return nil, opError(opListObjects, err)
	}

	res, err := call(ctx, sess, opListObjects,
		func(ctx context.Context) (*s3.ListObjectsV2Output, error) {
			return st.api.ListObjectsV2(ctx, params)
		})
	if err != nil {
		return nil, err
	}

	out, next, err := listPage(res)
	if err != nil {
		return nil, requestError(opListObjects, err)
	}

	if next != "" {
		out.NextContinuationToken, err = sealToken(st.codec, scope, next)
		if err != nil {
			return nil, requestError(opListObjects, err)
		}
	}

	return out, nil
}

// listPage returns the page of res, a ListObjectsV2 reply asked with the
// url encoding type, with its keys and common prefixes decoded, and S3's
// continuation token for the next page, which is empty on the last page,
// and only there.
func listPage(res *s3.ListObjectsV2Output) (*storage.ListObjectsOutput, string, error) {
	next := aws.ToString(res.NextContinuationToken)
	out := &storage.ListObjectsOutput{IsTruncated: aws.ToBool(res.IsTruncated)}

	if out.IsTruncated != (next != "") {
		return nil, "", errBadPage
	}

	for _, o := range res.Contents {
		key, err := url.QueryUnescape(aws.ToString(o.Key))
		if err != nil {
			return nil, "", errBadKey
		}

		out.Objects = append(out.Objects, storage.ObjectSummary{
			LastModified: second(aws.ToTime(o.LastModified)),
			Key:          key,
			ETag:         unquote(aws.ToString(o.ETag)),
			Size:         aws.ToInt64(o.Size),
		})
	}

	for _, cp := range res.CommonPrefixes {
		prefix, err := url.QueryUnescape(aws.ToString(cp.Prefix))
		if err != nil {
			return nil, "", errBadKey
		}

		out.CommonPrefixes = append(out.CommonPrefixes, prefix)
	}

	return out, next, nil
}

// fields are the fields of a HeadObject or GetObject reply that describe
// the object.
type fields struct {
	// cacheControl, contentDisposition, contentEncoding, contentLanguage,
	// contentType and expires are the header fields.
	cacheControl, contentDisposition, contentEncoding, contentLanguage, contentType, expires *string
	// etag is the ETag, quoted.
	etag *string
	// version is the version ID.
	version *string
	// lastModified is when the object was written.
	lastModified *time.Time
	// size is the length of the bytes selected.
	size *int64
	// metadata is the user metadata, which the AWS SDK returns with names
	// in lower case, in a map of its own.
	metadata map[string]string
}

// objectInfo returns the description of the object of f.
func objectInfo(f *fields) storage.ObjectInfo {
	return storage.ObjectInfo{
		CacheControl:       aws.ToString(f.cacheControl),
		ContentDisposition: aws.ToString(f.contentDisposition),
		ContentEncoding:    aws.ToString(f.contentEncoding),
		ContentLanguage:    aws.ToString(f.contentLanguage),
		ContentType:        aws.ToString(f.contentType),
		Expires:            aws.ToString(f.expires),
		LastModified:       second(aws.ToTime(f.lastModified)),
		Metadata:           f.metadata,
		ETag:               unquote(aws.ToString(f.etag)),
		Version:            aws.ToString(f.version),
		Size:               aws.ToInt64(f.size),
	}
}

// openToken returns S3's continuation token sealed in token for the
// listing of scope, or nil for an empty token.
func openToken(codec *listing.Codec, scope []byte, token string) (*string, error) {
	if token == "" {
		return nil, nil
	}

	cur, err := codec.Open(scope, token)
	if err != nil {
		return nil, err //nolint:wrapcheck // the method adds the name and the operation.
	}

	return new(cur.After), nil
}

// errTokenTooLong reports a continuation token of S3's too long to seal,
// which the store could not open again: longer than
// listing.MaxCursorLength.
var errTokenTooLong = fmt.Errorf("%w: continuation token too long", errInvalidReply)

// sealToken returns next, S3's continuation token, sealed for the
// listing of scope, or errTokenTooLong when it is too long to seal.
func sealToken(codec *listing.Codec, scope []byte, next string) (string, error) {
	token, err := codec.Seal(scope, listing.Cursor{After: next})
	if err != nil {
		return "", errTokenTooLong
	}

	return token, nil
}

// rangeHeader returns the Range header of r, or nil for none.
func rangeHeader(r *storage.ByteRange) *string {
	if r == nil {
		return nil
	}

	return new(r.String())
}

// nonEmpty returns a pointer to s, or nil for an empty s.
func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}

	return new(s)
}

// nonZero returns a pointer to n, or nil for 0.
func nonZero(n int32) *int32 {
	if n == 0 {
		return nil
	}

	return new(n)
}

// second returns t in UTC, truncated to the second, as the package
// documentation describes the times of objects and buckets.
func second(t time.Time) time.Time { return t.UTC().Truncate(time.Second) }

// unquote returns etag without the quotes S3 puts around it.
func unquote(etag string) string {
	if len(etag) >= 2 && etag[0] == '"' && etag[len(etag)-1] == '"' {
		return etag[1 : len(etag)-1]
	}

	return etag
}
