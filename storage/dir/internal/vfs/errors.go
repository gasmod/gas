package vfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// The sentinels below classify the errors of the package so that the dir
// backend can map them to its own. Each is wrapped only where its
// condition holds; every other error is an I/O failure.
var (
	// ErrBucketNotFound indicates that a directory of the bucket does not
	// exist.
	ErrBucketNotFound = errors.New("bucket not found")
	// ErrUnnamed indicates that a bucket's name record is missing or holds
	// a name that does not hash to the bucket, so the bucket cannot be
	// named.
	ErrUnnamed = errors.New("bucket cannot be named")
	// ErrBucketNotEmpty indicates that DeleteBucket found an object in the
	// bucket.
	ErrBucketNotEmpty = errors.New("bucket not empty")
	// ErrPreconditionFailed indicates that the condition of CreateObject
	// did not hold, so nothing was written.
	ErrPreconditionFailed = errors.New("precondition failed")
	// ErrObjectNotFound indicates that the object has no metadata file.
	ErrObjectNotFound = errors.New("object not found")
	// ErrCorrupt indicates that the object's metadata exists but cannot be
	// used: it is not a regular file, is too large, does not decode, holds
	// another key, breaks a rule of the storage package, names no valid
	// data file, or names a data file that is missing or not a regular
	// file. It never means that the object does not exist. It also marks
	// an index entry whose key, or a name record whose bucket name, breaks
	// those rules, or a name record that is too large.
	ErrCorrupt = errors.New("object cannot be read")
	// ErrOldLayout indicates that the root holds buckets but no layout
	// marker: they were written by an earlier version of the package,
	// whose layout this one does not read.
	ErrOldLayout = errors.New("the root holds buckets of an earlier layout")
	// ErrForeign indicates a file in a directory of the package that the
	// package did not write.
	ErrForeign = errors.New("file this backend did not write")

	// errNotLocked reports an Unlock or a Release of a name the
	// ObjectsLock does not hold.
	errNotLocked = errors.New("not locked")
	// errNotDir reports a directory of a bucket that is something else.
	errNotDir = errors.New("not a directory")
	// errNotRegular reports a file that is something else.
	errNotRegular = errors.New("not a regular file")
	// errDataFileGone reports metadata that names a data file that is not
	// there.
	errDataFileGone = errors.New("data file is gone")
	// errLayoutMarker reports a layout marker that holds another layout.
	errLayoutMarker = errors.New("layout marker holds another layout")
)

// BodyError is the error of reading a body that CreateObject was given,
// as its reader returned it. Its text is the reader's own.
type BodyError struct {
	// Err is the reader's error.
	Err error
}

// Error returns the reader's error text.
func (e *BodyError) Error() string { return e.Err.Error() }

// Unwrap returns the reader's error.
func (e *BodyError) Unwrap() error { return e.Err }

// cause returns the error an os call failed with, without the path or the
// paths the call quotes: the errno, or os.Root's own reason. Paths under a
// root are hashes of bucket names and keys, which errors never hold.
func cause(err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err
	}

	if le, ok := errors.AsType[*os.LinkError](err); ok {
		return le.Err
	}

	return err
}

// ioErr returns err, an error of an os call, as the failure of what,
// without the path the call quotes.
func ioErr(what string, err error) error {
	return fmt.Errorf("%s: %w", what, cause(err))
}

// JoinedError is errors that happened together: the first is the failure, and
// the others failed as the call cleaned up after it.
type JoinedError struct {
	// Errs are the errors, the failure first.
	Errs []error
}

// Error returns the texts of the errors, `; ` apart.
func (j *JoinedError) Error() string {
	texts := make([]string, len(j.Errs))
	for i, e := range j.Errs {
		texts[i] = e.Error()
	}

	return strings.Join(texts, "; ")
}

// Unwrap returns the errors.
func (j *JoinedError) Unwrap() []error { return j.Errs }

// joinErrs returns the errors that are not nil, the errors of a JoinedError
// among them, as one: nil for none, the error itself for one, and else a
// JoinedError.
func joinErrs(errs ...error) error {
	var out []error

	for _, e := range errs {
		if j, ok := e.(*JoinedError); ok { //nolint:errorlint // a Joined at the top only.
			out = append(out, j.Errs...)

			continue
		}

		if e != nil {
			out = append(out, e)
		}
	}

	switch len(out) {
	case 0:
		return nil
	case 1:
		return out[0]
	default:
		return &JoinedError{Errs: out}
	}
}
