package vfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
// Each piece of debris removed, and each leftover or error of the last
// step, is passed to report, which may be nil, after DeleteBucket has given
// up every lock it took, so report may call back into the VFS.
//
// It holds the bucket's lifecycle lock throughout (see lockBucket), so no
// CreateBucket of the same name can interleave with any of these steps;
// such a CreateBucket waits for it, slow writes included.
func (vfs *VFS) DeleteBucket(ctx context.Context, name string, report func(Skipped)) (err error) {
	// The steps below run under the lifecycle lock, and some under a key's
	// lock too, so they collect what they report, and report gets it only
	// once every lock is given up: a report that calls back into the VFS
	// for the same bucket or key would otherwise wait on a lock this call
	// holds.
	var reports []Skipped
	defer func() {
		if report != nil {
			for _, s := range reports {
				report(s)
			}
		}
	}()
	collect := func(s Skipped) { reports = append(reports, s) }

	unlock, lErr := vfs.lockBucket(ctx, name)
	if lErr != nil {
		return lErr
	}
	defer func() { err = errors.Join(err, unlock()) }()

	bk, err := vfs.OpenBucket(name)
	if err != nil {
		return err
	}
	if sErr := bk.sweep(ctx, collect); sErr != nil {
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

	vfs.removeLeftovers(objDir, locksDir, collect)
	return nil
}

// sweep readies the metadata directory for removal. It first classifies
// every entry under its key's lock, and returns an error wrapping
// ErrBucketNotEmpty at the first object, having removed nothing. Only when
// it found no object does it remove the debris, each piece again under its
// key's lock and only if it is still debris. See DeleteBucket.
func (b *Bucket) sweep(ctx context.Context, report func(Skipped)) error {
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
		state, cErr := b.lockedState(ctx, name, e.Name(), temp)
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
		if rErr := b.removeDebris(ctx, file, report); rErr != nil {
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

// entry is what entryState found for a metadata directory entry: its state,
// the metadata of an object or of debris that names a data file, and why
// debris is debris.
type entry struct {
	meta   *ObjectMetadata
	reason error
	state  entryState
}

// lockedState returns the state of the entry file of name (one of its temp
// files when temp is set), read under the key's lock, which it gives up
// again keeping the lock file.
func (b *Bucket) lockedState(
	ctx context.Context,
	name, file string,
	temp bool,
) (entryState, error) {
	if err := b.lock.Lock(ctx, name); err != nil {
		return stateGone, fmt.Errorf("lock object: %w", err)
	}
	e, err := b.entryState(name, file, temp)
	return e.state, b.unlockWith(name, err)
}

// entryState returns what the entry file of name holds. The caller holds
// the key's lock, so a temp file is never one of a write in progress: a
// writer holds the lock across its temp file and the rename that commits
// it. A temp file the write renamed away while the caller waited for the
// lock is gone, not debris.
func (b *Bucket) entryState(name, file string, temp bool) (entry, error) {
	if temp {
		return b.tempState(file)
	}
	meta, err := b.meta(name)
	switch {
	case errors.Is(err, ErrObjectNotFound):
		return entry{state: stateGone}, nil
	case errors.Is(err, ErrCorrupt):
		// Its data file name cannot be trusted; the cleanup of the objects
		// directory removes that data file, if there is one.
		return entry{state: stateDebris, reason: err}, nil
	case err != nil:
		return entry{}, err
	}

	fi, err := b.objRoot.Lstat(meta.DataFile)
	switch {
	case err == nil && fi.Mode().IsRegular():
		return entry{state: stateObject, meta: meta}, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return entry{}, fmt.Errorf("stat data file: %w", err)
	}
	// The data file is gone or is not a file: OpenObject reports the object
	// as corrupt.
	return entry{
		state:  stateDebris,
		meta:   meta,
		reason: fmt.Errorf("%w: data file is gone or not a regular file", ErrCorrupt),
	}, nil
}

// tempState returns what the temp meta file file holds: debris a crashed
// write left, or gone when the write renamed it away while the caller
// waited for the key's lock.
func (b *Bucket) tempState(file string) (entry, error) {
	_, err := b.metaRoot.Lstat(file)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return entry{state: stateGone}, nil
	case err != nil:
		return entry{}, fmt.Errorf("stat temp meta file: %w", err)
	}
	return entry{
		state:  stateDebris,
		reason: errors.New("temp meta file of a crashed write"),
	}, nil
}

// removeDebris removes the metadata directory entry file, under its key's
// lock and only if it is still debris: a write may have replaced it since
// it was classified. The lock file goes with it, and report gets the entry.
func (b *Bucket) removeDebris(ctx context.Context, file string, report func(Skipped)) error {
	name, temp, _ := metaEntryKey(file)
	if err := b.lock.Lock(ctx, name); err != nil {
		return fmt.Errorf("lock object: %w", err)
	}
	e, err := b.entryState(name, file, temp)
	if err != nil || e.state != stateDebris {
		return b.unlockWith(name, err)
	}
	what := "meta file"
	if temp {
		what = "temp meta file"
	}
	rmErr := rmInRoot(b.metaRoot, file, what)
	if rmErr == nil && e.meta != nil {
		rmErr = rmInRoot(b.objRoot, e.meta.DataFile, "data file")
	}
	if rmErr == nil {
		report(Skipped{
			Path: filepath.Join(b.dir, file),
			Err:  fmt.Errorf("removed with the bucket: %w", e.reason),
		})
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
// and with them their directories. Each leftover, and each error, goes to
// report rather than to the caller; see DeleteBucket.
func (vfs *VFS) removeLeftovers(objDir, locksDir string, report func(Skipped)) {
	locksRoot, err := vfs.root.OpenRoot(locksDir)
	if err != nil {
		report(Skipped{Path: locksDir, Err: fmt.Errorf("open locks dir, left in place: %w", err)})
		return
	}
	defer func() { _ = locksRoot.Close() }()

	if oErr := vfs.removeDataFiles(objDir, locksRoot, report); oErr != nil {
		report(Skipped{Path: objDir, Err: oErr})
	} else if rErr := vfs.root.Remove(objDir); rErr != nil {
		report(Skipped{Path: objDir, Err: fmt.Errorf("objects dir left in place: %w", rErr)})
	}

	entries, err := fs.ReadDir(locksRoot.FS(), ".")
	if err != nil {
		report(Skipped{Path: locksDir, Err: fmt.Errorf("read locks dir: %w", err)})
		return
	}
	for _, e := range entries {
		path := filepath.Join(locksDir, e.Name())
		ran, lErr := withFreeLock(locksRoot, e.Name(), nil)
		switch {
		case lErr != nil:
			report(Skipped{Path: path, Err: lErr})
		case !ran:
			report(
				Skipped{
					Path: path,
					Err:  errors.New("lock file another holder has locked, left in place"),
				},
			)
		}
	}
	if rErr := vfs.root.Remove(locksDir); rErr != nil {
		report(Skipped{Path: locksDir, Err: fmt.Errorf("locks dir left in place: %w", rErr)})
	}
}

// removeDataFiles removes the data files of a deleted bucket's objects
// directory objDir, each while holding its key's lock (see withFreeLock),
// and reports those it must leave. It returns an error only when it cannot
// read the directory at all.
func (vfs *VFS) removeDataFiles(objDir string, locksRoot *os.Root, report func(Skipped)) error {
	objRoot, err := vfs.root.OpenRoot(objDir)
	if err != nil {
		return fmt.Errorf("open objects dir, left in place: %w", err)
	}
	defer func() { _ = objRoot.Close() }()
	entries, err := fs.ReadDir(objRoot.FS(), ".")
	if err != nil {
		return fmt.Errorf("read objects dir, left in place: %w", err)
	}
	for _, e := range entries {
		path := filepath.Join(objDir, e.Name())
		name, _, isData := strings.Cut(e.Name(), "_")
		if !isData || !isHash(name) {
			report(
				Skipped{
					Path: path,
					Err:  errors.New("file this backend did not write, left in place"),
				},
			)
			continue
		}
		ran, lErr := withFreeLock(locksRoot, name, func() error {
			return rmInRoot(objRoot, e.Name(), "data file")
		})
		switch {
		case lErr != nil:
			report(Skipped{Path: path, Err: lErr})
		case !ran:
			report(
				Skipped{
					Path: path,
					Err:  errors.New("data file of a write in progress, left in place"),
				},
			)
		}
	}
	return nil
}

// withFreeLock runs fn, when it is not nil, unless another holder, in this
// process or another, has the lock file name in locksRoot locked; then it
// does nothing and reports false. It holds that lock while fn runs and, like Release, removes
// the lock file before giving it up, so a waiter that then gets it sees it
// is no longer current and retries. A missing lock file is free: no holder
// can be in the middle of a write without one.
func withFreeLock(locksRoot *os.Root, name string, fn func() error) (bool, error) {
	f, err := locksRoot.OpenFile(name, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		if fn == nil {
			return true, nil
		}
		return true, fn()
	}
	if err != nil {
		return false, fmt.Errorf("open lock file: %w", err)
	}
	locked, err := tryLock(f)
	if err != nil || !locked {
		if cErr := util.CloseWithErr(err, f); cErr != nil {
			return false, fmt.Errorf("try lock file: %w", cErr)
		}
		return false, nil
	}
	var fnErr error
	if fn != nil {
		fnErr = fn()
	}
	rmErr := rmInRoot(locksRoot, name, "lock file")
	return true, errors.Join(fnErr, rmErr, util.CloseWithErr(unlock(f), f))
}
