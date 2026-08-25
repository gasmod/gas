package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/gasmod/gas"
	storage "github.com/gasmod/gas/storage"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const serviceName = "gas/storage/s3"

// Service is an S3-backed storage implementing gas.Service and
// gas.StorageProvider.
type Service struct {
	client    *s3.Client
	presigner *s3.PresignClient
	uploader  *transfermanager.Client
	cfg       *Config
	logger    gas.Logger

	cfgProvider          gas.ConfigProvider
	customConfigProvided bool
	closed               atomic.Bool
}

var _ gas.Service = (*Service)(nil)
var _ gas.StorageProvider = (*Service)(nil)
var _ gas.ReadyReporter = (*Service)(nil)

// Option configures a Service.
type Option func(*Service)

// WithConfig sets a custom configuration.
func WithConfig(cfg *Config) Option {
	return func(s *Service) {
		s.cfg = cfg
		s.customConfigProvided = true
	}
}

// New captures options and returns a DI-injectable constructor.
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) *Service {
	return func(cfgProvider gas.ConfigProvider, logger gas.Logger) *Service {
		s := &Service{
			cfg:         DefaultConfig(),
			cfgProvider: cfgProvider,
			logger:      logger.With().Str("service", serviceName).Logger(),
		}
		for _, opt := range opts {
			opt(s)
		}
		return s
	}
}

// Name returns the service identifier.
func (s *Service) Name() string { return serviceName }

// Init validates the configuration and creates the S3 client.
func (s *Service) Init() error {
	if !s.customConfigProvided {
		if s.cfgProvider != nil {
			if err := s.cfgProvider.Bind(s.cfg); err != nil {
				return fmt.Errorf("%s: config binding: %w", s.Name(), err)
			}
		}
	}

	if err := s.cfg.Validate(); err != nil {
		s.logger.Error("invalid storage configuration").Err("error", err).Send()
		return err
	}

	if err := s.connect(); err != nil {
		return err
	}

	s.closed.Store(false)
	s.logger.Info("s3 storage initialized").
		Str("region", s.cfg.Storage.Region).
		Str("bucket", s.cfg.Storage.Bucket).
		Send()
	return nil
}

func (s *Service) connect() error {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(s.cfg.Storage.Region),
	}

	if s.cfg.Storage.AccessKeyID != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				s.cfg.Storage.AccessKeyID,
				s.cfg.Storage.SecretAccessKey,
				"",
			),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		s.logger.Error("failed to load AWS config").Err("error", err).Send()
		return fmt.Errorf("%s: load AWS config: %w", s.Name(), err)
	}

	s.client = s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if s.cfg.Storage.Endpoint != "" {
			o.BaseEndpoint = new(s.cfg.Storage.Endpoint)
			o.UsePathStyle = true
		}
	})
	s.presigner = s3.NewPresignClient(s.client)
	// The transfer manager handles arbitrary readers, including unseekable
	// streams of unknown length (multipart under the hood; no overall
	// Content-Length required).
	s.uploader = transfermanager.New(s.client, func(o *transfermanager.Options) {
		o.RequestChecksumCalculation = awsCfg.RequestChecksumCalculation
	})
	return nil
}

// CheckReady reports whether the service can accept traffic. When a default
// bucket is configured, it issues a HeadBucket against it to verify that
// credentials are valid and the bucket is reachable from this pod.
//
// IAM requirement: the caller's principal MUST have the s3:ListBucket
// permission on the configured bucket. Without it, HeadBucket returns 403
// and the readiness probe will fail, preventing the pod from receiving
// traffic. If your deployment scopes S3 permissions narrowly (e.g. only
// s3:GetObject / s3:PutObject on specific prefixes), add s3:ListBucket on
// the bucket resource to the policy.
//
// When no default bucket is configured (per-call bucket mode), readiness
// succeeds as soon as Init has completed — there is no single bucket to
// probe, and reachability is the caller's responsibility.
func (s *Service) CheckReady(ctx context.Context) error {
	if s.closed.Load() {
		return storage.ErrClosed
	}
	if s.client == nil {
		return fmt.Errorf("%s: not initialized", s.Name())
	}
	bucket := s.cfg.Storage.Bucket
	if bucket == "" {
		return nil
	}
	if _, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: new(bucket),
	}); err != nil {
		return fmt.Errorf("%s: bucket %q not reachable (verify s3:ListBucket permission): %w", s.Name(), bucket, err)
	}
	return nil
}

// Close marks the service as closed.
func (s *Service) Close() error {
	s.closed.Store(true)
	s.logger.Info("s3 storage closed").Send()
	return nil
}

// resolveBucket returns the bucket from opts, falling back to the configured
// default. Returns ErrBucketRequired when neither is set.
func (s *Service) resolveBucket(bucket string) (string, error) {
	if bucket == "" {
		bucket = s.cfg.Storage.Bucket
	}
	if bucket == "" {
		return "", storage.ErrBucketRequired
	}
	return bucket, nil
}

