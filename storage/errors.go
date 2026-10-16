package storage

import "errors"

// Sentinel errors returned by every storage backend. Backends wrap them with
// the backend and operation that failed, so match them with errors.Is, never
// by comparing messages. Each sentinel is distinct: none matches another.
var (
	// ErrNotInitialized is returned by every Provider and Presigner method
	// called on a backend that was not built by its constructor, such as a
	// zero value.
	ErrNotInitialized = errors.New("storage: not initialized")

	// ErrInputRequired is returned when a method is called with a nil input.
	ErrInputRequired = errors.New("storage: input required")

	// ErrInvalidKey is returned when an object key is empty, longer than 1024
	// bytes, not valid UTF-8, "." after cleaning, or has "." as its last path
	// element, or is otherwise not accepted by the backend. Only "/"
	// separates path elements, on every platform, so a backslash is an
	// ordinary key byte. It is checked before the bucket and nothing is sent
	// to the backend.
	ErrInvalidKey = errors.New("storage: invalid key")

	// ErrKeyNotFound is returned by GetObject when the bucket exists but the
	// key does not. HeadObject reports a missing key as ErrNotFound instead,
	// and DeleteObject of a missing key succeeds.
	ErrKeyNotFound = errors.New("storage: key not found")

	// ErrVersionNotFound is returned by GetObject when the object exists but
	// the requested Version does not (S3's NoSuchVersion). It is s3 only: the
	// dir backend rejects any Version with ErrNotSupported. A HEAD response
	// carries no error code, so
	// on S3 HeadObject reports an unknown version as ErrNotFound instead. It
	// does not match ErrKeyNotFound or ErrNotFound. A malformed version ID is
	// not guaranteed to produce it: AWS may reject one with a 400 that maps
	// to no sentinel.
	ErrVersionNotFound = errors.New("storage: version not found")

	// ErrNotFound is returned by HeadObject when the object does not exist.
	// On S3 a HEAD response cannot tell a missing key from a missing bucket,
	// so the s3 backend also returns it for a missing bucket. It does not
	// match ErrKeyNotFound or ErrBucketNotFound.
	ErrNotFound = errors.New("storage: not found")

	// ErrInvalidBucket is returned when a bucket name is not a valid S3
	// bucket name: the explicit Bucket of an input, the bucket named in
	// CreateBucketInput, or the configured bucket when an input leaves
	// Bucket nil (including when no bucket is configured). The s3
	// constructors also return it for an invalid configured bucket. Nothing
	// is sent to the backend.
	ErrInvalidBucket = errors.New("storage: invalid bucket")

	// ErrBucketNotFound is returned by object operations when the bucket does
	// not exist (except by the s3 backend's HeadObject; see ErrNotFound).
	ErrBucketNotFound = errors.New("storage: bucket not found")

	// ErrBucketExists is returned by CreateBucket when the bucket name is
	// taken by someone else (S3's BucketAlreadyExists). Creating a bucket the
	// caller already owns is not an error. Only the s3 backend returns it:
	// every dir bucket belongs to the caller.
	ErrBucketExists = errors.New("storage: bucket already exists")

	// ErrInvalidRange is returned by GetObject and HeadObject when the Range
	// cannot be satisfied: its first byte is at or past the end of the
	// object, or it is the zero-length suffix "bytes=-0". S3 answers 416
	// InvalidRange. A Range header that is not a single valid byte range is
	// ignored instead, and the whole object is returned.
	ErrInvalidRange = errors.New("storage: invalid range")

	// ErrContentLengthMismatch is returned by PutObject when ContentLength is
	// set and Body yields a different number of bytes, fewer or more, or when
	// ContentLength is negative. Nothing is stored: an object already under
	// the key is left as it was.
	ErrContentLengthMismatch = errors.New("storage: content length mismatch")

	// ErrNotSupported is returned when an input asks for a feature the
	// backend does not implement. The dir backend returns it from HeadObject,
	// GetObject and DeleteObject when Version is set, because it does not
	// version objects, and from HeadObject and GetObject when PartNumber is
	// set, because it does not store objects in parts. Nothing is read or
	// changed.
	ErrNotSupported = errors.New("storage: not supported")

	// ErrInvalidOption is returned by a backend constructor given a nil
	// option. The message names the option's zero-based position in the
	// constructor's argument list.
	ErrInvalidOption = errors.New("storage: invalid option")

	// ErrInvalidTTL is returned by Presigner methods for a TTL that is
	// negative, under one second, or over seven days. Nothing is signed.
	ErrInvalidTTL = errors.New("storage: invalid TTL")
)
