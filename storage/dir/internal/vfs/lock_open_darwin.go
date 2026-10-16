//go:build darwin

package vfs

import (
	"errors"
	"fmt"
	"os"
)

// openInRoot opens name in r for reading and writing, creating it with
// filePerm if it does not exist. what names the file in the error.
//
// os.Root on darwin can fail an O_CREATE open of a file that another
// goroutine is creating at the same moment (see
// https://github.com/golang/go/issues/81246), so the create is exclusive and
// an existing file is opened without O_CREATE, retrying only when it was
// removed between the two opens.
func openInRoot(r *os.Root, name, what string) (*os.File, error) {
	for {
		f, err := r.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, filePerm)
		if !errors.Is(err, os.ErrExist) {
			return wrapOpen(f, err, what)
		}
		f, err = r.OpenFile(name, os.O_CREATE|os.O_RDWR, filePerm)
		if !errors.Is(err, os.ErrNotExist) {
			return wrapOpen(f, err, what)
		}
	}
}

// wrapOpen returns f, or err wrapped with what when the open failed.
func wrapOpen(f *os.File, err error, what string) (*os.File, error) {
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", what, err)
	}
	return f, nil
}
