package storage

import "errors"

// The sentinel errors every backend of the module shares, as described in
// the package documentation. A backend's errors start with its own
// service name and the operation, and wrap one of these.
var (
	// ErrNilInput indicates that a method was given a nil input.
	ErrNilInput = errors.New("input is nil")
	// ErrInvalidKey indicates that an object key breaks the key rules.
	ErrInvalidKey = errors.New("invalid key")
	// ErrInvalidBucket indicates that a bucket name breaks the bucket
	// rules, or that an input names no bucket and none is configured.
	ErrInvalidBucket = errors.New("invalid bucket")
	// ErrInvalidMetadata indicates that the user metadata of a write breaks
	// the metadata rules.
	ErrInvalidMetadata = errors.New("invalid metadata")
	// ErrInvalidHeader indicates that a header field of a write, or a
	// response header of a presigned download, breaks the header rules.
	ErrInvalidHeader = errors.New("invalid header")
	// ErrInvalidCondition indicates that a WriteCondition cannot be
	// evaluated.
	ErrInvalidCondition = errors.New("invalid condition")
	// ErrInvalidRange indicates that a Range is not one byte range.
	ErrInvalidRange = errors.New("invalid range")
	// ErrInvalidArgument indicates that a limit, a length, a part number, a
	// version, a listing argument or a presign TTL is out of its bounds.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrInvalidToken indicates that a continuation token is empty, or is
	// not one the backend issued for the same listing.
	ErrInvalidToken = errors.New("invalid continuation token")
	// ErrContentLengthMismatch indicates that a body yielded more or fewer
	// bytes than its ContentLength.
	ErrContentLengthMismatch = errors.New("content length mismatch")
	// ErrNotSupported indicates that an input asks for a feature the
	// backend does not have, such as a Version on a backend that does not
	// version objects.
	ErrNotSupported = errors.New("not supported")
	// ErrObjectNotFound indicates that no object is stored under the key,
	// or that its bucket does not exist.
	ErrObjectNotFound = errors.New("object not found")
	// ErrVersionNotFound indicates that the object exists but not the
	// version asked for. It comes with ErrObjectNotFound.
	ErrVersionNotFound = errors.New("version not found")
	// ErrBucketNotFound indicates that the bucket does not exist.
	ErrBucketNotFound = errors.New("bucket not found")
	// ErrBucketNotEmpty indicates that DeleteBucket found the bucket
	// holding objects.
	ErrBucketNotEmpty = errors.New("bucket not empty")
	// ErrBucketExists indicates that CreateBucket found the name taken by
	// another owner.
	ErrBucketExists = errors.New("bucket exists")
	// ErrAccessDenied indicates that the backend refused the credentials,
	// or their permission for the request.
	ErrAccessDenied = errors.New("access denied")
	// ErrPreconditionFailed indicates that a WriteCondition did not hold
	// when the write would have committed.
	ErrPreconditionFailed = errors.New("precondition failed")
	// ErrRangeNotSatisfiable indicates that a Range selects no byte of the
	// object.
	ErrRangeNotSatisfiable = errors.New("range not satisfiable")
	// ErrBackend indicates that the backend failed for a reason no other
	// sentinel names, such as an I/O error or a request that got no reply.
	ErrBackend = errors.New("backend failed")
	// ErrNotInitialized indicates that a method was called before the
	// backend's first successful Init.
	ErrNotInitialized = errors.New("service not initialized")
	// ErrAlreadyInitialized indicates that Init was called on a backend
	// that is initialized.
	ErrAlreadyInitialized = errors.New("service already initialized")
	// ErrClosed indicates that a method was called after Close, before
	// Init succeeded again, or that Close ended the call.
	ErrClosed = errors.New("service closed")
	// ErrNilLogger indicates that a backend's constructor was given a nil
	// logger.
	ErrNilLogger = errors.New("logger is nil")
	// ErrNilOption indicates that a backend's constructor was given a nil
	// option.
	ErrNilOption = errors.New("option is nil")
	// ErrNilConfig indicates that WithConfig was given a nil
	// configuration.
	ErrNilConfig = errors.New("config is nil")
	// ErrNoConfigProvider indicates that there is neither a configuration
	// given to WithConfig nor a ConfigProvider to bind one from.
	ErrNoConfigProvider = errors.New("no config provider")
	// ErrInvalidConfig indicates that the configuration failed to bind,
	// failed its validate tags, or broke one of the backend's rules.
	ErrInvalidConfig = errors.New("invalid config")
)
