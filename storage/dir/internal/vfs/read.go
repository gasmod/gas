package vfs

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// maxRecordSize is the most bytes a read of a metadata file or of a name
// record takes. The rules of the storage package bound what a metadata
// file holds: a key of at most 1024 bytes, six header fields of at most
// 1024 bytes each, user metadata of at most 2048 bytes and the names of
// the fields, each at most six bytes in JSON per byte, well below 64 KiB.
// A name record holds a bucket name of at most 63 bytes.
const maxRecordSize = 64 << 10

// errTooLarge reports a file longer than a read of it takes.
var errTooLarge = errors.New("file is too large")

// errReplaced reports a file that another regular file replaced at its
// name between its open and the check that it is the file at the name,
// as a write's rename of its metadata does. Without the key's lock it is
// no proof of damage; under the lock nothing replaces the file.
var errReplaced = errors.New("file replaced while it was opened")

// openRegular opens name in r for reading and returns it, with its
// FileInfo, when it is a regular file, and else an error wrapping
// errNotRegular. It opens with openReadFlags, so a FIFO or a device in its
// place never blocks the open. os.Root follows a symlink that stays inside
// the root even with O_NOFOLLOW, so it also checks that the file opened is
// the one at name itself, not a symlink's target; a regular file at the
// name that is another one than the file opened is errReplaced. Errors of
// the open are returned as they are, for the caller to classify and wrap.
func openRegular(r *os.Root, name string) (*os.File, os.FileInfo, error) {
	f, err := r.OpenFile(name, os.O_RDONLY|openReadFlags, 0)
	if err != nil {
		return nil, nil, err //nolint:wrapcheck // the caller classifies and wraps it.
	}

	fi, err := f.Stat()
	if err != nil {
		return nil, nil, closeAll(ioErr("stat", err), f)
	}

	if !fi.Mode().IsRegular() {
		return nil, nil, closeAll(errNotRegular, f)
	}

	li, err := r.Lstat(name)
	if err != nil {
		return nil, nil, closeAll(ioErr("stat", err), f)
	}

	switch {
	case os.SameFile(fi, li):
	case li.Mode().IsRegular():
		return nil, nil, closeAll(errReplaced, f)
	default:
		return nil, nil, closeAll(errNotRegular, f)
	}

	return f, fi, nil
}

// readCapped reads the regular file name in r, as openRegular opens it,
// and returns its contents. A file longer than limit is an error wrapping
// errTooLarge, read no further than one byte past limit.
func readCapped(r *os.Root, name string, limit int64) ([]byte, error) {
	f, _, err := openRegular(r, name)
	if err != nil {
		return nil, err
	}

	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, closeAll(ioErr("read", err), f)
	}

	err = f.Close()
	if err != nil {
		return nil, ioErr("close", err)
	}

	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: more than %d bytes", errTooLarge, limit)
	}

	return data, nil
}
