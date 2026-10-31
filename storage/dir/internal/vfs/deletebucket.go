package vfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync/atomic"
)

// DeleteBucket deletes the bucket name, which must hold no object, as
// described in the package documentation. A missing bucket is an error
// wrapping ErrBucketNotFound, and one that holds an object an error
// wrapping ErrBucketNotEmpty; both delete nothing.
//
// It first classifies every entry of the metadata directory, each under
// its key's lock, so it never races a write in progress: a writer holds
// that lock across its temp file and the rename that commits it, so a
// slow write holds DeleteBucket until it ends or ctx ends. An object stops
// the delete before anything is removed. Anything no read can use is
// debris: metadata that cannot be used, metadata whose data file is gone,
// and the temp files of crashed writes. Only once no object was found is
// the debris removed, each piece again under its key's lock and only if it
// is still debris. A file in the metadata directory that the package did
// not write stops the delete, which leaves it.
//
// Removing the emptied metadata directory is the commit, which a sync of
// the root makes durable: a write that landed after the sweep makes it
// fail with ErrBucketNotEmpty, though the debris is gone by then. The
// name record goes next, so that a bucket created again gets a new
// creation time; if that fails, the bucket is deleted and the error says
// so. Last, the objects, locks and keys directories are emptied and
// removed as far as they can be: a data file whose key's lock another
// holder has belongs to a write of a new key still reading its body,
// which is never cut short and then fails for want of the bucket, and a
// lock file another holder has locked stays too.
//
// Each piece of debris removed, and each leftover or error of the last
// step, goes to report, once DeleteBucket has given up every lock it took,
// so that report may call the VFS. It holds the bucket's lifecycle lock
// throughout, so no CreateBucket of the same name interleaves with it.
func (v *VFS) DeleteBucket(ctx context.Context, name string, report func(Skipped)) (err error) {
	// The steps run under the lifecycle lock, some under a key's lock too,
	// so they collect what they report, and report gets it once every lock
	// is given up: a report that calls the VFS for the same bucket or key
	// would otherwise wait on a lock this call holds.
	var reports []Skipped

	defer func() {
		for _, s := range reports {
			report(s)
		}
	}()

	collect := func(s Skipped) { reports = append(reports, s) }

	unlock, err := v.lockBucket(ctx, name)
	if err != nil {
		return err
	}

	defer func() { err = joinErrs(err, unlock()) }()

	bk, err := v.OpenBucket(name)
	if err != nil {
		return err
	}

	err = bk.sweep(ctx, collect)
	if err != nil {
		return closeAll(err, bk)
	}

	err = bk.Close()
	if err != nil {
		return err
	}

	meta, objects, locks, keys := bucketDirs(name)

	err = v.root.Remove(meta)

	switch {
	case err != nil && isNotEmpty(err):
		return ErrBucketNotEmpty
	case err != nil:
		return ioErr("remove meta dir", err)
	}

	err = joinErrs(
		syncDir(v.root, "."),
		rmInRoot(v.root, bucketNameFile(name), "bucket name record"),
	)
	if err != nil {
		return fmt.Errorf("bucket deleted: %w", err)
	}

	v.removeLeftovers(objects, locks, keys, collect)

	return nil
}

// sweep readies the metadata directory for removal. It first classifies
// every entry under its key's lock, and returns ErrBucketNotEmpty at the
// first object, having removed nothing. Only when it found no object does
// it remove the debris, each piece again under its key's lock and only if
// it is still debris. See DeleteBucket.
func (b *Bucket) sweep(ctx context.Context, report func(Skipped)) error {
	entries, err := fs.ReadDir(b.metaRoot.FS(), ".")
	if err != nil {
		return ioErr("read meta dir", err)
	}

	var debris []string

	for _, e := range entries {
		name, temp, ok := metaEntryKey(e.Name())
		if !ok {
			return fmt.Errorf("meta dir: %w", ErrForeign)
		}

		state, sErr := b.lockedState(ctx, name, e.Name(), temp)
		if sErr != nil {
			return sErr
		}

		switch state {
		case stateObject:
			return ErrBucketNotEmpty
		case stateDebris:
			debris = append(debris, e.Name())
		case stateGone:
		}
	}

	for _, file := range debris {
		err = b.removeDebris(ctx, file, report)
		if err != nil {
			return err
		}
	}

	return nil
}

// entryState is what an entry of the metadata directory holds.
type entryState int

const (
	// stateGone is an entry removed since the directory was read.
	stateGone entryState = iota
	// stateObject is an object: usable metadata naming a data file that
	// is there.
	stateObject
	// stateDebris is what no read can use: metadata that cannot be used,
	// metadata whose data file is gone, or a temp file a crashed write
	// left.
	stateDebris
)

// entry is what entryState found for an entry of the metadata directory:
// its state, the metadata of an object or of debris that names a data
// file, and why debris is debris.
type entry struct {
	// meta is the metadata, if any.
	meta *Metadata
	// reason says why debris is debris.
	reason error
	// state is the entry's state.
	state entryState
}

