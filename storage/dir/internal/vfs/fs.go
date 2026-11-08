package vfs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
	"uuid"
)

// The layout of a root, relative to it, as described in the package
// documentation.
const (
	// gasPath holds every directory of the package but the metadata
	// directories of the buckets.
	gasPath = ".gas"
	// objectsPath holds a directory of data files per bucket.
	objectsPath = ".gas/objects"
	// locksPath holds a directory of lock files per bucket, and the
	// lifecycle locks of the buckets.
	locksPath = ".gas/locks"
	// bucketsPath holds the name record of each bucket.
	bucketsPath = ".gas/buckets"
	// keysPath holds the key index of each bucket.
	keysPath = ".gas/keys"
	// layoutPath is the layout marker.
	layoutPath = ".gas/layout"
	// layoutVersion is what the layout marker holds.
	layoutVersion = "2\n"
	// metaFileExt ends the name of a metadata file.
	metaFileExt = ".gas.json"
)

// The modes of what the package creates, before the umask.
const (
	// dirPerm is the mode of a directory.
	dirPerm = 0o750
	// filePerm is the mode of a file.
	filePerm = 0o600
)

// ensureDir creates the directory name in r, and any missing parents.
// what names the directory in the error.
func ensureDir(r *os.Root, name, what string) error {
	err := r.MkdirAll(name, dirPerm)
	if err != nil {
		return ioErr("create "+what+" dir", err)
	}

	return nil
}

// rmInRoot removes the file or empty directory name in r. A name that is
// not there is not an error. what names the file in the error.
func rmInRoot(r *os.Root, name, what string) error {
	err := r.Remove(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ioErr("remove "+what, err)
	}

	return nil
}

// The retries of an operation on a name that another holder is removing
// (see isDeletePending): it is retried for up to deletePendingTimeout,
// deletePendingBackoff apart. The removal ends once the handles to the
// name are closed, so the bound only matters for an error with another
// cause, which then surfaces.
const (
	// deletePendingTimeout bounds the retries.
	deletePendingTimeout = 5 * time.Second
	// deletePendingBackoff is the wait between retries.
	deletePendingBackoff = time.Millisecond
)

// retryPending calls op, and calls it again while it fails because a name
// it uses is being removed, for up to deletePendingTimeout. It returns
// the last results, which then see the name gone or replaced.
func retryPending[T any](op func() (T, error)) (T, error) {
	v, err := op()
	for deadline := time.Now().Add(deletePendingTimeout); isDeletePending(err) &&
		time.Now().Before(deadline); {
		time.Sleep(deletePendingBackoff)

		v, err = op()
	}

	return v, err
}

// discard removes a file a failed write left in r, and joins any removal
// error with err.
func discard(r *os.Root, name string, err error) error {
	return joinErrs(err, rmInRoot(r, name, "temp file"))
}

// closeAll closes every closer, in order, and returns err joined with each
// close error.
func closeAll(err error, closers ...io.Closer) error {
	for _, c := range closers {
		cErr := closeOne(c)
		if cErr != nil {
			err = joinErrs(err, ioErr("close", cErr))
		}
	}

	return err
}

// writeSynced writes data to the new file name in r, created exclusively,
// and syncs it before it closes it. A failure removes the file.
func writeSynced(r *os.Root, name string, data []byte) error {
	f, err := r.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, filePerm)
	if err != nil {
		return ioErr("create", err)
	}

	err = writeFile(f, data)
	if err != nil {
		return discard(r, name, closeAll(ioErr("write", err), f))
	}

	err = syncFile(f)
	if err != nil {
		return discard(r, name, closeAll(ioErr("sync", err), f))
	}

	err = closeOne(f)
	if err != nil {
		return discard(r, name, ioErr("close", err))
	}

	return nil
}

// syncDir syncs the directory name in r, so that the entries created,
// renamed and removed in it survive a power loss.
func syncDir(r *os.Root, name string) error {
	err := syncDirFile(r, name)
	if err != nil {
		return ioErr("sync dir", err)
	}

	return nil
}

// join joins the elements of a path under a root.
func join(elem ...string) string { return filepath.Join(elem...) }

// bucketDirs returns the metadata, objects, locks and keys directories of
// the bucket name, relative to the root, each named by the bucket's hash.
func bucketDirs(name string) (meta, objects, locks, keys string) {
	h := hash(name)

	return h, join(objectsPath, h), join(locksPath, h), join(keysPath, h)
}

// newTempSuffix returns what ends the name of a new temp file: a new UUID.
func newTempSuffix() string { return uuid.New().String() }
