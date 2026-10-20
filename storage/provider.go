package storage

import (
	"context"
	"io"
	"time"
)

// Provider is an object store: it creates and lists buckets and writes,
// reads, inspects, lists, and deletes objects. Every method validates its input before
// contacting the backend and returns the sentinel errors in this package,
// wrapped with the backend and operation; see the package documentation for
// which sentinel each case produces. Implementations are safe for concurrent
// use.
type Provider interface {
	// CreateBucket creates the bucket named in input. Creating a bucket the
	// caller already owns succeeds; ErrBucketExists means the name is taken
	// by someone else.
	CreateBucket(ctx context.Context, input *CreateBucketInput) (*Bucket, error)
	// HeadBucket reports whether a bucket exists and what the backend knows
	// about it. A missing bucket is ErrBucketNotFound, and one the caller may
	// not access is ErrAccessDenied.
	HeadBucket(ctx context.Context, input *HeadBucketInput) (*Bucket, error)
	// DeleteBucket deletes an empty bucket. A bucket that holds objects is
	// ErrBucketNotEmpty, and a missing bucket is ErrBucketNotFound: unlike
	// DeleteObject of a missing key, which succeeds, as on S3.
	DeleteBucket(ctx context.Context, input *DeleteBucketInput) error
	// ListBuckets returns one page of the caller's buckets, in name order.
	ListBuckets(ctx context.Context, input *ListBucketsInput) (*ListBucketsOutput, error)

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
	// ListObjects returns one page of a bucket's objects, in key order. A
	// missing bucket is ErrBucketNotFound.
	ListObjects(ctx context.Context, input *ListObjectsInput) (*ListObjectsOutput, error)
}

// CreateBucketInput names the bucket CreateBucket creates.
type CreateBucketInput struct {
	// Bucket is the name of the bucket to create. It must be a valid S3
	// bucket name; the configured bucket is not a fallback.
	Bucket string
}

// HeadBucketInput names the bucket HeadBucket describes.
type HeadBucketInput struct {
	// Bucket is the name of the bucket. It must be a valid S3 bucket name;
	// the configured bucket is not a fallback.
	Bucket string
}

// DeleteBucketInput names the bucket DeleteBucket deletes.
type DeleteBucketInput struct {
	// Bucket is the name of the bucket. It must be a valid S3 bucket name;
	// the configured bucket is not a fallback.
	Bucket string
}

