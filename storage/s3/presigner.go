package s3

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal"
)

// TTL bounds for presigned URLs. AWS SigV4 query-string authentication
// accepts an X-Amz-Expires of 1 to 604800 seconds; defaultPresignTTL is the
// expiry signed when a TTL is nil or zero.
const (
	defaultPresignTTL = 15 * time.Minute
	minPresignTTL     = time.Second
	maxPresignTTL     = 7 * 24 * time.Hour
)

// Presigner implements storage.Presigner on S3. Build it with NewPresigner; a
// Presigner that was not, including a nil pointer, returns
// storage.ErrNotInitialized from every method.
type Presigner struct {
	s3Client      *s3.Client
	presignClient *s3.PresignClient
	config        *Config
}

var _ storage.Presigner = (*Presigner)(nil)

// PresignerOption configures a Presigner built by NewPresigner.
type PresignerOption func(*Presigner)

// WithPresignerS3Client makes the Presigner sign with a presign client built
// on client instead of a client built from configuration. It is ignored when
// WithPresignClient supplies a presign client. A nil client is ignored.
func WithPresignerS3Client(client *s3.Client) PresignerOption {
	return func(s *Presigner) { s.s3Client = client }
}

// WithPresignClient makes the Presigner sign with presigner. It takes
// precedence over WithPresignerS3Client. A nil presigner is ignored.
func WithPresignClient(presigner *s3.PresignClient) PresignerOption {
	return func(s *Presigner) { s.presignClient = presigner }
}

// NewPresigner returns a constructor for the DI container that builds a
// Presigner. The constructor takes the gas.ConfigProvider as its dependency
// (a nil one returns an error) and binds Config from it,
// fails with storage.ErrInvalidBucket when the configured bucket is set but
// invalid, applies opts, and builds a client from configuration when no
// option supplied one.
func NewPresigner(
	opts ...PresignerOption,
) func(cfgProvider gas.ConfigProvider) (*Presigner, error) {
	return func(cfgProvider gas.ConfigProvider) (*Presigner, error) {
		p := &Presigner{}

		cfg, err := internal.BindConfig[Config](cfgProvider)
		if err != nil {
			return nil, internal.Errorf("s3/presigner", "%w", err)
		}
		p.config = cfg

		if bk := p.config.Storage.Bucket; bk != "" && !internal.IsValidBucketName(bk) {
			return nil, internal.Errorf(
				"s3/presigner",
				"%w: %s",
				storage.ErrInvalidBucket,
				bk,
			)
		}

		for i, o := range opts {
			if o == nil {
				return nil, internal.Errorf(
					"s3/presigner",
					"%w: option %d is nil",
					storage.ErrInvalidOption,
					i,
				)
			}
			o(p)
		}

		if p.s3Client == nil && p.presignClient == nil {
			client, clientErr := newClient(p.config)
			if clientErr != nil {
				return nil, internal.Errorf("s3/presigner", "create S3 client: %w", clientErr)
			}
			p.s3Client = client
		}

		if p.presignClient == nil {
			p.presignClient = s3.NewPresignClient(p.s3Client)
		}

		return p, nil
	}
}

