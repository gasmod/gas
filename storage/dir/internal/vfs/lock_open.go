//go:build !darwin

package vfs

import (
	"fmt"
	"os"
)

// openInRoot opens name in r for reading and writing, creating it with
// filePerm if it does not exist. what names the file in the error.
func openInRoot(r *os.Root, name, what string) (*os.File, error) {
	f, err := r.OpenFile(name, os.O_CREATE|os.O_RDWR, filePerm)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", what, err)
	}
	return f, nil
}
