package vfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
)

// VFS stores buckets of objects under an os.Root, as described in the
// package documentation. It holds no open file of its own but the root's;
// each OpenBucket opens the directories of a bucket for one operation.
type VFS struct {
	// root is the root everything is stored under.
	root *os.Root
	// slots holds the in-process slots of every lock name of the VFS,
	// shared by the locks of all its Bucket handles.
	slots *slotTable
}

// Open returns a VFS on root, which the caller keeps and closes. It
// creates the directories of the package that are missing and the layout
// marker. A root without the marker that holds buckets is an error
// wrapping ErrOldLayout.
func Open(root *os.Root) (*VFS, error) {
	v := &VFS{root: root, slots: newSlotTable()}

	err := v.checkLayout()
	if err != nil {
		return nil, err
	}

	for _, d := range []struct{ path, what string }{
		{objectsPath, "objects"},
		{locksPath, "locks"},
		{bucketsPath, "buckets"},
		{keysPath, "keys"},
	} {
		err = ensureDir(root, d.path, d.what)
		if err != nil {
			return nil, err
		}
	}

	err = v.writeLayout()
	if err != nil {
		return nil, err
	}

	return v, nil
}

// Check returns nil when the layout marker is still in the root, and else
// why not, such as a root whose directory was removed.
func (v *VFS) Check() error {
	_, err := lstatIn(v.root, layoutPath)
	if err != nil {
		return ioErr("stat layout marker", err)
	}

	return nil
}

// CreateBucket creates the bucket name, or completes it, and returns nil
// when it exists already. It creates the objects, locks and keys
// directories, writes the name record, then creates the metadata
// directory, which is the commit, and syncs them. It holds the bucket's
// lifecycle lock throughout, waiting for it until ctx ends, so a
// DeleteBucket of the same name in progress holds it up.
func (v *VFS) CreateBucket(ctx context.Context, name string) (err error) {
	unlock, err := v.lockBucket(ctx, name)
	if err != nil {
		return err
	}

	defer func() { err = joinErrs(err, unlock()) }()

	meta, objects, locks, keys := bucketDirs(name)

	for _, d := range []struct{ path, what string }{
		{objects, "objects"},
		{locks, "locks"},
		{keys, "keys"},
	} {
		err = ensureDir(v.root, d.path, d.what)
		if err != nil {
			return err
		}
	}

	err = v.writeBucketName(name)
	if err != nil {
		return err
	}

	err = v.root.Mkdir(meta, dirPerm)

	switch {
	case errors.Is(err, os.ErrExist):
		// Only a directory is the bucket; anything else at its name is an
		// I/O error.
		fi, sErr := lstatIn(v.root, meta)
		if sErr == nil && fi.IsDir() {
			return nil
		}

		return ioErr("create meta dir", err)
	case err != nil:
		return ioErr("create meta dir", err)
	}

	return joinErrs(
		syncDir(v.root, objectsPath),
		syncDir(v.root, locksPath),
		syncDir(v.root, keysPath),
		syncDir(v.root, "."),
	)
}

// HeadBucket returns nil when the bucket name exists, an error wrapping
// ErrBucketNotFound when it does not, and else why it cannot tell. The
// bucket exists when its metadata directory does.
func (v *VFS) HeadBucket(name string) error {
	fi, err := lstatIn(v.root, hash(name))

	switch {
	case errors.Is(err, os.ErrNotExist):
		return ErrBucketNotFound
	case err != nil:
		return ioErr("stat meta dir", err)
	case !fi.IsDir():
		return fmt.Errorf("meta dir: %w", errNotDir)
	}

	return nil
}

// Removed reports whether the metadata directory b was opened on is no
// longer the bucket's: DeleteBucket removed it, and CreateBucket may have
// made another of the same name since. An operation on b that failed for
// want of its files then failed because the bucket was deleted under it.
// An error comparing the two counts as not removed.
func (v *VFS) Removed(b *Bucket) bool {
	cur, err := lstatIn(v.root, b.dir)
	// A directory being removed is removed; b's own handle may be what
	// keeps it from being gone yet (see isDeletePending).
	if errors.Is(err, os.ErrNotExist) || isDeletePending(err) {
		return true
	}

	if err != nil {
		return false
	}

	opened, err := statIn(b.metaRoot, ".")
	if err != nil {
		// On Windows a removed directory can be delete-pending under the
		// open handle; see IsMissing.
		return IsMissing(err)
	}

	return !os.SameFile(opened, cur)
}

// OpenBucket opens the directories of the bucket name. A missing
// directory is an error wrapping ErrBucketNotFound. The caller closes the
// Bucket.
func (v *VFS) OpenBucket(name string) (*Bucket, error) {
	return v.openBucketDirs(bucketDirs(name))
}