// PresignUploadURL returns a presigned PUT URL for the object in input.
// ContentLength and every header of ObjectHeaders that is set are signed, so
// the client must send matching headers; Body is ignored. Expires must be an
// IMF-fixdate, as for PutObject; anything else returns an error wrapping
// storage.ErrInvalidHeader and nothing is signed. A nil or zero TTL signs a
// 15-minute expiry; a TTL that is negative, under one second, or over seven
// days returns an error wrapping storage.ErrInvalidTTL. Fractional seconds
// are truncated.
func (p *Presigner) PresignUploadURL(
	ctx context.Context,
	input *storage.PresignUploadURLInput,
) (*storage.PresignURLResponse, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf("s3/presigner", "presign upload: %w", storage.ErrInputRequired)
	}

	bucket, bucketErr := internal.ValidateInput(&input.BaseObjectInput, p.config.Storage.Bucket)
	if bucketErr != nil {
		return nil, internal.Errorf("s3/presigner", "presign upload: %w", bucketErr)
	}

	ttl, ttlErr := presignTTL(input.TTL)
	if ttlErr != nil {
		return nil, internal.Errorf("s3/presigner", "presign upload: %w", ttlErr)
	}

	expires, expErr := parseExpires(input.Expires)
	if expErr != nil {
		return nil, internal.Errorf("s3/presigner", "presign upload: %w", expErr)
	}

	in := &s3.PutObjectInput{
		Bucket:             new(bucket),
		Key:                new(input.Key),
		ContentLength:      input.ContentLength,
		ContentType:        input.ContentType,
		CacheControl:       input.CacheControl,
		ContentDisposition: input.ContentDisposition,
		ContentEncoding:    input.ContentEncoding,
		ContentLanguage:    input.ContentLanguage,
		Metadata:           input.Metadata,
		Expires:            expires,
	}

	req, err := p.presignClient.PresignPutObject(ctx, in, s3.WithPresignExpires(ttl))
	if err != nil {
		return nil, internal.Errorf("s3/presigner", "presign upload: %w", err)
	}
	return &storage.PresignURLResponse{
		URL:          req.URL,
		Method:       req.Method,
		SignedHeader: req.SignedHeader,
	}, nil
}

// PresignDownloadURL returns a presigned GET URL for the object in input,
// with the Range, PartNumber, Version, and response header overrides it
// sets. TTL is handled as in PresignUploadURL.
func (p *Presigner) PresignDownloadURL(
	ctx context.Context,
	input *storage.PresignDownloadURLInput,
) (*storage.PresignURLResponse, error) {
	if err := p.checkReady(); err != nil {
		return nil, err
	}

	if input == nil {
		return nil, internal.Errorf(
			"s3/presigner",
			"presign download: %w",
			storage.ErrInputRequired,
		)
	}

	bucket, bucketErr := internal.ValidateInput(&input.BaseObjectInput, p.config.Storage.Bucket)
	if bucketErr != nil {
		return nil, internal.Errorf("s3/presigner", "presign download: %w", bucketErr)
	}

	ttl, ttlErr := presignTTL(input.TTL)
	if ttlErr != nil {
		return nil, internal.Errorf("s3/presigner", "presign download: %w", ttlErr)
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

	req, err := p.presignClient.PresignGetObject(ctx, in, s3.WithPresignExpires(ttl))
	if err != nil {
		return nil, internal.Errorf("s3/presigner", "presign download: %w", err)
	}
	return &storage.PresignURLResponse{
		URL:          req.URL,
		Method:       req.Method,
		SignedHeader: req.SignedHeader,
	}, nil
}

// PresignClient returns the presign client the Presigner signs with. It
// returns nil for a nil or unconstructed Presigner.
func (p *Presigner) PresignClient() *s3.PresignClient {
	if p == nil {
		return nil
	}
	return p.presignClient
}

// checkReady returns storage.ErrNotInitialized unless p was built by
// NewPresigner. It is safe to call on a nil pointer.
func (p *Presigner) checkReady() error {
	if p == nil || p.presignClient == nil || p.config == nil {
		return internal.Errorf("s3/presigner", "%w", storage.ErrNotInitialized)
	}
	return nil
}

// presignTTL returns the expiry to sign for ttl: defaultPresignTTL when ttl
// is nil or zero, and ttl itself when it lies within minPresignTTL and
// maxPresignTTL inclusive. Any other ttl returns an error wrapping
// storage.ErrInvalidTTL for the caller to prefix with its operation.
func presignTTL(ttl *time.Duration) (time.Duration, error) {
	if ttl == nil || *ttl == 0 {
		return defaultPresignTTL, nil
	}
	if *ttl < minPresignTTL || *ttl > maxPresignTTL {
		return 0, fmt.Errorf(
			"%w: %v is outside %v to %v",
			storage.ErrInvalidTTL,
			*ttl,
			minPresignTTL,
			maxPresignTTL,
		)
	}
	return *ttl, nil
}
