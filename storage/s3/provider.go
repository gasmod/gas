package s3

import (
	"bytes"
	"context"

	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal"
)

// Provider implements storage.Provider on S3. Build it with NewProvider; a
// Provider that was not, including a nil pointer, returns
// storage.ErrNotInitialized from every method.
type Provider struct {
	client   *s3.Client
	uploader *transfermanager.Client
	config   *Config
}

var _ storage.Provider = (*Provider)(nil)

// ProviderOption configures a Provider built by NewProvider.
type ProviderOption func(*Provider)

// WithClient makes the Provider use client for every operation except the
// upload in PutObject (see WithUploaderClient), instead of a client built from
// configuration. A nil client is ignored.
func WithClient(client *s3.Client) ProviderOption {
	return func(s *Provider) { s.client = client }
}

// WithUploaderClient makes PutObject upload through uploader instead of a
// transfer manager built on the Provider's client. A nil uploader is ignored.
func WithUploaderClient(uploader *transfermanager.Client) ProviderOption {
	return func(s *Provider) { s.uploader = uploader }
}

// NewProvider returns a constructor for the DI container that builds a
// Provider. The constructor takes the gas.ConfigProvider as its dependency
// (a nil one returns an error) and binds Config from it,
// fails with storage.ErrInvalidBucket when the configured bucket is set but
// invalid, applies opts, and builds any client an option did not supply.
func NewProvider(opts ...ProviderOption) func(cfgProvider gas.ConfigProvider) (*Provider, error) {
	return func(cfgProvider gas.ConfigProvider) (*Provider, error) {
		p := &Provider{}

		cfg, err := internal.BindConfig[Config](cfgProvider)
		if err != nil {
			return nil, internal.Errorf("s3/provider", "%w", err)
		}
		p.config = cfg

		if bk := p.config.Storage.Bucket; bk != "" && !internal.IsValidBucketName(bk) {
			return nil, internal.Errorf(
				"s3/provider",
				"%w: %s",
				storage.ErrInvalidBucket,
				bk,
			)
		}

		for i, o := range opts {
			if o == nil {
				return nil, internal.Errorf(
					"s3/provider",
					"%w: option %d is nil",
					storage.ErrInvalidOption,
					i,
				)
			}
			o(p)
		}

		if p.client == nil {
			client, clientErr := newClient(p.config)
			if clientErr != nil {
				return nil, internal.Errorf("s3/provider", "create S3 client: %w", clientErr)
			}
			p.client = client
		}

		if p.uploader == nil {
			// The transfer manager handles arbitrary readers, including unseekable
			// streams of unknown length (multipart under the hood; no overall
			// Content-Length required).
			p.uploader = transfermanager.New(p.client, func(o *transfermanager.Options) {
				o.RequestChecksumCalculation = p.client.Options().RequestChecksumCalculation
			})
		}

		return p, nil
	}
}

// CreateBucket creates the bucket named in input and returns its name and
// the ARN S3 reports; CreationDate and Region are empty, since S3 reports
// neither. The configured bucket is not a fallback. The
// bucket is created in the client's region: outside us-east-1 (and the
// aws-global pseudo-region, which maps to it) the request carries that region
// as its location constraint, and the region the SDK resolved is used when
// none is configured. A bucket the caller already owns is success, and its
// Bucket may hold only the name: in us-east-1 S3 is said to answer 200 and
// report the ARN as for a new bucket, while elsewhere it answers
// BucketAlreadyOwnedByYou with none (not verified against AWS). A name another account owns is
// storage.ErrBucketExists.
func (p *Provider) CreateBucket(
	ctx context.Context,
	input *storage.CreateBucketInput,
) (*storage.Bucket, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf("s3/provider", "create bucket: %w", storage.ErrInputRequired)
	}

	if !internal.IsValidBucketName(input.Bucket) {
		return nil, internal.Errorf("s3/provider", "create bucket: %w", storage.ErrInvalidBucket)
	}

	res, err := p.client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket:                    new(input.Bucket),
		CreateBucketConfiguration: createBucketConfiguration(p.client.Options().Region),
	})
	if isBucketOwnedByCaller(err) {
		return &storage.Bucket{Name: input.Bucket}, nil
	}
	if err != nil {
		return nil, internal.Errorf("s3/provider", "create bucket: %w", wrapS3Error(err))
	}

	return &storage.Bucket{Name: input.Bucket, ID: valueOf(res.BucketArn)}, nil
}