// lockedState returns the state of the entry file of name, or of one of
// its temp files when temp is set, read under the key's lock, which it
// gives up again keeping the lock file.
func (b *Bucket) lockedState(
	ctx context.Context,
	name, file string,
	temp bool,
) (entryState, error) {
	err := b.lock.Lock(ctx, name)
	if err != nil {
		return stateGone, fmt.Errorf("lock object: %w", err)
	}

	e, err := b.entryState(name, file, temp)

	return e.state, b.unlockWith(name, err)
}

// entryState returns what the entry file of name holds. The caller holds
// the key's lock, so a temp file is never one of a write in progress: a
// writer holds the lock across its temp file and the rename that commits
// it. A temp file that a write renamed away while the caller waited for
// the lock is gone, not debris.
func (b *Bucket) entryState(name, file string, temp bool) (entry, error) {
	if temp {
		return b.tempState(file)
	}

	meta, err := b.meta(name)

	switch {
	case errors.Is(err, ErrObjectNotFound):
		return entry{state: stateGone}, nil
	case errors.Is(err, ErrCorrupt):
		// Its data file name cannot be trusted; the cleanup of the
		// objects directory removes that data file, if there is one.
		return entry{state: stateDebris, reason: err}, nil
	case err != nil:
		return entry{}, err
	}

	fi, err := retryPending(func() (os.FileInfo, error) { return b.objRoot.Lstat(meta.DataFile) })

	switch {
	case err == nil && fi.Mode().IsRegular():
		return entry{state: stateObject, meta: meta}, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return entry{}, ioErr("stat data file", err)
	}

	// The data file is gone or is not a file: OpenObject reports the
	// object as corrupt.
	return entry{
		state:  stateDebris,
		meta:   meta,
		reason: fmt.Errorf("%w: data file is gone or not a regular file", ErrCorrupt),
	}, nil
}

// errCrashedTemp is why a temp metadata file is debris.
var errCrashedTemp = errors.New("temp file of a crashed write")

// tempState returns what the temp metadata file holds: debris
// crashed write left, or gone when the write renamed it away while the
// caller waited for the key's lock.
func (b *Bucket) tempState(file string) (entry, error) {
	_, err := retryPending(func() (os.FileInfo, error) { return b.metaRoot.Lstat(file) })

	switch {
	case errors.Is(err, os.ErrNotExist):
		return entry{state: stateGone}, nil
	case err != nil:
		return entry{}, ioErr("stat temp meta file", err)
	}

	return entry{state: stateDebris, reason: errCrashedTemp}, nil
}

// removeDebris removes the entry file of the metadata directory, under
// its key's lock and only if it is still debris: a write may have
// replaced it since it was classified. The lock file goes with it, and
// report gets the entry.
func (b *Bucket) removeDebris(ctx context.Context, file string, report func(Skipped)) error {
	name, temp, _ := metaEntryKey(file)

	err := b.lock.Lock(ctx, name)
	if err != nil {
		return fmt.Errorf("lock object: %w", err)
	}

	e, err := b.entryState(name, file, temp)
	if err != nil || e.state != stateDebris {
		return b.unlockWith(name, err)
	}

	err = rmInRoot(b.metaRoot, file, "meta file")
	if err == nil && e.meta != nil {
		err = rmInRoot(b.objRoot, e.meta.DataFile, "data file")
	}

	if err == nil {
		report(Skipped{
			Path: join(b.dir, file),
			Err:  fmt.Errorf("removed with the bucket: %w", e.reason),
		})
	}

	return b.releaseWith(name, err)
}

// metaEntryKey returns the key hash of an entry of the metadata
// directory: one of its metadata files, or one of their temp files (temp
// set). ok is false for any other name.
func metaEntryKey(file string) (name string, temp, ok bool) {
	if h, isMeta := strings.CutSuffix(file, metaFileExt); isMeta && isHash(h) {
		return h, false, true
	}

	name, id, ok := strings.Cut(file, metaFileExt)
	if !ok || !isHash(name) {
		return "", false, false
	}

	return name, true, isUUID(id)
}

