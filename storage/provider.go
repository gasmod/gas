package storage

import (
	"context"
	"io"
	"time"
)

// Provider is an object store: it creates, describes, lists and deletes
// buckets, and writes, reads, describes, lists and deletes the objects
// they hold, as described in the package documentation. Every backend of
// the module implements it, and is safe for concurrent use.
type Provider interface {
	// CreateBucket creates a bucket. Creating a bucket the caller owns
	// already is not an error.
	CreateBucket(ctx context.Context, in *CreateBucketInput) error
	// HeadBucket describes a bucket, or returns ErrBucketNotFound.
	HeadBucket(ctx context.Context, in *HeadBucketInput) (*Bucket, error)
	// DeleteBucket deletes an empty bucket.
	DeleteBucket(ctx context.Context, in *DeleteBucketInput) error
	// ListBuckets returns one page of the caller's buckets, in the byte
	// order of their names.
	ListBuckets(ctx context.Context, in *ListBucketsInput) (*ListBucketsOutput, error)
	// HeadObject describes an object without its contents.
	HeadObject(ctx context.Context, in *HeadObjectInput) (*ObjectInfo, error)
	// PutObject writes an object, replacing the one stored under its key,
	// if any, at once and whole.
	PutObject(ctx context.Context, in *PutObjectInput) (*PutObjectOutput, error)
	// GetObject returns an object with a reader of its contents, which the
	// caller closes.
	GetObject(ctx context.Context, in *GetObjectInput) (*Object, error)
	// DeleteObject deletes an object. Deleting a key that holds none is
	// not an error.
	DeleteObject(ctx context.Context, in *DeleteObjectInput) error
	// ListObjects returns one page of the objects of a bucket, in the byte
	// order of their keys.
	ListObjects(ctx context.Context, in *ListObjectsInput) (*ListObjectsOutput, error)
}

// CreateBucketInput names the bucket CreateBucket creates.
type CreateBucketInput struct {
	// Bucket is the name of the bucket. The configured bucket is not a
	// default.
	Bucket string
}

// HeadBucketInput names the bucket HeadBucket describes.
type HeadBucketInput struct {
	// Bucket is the name of the bucket. The configured bucket is not a
	// default.
	Bucket string
}

// DeleteBucketInput names the bucket DeleteBucket deletes.
type DeleteBucketInput struct {
	// Bucket is the name of the bucket. The configured bucket is not a
	// default.
	Bucket string
}

// Bucket describes a bucket.
type Bucket struct {
	// CreationDate is when the bucket was created, in UTC to the second.
	// ListBuckets sets it; HeadBucket leaves it zero.
	CreationDate time.Time
	// Name is the name of the bucket.
	Name string
	// Region is the region the bucket is in, or empty on a backend that has
	// no regions.
	Region string
}

// ListBucketsInput selects the page of buckets ListBuckets returns. The
// zero value selects the first page of every bucket.
type ListBucketsInput struct {
	// ContinuationToken resumes a listing after the page whose
	// NextContinuationToken it is. Nil starts at the first bucket.
	ContinuationToken *string
	// MaxBuckets is the most buckets the page holds, 1 to MaxListBuckets.
	// Nil is MaxListBuckets.
	MaxBuckets *int32
	// Prefix limits the listing to the buckets whose names start with it.
	Prefix *string
}

// ListBucketsOutput is one page of buckets.
type ListBucketsOutput struct {
	// NextContinuationToken resumes the listing after this page. It is
	// empty on the last page, and only there.
	NextContinuationToken string
	// Buckets are the buckets of the page, in the byte order of their
	// names.
	Buckets []Bucket
}

// BaseObjectInput names an object. Every object operation's input embeds
// it.
type BaseObjectInput struct {
	// Bucket is the bucket of the object. Nil is the configured bucket; a
	// pointer to an empty name is not.
	Bucket *string
	// Key is the key of the object.
	Key string
}

// HeadObjectInput names the object HeadObject describes, and what part of
// it.
//
//nolint:govet // fieldalignment: the embedded fields come first, as embeddedstructfieldcheck wants.
type HeadObjectInput struct {
	BaseObjectInput

	// PartNumber selects one part of an object uploaded in parts, from 1,
	// on a backend that stores parts.
	PartNumber *int32
	// Range selects a range of the object's bytes, as the value of an HTTP
	// Range header holding one byte range.
	Range *string
	// Version selects a version of the object, on a backend that keeps
	// versions, instead of the latest.
	Version *string
}

// GetObjectInput names the object GetObject returns, and what part of it,
// as HeadObjectInput does.
type GetObjectInput struct {
	HeadObjectInput
}

// PutObjectInput describes the object PutObject writes.
//
//nolint:govet // fieldalignment: the embedded fields come first, as embeddedstructfieldcheck wants.
type PutObjectInput struct {
	BaseObjectInput
	ObjectHeaders
	WriteCondition

	// Body yields the contents. Nil is an empty object. It may be any
	// reader, including one that cannot seek or whose length is unknown.
	Body io.Reader
	// ContentLength is the length of Body in bytes, when it is known. Nil
	// is unknown. When it is set, Body must yield exactly that many bytes.
	ContentLength *int64
}

// PutObjectOutput is what PutObject reports of the object it wrote.
type PutObjectOutput struct {
	// ETag identifies the contents written, as ObjectInfo.ETag.
	ETag string
	// Version identifies the version written, or is empty on a backend
	// that does not keep versions.
	Version string
	// Size is the length of the contents written, in bytes.
	Size int64
}

// DeleteObjectInput names the object DeleteObject deletes.
//
//nolint:govet // fieldalignment: the embedded fields come first, as embeddedstructfieldcheck wants.
type DeleteObjectInput struct {
	BaseObjectInput

	// Version deletes one version of the object, on a backend that keeps
	// versions, instead of the latest.
	Version *string
}

// WriteCondition makes a write depend on the object it would replace. The
// zero value writes whatever is stored. At most one field is set.
type WriteCondition struct {
	// IfMatch writes only over an object whose ETag is this one, quoted or
	// not.
	IfMatch *string
	// IfNoneMatch writes only when no object is stored under the key. It
	// must be `*`.
	IfNoneMatch *string
}

// ListObjectsInput selects the page of objects ListObjects returns, as
// S3's ListObjectsV2 does. Every field is optional.
type ListObjectsInput struct {
	// Bucket is the bucket to list. Nil is the configured bucket, as in
	// BaseObjectInput.
	Bucket *string
	// ContinuationToken resumes a listing after the page whose
	// NextContinuationToken it is. Nil starts at the first key.
	ContinuationToken *string
	// Delimiter rolls up the keys that hold it after Prefix into common
	// prefixes.
	Delimiter *string
	// MaxKeys is the most objects and common prefixes the page holds
	// together, 1 to MaxListKeys. Nil is MaxListKeys.
	MaxKeys *int32
	// Prefix limits the listing to the keys that start with it.
	Prefix *string
	// StartAfter starts the listing after this key, which need not exist.
	StartAfter *string
}

// ListObjectsOutput is one page of objects.
type ListObjectsOutput struct {
	// NextContinuationToken resumes the listing after this page. It is
	// empty on the last page, and only there.
	NextContinuationToken string
	// Objects are the objects of the page, in the byte order of their
	// keys.
	Objects []ObjectSummary
	// CommonPrefixes are the prefixes Delimiter rolled keys up into, in
	// byte order. Each is listed once across the pages of a listing.
	CommonPrefixes []string
	// IsTruncated reports whether another page follows.
	IsTruncated bool
}
