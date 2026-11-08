package sysfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

var (
	// ErrUnsafeFilePathOutsideDirectory indicates a path that leads out of
	// the working directory, as written or through a symlink.
	ErrUnsafeFilePathOutsideDirectory = errors.New("unsafe file path: outside allowed directory")
	// ErrNotRegularFile indicates a directory, FIFO, socket or device.
	ErrNotRegularFile = errors.New("not a regular file")
	// ErrConfigFileTooLarge indicates a file over 1 MiB.
	ErrConfigFileTooLarge = errors.New("config file too large")
)

const maxConfigFileSize = 1 << 20

// SysFS reads files inside the working directory, as described in the
// package documentation. The zero value is ready to use.
type SysFS struct{}

var _ fs.FS = SysFS{}

// Open opens the named file for reading. name is a path relative to the
// working directory, or an absolute one. A refusal returns one of the
// package's sentinel errors as it is.
func (SysFS) Open(name string) (fs.File, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}

	rel, err := relative(wd, name)
	if err != nil {
		return nil, err
	}

	root, err := os.OpenRoot(wd)
	if err != nil {
		return nil, fmt.Errorf("open working directory: %w", err)
	}

	defer func() { _ = root.Close() }()

	// O_NONBLOCK keeps opening a FIFO from waiting for a writer. It does
	// not change reading a regular file, the only kind returned.
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if escapes(err) {
			return nil, ErrUnsafeFilePathOutsideDirectory
		}

		//nolint:wrapcheck // The *fs.PathError names the path; the provider adds its sentinel.
		return nil, err
	}

	cerr := check(f)
	if cerr != nil {
		_ = f.Close()

		return nil, cerr
	}

	return &file{f: f, left: maxConfigFileSize + 1}, nil
}

// relative returns name relative to wd, or ErrUnsafeFilePathOutsideDirectory
// when it is not wd or inside it.
func relative(wd, name string) (string, error) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}

	rel, err := filepath.Rel(wd, abs)
	if err != nil || !filepath.IsLocal(rel) {
		return "", ErrUnsafeFilePathOutsideDirectory
	}

	// Abs drops a trailing separator; keeping it makes os.Root require a
	// directory, as the operating system does.
	if name != "" && os.IsPathSeparator(name[len(name)-1]) {
		rel += string(filepath.Separator)
	}

	return rel, nil
}

// escapes reports whether err is os.Root's refusal of a path that leads
// out of the root, or of an absolute symlink. os does not export that
// error, so it is matched by its message.
func escapes(err error) bool {
	var pathErr *fs.PathError

	return errors.As(err, &pathErr) && pathErr.Err.Error() == "path escapes from parent"
}

// check refuses an opened file that is not a regular file or is over the
// size limit.
func check(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}

	if !info.Mode().IsRegular() {
		return ErrNotRegularFile
	}

	if info.Size() > maxConfigFileSize {
		return ErrConfigFileTooLarge
	}

	return nil
}

// file is the opened file with reads limited to maxConfigFileSize, for a
// file that grows after it is opened. It is not an *os.File, so that no
// method such as WriteTo can read past the limit.
type file struct {
	f *os.File
	// left is the number of bytes to read before the limit is passed.
	left int64
}

func (f *file) Stat() (fs.FileInfo, error) {
	//nolint:wrapcheck // As *os.File reports it.
	return f.f.Stat()
}

func (f *file) Read(p []byte) (int, error) {
	if int64(len(p)) > f.left {
		p = p[:f.left]
	}

	n, err := f.f.Read(p)

	f.left -= int64(n)

	if f.left == 0 {
		return n, ErrConfigFileTooLarge
	}

	//nolint:wrapcheck // io.EOF must reach the caller as it is.
	return n, err
}

func (f *file) Close() error {
	//nolint:wrapcheck // As *os.File reports it.
	return f.f.Close()
}