// removeLeftovers empties and removes the objects, locks and keys
// directories of a deleted bucket as far as it can. A data file whose
// key's lock another holder has is left: it belongs to a write still in
// progress, which then fails for want of the bucket. A lock file another
// holder has locked stays too, and with them their directories. Each
// leftover, and each error, goes to report rather than to the caller; see
// DeleteBucket.
func (v *VFS) removeLeftovers(objects, locks, keys string, report func(Skipped)) {
	err := v.root.RemoveAll(keys)
	if err != nil {
		report(Skipped{Path: keys, Err: ioErr("keys dir left in place", err)})
	}

	locksRoot, err := v.root.OpenRoot(locks)
	if err != nil {
		report(Skipped{Path: locks, Err: ioErr("open locks dir, left in place", err)})

		return
	}

	defer func() {
		cErr := locksRoot.Close()
		if cErr != nil {
			report(Skipped{Path: locks, Err: ioErr("close locks dir", cErr)})
		}
	}()

	err = v.removeDataFiles(objects, locksRoot, report)
	if err != nil {
		report(Skipped{Path: objects, Err: err})
	} else {
		err = v.root.Remove(objects)
		if err != nil {
			report(Skipped{Path: objects, Err: ioErr("objects dir left in place", err)})
		}
	}

	if removeLocks(locksRoot, locks, report) {
		err = v.root.Remove(locks)
		if err != nil {
			report(Skipped{Path: locks, Err: ioErr("locks dir left in place", err)})
		}
	}
}

// removeLocks removes the free lock files of locksRoot, the locks
// directory locks of a deleted bucket, reports those it leaves, and
// reports whether it could read the directory.
func removeLocks(locksRoot *os.Root, locks string, report func(Skipped)) bool {
	entries, err := fs.ReadDir(locksRoot.FS(), ".")
	if err != nil {
		report(Skipped{Path: locks, Err: ioErr("read locks dir", err)})

		return false
	}

	for _, e := range entries {
		ran, lErr := withFreeLock(locksRoot, e.Name(), nil)

		switch {
		case lErr != nil:
			report(Skipped{Path: join(locks, e.Name()), Err: lErr})
		case !ran:
			report(Skipped{Path: join(locks, e.Name()), Err: errLockHeld})
		}
	}

	return true
}

// The reasons a leftover of DeleteBucket stays.
var (
	// errLockHeld is a lock file another holder has locked.
	errLockHeld = errors.New("lock file another holder has locked, left in place")
	// errWriteInProgress is a data file of a write in progress.
	errWriteInProgress = errors.New("data file of a write in progress, left in place")
)

// removeDataFiles removes the data files of the objects directory of a
// deleted bucket, each while holding its key's lock (see withFreeLock),
// and reports those it leaves. It returns an error only when it cannot
// read the directory at all.
func (v *VFS) removeDataFiles(objects string, locksRoot *os.Root, report func(Skipped)) error {
	objRoot, err := v.root.OpenRoot(objects)
	if err != nil {
		return ioErr("open objects dir, left in place", err)
	}

	entries, err := fs.ReadDir(objRoot.FS(), ".")
	if err != nil {
		return closeAll(ioErr("read objects dir, left in place", err), objRoot)
	}

	for _, e := range entries {
		file := join(objects, e.Name())

		name, _, isData := strings.Cut(e.Name(), "_")
		if !isData || !isHash(name) {
			report(Skipped{Path: file, Err: ErrForeign})

			continue
		}

		ran, lErr := withFreeLock(locksRoot, name, func() error {
			return rmInRoot(objRoot, e.Name(), "data file")
		})

		switch {
		case lErr != nil:
			report(Skipped{Path: file, Err: lErr})
		case !ran:
			report(Skipped{Path: file, Err: errWriteInProgress})
		}
	}

	return closeAll(nil, objRoot)
}

// withFreeLock runs fn, when it is not nil, unless another holder, in
// this process or another, has the lock name in locksRoot; then it does
// nothing and reports false. It takes the lock as an ObjectsLock does,
// without waiting: it opens the file at name, creating it when it is
// missing, locks it, and checks that it is still the file at name. A
// file that is no longer current was released while it was being locked,
// and a newcomer may hold the one that replaced it, so it counts as held.
// It holds the lock while fn runs and, as Release does, removes the lock
// file before it gives it up, so a waiter that then gets it sees that it
// is no longer current and retries.
func withFreeLock(locksRoot *os.Root, name string, fn func() error) (bool, error) {
	// A holder removing the file is giving it up; wait for it to be gone.
	f, err := retryPending(
		func() (*os.File, error) { return openInRoot(locksRoot, name, "lock file") },
	)
	if err != nil {
		return false, err
	}

	if hook := FreeLockOpened.Load(); hook != nil {
		(*hook)(name)
	}

	locked, err := tryLock(f)
	if err != nil || !locked {
		return false, closeAll(err, f)
	}

	current, err := isCurrent(locksRoot, f, name)
	if err != nil || !current {
		return false, closeAll(joinErrs(err, unlock(f)), f)
	}

	var fnErr error

	if fn != nil {
		fnErr = fn()
	}

	rmErr := rmInRoot(locksRoot, name, "lock file")

	return true, joinErrs(fnErr, rmErr, closeAll(unlock(f), f))
}

// FreeLockOpened is a hook for tests: when set, it runs in withFreeLock
// with the lock name between the open of the lock file and the attempt to
// lock it, so that a test can hand the lock to another holder in that
// window. Nothing else sets it.
var FreeLockOpened atomic.Pointer[func(name string)]
