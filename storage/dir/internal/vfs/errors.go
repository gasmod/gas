package vfs

import "errors"

// The sentinels below classify vfs errors so the dir provider can map them
// to storage errors. Each is wrapped, together with its cause, only where the
// condition is true; every other error is plain I/O.
var (
	// ErrBucketNotFound reports that a directory of the bucket does not exist.
	ErrBucketNotFound = errors.New("bucket not found")

	// ErrBucketExists reports that CreateBucket found the bucket already
	// created.
	ErrBucketExists = errors.New("bucket already exists")

	// ErrBucketNotEmpty reports that DeleteBucket found an object in the
	// bucket.
	ErrBucketNotEmpty = errors.New("bucket not empty")

	// ErrPreconditionFailed reports that CreateObject's Condition did not
	// hold, so nothing was written.
	ErrPreconditionFailed = errors.New("precondition failed")

	// ErrObjectNotFound reports that the object has no metadata file.
	ErrObjectNotFound = errors.New("object not found")

	// ErrCorrupt reports that the object's metadata exists but cannot be
	// used: it is not a regular file, does not decode, names no valid data
	// file, or names a data file that is missing or not a regular file. It
	// never means the object does not exist.
	ErrCorrupt = errors.New("corrupt object")

	// ErrStaleData reports that CreateObject committed the new object but
	// could not remove the data file of the object it replaced.
	ErrStaleData = errors.New("previous data file not removed")

	// errNotLocked reports an Unlock or Release of a name the ObjectsLock
	// does not hold.
	errNotLocked = errors.New("not locked")
)
