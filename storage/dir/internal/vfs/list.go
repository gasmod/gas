package vfs

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// ListKeys calls yield with each key within bounds that holds an object,
// in byte order, until yield returns false, as described in the package
// documentation. It walks the key index and checks that the metadata file
// of each key is there, without reading it and without the key's lock;
// a key for which pass returns true is passed over unchecked, such as one
// that the caller rolls up into a prefix it has. An entry of the index
// that the package did not write goes to report.
func (b *Bucket) ListKeys(
	ctx context.Context,
	bounds Bounds,
	pass func(key string) bool,
	yield func(key string) bool,
	report func(Skipped),
) error {
	var err error

	walkErr := b.walkIndex(ctx, bounds, func(key string) bool {
		if pass(key) {
			return true
		}

		_, sErr := retryPending(func() (os.FileInfo, error) {
			return lstatIn(b.metaRoot, hash(key)+metaFileExt)
		})

		switch {
		case errors.Is(sErr, os.ErrNotExist):
			// An entry whose object is gone, or not yet written.
			return true
		case sErr != nil:
			err = ioErr("stat meta file", sErr)

			return false
		}

		return yield(key)
	}, report)

	return joinErrs(walkErr, err)
}

// Stat returns the metadata of the object key, and the size and the
// modification time of its data file. A missing object is an error
// wrapping ErrObjectNotFound, and one no read can use, one wrapping
// ErrCorrupt; MetaPath then names its metadata file.
//
// It reads without the key's lock, so a write can be halfway: a write
// renames its metadata in, then removes the data file it replaced, and a
// delete removes the metadata, then the data file, so metadata read just
// before either names a data file that is gone, and a metadata file can be
// renamed over while it is opened. Neither alone is proof of damage, so a
// missing data file or a replaced metadata file is looked at again under
// the key's lock, waiting for it until ctx ends. Where a rename over a
// file can briefly hide it (see renameMayHide), so is a missing metadata
// file: a write renames under the key's lock, so the second read sees the
// object or its absence whole.
func (b *Bucket) Stat(ctx context.Context, key string) (ObjectStat, error) {
	name := hash(key)

	st, err := b.statUnlocked(name)

	recheck := errors.Is(err, errDataFileGone) || errors.Is(err, errReplaced) ||
		(renameMayHide && errors.Is(err, ErrObjectNotFound))
	if !recheck {
		return st, err
	}

	err = b.lock.Lock(ctx, name)
	if err != nil {
		return ObjectStat{}, fmt.Errorf("lock object: %w", err)
	}

	st, err = b.statUnlocked(name)
	if errors.Is(err, ErrObjectNotFound) {
		// No usable object, so the lock file need not stay, as in
		// OpenObject.
		return ObjectStat{}, b.releaseWith(name, err)
	}

	return st, b.unlockWith(name, err)
}

// statUnlocked returns what Stat does, read once, without the lock.
func (b *Bucket) statUnlocked(name string) (ObjectStat, error) {
	meta, err := b.meta(name)
	if err != nil {
		return ObjectStat{}, err
	}

	fi, err := retryPending(
		func() (os.FileInfo, error) { return lstatIn(b.objRoot, meta.DataFile) },
	)

	switch {
	case errors.Is(err, os.ErrNotExist):
		return ObjectStat{}, fmt.Errorf("%w: %w", ErrCorrupt, errDataFileGone)
	case err != nil:
		return ObjectStat{}, ioErr("stat data file", err)
	case !fi.Mode().IsRegular():
		return ObjectStat{}, fmt.Errorf("%w: data file: %w", ErrCorrupt, errNotRegular)
	}

	return stat(meta, fi), nil
}

// MetaPath returns the path of the metadata file of key, relative to the
// root, for a report.
func (b *Bucket) MetaPath(key string) string {
	return join(b.dir, hash(key)+metaFileExt)
}
