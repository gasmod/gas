//go:build unix

package vfs

import "os"

// syncDirFile syncs the directory name in r.
func syncDirFile(r *os.Root, name string) error {
	f, err := r.Open(name)
	if err != nil {
		return err //nolint:wrapcheck // syncDir wraps it.
	}

	err = f.Sync()
	if err != nil {
		return closeAll(err, f)
	}

	return f.Close() //nolint:wrapcheck // syncDir wraps it.
}
