//go:build darwin

package vfs

import (
	"errors"
	"fmt"
	"os"
)

// maxOpenAttempts bounds how many times openInRoot retries the pair of
// opens. Under heavy create-and-remove churn a retry is rarely needed more
// than once, so the bound only matters when the second open fails with
// ENOENT for a lasting reason, such as a lock name that is a dangling
// symlink.
const maxOpenAttempts = 10

// openInRoot opens name in r for reading and writing, creating it with
// filePerm if it does not exist. what names the file in the error.
//
// os.Root on darwin can fail an O_CREATE open of a file that another
// goroutine is creating at the same moment (see
// https://github.com/golang/go/issues/81246), so the first open is an
// exclusive create. When the file already exists, the second open keeps
// O_CREATE, so an ENOENT from it means either the file was removed between
// the two opens or the same os.Root issue struck again. Both are retried, up
// to maxOpenAttempts times; after that the last error is returned, wrapping
// os.ErrNotExist as the single open on other platforms would.
func openInRoot(r *os.Root, name, what string) (*os.File, error) {
	var err error
	for range maxOpenAttempts {
		var f *os.File
		f, err = r.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, filePerm)
		if !errors.Is(err, os.ErrExist) {
			return wrapOpen(f, err, what)
		}
		f, err = r.OpenFile(name, os.O_CREATE|os.O_RDWR, filePerm)
		if !errors.Is(err, os.ErrNotExist) {
			return wrapOpen(f, err, what)
		}
	}
	return wrapOpen(nil, err, what)
}

// wrapOpen returns f, or err wrapped with what when the open failed.
func wrapOpen(f *os.File, err error, what string) (*os.File, error) {
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", what, err)
	}
	return f, nil
}
