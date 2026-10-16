package storage

import (
	"context"
	"io"
	"time"
)

// Provider is an object store: it creates buckets and writes, reads,
// inspects, and deletes objects. Every method validates its input before
// contacting the backend and returns the sentinel errors in this package,
// wrapped with the backend and operation; see the package documentation for
// which sentinel each case produces. Implementations are safe for concurrent
// use.
type Provider interface {
	// CreateBucket creates the bucket named in input. Creating a bucket the
	// caller already owns succeeds; ErrBucketExists means the name is taken
	// by someone else.
	CreateBucket(ctx context.Context, input *CreateBucketInput) (*BucketInfo, error)

	// HeadObject returns the description of an object without its contents.
	// A missing object is ErrNotFound.
	HeadObject(ctx context.Context, input *HeadObjectInput) (*ObjectInfo, error)
	// PutObject writes an object, replacing any object stored under the same
	// key, and returns what the backend reports about the write.
	PutObject(ctx context.Context, input *PutObjectInput) (*ObjectInfo, error)
	// GetObject returns an object and a reader for its contents, which the
	// caller must close. A missing key is ErrKeyNotFound.
	GetObject(ctx context.Context, input *GetObjectInput) (*Object, error)
	// DeleteObject deletes an object. Deleting a missing key succeeds.
	DeleteObject(ctx context.Context, input *DeleteObjectInput) (*ObjectInfo, error)
}

// CreateBucketInput names the bucket CreateBucket creates.
type CreateBucketInput struct {
	// Bucket is the name of the bucket to create. It must be a valid S3
	// bucket name; the configured bucket is not a fallback.
	Bucket string
}

// BucketInfo describes a bucket CreateBucket created. Fields the backend does
// not report are empty.
type BucketInfo struct {
	// ID identifies the bucket, such as its ARN on S3.
	ID string
	// Location is where the backend placed the bucket, such as the Location
	// header S3 returns.
	Location string
}

// BaseObjectInput identifies an object. It is embedded in every object
// operation's input.
type BaseObjectInput struct {
	// Bucket is the bucket that holds the object. Nil selects the bucket the
	// backend was configured with; a non-nil pointer to an empty string is
	// invalid, not a fallback.
	Bucket *string
	// Key is the object key. It must not be empty; see the package
	// documentation for the other rules.
	Key string
}

// HeadObjectInput identifies the object HeadObject describes. The optional
// fields map to the S3 HeadObject request parameters of the same name;
// backends that do not support one ignore it.
type HeadObjectInput struct {
	// PartNumber selects one part of a multipart object.
	PartNumber *int32
	// Range selects a byte range, in HTTP Range header syntax.
	Range *string
	// ResponseCacheControl overrides the Cache-Control response header.
	ResponseCacheControl *string
	// ResponseContentDisposition overrides the Content-Disposition response
	// header.
	ResponseContentDisposition *string
	// ResponseContentEncoding overrides the Content-Encoding response header.
	ResponseContentEncoding *string
	// ResponseContentLanguage overrides the Content-Language response header.
	ResponseContentLanguage *string
	// ResponseContentType overrides the Content-Type response header.
	ResponseContentType *string
	// ResponseExpires overrides the Expires response header.
	ResponseExpires *time.Time
	// Version selects a version of the object instead of the latest.
	Version *string
	BaseObjectInput
}

// PutObjectInput describes the object PutObject writes.
type PutObjectInput struct {
	// Body supplies the contents. Nil writes an empty object. It may be any
	// reader, including one that cannot seek or whose length is unknown.
	Body io.Reader
	// ContentLength is the length of Body in bytes, when known. It is a
	// hint: the s3 backend sends the length it actually reads and uses this
	// only to size multipart uploads.
	ContentLength *int64
	// ContentType is the object's media type.
	ContentType *string
	// Expires is stored as the object's Expires header.
	Expires *time.Time
	// Metadata is user metadata stored with the object.
	Metadata map[string]string
	BaseObjectInput
}

// GetObjectInput identifies the object GetObject returns. It takes the same
// parameters as HeadObjectInput.
type GetObjectInput struct {
	HeadObjectInput
}

// DeleteObjectInput identifies the object DeleteObject deletes.
type DeleteObjectInput struct {
	// Version deletes one version of the object instead of the latest.
	Version *string
	BaseObjectInput
}
