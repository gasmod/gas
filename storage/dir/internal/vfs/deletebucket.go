package vfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"uuid"

	"github.com/gasmod/gas/storage/dir/internal/util"
)

// HeadBucket returns the entry of the bucket name: its name, and its
// creation time when its name record names it (zero otherwise, as for a
// bucket ListBuckets leaves out). The bucket exists when its metadata
// directory does; a missing one returns an error wrapping ErrBucketNotFound.
// A name record that cannot be read fails the call, as in ListBuckets.
func (vfs *VFS) HeadBucket(name string) (BucketEntry, error) {
	metaDir, _, _ := bucketDirs(name)
	fi, err := vfs.root.Lstat(metaDir)
	if errors.Is(err, os.ErrNotExist) {
		return BucketEntry{}, fmt.Errorf("%w: stat meta dir: %w", ErrBucketNotFound, err)
	}
	if err != nil {
		return BucketEntry{}, fmt.Errorf("stat meta dir: %w", err)
	}
	if !fi.IsDir() {
		return BucketEntry{}, errors.New("meta dir is not a directory")
	}

	entry, _, err := vfs.bucketEntry(hash(name))
	if err != nil {
		return BucketEntry{}, err
	}
	entry.Name = name
	return entry, nil
}

// DeleteBucket deletes the bucket name, which must hold no object. A missing
// bucket returns an error wrapping ErrBucketNotFound, and one that holds an
// object an error wrapping ErrBucketNotEmpty; both delete nothing.
//
// It first classifies every entry of the metadata directory, each under its
// key's lock, so it never races a write in progress: a writer holds that
// lock across its temp file and the rename that commits it, and a slow
// write therefore holds DeleteBucket until it ends or ctx is done. An
// object (metadata naming a data file that is there) stops the delete
// before anything is removed. Anything no read can use is debris: metadata
// that cannot be used, metadata whose data file is gone, and temp metadata
// a crashed write left. Only once no object was found is the debris
// removed, each piece again under its key's lock and only if it is still
// debris. Debris must not block the delete, since no caller can read or
// name it. A file in the metadata directory that this package did not
// write stops the delete with an error and is left alone.
//
// Removing the emptied metadata directory is the commit point: a write
// that landed after the sweep makes it fail, which returns
// ErrBucketNotEmpty, though the debris is already gone by then. The name
// record goes next, so that a bucket created again gets a new
// CreationDate; if that fails, the bucket is deleted and the error reports
// the record left behind. A crash between the two leaves the record, and a
// bucket created again then keeps the old CreationDate. Last, the objects
// and locks directories are emptied and removed as far as possible.
// Leftovers there are harmless (CreateBucket reuses the directories) and are
// not reported as errors: a data file whose key's lock another holder has,
// which belongs to a write of a new key still copying its body and is
// never cut short, and a lock file another holder has locked. Such a write
// then fails for want of the bucket.
//
// It holds the bucket's lifecycle lock throughout (see lockBucket), so no
// CreateBucket of the same name can interleave with any of these steps;
// such a CreateBucket waits for it, slow writes included.
func (vfs *VFS) DeleteBucket(ctx context.Context, name string) (err error) {
	unlock, lErr := vfs.lockBucket(ctx, name)
	if lErr != nil {
		return lErr
	}
	defer func() { err = errors.Join(err, unlock()) }()

	bk, err := vfs.OpenBucket(name)
	if err != nil {
		return err
	}
	if sErr := bk.sweep(ctx); sErr != nil {
		return errors.Join(sErr, bk.Close())
	}
	if cErr := bk.Close(); cErr != nil {
		return cErr
	}

	metaDir, objDir, locksDir := bucketDirs(name)
	if rErr := vfs.root.Remove(metaDir); rErr != nil {
		if isNotEmpty(rErr) {
			return fmt.Errorf("%w: remove meta dir: %w", ErrBucketNotEmpty, rErr)
		}
		return fmt.Errorf("remove meta dir: %w", rErr)
	}

	if rErr := rmInRoot(vfs.root, bucketNameFile(name), "bucket name record"); rErr != nil {
		return fmt.Errorf("bucket deleted, name record left: %w", rErr)
	}

	vfs.removeLeftovers(objDir, locksDir)
	return nil
}

// sweep readies the metadata directory for removal. It first classifies
// every entry under its key's lock, and returns an error wrapping
// ErrBucketNotEmpty at the first object, having removed nothing. Only when
// it found no object does it remove the debris, each piece again under its
// key's lock and only if it is still debris. See DeleteBucket.
func (b *Bucket) sweep(ctx context.Context) error {
	entries, err := fs.ReadDir(b.metaRoot.FS(), ".")
	if err != nil {
		return fmt.Errorf("read meta dir: %w", err)
	}
	var debris []string
	for _, e := range entries {
		name, temp, ok := metaEntryKey(e.Name())
		if !ok {
			// Never quote the name: it is not ours, and may be anything.
			return errors.New("meta dir holds a file this backend did not write")
		}
		state, cErr := b.lockedState(ctx, name, temp)
		if cErr != nil {
			return cErr
		}
		switch state {
		case stateObject:
			return fmt.Errorf("%w: an object remains", ErrBucketNotEmpty)
		case stateDebris:
			debris = append(debris, e.Name())
		}
	}
	for _, file := range debris {
		if rErr := b.removeDebris(ctx, file); rErr != nil {
			return rErr
		}
	}
	return nil
}

// entryState is what a metadata directory entry holds for DeleteBucket.
type entryState int

