package s3

import (
	"context"
	"errors"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal/lifecycle"
)

// copyAttempts is how many times CopyObject sends its requests when S3
// refuses the copy with a conflict, such as a write of the source between
// the HeadObject and the CopyObject.
const copyAttempts = 3

// CopyObject copies the object in names, as described in the package
// documentation: a HeadObject of the source gives its size and its ETag,
// then a CopyObject with the source's ETag as its
// x-amz-copy-source-if-match writes the copy, so that the size returned
// is that of the contents copied. A HeadObject that finds no source is
// followed by a HeadBucket of its bucket, which tells a missing bucket
// from a missing key. A copy S3 refuses with a conflict, which writes
// nothing, is sent again, from the HeadObject, up to copyAttempts times in
// all. Each request is bounded by OpTimeout.
func (p *Provider) CopyObject(
	ctx context.Context,
	in *storage.CopyObjectInput,
) (*storage.CopyObjectOutput, error) {
	if in == nil {
		return nil, opError(opCopyObject, storage.ErrNilInput)
	}

	sess, err := p.begin(ctx)
	if err != nil {
		return nil, opError(opCopyObject, err)
	}
	defer sess.End()

	req, err := storage.CheckCopy(in, sess.Value.bucket)
	if err != nil {
		return nil, opError(opCopyObject, err)
	}

	for attempt := 1; ; attempt++ {
		out, err := copyOnce(ctx, sess, &req)
		if err == nil || !errors.Is(err, ErrConflict) || attempt == copyAttempts {
			return out, err
		}
	}
}

// copyOnce sends the HeadObject of the source of req, then the CopyObject
// pinned to the ETag it returned, and returns the copy written.
func copyOnce(
	ctx context.Context,
	sess *lifecycle.Session[*state],
	req *storage.CopyRequest,
) (*storage.CopyObjectOutput, error) {
	head, err := headSource(ctx, sess, req.Source)
	if err != nil {
		return nil, err
	}

	size := aws.ToInt64(head.ContentLength)

	err = storage.CheckCopySize(size)
	if err != nil {
		return nil, opError(opCopyObject, err)
	}

	params, err := copyInput(req, head.ETag)
	if err != nil {
		return nil, opError(opCopyObject, err)
	}

	res, err := call(ctx, sess, opCopyObject,
		func(ctx context.Context) (*s3.CopyObjectOutput, error) {
			return sess.Value.api.CopyObject(ctx, params)
		})
	if err != nil {
		return nil, err
	}

	out := &storage.CopyObjectOutput{Version: aws.ToString(res.VersionId), Size: size}
	if res.CopyObjectResult != nil {
		out.ETag = unquote(aws.ToString(res.CopyObjectResult.ETag))
	}

	return out, nil
}

// headSource sends the HeadObject of src, classified as HeadObject's,
// and returns its reply, or the failure of CopyObject: a source that is
// not found is storage.ErrObjectNotFound, and storage.ErrBucketNotFound
// too when a HeadBucket finds its bucket missing.
func headSource(
	ctx context.Context,
	sess *lifecycle.Session[*state],
	src storage.ObjectRef,
) (*s3.HeadObjectOutput, error) {
	api := sess.Value.api

	head, err := call(ctx, sess, opCopyObject,
		func(ctx context.Context) (*s3.HeadObjectOutput, error) {
			out, hErr := api.HeadObject(ctx, &s3.HeadObjectInput{
				Bucket: new(src.Bucket),
				Key:    new(src.Key),
			})
			if hErr != nil {
				return nil, reasonOf(opHeadObject, hErr)
			}

			return out, nil
		})
	if !errors.Is(err, storage.ErrObjectNotFound) {
		return head, err
	}

	_, bErr := call(ctx, sess, opCopyObject,
		func(ctx context.Context) (*s3.HeadBucketOutput, error) {
			out, hErr := api.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: new(src.Bucket)})
			if hErr != nil {
				return nil, reasonOf(opHeadBucket, hErr)
			}

			return out, nil
		})

	switch {
	case errors.Is(bErr, storage.ErrBucketNotFound):
		return nil, &fixedError{
			msg:  opPrefix(opCopyObject) + ": " + joinText(errObjectInMissingBucket),
			errs: errObjectInMissingBucket,
		}
	case bErr != nil:
		return nil, bErr
	}

	return nil, err
}

// copyInput returns the CopyObject request of req, whose source has the
// ETag etag, quoted as S3 returned it.
func copyInput(req *storage.CopyRequest, etag *string) (*s3.CopyObjectInput, error) {
	params := &s3.CopyObjectInput{
		Bucket:            new(req.Destination.Bucket),
		Key:               new(req.Destination.Key),
		CopySource:        new(copySource(req.Source.Bucket, req.Source.Key)),
		CopySourceIfMatch: etag,
		MetadataDirective: types.MetadataDirectiveCopy,
	}

	if !req.Replace {
		return params, nil
	}

	params.MetadataDirective = types.MetadataDirectiveReplace
	params.CacheControl = nonEmpty(req.CacheControl)
	params.ContentDisposition = nonEmpty(req.ContentDisposition)
	params.ContentEncoding = nonEmpty(req.ContentEncoding)
	params.ContentLanguage = nonEmpty(req.ContentLanguage)
	params.ContentType = nonEmpty(req.ContentType)
	params.Metadata = req.Metadata

	var err error

	params.Expires, err = expiresTime(req.Expires)
	if err != nil {
		return nil, err
	}

	return params, nil
}

// copySource returns the x-amz-copy-source of key in bucket: the bucket,
// `/`, then the key with every byte but the unreserved characters of RFC
// 3986 percent-encoded, as S3 decodes it. A bucket name holds only
// unreserved characters.
func copySource(bucket, key string) string {
	const hex = "0123456789ABCDEF"

	var b strings.Builder

	b.Grow(len(bucket) + 1 + 3*len(key))
	b.WriteString(bucket)
	b.WriteByte('/')

	for i := range len(key) {
		c := key[i]
		if isUnreserved(c) {
			b.WriteByte(c)

			continue
		}

		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0xf])
	}

	return b.String()
}

// isUnreserved reports whether c is an unreserved character of RFC 3986:
// a letter, a digit, `-`, `.`, `_` or `~`.
func isUnreserved(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
		c == '-' || c == '.' || c == '_' || c == '~'
}
