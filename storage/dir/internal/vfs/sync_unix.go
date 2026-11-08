//go:build unix

package vfs

import "os"

// syncDirFile syncs the directory name in r.
func syncDirFile(r *os.Root, name string) error {
	f, err := r.Open(name)
	if err != nil {
		return err //nolint:wrapcheck // syncDir wraps it.
	}

	err = syncFile(f)
	if err != nil {
		return closeAll(err, f)
	}

	return closeOne(f)
}
