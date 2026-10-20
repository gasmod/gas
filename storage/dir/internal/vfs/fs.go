package vfs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	objectsPath  = ".gas/objects"
	locksPath    = ".gas/locks"
	bucketsPath  = ".gas/buckets"
	metaFileExt  = ".gas.json"
	fileHeadSize = 512

	dirPerm  = 0o750
	filePerm = 0o600
)

// ensureDirInRoot creates the directory name in r, and any missing parents,
// with dirPerm. what names the directory in the error.
func ensureDirInRoot(r *os.Root, name, what string) error {
	if err := r.MkdirAll(name, dirPerm); err != nil {
		return fmt.Errorf("create %s dir: %w", what, err)
	}
	return nil
}

// rmInRoot removes the file or empty directory name in r. A name that does
// not exist is not an error. what names the file in the error.
func rmInRoot(r *os.Root, name, what string) error {
	if err := r.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", what, err)
	}
	return nil
}

// deletePendingRetries bounds how many times an operation on a name is
// retried while another holder is removing it (see isDeletePending), with
// deletePendingBackoff between tries. The removal ends as soon as the
// holder's remove returns, so the bound only matters for an error with
// another cause, which then surfaces.
const (
	deletePendingRetries = 100
	deletePendingBackoff = time.Millisecond
)

// retryPending calls op, and calls it again while it fails because a name
// it uses is being removed, up to deletePendingRetries times. It returns
// the last results, which then see the name gone or replaced.
func retryPending[T any](op func() (T, error)) (T, error) {
	v, err := op()
	for i := 0; i < deletePendingRetries && isDeletePending(err); i++ {
		time.Sleep(deletePendingBackoff)
		v, err = op()
	}
	return v, err
}

// discardTmp removes a temp file left by a failed write and joins any removal
// error with err.
func discardTmp(r *os.Root, name string, err error) error {
	return errors.Join(err, rmInRoot(r, name, "temp file"))
}

// bucketDirs returns the metadata, objects and locks directories of the
// bucket name, relative to the VFS root. Each is named by the bucket's hash.
func bucketDirs(name string) (meta, obj, locks string) {
	h := hash(name)
	return h, filepath.Join(objectsPath, h), filepath.Join(locksPath, h)
}

// fileHead reads up to fileHeadSize bytes from f for content sniffing. It
// returns them with a reader that yields the whole stream, head included.
func fileHead(f io.Reader) ([]byte, io.Reader, error) {
	head := make([]byte, fileHeadSize)
	// Not io.ReadFull: it drops an error returned with the bytes that fill head.
	var n int
	var rErr error
	for n < len(head) && rErr == nil {
		var m int
		m, rErr = f.Read(head[n:])
		n += m
	}
	if rErr != nil && !errors.Is(rErr, io.EOF) {
		return nil, f, fmt.Errorf("read body: %w", rErr)
	}
	head = head[:n]
	return head, io.MultiReader(bytes.NewReader(head), f), nil
}