// HeadObject returns an object's size, content type, last-modified time,
// version, ETag, metadata and stored headers. Expires is the header's text as
// S3 returned it. A missing key or bucket is storage.ErrNotFound.
func (p *Provider) HeadObject(
	ctx context.Context,
	input *storage.HeadObjectInput,
) (*storage.ObjectInfo, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf("s3/provider", "head: %w", storage.ErrInputRequired)
	}

	bucket, bucketErr := internal.ValidateInput(&input.BaseObjectInput, p.config.Storage.Bucket)
	if bucketErr != nil {
		return nil, internal.Errorf("s3/provider", "head: %w", bucketErr)
	}

	in := &s3.HeadObjectInput{
		Bucket:                     new(bucket),
		Key:                        new(input.Key),
		PartNumber:                 input.PartNumber,
		Range:                      input.Range,
		ResponseCacheControl:       input.ResponseCacheControl,
		ResponseContentDisposition: input.ResponseContentDisposition,
		ResponseContentEncoding:    input.ResponseContentEncoding,
		ResponseContentLanguage:    input.ResponseContentLanguage,
		ResponseContentType:        input.ResponseContentType,
		ResponseExpires:            input.ResponseExpires,
		VersionId:                  input.Version,
	}

	res, err := p.client.HeadObject(ctx, in)
	if err != nil {
		return nil, internal.Errorf("s3/provider", "head: %w", wrapS3Error(err))
	}

	return &storage.ObjectInfo{
		Size:               valueOf(res.ContentLength),
		ContentType:        valueOf(res.ContentType),
		LastModified:       valueOf(res.LastModified),
		Version:            valueOf(res.VersionId),
		Metadata:           res.Metadata,
		CacheControl:       valueOf(res.CacheControl),
		ContentDisposition: valueOf(res.ContentDisposition),
		ContentEncoding:    valueOf(res.ContentEncoding),
		ContentLanguage:    valueOf(res.ContentLanguage),
		Expires:            valueOf(res.ExpiresString),
		ETag:               unquoteETag(res.ETag),
	}, nil
}

// PutObject uploads an object through the transfer manager and returns the
// ETag and the version S3 assigned, if any; the other ObjectInfo fields are
// empty. A body large enough for the transfer manager to upload in parts
// gets a multipart ETag, ending in "-" and the part count. Expires must be an
// IMF-fixdate ("Wed, 21 Oct 2026 07:28:00 GMT", with the right weekday),
// since the SDK sends it only as a time and writes it back in that form;
// anything else returns an error wrapping storage.ErrInvalidHeader and sends
// nothing. A nil Body uploads an empty object. A set ContentLength must match
// the body: a shorter or longer body, or a
// negative length, returns an error wrapping storage.ErrContentLengthMismatch
// and the object is not stored. A multipart upload in progress is aborted;
// if the abort itself fails, the incomplete upload remains (its parts are
// stored and billed until a lifecycle rule removes them) and the abort's
// error is not reported. Body is read to EOF, so a body that blocks after
// exactly ContentLength bytes without ending blocks PutObject. With
// ContentLength set, a seekable Body is sized from ContentLength, not by
// seeking, and buffered in parts like any other reader.
func (p *Provider) PutObject(
	ctx context.Context,
	input *storage.PutObjectInput,
) (*storage.ObjectInfo, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf("s3/provider", "put: %w", storage.ErrInputRequired)
	}

	bucket, bucketErr := internal.ValidateInput(&input.BaseObjectInput, p.config.Storage.Bucket)
	if bucketErr != nil {
		return nil, internal.Errorf("s3/provider", "put: %w", bucketErr)
	}

	expires, expErr := parseExpires(input.Expires)
	if expErr != nil {
		return nil, internal.Errorf("s3/provider", "put: %w", expErr)
	}

	// The transfer manager buffers the body and sends what it read, so it
	// accepts a body that does not match ContentLength; the check is ours.
	body, lenErr := internal.CheckLength(input.Body, input.ContentLength)
	if lenErr != nil {
		return nil, internal.Errorf("s3/provider", "put: %w", lenErr)
	}

	in := &transfermanager.UploadObjectInput{
		Bucket:             new(bucket),
		Key:                new(input.Key),
		Body:               body,
		ContentLength:      input.ContentLength,
		ContentType:        input.ContentType,
		CacheControl:       input.CacheControl,
		ContentDisposition: input.ContentDisposition,
		ContentEncoding:    input.ContentEncoding,
		ContentLanguage:    input.ContentLanguage,
		Expires:            expires,
		Metadata:           input.Metadata,
	}

	if in.Body == nil {
		in.Body = bytes.NewReader(nil)
	}

	// Use the transfer manager rather than a single PutObject so that unseekable
	// and unknown-length bodies (e.g. a streaming io.Pipe) upload without an
	// overall Content-Length. This also lifts PutObject's 5GB limit.
	res, err := p.uploader.UploadObject(ctx, in)
	if err != nil {
		return nil, internal.Errorf("s3/provider", "put: %w", wrapS3Error(err))
	}

	return &storage.ObjectInfo{
		ETag:    unquoteETag(res.ETag),
		Version: valueOf(res.VersionID),
	}, nil
}