// checkLayout returns nil when the root holds the layout marker of this
// version, or no marker and no bucket, and else why not.
func (v *VFS) checkLayout() error {
	data, err := v.root.ReadFile(layoutPath)

	switch {
	case err == nil && string(data) == layoutVersion:
		return nil
	case err == nil:
		return errLayoutMarker
	case !errors.Is(err, os.ErrNotExist):
		return ioErr("read layout marker", err)
	}

	entries, err := readDirIn(v.root, ".")
	if err != nil {
		return ioErr("read root dir", err)
	}

	if slices.ContainsFunc(
		entries,
		func(e fs.DirEntry) bool { return e.IsDir() && isHash(e.Name()) },
	) {
		return ErrOldLayout
	}

	return nil
}

// writeLayout writes the layout marker unless it is there, and syncs it.
func (v *VFS) writeLayout() error {
	fi, err := lstatIn(v.root, layoutPath)

	switch {
	case err == nil && !fi.Mode().IsRegular():
		return fmt.Errorf("layout marker: %w", errNotRegular)
	case err == nil:
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return ioErr("stat layout marker", err)
	}

	tmp := layoutPath + newTempSuffix()

	err = writeSynced(v.root, tmp, []byte(layoutVersion))
	if err != nil {
		return fmt.Errorf("write layout marker: %w", err)
	}

	err = renameIn(v.root, tmp, layoutPath)
	if err != nil {
		return discard(v.root, tmp, ioErr("commit layout marker", err))
	}

	return joinErrs(syncDir(v.root, gasPath), syncDir(v.root, "."))
}

// lockBucket takes the lifecycle lock of the bucket name, waiting for it
// until ctx ends, and returns the function that gives it up and removes
// its file. CreateBucket and DeleteBucket each hold it for their whole
// run, in every process, so a bucket is never created while it is being
// deleted: the delete's cleanup could otherwise remove the name record,
// or a data file, of the bucket created in its place. Object operations do
// not take it; the per-key locks order them against DeleteBucket.
func (v *VFS) lockBucket(ctx context.Context, name string) (func() error, error) {
	root, err := openRootIn(v.root, locksPath)
	if err != nil {
		return nil, ioErr("open locks dir", err)
	}

	lock := newSharedLock(root, v.slots, locksPath)
	lockName := hash(name) + bucketLockExt

	err = lock.Lock(ctx, lockName)
	if err != nil {
		return nil, joinErrs(fmt.Errorf("lock bucket: %w", err), lock.Close())
	}

	return func() error {
		rErr := joinErrs(lock.Release(lockName), lock.Close())
		if rErr != nil {
			return fmt.Errorf("release bucket lock: %w", rErr)
		}

		return nil
	}, nil
}

// openBucketDirs opens a bucket with the given metadata, objects, locks and
// keys directories, as OpenBucket does.
func (v *VFS) openBucketDirs(meta, objects, locks, keys string) (*Bucket, error) {
	metaRoot, err := v.openBucketDir(meta, "meta")
	if err != nil {
		return nil, err
	}

	objRoot, err := v.openBucketDir(objects, "objects")
	if err != nil {
		return nil, closeAll(err, metaRoot)
	}

	locksRoot, err := v.openBucketDir(locks, "locks")
	if err != nil {
		return nil, closeAll(err, metaRoot, objRoot)
	}

	keysRoot, err := v.openBucketDir(keys, "keys")
	if err != nil {
		return nil, closeAll(err, metaRoot, objRoot, locksRoot)
	}

	return &Bucket{
		dir:      meta,
		keysDir:  keys,
		objDir:   objects,
		metaRoot: metaRoot,
		objRoot:  objRoot,
		keysRoot: keysRoot,
		lock:     newSharedLock(locksRoot, v.slots, locks),
	}, nil
}

// openBucketDir opens the bucket directory name as a root. what names the
// directory in the error.
func (v *VFS) openBucketDir(name, what string) (*os.Root, error) {
	r, err := retryPending(func() (*os.Root, error) { return openRootIn(v.root, name) })
	if errors.Is(err, os.ErrNotExist) {
		// Windows reports a file in place of the directory as a missing
		// path.
		fi, sErr := lstatIn(v.root, name)
		if sErr == nil && !fi.IsDir() {
			return nil, fmt.Errorf("open %s dir: %w", what, errNotDir)
		}

		return nil, fmt.Errorf("%w: open %s dir: %w", ErrBucketNotFound, what, cause(err))
	}

	if err != nil {
		return nil, ioErr("open "+what+" dir", err)
	}

	return r, nil
}

// bucketLockExt ends the name of a bucket's lifecycle lock file in the
// locks directory, which sets it apart from the bucket's own locks
// directory there.
const bucketLockExt = ".bucket"
