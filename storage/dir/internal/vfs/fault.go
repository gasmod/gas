package vfs

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
)

// Op names a kind of file operation, for Fault.
type Op string

// The operations Fault can fail.
const (
	// OpClose is the close of a file or a directory; it runs before the
	// fault, so a failed close still releases the descriptor.
	OpClose Op = "close"
	// OpStat is the stat of an open file, or of a name in a root.
	OpStat Op = "stat"
	// OpLstat is the Lstat of a name in a root.
	OpLstat Op = "lstat"
	// OpWrite is a write to a file.
	OpWrite Op = "write"
	// OpSync is the sync of a file or a directory.
	OpSync Op = "sync"
	// OpRead is the read of a record file.
	OpRead Op = "read"
	// OpRename is a rename in a root.
	OpRename Op = "rename"
	// OpOpenRoot is the open of a directory as a root.
	OpOpenRoot Op = "openroot"
	// OpReadDir is the read of a directory's entries.
	OpReadDir Op = "readdir"
	// OpFlock is an attempt to take a file lock, blocking or not.
	OpFlock Op = "flock"
)

// Fault is a hook for tests: when set, every operation of the kinds Op
// names calls it with the operation and the path it acts on, and fails
// with the error it returns, which the caller handles as the operation's
// own error. A nil error lets the operation run. It reaches the failure
// paths that a real file system does not take on demand. Nothing else
// sets it.
var Fault atomic.Pointer[func(op Op, path string) error]

// fault returns the error Fault gives op on path, or nil.
func fault(op Op, path string) error {
	if hook := Fault.Load(); hook != nil {
		return (*hook)(op, path)
	}

	return nil
}

// inRoot returns the path of name in r, for Fault.
func inRoot(r *os.Root, name string) string { return filepath.Join(r.Name(), name) }

// closeOne closes c. The close of a file or a directory then goes through
// Fault.
func closeOne(c io.Closer) error {
	err := c.Close()

	if n, ok := c.(interface{ Name() string }); ok && err == nil {
		err = fault(OpClose, n.Name())
	}

	return err
}

// statFile returns the FileInfo of the open file f.
func statFile(f *os.File) (os.FileInfo, error) {
	err := fault(OpStat, f.Name())
	if err != nil {
		return nil, err
	}

	return f.Stat() //nolint:wrapcheck // the caller wraps it.
}

// statIn returns the FileInfo of name in r, following a symlink.
func statIn(r *os.Root, name string) (os.FileInfo, error) {
	err := fault(OpStat, inRoot(r, name))
	if err != nil {
		return nil, err
	}

	return r.Stat(name) //nolint:wrapcheck // the caller wraps it.
}

// lstatIn returns the FileInfo of name in r, not following a symlink.
func lstatIn(r *os.Root, name string) (os.FileInfo, error) {
	err := fault(OpLstat, inRoot(r, name))
	if err != nil {
		return nil, err
	}

	return r.Lstat(name) //nolint:wrapcheck // the caller wraps it.
}

// writeFile writes p to f.
func writeFile(f *os.File, p []byte) error {
	err := fault(OpWrite, f.Name())
	if err != nil {
		return err
	}

	_, err = f.Write(p)

	return err //nolint:wrapcheck // the caller wraps it.
}

// syncFile syncs f.
func syncFile(f *os.File) error {
	err := fault(OpSync, f.Name())
	if err != nil {
		return err
	}

	return f.Sync() //nolint:wrapcheck // the caller wraps it.
}

// readAll reads f to its end, or to n bytes.
func readAll(f *os.File, n int64) ([]byte, error) {
	err := fault(OpRead, f.Name())
	if err != nil {
		return nil, err
	}

	return io.ReadAll(io.LimitReader(f, n)) //nolint:wrapcheck // the caller wraps it.
}

// renameIn renames oldName to newName in r.
func renameIn(r *os.Root, oldName, newName string) error {
	err := fault(OpRename, inRoot(r, oldName))
	if err != nil {
		return err
	}

	return r.Rename(oldName, newName) //nolint:wrapcheck // the caller wraps it.
}

// openRootIn opens the directory name in r as a root.
func openRootIn(r *os.Root, name string) (*os.Root, error) {
	err := fault(OpOpenRoot, inRoot(r, name))
	if err != nil {
		return nil, err
	}

	return r.OpenRoot(name) //nolint:wrapcheck // the caller wraps it.
}

// readDirIn returns the entries of the directory name in r, sorted by
// name. An fs.FS path is slash-separated: on Windows r.FS rejects one with
// a backslash as invalid, so name, joined with filepath.Join, is converted.
func readDirIn(r *os.Root, name string) ([]fs.DirEntry, error) {
	err := fault(OpReadDir, inRoot(r, name))
	if err != nil {
		return nil, err
	}

	return fs.ReadDir(r.FS(), filepath.ToSlash(name)) //nolint:wrapcheck // the caller wraps it.
}