// Bucket describes a bucket. CreateBucket, HeadBucket and ListBuckets
// return it; fields
// the backend does not report for an operation are empty, and each backend's
// documentation says which those are.
type Bucket struct {
	// CreationDate is when the bucket was created, as the backend reports
	// it.
	CreationDate time.Time
	// Name is the bucket name.
	Name string
	// ID identifies the bucket on the backend, such as its ARN on S3.
	ID string
	// Region is the region the bucket is in.
	Region string
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

// ObjectHeaders are the headers stored with an object. Each one a write sets
// reads back on ObjectInfo; a nil one is not stored.
type ObjectHeaders struct {
	// Metadata is user metadata stored with the object.
	Metadata map[string]string
	// CacheControl is stored as the object's Cache-Control header.
	CacheControl *string
	// ContentDisposition is stored as the object's Content-Disposition
	// header.
	ContentDisposition *string
	// ContentEncoding is stored as the object's Content-Encoding header. The
	// body is stored as given, not decoded or encoded.
	ContentEncoding *string
	// ContentLanguage is stored as the object's Content-Language header.
	ContentLanguage *string
	// ContentType is the object's media type.
	ContentType *string
	// Expires is stored as the object's Expires header, as text. A backend
	// may accept only the one form it can store unchanged, and return
	// ErrInvalidHeader for anything else; see each backend's documentation.
	Expires *string
}

// WriteCondition makes a write conditional on the object it would replace.
// The zero value writes unconditionally. At most one field may be set.
type WriteCondition struct {
	// IfMatch writes only when an object exists under the key and its ETag
	// is this one, quoted or not: another ETag, including the empty one of
	// an object stored by a backend version that recorded none, is
	// ErrPreconditionFailed, and a missing key is ErrKeyNotFound. An empty
	// value or "*" is ErrInvalidCondition.
	IfMatch *string
	// IfNoneMatch writes only when no object exists under the key, and is
	// ErrPreconditionFailed otherwise. It must be "*"; any other value is
	// ErrInvalidCondition.
	IfNoneMatch *string
}

// PutObjectInput describes the object PutObject writes.
type PutObjectInput struct {
	// Body supplies the contents. Nil writes an empty object. It may be any
	// reader, including one that cannot seek or whose length is unknown.
	Body io.Reader
	// ContentLength is the length of Body in bytes, when known. Nil means the
	// length is unknown. When it is set, Body must yield exactly that many
	// bytes: a shorter or longer body, or a negative length, fails with
	// ErrContentLengthMismatch and stores nothing, leaving any object already
	// under the key as it was. Zero with an empty or nil Body is valid. Body
	// is read to EOF, so one that blocks after exactly ContentLength bytes
	// without ending blocks PutObject.
	ContentLength *int64
	ObjectHeaders
	WriteCondition
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

// ListBucketsInput selects the page of buckets ListBuckets returns. Every
// field is optional; the zero value lists the first page of every bucket.
type ListBucketsInput struct {
	// ContinuationToken resumes a listing from the NextContinuationToken of
	// the previous page. A non-nil empty string is ErrInvalidContinuationToken.
	ContinuationToken *string
	// MaxBuckets is the most buckets the page holds, from 1 to 10000. Nil
	// means 10000. A value outside the range is ErrInvalidLimit.
	MaxBuckets *int32
	// Prefix limits the listing to bucket names that start with it.
	Prefix *string
}

// ListBucketsOutput is one page of buckets.
type ListBucketsOutput struct {
	// NextContinuationToken resumes the listing after this page. It is empty
	// on the last page, and only there.
	NextContinuationToken string
	// Buckets are the buckets of the page, in name order.
	Buckets []Bucket
	// Skipped counts the buckets this listing left out because the backend
	// could not read them, such as a dir bucket whose name record is
	// damaged. It covers every bucket, regardless of Prefix or the page, so
	// every page of one listing repeats it. The buckets returned are
	// correct. It is always 0 on s3.
	Skipped int
}

// ListObjectsInput selects the page of objects ListObjects returns. It
// follows S3 ListObjectsV2. Only Bucket resolves like an object input's;
// every other field is optional.
type ListObjectsInput struct {
	// Bucket is the bucket to list. Nil selects the configured bucket, as in
	// BaseObjectInput.
	Bucket *string
	// ContinuationToken resumes a listing from the NextContinuationToken of
	// the previous page. A non-nil empty string is
	// ErrInvalidContinuationToken.
	ContinuationToken *string
	// Delimiter groups keys: a key that holds Delimiter after Prefix is not
	// listed itself but rolled up into a common prefix, which runs from the
	// start of the key to the end of the first Delimiter after Prefix.
	Delimiter *string
	// MaxKeys is the most objects and common prefixes the page holds
	// together. Nil means 1000, a value over 1000 is treated as 1000, and a
	// value under 1 is ErrInvalidLimit.
	MaxKeys *int32
	// Prefix limits the listing to keys that start with it.
	Prefix *string
	// StartAfter starts the listing after this key, which need not exist.
	StartAfter *string
}

// ListObjectsOutput is one page of objects.
type ListObjectsOutput struct {
	// NextContinuationToken resumes the listing after this page. It is empty
	// on the last page, and only there, which is when IsTruncated is false.
	NextContinuationToken string
	// Objects are the objects of the page, in key order.
	Objects []ObjectSummary
	// CommonPrefixes are the prefixes keys were rolled up into by Delimiter,
	// in order. Each is listed once across all pages.
	CommonPrefixes []string
	// Skipped counts the objects this listing left out because the backend
	// could not read them, such as a dir object whose metadata is corrupt or
	// whose data file is gone. It covers the whole bucket, regardless of
	// Prefix, StartAfter, Delimiter or the page, so every page of one
	// listing repeats that count; a page also adds any of its own objects
	// found damaged while it was being built, so pages can differ when
	// objects are damaged during a listing. The objects returned are
	// correct. It is always 0 on s3.
	Skipped int
	// IsTruncated reports whether more objects or common prefixes follow.
	IsTruncated bool
}