// GetObject returns an object and its body, which the caller must close. A
// missing key is storage.ErrKeyNotFound and a missing bucket
// storage.ErrBucketNotFound.
func (p *Provider) GetObject(
	ctx context.Context,
	input *storage.GetObjectInput,
) (*storage.Object, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf("s3/provider", "get: %w", storage.ErrInputRequired)
	}

	bucket, bucketErr := internal.ValidateInput(&input.BaseObjectInput, p.config.Storage.Bucket)
	if bucketErr != nil {
		return nil, internal.Errorf("s3/provider", "get: %w", bucketErr)
	}

	in := &s3.GetObjectInput{
		Bucket:                     new(bucket),
		Key:                        new(input.Key),
		PartNumber:                 input.PartNumber,
		Range:                      input.Range,
		ResponseCacheControl:       input.ResponseCacheControl,
		ResponseContentDisposition: input.ResponseContentDisposition,
		ResponseContentEncoding:    input.ResponseContentEncoding,
		ResponseContentLanguage:    input.ResponseContentLanguage,
		ResponseContentType:        input.ResponseContentType,
		ResponseExpires:            input.ResponseExpires,
		VersionId:                  input.Version,
	}

	res, err := p.client.GetObject(ctx, in)
	if err != nil {
		return nil, internal.Errorf("s3/provider", "get: %w", wrapS3Error(err))
	}

	return &storage.Object{
		Body: res.Body,
		ObjectInfo: storage.ObjectInfo{
			Size:               valueOf(res.ContentLength),
			ContentType:        valueOf(res.ContentType),
			LastModified:       valueOf(res.LastModified),
			Version:            valueOf(res.VersionId),
			Metadata:           res.Metadata,
			CacheControl:       valueOf(res.CacheControl),
			ContentDisposition: valueOf(res.ContentDisposition),
			ContentEncoding:    valueOf(res.ContentEncoding),
			ContentLanguage:    valueOf(res.ContentLanguage),
			Expires:            valueOf(res.ExpiresString),
			ETag:               unquoteETag(res.ETag),
		},
	}, nil
}

// DeleteObject deletes an object, or one version of it when Version is set,
// and returns the version S3 reports, such as a delete marker's. Deleting a
// missing key succeeds; a missing bucket is storage.ErrBucketNotFound.
func (p *Provider) DeleteObject(
	ctx context.Context,
	input *storage.DeleteObjectInput,
) (*storage.ObjectInfo, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf("s3/provider", "delete: %w", storage.ErrInputRequired)
	}

	bucket, bucketErr := internal.ValidateInput(&input.BaseObjectInput, p.config.Storage.Bucket)
	if bucketErr != nil {
		return nil, internal.Errorf("s3/provider", "delete: %w", bucketErr)
	}

	in := &s3.DeleteObjectInput{
		Bucket:    new(bucket),
		Key:       new(input.Key),
		VersionId: input.Version,
	}

	res, err := p.client.DeleteObject(ctx, in)
	if err != nil {
		return nil, internal.Errorf("s3/provider", "delete: %w", wrapS3Error(err))
	}

	return &storage.ObjectInfo{
		Version: valueOf(res.VersionId),
	}, nil
}

// Client returns the S3 client the Provider uses, for operations the Provider
// does not wrap. It returns nil for a nil or unconstructed Provider.
func (p *Provider) Client() *s3.Client {
	if p == nil {
		return nil
	}
	return p.client
}

// checkReady returns storage.ErrNotInitialized unless p was built by
// NewProvider. It is safe to call on a nil pointer.
func (p *Provider) checkReady() error {
	if p == nil || p.client == nil || p.uploader == nil || p.config == nil {
		return internal.Errorf("s3/provider", "%w", storage.ErrNotInitialized)
	}
	return nil
}