// Upload uploads an object to S3.
func (s *Service) Upload(ctx context.Context, key string, data io.Reader, opts ...gas.StorageOption) error {
	if s.closed.Load() {
		return storage.ErrClosed
	}

	bucket, contentType, metadata := gas.ApplyStorageOptions(opts)
	bucket, err := s.resolveBucket(bucket)
	if err != nil {
		return fmt.Errorf("%s: upload %q: %w", s.Name(), key, err)
	}

	input := &transfermanager.UploadObjectInput{
		Bucket: new(bucket),
		Key:    new(key),
		Body:   data,
	}
	if contentType != "" {
		input.ContentType = new(contentType)
	}
	if len(metadata) > 0 {
		input.Metadata = metadata
	}

	// Use the transfer manager rather than a single PutObject so that unseekable
	// / unknown-length bodies (e.g. a streaming io.Pipe) upload without an
	// overall Content-Length. This also lifts PutObject's 5GB limit.
	_, err = s.uploader.UploadObject(ctx, input)
	if err != nil {
		return fmt.Errorf("%s: upload %q: %w", s.Name(), key, err)
	}
	return nil
}

// Download downloads an object from S3. Returns storage.ErrKeyNotFound
// if the key does not exist.
func (s *Service) Download(ctx context.Context, key string, opts ...gas.StorageOption) (*gas.StorageObject, error) {
	if s.closed.Load() {
		return nil, storage.ErrClosed
	}

	bucket, _, _ := gas.ApplyStorageOptions(opts)
	bucket, err := s.resolveBucket(bucket)
	if err != nil {
		return nil, fmt.Errorf("%s: download %q: %w", s.Name(), key, err)
	}

	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: new(bucket),
		Key:    new(key),
	})
	if err != nil {
		if _, ok := errors.AsType[*types.NoSuchKey](err); ok {
			return nil, storage.ErrKeyNotFound
		}
		return nil, fmt.Errorf("%s: download %q: %w", s.Name(), key, err)
	}

	obj := &gas.StorageObject{
		Body: result.Body,
	}
	if result.ContentType != nil {
		obj.ContentType = *result.ContentType
	}
	if result.ContentLength != nil {
		obj.Size = *result.ContentLength
	}
	if result.Metadata != nil {
		obj.Metadata = result.Metadata
	}

	return obj, nil
}

// Delete deletes an object from S3.
func (s *Service) Delete(ctx context.Context, key string, opts ...gas.StorageOption) error {
	if s.closed.Load() {
		return storage.ErrClosed
	}

	bucket, _, _ := gas.ApplyStorageOptions(opts)
	bucket, err := s.resolveBucket(bucket)
	if err != nil {
		return fmt.Errorf("%s: delete %q: %w", s.Name(), key, err)
	}

	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: new(bucket),
		Key:    new(key),
	})
	if err != nil {
		return fmt.Errorf("%s: delete %q: %w", s.Name(), key, err)
	}
	return nil
}

// PresignDownloadURL generates a presigned GET URL for the specified object key,
// valid for the given expiry duration.
func (s *Service) PresignDownloadURL(ctx context.Context, key string, expiry time.Duration, opts ...gas.StorageOption) (string, error) {
	if s.closed.Load() {
		return "", storage.ErrClosed
	}

	bucket, _, _ := gas.ApplyStorageOptions(opts)
	bucket, err := s.resolveBucket(bucket)
	if err != nil {
		return "", fmt.Errorf("%s: presign %q: %w", s.Name(), key, err)
	}

	req, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: new(bucket),
		Key:    new(key),
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("%s: presign %q: %w", s.Name(), key, err)
	}
	return req.URL, nil
}

// PresignUploadURL generates a presigned PUT URL for the specified object key,
// valid for the given expiry duration.
func (s *Service) PresignUploadURL(ctx context.Context, key string, expiry time.Duration, opts ...gas.StorageOption) (string, error) {
	if s.closed.Load() {
		return "", storage.ErrClosed
	}

	bucket, contentType, metadata := gas.ApplyStorageOptions(opts)
	bucket, err := s.resolveBucket(bucket)
	if err != nil {
		return "", fmt.Errorf("%s: presign upload %q: %w", s.Name(), key, err)
	}

	input := &s3.PutObjectInput{
		Bucket: new(bucket),
		Key:    new(key),
	}
	if contentType != "" {
		input.ContentType = new(contentType)
	}
	if len(metadata) > 0 {
		input.Metadata = metadata
	}

	req, err := s.presigner.PresignPutObject(ctx, input, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("%s: presign upload %q: %w", s.Name(), key, err)
	}
	return req.URL, nil
}

// Head retrieves object metadata without downloading the body.
// Returns storage.ErrKeyNotFound if the key does not exist.
func (s *Service) Head(ctx context.Context, key string, opts ...gas.StorageOption) (*gas.ObjectInfo, error) {
	if s.closed.Load() {
		return nil, storage.ErrClosed
	}

	bucket, _, _ := gas.ApplyStorageOptions(opts)
	bucket, err := s.resolveBucket(bucket)
	if err != nil {
		return nil, fmt.Errorf("%s: head %q: %w", s.Name(), key, err)
	}

	result, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: new(bucket),
		Key:    new(key),
	})
	if err != nil {
		if _, ok := errors.AsType[*types.NotFound](err); ok {
			return nil, storage.ErrKeyNotFound
		}
		return nil, fmt.Errorf("%s: head %q: %w", s.Name(), key, err)
	}

	info := &gas.ObjectInfo{}
	if result.ContentType != nil {
		info.ContentType = *result.ContentType
	}
	if result.ContentLength != nil {
		info.Size = *result.ContentLength
	}
	if result.LastModified != nil {
		info.LastModified = *result.LastModified
	}
	if result.Metadata != nil {
		info.Metadata = result.Metadata
	}

	return info, nil
}

// Client returns the underlying S3 client for advanced operations.
func (s *Service) Client() *s3.Client {
	return s.client
}
