package vfs

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/gasmod/gas/storage/dir/internal/util"
)

// VFS stores buckets of objects under an os.Root. It holds no open files of
// its own; each OpenBucket opens the bucket's directories for one operation.
type VFS struct {
	root *os.Root
}

// NewVFS returns a VFS on root, creating the shared objects and locks
// directories if they are missing. The caller keeps ownership of root.
func NewVFS(root *os.Root) (*VFS, error) {
	vfs := &VFS{root: root}

	if err := ensureDirInRoot(vfs.root, objectsPath, "objects"); err != nil {
		return nil, err
	}
	if err := ensureDirInRoot(vfs.root, locksPath, "locks"); err != nil {
		return nil, err
	}
	if err := ensureDirInRoot(vfs.root, bucketsPath, "buckets"); err != nil {
		return nil, err
	}

	return vfs, nil
}

// CreateBucket creates the directories of the bucket name and writes its
// name record, which ListBuckets reads the name from. The metadata
// directory is created last, with Mkdir, and is the commit point: a bucket
// whose earlier directories exist but whose metadata directory does not is
// completed, and one whose metadata directory exists returns an error
// wrapping ErrBucketExists, together with the entry. It returns the bucket's
// entry, read from the name record it wrote. It holds the bucket's lifecycle
// lock throughout
// (see lockBucket), waiting for it until ctx is done: a DeleteBucket of the
// same name in progress, which can itself be waiting for a slow write,
// holds it up.
func (vfs *VFS) CreateBucket(ctx context.Context, name string) (entry BucketEntry, err error) {
	unlock, lErr := vfs.lockBucket(ctx, name)
	if lErr != nil {
		return BucketEntry{}, lErr
	}
	defer func() { err = errors.Join(err, unlock()) }()

	if cErr := vfs.createBucket(name); cErr != nil && !errors.Is(cErr, ErrBucketExists) {
		return BucketEntry{}, cErr
	} else if cErr != nil {
		err = cErr
	}

	// Read under the lifecycle lock, so no DeleteBucket can remove the
	// record between its write and this read.
	entry, named, eErr := vfs.bucketEntry(hash(name))
	if eErr != nil {
		return BucketEntry{}, eErr
	}
	if !named {
		return BucketEntry{}, errors.New("name record does not hold the name just written")
	}
	return entry, err
}

// createBucket creates the directories and name record of the bucket name;
// see CreateBucket, which holds the lifecycle lock around it.
func (vfs *VFS) createBucket(name string) error {
	metaDir, objDir, locksDir := bucketDirs(name)

	if err := ensureDirInRoot(vfs.root, objDir, "objects"); err != nil {
		return err
	}
	if err := ensureDirInRoot(vfs.root, locksDir, "locks"); err != nil {
		return err
	}
	if err := vfs.writeBucketName(name); err != nil {
		return err
	}

	mErr := vfs.root.Mkdir(metaDir, dirPerm)
	if mErr == nil {
		return nil
	}
	if errors.Is(mErr, os.ErrExist) {
		// Only a directory is the bucket; anything else at its name is an
		// I/O error.
		if fi, sErr := vfs.root.Lstat(metaDir); sErr == nil && fi.IsDir() {
			return fmt.Errorf("%w: create meta dir: %w", ErrBucketExists, mErr)
		}
	}
	return fmt.Errorf("create meta dir: %w", mErr)
}

// Removed reports whether the metadata directory b was opened on is no
// longer the bucket's: DeleteBucket removed it, and CreateBucket may have
// made a new one of the same name since. An operation on b that failed for
// want of its files then failed because the bucket was deleted under it.
// An error comparing the two counts as not removed.
func (vfs *VFS) Removed(b *Bucket) bool {
	cur, err := vfs.root.Lstat(b.dir)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	opened, err := b.metaRoot.Stat(".")
	if err != nil {
		// On Windows a removed directory can be delete-pending under the
		// open handle; see IsMissing.
		return IsMissing(err)
	}
	return !os.SameFile(opened, cur)
}

// bucketLockExt ends the name of a bucket's lifecycle lock file in the locks
// directory, which sets it apart from the bucket's own locks directory there.
const bucketLockExt = ".bucket"

// bucketLockName returns the name of the lifecycle lock of the bucket name,
// relative to the shared locks directory.
func bucketLockName(name string) string {
	return hash(name) + bucketLockExt
}

// lockBucket takes the lifecycle lock of the bucket name, waiting for it
// until ctx is done, and returns the function that gives it up and removes
// its file. CreateBucket and DeleteBucket each hold it for their whole run,
// in every process, so a bucket is never created while it is being deleted:
// the delete's cleanup could otherwise remove the name record, or a data
// file, of the bucket created in its place. Object operations do not take
// it; the per-key locks order them against DeleteBucket.
func (vfs *VFS) lockBucket(ctx context.Context, name string) (func() error, error) {
	root, err := vfs.root.OpenRoot(locksPath)
	if err != nil {
		return nil, fmt.Errorf("open locks dir: %w", err)
	}
	lock := NewLock(root)
	lockName := bucketLockName(name)
	if lErr := lock.Lock(ctx, lockName); lErr != nil {
		return nil, errors.Join(fmt.Errorf("lock bucket: %w", lErr), lock.Close())
	}
	return func() error {
		if rErr := errors.Join(lock.Release(lockName), lock.Close()); rErr != nil {
			return fmt.Errorf("release bucket lock: %w", rErr)
		}
		return nil
	}, nil
}

// OpenBucket opens the directories of the bucket name. A missing directory
// returns an error wrapping ErrBucketNotFound. The caller closes the Bucket.
func (vfs *VFS) OpenBucket(name string) (*Bucket, error) {
	metaDir, objDir, locksDir := bucketDirs(name)

	metaRoot, mErr := vfs.openBucketDir(metaDir, "meta")
	if mErr != nil {
		return nil, fmt.Errorf("open bucket: %w", mErr)
	}

	objRoot, oErr := vfs.openBucketDir(objDir, "objects")
	if oErr != nil {
		return nil, fmt.Errorf("open bucket: %w", util.CloseWithErr(oErr, metaRoot))
	}

	locksRoot, lErr := vfs.openBucketDir(locksDir, "locks")
	if lErr != nil {
		return nil, fmt.Errorf("open bucket: %w", util.CloseWithErr(lErr, metaRoot, objRoot))
	}

	return NewBucket(metaDir, metaRoot, objRoot, locksRoot), nil
}

// openBucketDir opens the bucket directory name as a root. what names the
// directory in the error.
func (vfs *VFS) openBucketDir(name, what string) (*os.Root, error) {
	r, err := vfs.root.OpenRoot(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: open %s dir: %w", ErrBucketNotFound, what, err)
	}
	if err != nil {
		return nil, fmt.Errorf("open %s dir: %w", what, err)
	}
	return r, nil
}

// Root returns the root the VFS was created on.
func (vfs *VFS) Root() *os.Root {
	return vfs.root
}
