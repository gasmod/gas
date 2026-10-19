package vfs

import (
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
// wrapping ErrBucketExists.
func (vfs *VFS) CreateBucket(name string) error {
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

	err := vfs.root.Mkdir(metaDir, dirPerm)
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrExist) {
		// Only a directory is the bucket; anything else at its name is an
		// I/O error.
		if fi, sErr := vfs.root.Lstat(metaDir); sErr == nil && fi.IsDir() {
			return fmt.Errorf("%w: create meta dir: %w", ErrBucketExists, err)
		}
	}
	return fmt.Errorf("create meta dir: %w", err)
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

	return NewBucket(metaRoot, objRoot, locksRoot), nil
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