const (
	// stateGone is an entry removed since the directory was read.
	stateGone entryState = iota
	// stateObject is an object: usable metadata naming a data file that is
	// there.
	stateObject
	// stateDebris is what no read can use: metadata that cannot be used,
	// metadata whose data file is gone, or a temp file a crashed write left.
	stateDebris
)

// lockedState returns the state of the entry of name (its temp file when
// temp is set), read under the key's lock, which it gives up again keeping
// the lock file.
func (b *Bucket) lockedState(ctx context.Context, name string, temp bool) (entryState, error) {
	if err := b.lock.Lock(ctx, name); err != nil {
		return stateGone, fmt.Errorf("lock object: %w", err)
	}
	state, _, err := b.entryState(name, temp)
	return state, b.unlockWith(name, err)
}

// entryState returns the state of the entry of name, and the metadata of an
// object or of debris that names a valid data file. The caller holds the
// key's lock, so a temp file is never one of a write in progress: a writer
// holds the lock across its temp file and the rename that commits it.
func (b *Bucket) entryState(name string, temp bool) (entryState, *ObjectMetadata, error) {
	if temp {
		return stateDebris, nil, nil
	}
	meta, err := b.meta(name)
	switch {
	case errors.Is(err, ErrObjectNotFound):
		return stateGone, nil, nil
	case errors.Is(err, ErrCorrupt):
		// Its data file name cannot be trusted; the cleanup of the objects
		// directory removes that data file, if there is one.
		return stateDebris, nil, nil
	case err != nil:
		return stateGone, nil, err
	}

	fi, err := b.objRoot.Lstat(meta.DataFile)
	switch {
	case err == nil && fi.Mode().IsRegular():
		return stateObject, meta, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return stateGone, nil, fmt.Errorf("stat data file: %w", err)
	}
	// The data file is gone or is not a file: OpenObject reports the object
	// as corrupt.
	return stateDebris, meta, nil
}

// removeDebris removes the metadata directory entry file, under its key's
// lock and only if it is still debris: a write may have replaced it since
// it was classified. The lock file goes with it.
func (b *Bucket) removeDebris(ctx context.Context, file string) error {
	name, temp, _ := metaEntryKey(file)
	if err := b.lock.Lock(ctx, name); err != nil {
		return fmt.Errorf("lock object: %w", err)
	}
	state, meta, err := b.entryState(name, temp)
	if err != nil || state != stateDebris {
		return b.unlockWith(name, err)
	}
	what := "meta file"
	if temp {
		what = "temp meta file"
	}
	rmErr := rmInRoot(b.metaRoot, file, what)
	if rmErr == nil && meta != nil {
		rmErr = rmInRoot(b.objRoot, meta.DataFile, "data file")
	}
	return b.releaseWith(name, rmErr)
}

// metaEntryKey returns the key hash of a metadata directory entry: one of
// its metadata files, or one of their temp files (temp set). ok is false for
// any other name.
func metaEntryKey(file string) (name string, temp, ok bool) {
	if h, isMeta := strings.CutSuffix(file, metaFileExt); isMeta && isHash(h) {
		return h, false, true
	}
	name, id, ok := strings.Cut(file, metaFileExt)
	if !ok || !isHash(name) {
		return "", false, false
	}
	u, err := uuid.Parse(id)
	return name, true, err == nil && u.String() == id
}

// removeLeftovers empties and removes a deleted bucket's objects and locks
// directories as far as it can. A data file whose key's lock another holder
// has is left: it belongs to a write still in progress, which then fails
// for want of the bucket. A lock file another holder has locked stays too,
// and with them their directories. Errors are not returned; see
// DeleteBucket.
func (vfs *VFS) removeLeftovers(objDir, locksDir string) {
	locksRoot, err := vfs.root.OpenRoot(locksDir)
	if err != nil {
		return
	}
	defer func() { _ = locksRoot.Close() }()

	if objRoot, oErr := vfs.root.OpenRoot(objDir); oErr == nil {
		if entries, rErr := fs.ReadDir(objRoot.FS(), "."); rErr == nil {
			for _, e := range entries {
				name, _, isData := strings.Cut(e.Name(), "_")
				if !isData || !isHash(name) {
					continue
				}
				_ = withFreeLock(locksRoot, name, func() error {
					return rmInRoot(objRoot, e.Name(), "data file")
				})
			}
		}
		_ = objRoot.Close()
		_ = vfs.root.Remove(objDir)
	}

	if entries, rErr := fs.ReadDir(locksRoot.FS(), "."); rErr == nil {
		for _, e := range entries {
			_ = withFreeLock(locksRoot, e.Name(), nil)
		}
	}
	_ = vfs.root.Remove(locksDir)
}

// withFreeLock runs fn, when it is not nil, unless another holder, in this
// process or another, has the lock file name in locksRoot locked; then it
// does nothing. It holds that lock while fn runs and, like Release, removes
// the lock file before giving it up, so a waiter that then gets it sees it
// is no longer current and retries. A missing lock file is free: no holder
// can be in the middle of a write without one.
func withFreeLock(locksRoot *os.Root, name string, fn func() error) error {
	f, err := locksRoot.OpenFile(name, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		if fn == nil {
			return nil
		}
		return fn()
	}
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	locked, err := tryLock(f)
	if err != nil || !locked {
		if cErr := util.CloseWithErr(err, f); cErr != nil {
			return fmt.Errorf("try lock file: %w", cErr)
		}
		return nil
	}
	var fnErr error
	if fn != nil {
		fnErr = fn()
	}
	rmErr := rmInRoot(locksRoot, name, "lock file")
	return errors.Join(fnErr, rmErr, util.CloseWithErr(unlock(f), f))
}
