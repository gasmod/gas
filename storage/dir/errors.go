package dir

import (
	"context"
	"errors"
	"fmt"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir/internal/vfs"
	"github.com/gasmod/gas/storage/internal/lifecycle"
)

// The sentinel errors of the dir store, as described in the package
// documentation.
var (
	// ErrNilRoot indicates that WithRoot was given a nil root.
	ErrNilRoot = errors.New("root is nil")
	// ErrCorrupt indicates an object the store cannot read: its metadata
	// file does not decode, holds another key, or names a data file that
	// is gone or not a regular file. An error that wraps it wraps
	// storage.ErrBackend too.
	ErrCorrupt = vfs.ErrCorrupt
	// ErrUnnamedBucket indicates a bucket whose name record is missing or
	// holds a name that does not hash to the bucket, so that the bucket
	// cannot be named. Only the skip handler gets it.
	ErrUnnamedBucket = vfs.ErrUnnamed
	// ErrOldLayout indicates that the directory holds buckets written by
	// an earlier version of the store, whose layout it does not read. An
	// error that wraps it wraps storage.ErrBackend too.
	ErrOldLayout = vfs.ErrOldLayout
)

// The reasons the store refuses what it does not keep.
var (
	// errNoVersions reports a Version on a store that keeps none.
	errNoVersions = fmt.Errorf("%w: the store keeps no versions", storage.ErrNotSupported)
	// errNoParts reports a PartNumber on a store that keeps no parts.
	errNoParts = fmt.Errorf("%w: the store keeps no parts", storage.ErrNotSupported)
	// errMissingBucket is a read of an object whose bucket does not
	// exist.
	errMissingBucket = fmt.Errorf("%w: %w", storage.ErrObjectNotFound, storage.ErrBucketNotFound)
)

// opError returns err as the failure of op, after ServiceName.
func opError(op string, err error) error {
	return fmt.Errorf("%s: %s: %w", ServiceName, op, err)
}

// isRead marks a failure of a read, of which a missing bucket is a
// missing object too.
const isRead = true

// failure returns what a call that began on sess with ctx reports for
// err, an error of the vfs package: storage.ErrClosed when Close canceled
// the call, ctx's error when ctx ended it, and else the error classified,
// as classify does.
func failure(
	ctx context.Context,
	sess *lifecycle.Session[*session],
	err error,
	read bool,
) error {
	switch {
	case sess.Context().Err() != nil:
		return storage.ErrClosed
	case ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
		return ctx.Err() //nolint:wrapcheck // the method adds the name and the operation.
	}

	return classify(err, read)
}

// classify returns err, an error of the vfs package, with the sentinel of
// the storage package its failure stands for, then the errors of its
// cleanup, if any. An error of the body is returned as the body's reader
// returned it.
func classify(err error, read bool) error {
	j, ok := err.(*vfs.JoinedError) //nolint:errorlint // a JoinedError at the top only.
	if !ok {
		return sentinelOf(err, read)
	}

	out := sentinelOf(j.Errs[0], read)
	for _, e := range j.Errs[1:] {
		out = fmt.Errorf("%w; %w", out, e)
	}

	return out
}

// sentinelOf returns err, one error of the vfs package, as the store
// reports it: a classified error as the bare sentinel, and any other as
// storage.ErrBackend and its reason.
func sentinelOf(err error, read bool) error {
	if be, ok := errors.AsType[*vfs.BodyError](err); ok {
		return be.Err
	}

	switch {
	case errors.Is(err, vfs.ErrPreconditionFailed):
		return storage.ErrPreconditionFailed
	case errors.Is(err, vfs.ErrBucketNotEmpty):
		return storage.ErrBucketNotEmpty
	case errors.Is(err, vfs.ErrBucketNotFound) && read:
		return errMissingBucket
	case errors.Is(err, vfs.ErrBucketNotFound):
		return storage.ErrBucketNotFound
	case errors.Is(err, vfs.ErrObjectNotFound):
		return storage.ErrObjectNotFound
	}

	return fmt.Errorf("%w: %w", storage.ErrBackend, err)
}
